package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

const classificationVersion = "v1"

const maximumSourceFileBytes = 16 << 20

var highConfidenceSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{50,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{20,}`),
}

func (u *Usecase) scan(
	ctx context.Context,
	root string,
	entries []sharedmodel.TrackedEntry,
	policy documentationmodel.SourcePolicy,
	reader trackedContentReader,
) (scanResult, error) {
	sortedEntries := append([]sharedmodel.TrackedEntry(nil), entries...)
	sort.Slice(sortedEntries, func(left, right int) bool {
		return sortedEntries[left].Path < sortedEntries[right].Path
	})

	result := scanResult{Decisions: make([]sharedmodel.SourceDecision, 0, len(sortedEntries))}
	for _, entry := range sortedEntries {
		if err := ctx.Err(); err != nil {
			return scanResult{}, err
		}
		decision, file, err := u.scanEntry(ctx, root, entry, policy, reader)
		if err != nil {
			return scanResult{}, err
		}
		result.Decisions = append(result.Decisions, decision)
		if file != nil {
			result.Files = append(result.Files, *file)
		}
	}
	return result, nil
}

type trackedContentReader func(context.Context, string, sharedmodel.TrackedEntry, int64) (sharedmodel.FileContent, error)

type scanResult struct {
	Decisions []sharedmodel.SourceDecision
	Files     []sharedmodel.SourceFile
}

func (u *Usecase) scanEntry(
	ctx context.Context,
	root string,
	entry sharedmodel.TrackedEntry,
	policy documentationmodel.SourcePolicy,
	reader trackedContentReader,
) (sharedmodel.SourceDecision, *sharedmodel.SourceFile, error) {
	if isSecretPath(entry.Path) {
		return excludedDecision(entry.Path, sharedmodel.RoleSecret, "secret_filename"), nil, nil
	}
	if pathWithin(entry.Path, policy.DocsDir) || entry.Path == policy.StatePath || (policy.ReportPath != "" && entry.Path == policy.ReportPath) {
		return excludedDecision(entry.Path, sharedmodel.RoleGeneratedDocumentation, "tool_owned_output"), nil, nil
	}
	if entry.Mode == "160000" {
		return excludedDecision(entry.Path, sharedmodel.RoleSpecialGitEntry, "git_submodule"), nil, nil
	}
	if entry.Mode != "100644" && entry.Mode != "100755" && entry.Mode != "120000" {
		return excludedDecision(entry.Path, sharedmodel.RoleSpecialGitEntry, "unsupported_git_mode"), nil, nil
	}

	preliminaryRole, _ := classifyRole(entry.Path, nil, policy.RoleOverrides)
	included, err := matchesAny(policy.Include, entry.Path)
	if err != nil {
		return sharedmodel.SourceDecision{}, nil, err
	}
	if !included {
		return excludedDecision(entry.Path, preliminaryRole, "source_include_miss"), nil, nil
	}
	excluded, err := matchesAny(policy.Exclude, entry.Path)
	if err != nil {
		return sharedmodel.SourceDecision{}, nil, err
	}
	if excluded {
		return excludedDecision(entry.Path, preliminaryRole, "source_exclude_pattern"), nil, nil
	}

	content, err := reader(ctx, root, entry, policy.MaxFileBytes)
	if err != nil {
		return sharedmodel.SourceDecision{}, nil, fmt.Errorf("read tracked path %q: %w", entry.Path, err)
	}
	indexSymlink := entry.Mode == "120000"
	if content.Symlink != indexSymlink {
		return sharedmodel.SourceDecision{}, nil, fmt.Errorf("tracked path %q changed file type during scan", entry.Path)
	}
	if content.Truncated {
		decision := excludedDecision(entry.Path, sharedmodel.RoleOversized, "file_size_limit")
		decision.Size = content.Size
		return decision, nil, nil
	}
	if isBinary(content.Data) {
		decision := excludedDecision(entry.Path, sharedmodel.RoleBinary, "binary_content")
		decision.Size = content.Size
		return decision, nil, nil
	}

	role, reason := classifyRole(entry.Path, content.Data, policy.RoleOverrides)
	includeAsContext, triggers := roleBehavior(role, policy)
	decision := sharedmodel.SourceDecision{
		Path:                 entry.Path,
		Role:                 role,
		IncludedAsContext:    includeAsContext,
		TriggersRegeneration: triggers,
		Reason:               reason,
		Size:                 content.Size,
	}
	if includeAsContext && containsHighConfidenceSecret(content.Data) {
		return sharedmodel.SourceDecision{}, nil, fmt.Errorf("high-confidence secret value detected in %q", entry.Path)
	}
	if !includeAsContext {
		return decision, nil, nil
	}
	digest := sha256.Sum256(content.Data)
	file := sharedmodel.SourceFile{
		Path:                 entry.Path,
		Role:                 role,
		SourceHash:           fmt.Sprintf("sha256:%x", digest),
		TriggersRegeneration: triggers,
		Data:                 append([]byte(nil), content.Data...),
		Size:                 content.Size,
	}
	return decision, &file, nil
}

func validateSourcePolicy(policy documentationmodel.SourcePolicy) error {
	if err := validateOwnedPath("docs_dir", policy.DocsDir, false); err != nil {
		return err
	}
	if err := validateOwnedPath("state_path", policy.StatePath, false); err != nil {
		return err
	}
	if err := validateOwnedPath("report_path", policy.ReportPath, true); err != nil {
		return err
	}
	if pathWithin(policy.StatePath, policy.DocsDir) || pathWithin(policy.DocsDir, policy.StatePath) {
		return fmt.Errorf("docs_dir and state_path must not overlap")
	}
	if policy.ReportPath != "" {
		if pathWithin(policy.ReportPath, policy.DocsDir) || pathWithin(policy.DocsDir, policy.ReportPath) || policy.ReportPath == policy.StatePath {
			return fmt.Errorf("report_path must be distinct from generated output and state")
		}
	}
	if len(policy.Include) == 0 {
		return fmt.Errorf("source include patterns must not be empty")
	}
	if policy.MaxFileBytes <= 0 {
		return fmt.Errorf("source file limit must be greater than zero")
	}
	if policy.MaxFileBytes > maximumSourceFileBytes {
		return fmt.Errorf("source file limit exceeds hard maximum of %d bytes", maximumSourceFileBytes)
	}
	for _, candidate := range append(append([]string(nil), policy.Include...), policy.Exclude...) {
		if strings.TrimSpace(candidate) == "" {
			return fmt.Errorf("source glob must not be empty")
		}
		if _, err := doublestar.Match(candidate, "validation/path"); err != nil {
			return fmt.Errorf("invalid source glob %q: %w", candidate, err)
		}
	}
	for index, override := range policy.RoleOverrides {
		if strings.TrimSpace(override.Pattern) == "" {
			return fmt.Errorf("role override %d pattern must not be empty", index)
		}
		if _, err := doublestar.Match(override.Pattern, "validation/path"); err != nil {
			return fmt.Errorf("invalid role override glob %q: %w", override.Pattern, err)
		}
		if !overridableRole(override.Role) {
			return fmt.Errorf("role override %d has unsupported role %q", index, override.Role)
		}
	}
	return nil
}

func validateOwnedPath(name, value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("%s must be a normalized repository-relative path", name)
	}
	return nil
}

func classifyRole(repositoryPath string, content []byte, overrides []documentationmodel.RoleOverride) (sharedmodel.SourceRole, string) {
	for _, override := range overrides {
		matched, err := doublestar.Match(override.Pattern, repositoryPath)
		if err == nil && matched {
			return override.Role, "role_override"
		}
	}

	lowerPath := strings.ToLower(repositoryPath)
	base := path.Base(lowerPath)
	extension := path.Ext(lowerPath)
	if generatedPath(lowerPath) || generatedHeader(content) {
		return sharedmodel.RoleGeneratedCode, "generated_code"
	}
	if lockFile(base, extension) {
		return sharedmodel.RoleLockFile, "dependency_lock"
	}
	if fixturePath(lowerPath) || minifiedArtifact(base) {
		return sharedmodel.RoleFixture, "fixture_or_snapshot"
	}
	if testPath(lowerPath, base) {
		return sharedmodel.RoleTest, "test_source"
	}
	if contractPath(lowerPath, base, extension) {
		return sharedmodel.RoleContract, "contract_or_schema"
	}
	if databasePath(lowerPath, extension) {
		return sharedmodel.RoleDatabase, "database_schema_or_migration"
	}
	if dependencyManifest(base) {
		return sharedmodel.RoleDependencyManifest, "dependency_manifest"
	}
	if runtimeConfigurationPath(lowerPath, base, extension) {
		return sharedmodel.RoleRuntimeConfiguration, "runtime_or_tool_configuration"
	}
	if productionSourcePath(lowerPath, extension) || hasSourceShebang(content) {
		return sharedmodel.RoleProductionSource, "production_source"
	}
	if prosePath(extension) {
		return sharedmodel.RoleProse, "handwritten_prose"
	}
	return sharedmodel.RoleUnknownSource, "unknown_text_source"
}

func roleBehavior(role sharedmodel.SourceRole, policy documentationmodel.SourcePolicy) (bool, bool) {
	switch role {
	case sharedmodel.RoleTest:
		return policy.Tests.IncludeAsContext, policy.Tests.TriggerOnChange
	case sharedmodel.RoleGeneratedCode:
		return policy.Generated.IncludeAsContext, policy.Generated.TriggerOnChange
	case sharedmodel.RoleFixture:
		return policy.Fixtures.IncludeAsContext, policy.Fixtures.TriggerOnChange
	case sharedmodel.RoleLockFile, sharedmodel.RoleGeneratedDocumentation, sharedmodel.RoleSecret,
		sharedmodel.RoleBinary, sharedmodel.RoleOversized, sharedmodel.RoleSpecialGitEntry:
		return false, false
	case sharedmodel.RoleProse:
		return true, false
	default:
		return true, true
	}
}

func overridableRole(role sharedmodel.SourceRole) bool {
	switch role {
	case sharedmodel.RoleProductionSource, sharedmodel.RoleContract, sharedmodel.RoleDatabase,
		sharedmodel.RoleRuntimeConfiguration, sharedmodel.RoleDependencyManifest, sharedmodel.RoleTest,
		sharedmodel.RoleGeneratedCode, sharedmodel.RoleFixture, sharedmodel.RoleProse, sharedmodel.RoleUnknownSource:
		return true
	default:
		return false
	}
}

func isSecretPath(repositoryPath string) bool {
	base := strings.ToLower(path.Base(repositoryPath))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	switch base {
	case ".netrc", ".npmrc", ".pypirc", "credentials", "credentials.json", "secrets.yml", "secrets.yaml",
		"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return true
	}
	switch path.Ext(base) {
	case ".key", ".p12", ".pfx", ".jks":
		return true
	}
	return strings.Contains(base, "service-account") && path.Ext(base) == ".json"
}

func containsHighConfidenceSecret(content []byte) bool {
	upper := bytes.ToUpper(content)
	if bytes.Contains(upper, []byte("-----BEGIN PRIVATE KEY-----")) ||
		bytes.Contains(upper, []byte("-----BEGIN RSA PRIVATE KEY-----")) ||
		bytes.Contains(upper, []byte("-----BEGIN EC PRIVATE KEY-----")) ||
		bytes.Contains(upper, []byte("-----BEGIN OPENSSH PRIVATE KEY-----")) {
		return true
	}
	for _, pattern := range highConfidenceSecretPatterns {
		if pattern.FindIndex(content) != nil {
			return true
		}
	}
	return false
}

func isBinary(content []byte) bool {
	return bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content)
}

func generatedHeader(content []byte) bool {
	if len(content) > 4096 {
		content = content[:4096]
	}
	lower := bytes.ToLower(content)
	return bytes.Contains(lower, []byte("code generated")) && bytes.Contains(lower, []byte("do not edit")) ||
		bytes.Contains(lower, []byte("@generated")) ||
		bytes.Contains(lower, []byte("autogenerated file"))
}

func generatedPath(value string) bool {
	return hasAnyPathSegment(value, "generated", "gen", "dist", "build", "target", "vendor", "node_modules")
}

func lockFile(base, extension string) bool {
	if extension == ".lock" {
		return true
	}
	switch base {
	case "go.sum", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "cargo.lock", "poetry.lock", "composer.lock", "gemfile.lock":
		return true
	}
	return false
}

func fixturePath(value string) bool {
	return hasAnyPathSegment(value, "fixture", "fixtures", "snapshot", "snapshots", "testdata", "__snapshots__")
}

func minifiedArtifact(base string) bool {
	return strings.Contains(base, ".min.") || strings.HasSuffix(base, ".map")
}

func testPath(value, base string) bool {
	if hasAnyPathSegment(value, "test", "tests", "__tests__", "spec", "specs") {
		return true
	}
	return strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

func contractPath(value, base, extension string) bool {
	switch extension {
	case ".proto", ".graphql", ".gql", ".avsc", ".thrift", ".wsdl", ".xsd":
		return true
	}
	return strings.Contains(base, "openapi") || strings.Contains(base, "swagger") || strings.Contains(base, "schema.json") ||
		hasAnyPathSegment(value, "contracts", "schemas")
}

func databasePath(value, extension string) bool {
	return extension == ".sql" || hasAnyPathSegment(value, "migration", "migrations")
}

func dependencyManifest(base string) bool {
	if strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt") {
		return true
	}
	switch base {
	case "go.mod", "package.json", "pyproject.toml", "pipfile", "cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts", "composer.json", "gemfile", "mix.exs", "setup.py", "setup.cfg", "environment.yml", "environment.yaml":
		return true
	}
	return false
}

func runtimeConfigurationPath(value, base, extension string) bool {
	if strings.HasPrefix(value, ".github/workflows/") || hasAnyPathSegment(value, "deploy", "deployment", "k8s", "kubernetes", "helm", "terraform") {
		return true
	}
	if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") || strings.HasPrefix(base, "docker-compose") || base == "makefile" {
		return true
	}
	if strings.HasPrefix(base, "tsconfig") && extension == ".json" ||
		strings.HasPrefix(base, ".eslintrc") || strings.HasPrefix(base, ".prettierrc") {
		return true
	}
	switch extension {
	case ".yaml", ".yml", ".toml", ".ini", ".conf", ".properties", ".tf", ".hcl":
		return true
	}
	return strings.HasPrefix(base, ".") && (strings.HasSuffix(base, "rc") || strings.Contains(base, "config"))
}

func productionSourcePath(value, extension string) bool {
	switch extension {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".java", ".kt", ".kts", ".scala", ".rs", ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".cs", ".fs", ".fsx", ".rb", ".php", ".swift", ".m", ".mm", ".ex", ".exs", ".erl", ".hrl", ".clj", ".cljs", ".sh", ".bash", ".zsh", ".fish", ".lua", ".r", ".dart", ".vue", ".svelte", ".sol", ".html", ".htm", ".css", ".scss", ".sass", ".less", ".templ", ".tmpl", ".hbs":
		return true
	}
	return hasAnyPathSegment(value, "cmd", "src", "lib", "app", "internal", "pkg", "services", "packages") && extension == ""
}

func hasSourceShebang(content []byte) bool {
	if !bytes.HasPrefix(content, []byte("#!")) {
		return false
	}
	firstLine := content
	if newline := bytes.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	lower := strings.ToLower(string(firstLine))
	for _, interpreter := range []string{"python", "node", "deno", "ruby", "perl", "php", "bash", "sh", "zsh"} {
		if strings.Contains(lower, interpreter) {
			return true
		}
	}
	return false
}

func prosePath(extension string) bool {
	switch extension {
	case ".md", ".mdx", ".markdown", ".adoc", ".asciidoc", ".rst", ".txt":
		return true
	}
	return false
}

func hasAnyPathSegment(value string, candidates ...string) bool {
	candidateSet := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidateSet[candidate] = struct{}{}
	}
	for _, segment := range strings.Split(value, "/") {
		if _, exists := candidateSet[segment]; exists {
			return true
		}
	}
	return false
}

func matchesAny(patterns []string, repositoryPath string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, repositoryPath)
		if err != nil {
			return false, fmt.Errorf("match source glob %q: %w", pattern, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func pathWithin(candidate, root string) bool {
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func excludedDecision(repositoryPath string, role sharedmodel.SourceRole, reason string) sharedmodel.SourceDecision {
	return sharedmodel.SourceDecision{Path: repositoryPath, Role: role, Reason: reason}
}
