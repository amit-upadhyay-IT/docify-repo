package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot ascends from the test working directory until it finds the module's go.mod so the
// release artifacts can be read regardless of where `go test` is invoked.
func repoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("go.mod not found above test directory")
		}
		directory = parent
	}
}

func readFile(t *testing.T, root, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

// dockerfileStages splits a Dockerfile into its stage bodies, one per FROM instruction, in
// order. The build stage is the first and the runtime stage is the last.
func dockerfileStages(t *testing.T, dockerfile string) []string {
	t.Helper()
	var stages []string
	var current []string
	started := false
	for _, line := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "FROM ") {
			if started {
				stages = append(stages, strings.Join(current, "\n"))
			}
			current = nil
			started = true
		}
		current = append(current, line)
	}
	if started {
		stages = append(stages, strings.Join(current, "\n"))
	}
	if len(stages) < 2 {
		t.Fatalf("expected a multi-stage Dockerfile, found %d stage(s)", len(stages))
	}
	return stages
}

func TestDockerfileBuildStageCrossCompilesReproducibly(t *testing.T) {
	root := repoRoot(t)
	dockerfile := readFile(t, root, "Dockerfile")
	build := dockerfileStages(t, dockerfile)[0]

	requirements := map[string]string{
		"--platform=$BUILDPLATFORM": "build stage must run on the builder platform for fast cross-compilation",
		"AS build":                  "first stage must be named build",
		"ARG TARGETOS":              "build stage must consume the target OS build arg",
		"ARG TARGETARCH":            "build stage must consume the target architecture build arg",
		"CGO_ENABLED=0":             "binary must be statically linked",
		"GOOS=":                     "build must set GOOS for cross-compilation",
		"GOARCH=":                   "build must set GOARCH for cross-compilation",
		"-trimpath":                 "build must trim paths for reproducibility",
		"go mod verify":             "modules must be cryptographically verified",
	}
	for needle, why := range requirements {
		if !strings.Contains(build, needle) {
			t.Errorf("build stage missing %q: %s", needle, why)
		}
	}

	if !regexp.MustCompile(`FROM --platform=\$BUILDPLATFORM golang:1\.\d+`).MatchString(build) {
		t.Errorf("build stage must pin a golang:1.x base image, got:\n%s", firstLine(build))
	}
	if strings.Contains(firstLine(build), ":latest") {
		t.Error("build base image must not use the latest tag")
	}
}

func TestDockerfileRuntimeStageIsMinimalNonRoot(t *testing.T) {
	root := repoRoot(t)
	dockerfile := readFile(t, root, "Dockerfile")
	stages := dockerfileStages(t, dockerfile)
	runtime := stages[len(stages)-1]

	// Minimal, pinned base.
	if !regexp.MustCompile(`FROM alpine:3\.\d+`).MatchString(runtime) {
		t.Errorf("runtime stage must use a pinned alpine:3.x base, got:\n%s", firstLine(runtime))
	}
	if strings.Contains(firstLine(runtime), ":latest") {
		t.Error("runtime base image must not use the latest tag")
	}

	// Only ca-certificates and git may be installed at runtime.
	match := regexp.MustCompile(`apk add --no-cache ([^\n\\]+)`).FindStringSubmatch(runtime)
	if match == nil {
		t.Fatal("runtime stage must install packages with `apk add --no-cache`")
	}
	allowed := map[string]bool{"ca-certificates": true, "git": true}
	packages := strings.Fields(strings.TrimSpace(match[1]))
	if len(packages) == 0 {
		t.Fatal("runtime apk add installs no packages")
	}
	for _, pkg := range packages {
		if !allowed[pkg] {
			t.Errorf("runtime stage installs disallowed package %q; only ca-certificates and git are permitted", pkg)
		}
	}

	// A declared, non-root user.
	users := regexp.MustCompile(`(?m)^USER (.+)$`).FindAllStringSubmatch(runtime, -1)
	if len(users) == 0 {
		t.Fatal("runtime stage must declare a USER")
	}
	last := strings.TrimSpace(users[len(users)-1][1])
	uid := last
	if index := strings.IndexByte(last, ':'); index >= 0 {
		uid = last[:index]
	}
	if uid == "root" || uid == "0" || uid == "" {
		t.Errorf("runtime USER must be non-root, got %q", last)
	}
}

func TestDockerfileRuntimeStageHasNoToolchainOrGitHubCLI(t *testing.T) {
	root := repoRoot(t)
	dockerfile := readFile(t, root, "Dockerfile")
	stages := dockerfileStages(t, dockerfile)
	runtime := strings.ToLower(stages[len(stages)-1])

	// No language runtime, package manager for target repositories, cloud CLI, or GitHub CLI
	// may appear in the runtime stage. The build stage legitimately references the Go toolchain
	// and is excluded here.
	forbidden := []string{
		"golang", "nodejs", " npm", "yarn", "python", "py3-pip", "openjdk", "ruby",
		"php", "cargo", "rustc", "aws-cli", "gcloud", "azure-cli", "github-cli",
		"docker-cli", "kubectl", "helm",
	}
	for _, token := range forbidden {
		if strings.Contains(runtime, token) {
			t.Errorf("runtime stage must not reference %q", strings.TrimSpace(token))
		}
	}
}

func TestDockerfileEntrypointIsBinaryNotShell(t *testing.T) {
	root := repoRoot(t)
	dockerfile := readFile(t, root, "Dockerfile")

	line := ""
	for _, candidate := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(strings.TrimSpace(candidate), "ENTRYPOINT") {
			line = strings.TrimSpace(candidate)
		}
	}
	if line == "" {
		t.Fatal("Dockerfile has no ENTRYPOINT")
	}
	raw := strings.TrimSpace(strings.TrimPrefix(line, "ENTRYPOINT"))
	if !strings.HasPrefix(raw, "[") {
		t.Fatalf("ENTRYPOINT must use JSON exec form, got %q", line)
	}
	var elements []string
	if err := json.Unmarshal([]byte(raw), &elements); err != nil {
		t.Fatalf("parse ENTRYPOINT exec form %q: %v", raw, err)
	}
	if len(elements) == 0 || elements[0] != "/usr/local/bin/docify-repo" {
		t.Fatalf("ENTRYPOINT must invoke the binary directly, got %v", elements)
	}
	for _, element := range elements {
		switch element {
		case "/bin/sh", "sh", "/bin/bash", "bash", "-c":
			t.Fatalf("ENTRYPOINT must not wrap a shell, got %v", elements)
		}
	}
}

func TestReleaseWorkflowIsMultiArchWithSupplyChain(t *testing.T) {
	root := repoRoot(t)
	workflow := readFile(t, root, ".github/workflows/release.yml")

	requirements := map[string]string{
		"linux/amd64":            "release must target linux/amd64",
		"linux/arm64":            "release must target linux/arm64",
		"sbom":                   "release must produce an SBOM",
		"provenance":             "release must attach build provenance",
		"cosign":                 "release must sign the image",
		"scan-action":            "release must scan for vulnerabilities",
		"IMAGE_SIZE_LIMIT_BYTES": "release must gate on image size",
		"image-checks.sh":        "release must run the image checks",
	}
	for needle, why := range requirements {
		if !strings.Contains(workflow, needle) {
			t.Errorf("release workflow missing %q: %s", needle, why)
		}
	}

	document := map[string]any{}
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatalf("release workflow is not valid YAML: %v", err)
	}
	// Triggered by version tags only.
	if !strings.Contains(workflow, `"v*"`) && !strings.Contains(workflow, "v*") {
		t.Error("release workflow must trigger on v* tags")
	}
}

func TestReferenceWorkflowNeedsNoGitHubCLIAndProtectsToken(t *testing.T) {
	root := repoRoot(t)
	workflow := readFile(t, root, ".github/workflows/docify-repo.yml")

	document := map[string]any{}
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatalf("reference workflow is not valid YAML: %v", err)
	}

	// No GitHub CLI invocation. Match gh/hub only as standalone command tokens so words like
	// "through" or "GitHub" in prose do not trip the check.
	if regexp.MustCompile(`(?m)(^|\s)(gh|hub)\s`).MatchString(workflow) {
		t.Error("reference workflow must not use the GitHub CLI")
	}

	// The token must only cross into the container as an -e environment reference and be
	// declared in an env block; it must never be embedded in a URL.
	if regexp.MustCompile(`https://[^\s"]*\$\{?[^\s"]*[Tt][Oo][Kk][Ee][Nn]`).MatchString(workflow) {
		t.Error("reference workflow must not embed a token in a URL")
	}
	if !strings.Contains(workflow, "-e DOCIFY_GITHUB_TOKEN") {
		t.Error("reference workflow must pass the token through the container environment")
	}

	// Hardening flags and no-op path avoidance.
	for _, needle := range []string{"--cap-drop ALL", "no-new-privileges", "paths-ignore", "persist-credentials: false"} {
		if !strings.Contains(workflow, needle) {
			t.Errorf("reference workflow missing hardening element %q", needle)
		}
	}
}

func TestWorkflowsAreValidYAML(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{
		".github/workflows/ci.yml",
		".github/workflows/release.yml",
		".github/workflows/docify-repo.yml",
	} {
		document := map[string]any{}
		if err := yaml.Unmarshal([]byte(readFile(t, root, name)), &document); err != nil {
			t.Errorf("%s is not valid YAML: %v", name, err)
		}
	}
}

func TestSecurityDocDeclaresSourceTransmission(t *testing.T) {
	root := repoRoot(t)
	document := strings.ToLower(readFile(t, root, "docs/security.md"))

	if !strings.Contains(document, "transmitted to the configured llm provider") {
		t.Error("security documentation must state that selected source is transmitted to the configured LLM provider")
	}
	if !strings.Contains(document, "source") {
		t.Error("security documentation must describe the source data leaving the repository")
	}
	// A few other properties the doc is expected to cover.
	for _, needle := range []string{"non-root", "sbom", "credential"} {
		if !strings.Contains(document, needle) {
			t.Errorf("security documentation should describe %q", needle)
		}
	}
}

func TestImageChecksScriptCoversInvariants(t *testing.T) {
	root := repoRoot(t)
	relative := "scripts/image-checks.sh"
	script := readFile(t, root, relative)

	if !strings.HasPrefix(script, "#!/bin/sh") {
		t.Error("image-checks.sh must be a POSIX sh script")
	}
	for _, needle := range []string{"id -u", "command -v", "git", "push", "--help", "no-new-privileges"} {
		if !strings.Contains(script, needle) {
			t.Errorf("image-checks.sh must exercise %q", needle)
		}
	}
	info, err := os.Stat(filepath.Join(root, relative))
	if err != nil {
		t.Fatalf("stat %s: %v", relative, err)
	}
	if info.Mode()&0o100 == 0 {
		t.Error("image-checks.sh must be executable")
	}
}

func TestFragmentQualificationUsesLocalBinaryAndForcedStrategy(t *testing.T) {
	root := repoRoot(t)
	script := readFile(t, root, "scripts/qualify-fragments.sh")
	for _, needle := range []string{
		`go -C "$WORKTREE" build`, "worktree add --detach", "DOCIFY_GENERATION_STRATEGY=fragments",
		"DOCIFY_QUALIFICATION_RUNS", "sync --full", `"status":"noop"`, "if ! diff -ru",
		`cp -R "$WORKTREE"`, `rm -f "$destination/.git"`, "snapshot_output",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("fragment qualification runner is missing %q", needle)
		}
	}
	if strings.Contains(script, `cat > "$WORKTREE/.docify.yml"`) || strings.Contains(script, `go -C "$ROOT" build`) {
		t.Error("fragment qualification must preserve repository config and build the detached revision")
	}
	for _, fixedPath := range []string{`cp "$WORKTREE/.docify/state.json"`, `cp -R "$WORKTREE/docs/generated"`} {
		if strings.Contains(script, fixedPath) {
			t.Errorf("fragment qualification artifact capture must not assume %q", fixedPath)
		}
	}
	for _, command := range []string{"docker run", "docker build"} {
		if strings.Contains(strings.ToLower(script), command) {
			t.Errorf("fragment qualification must not invoke %q", command)
		}
	}
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}
