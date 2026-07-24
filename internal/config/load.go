package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const configFileName = ".docify.yml"

type environmentLookup func(string) (string, bool)

// Load applies built-in defaults, .docify.yml, and supported environment
// variables in precedence order. CLI flags are applied by the transport.
func Load() (Config, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("resolve working directory: %w", err)
	}
	return load(workingDirectory, os.LookupEnv)
}

func load(workingDirectory string, lookup environmentLookup) (Config, error) {
	absoluteWorkingDirectory, err := filepath.Abs(workingDirectory)
	if err != nil {
		return Config{}, fmt.Errorf("resolve working directory: %w", err)
	}

	cfg := defaults()
	cfg.WorkingDirectory = filepath.Clean(absoluteWorkingDirectory)
	configPath := filepath.Join(cfg.WorkingDirectory, configFileName)
	if err := decodeFile(configPath, &cfg); err != nil {
		return Config{}, err
	}
	applyAliases(&cfg)
	applyEnvironment(&cfg, lookup)
	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("configuration: %w", err)
	}
	return cfg, nil
}

func applyAliases(cfg *Config) {
	for _, behavior := range []*SourceBehavior{&cfg.Source.Tests, &cfg.Source.Generated, &cfg.Source.Fixtures} {
		if behavior.Include != nil {
			behavior.IncludeAsContext = *behavior.Include
			behavior.Include = nil
		}
	}
}

func decodeFile(path string, cfg *Config) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", configFileName, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("decode %s: %w", configFileName, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: multiple YAML documents are not supported", configFileName)
		}
		return fmt.Errorf("decode %s: %w", configFileName, err)
	}
	return nil
}

func applyEnvironment(cfg *Config, lookup environmentLookup) {
	overrideString(lookup, "DOCIFY_DOCS_DIR", &cfg.DocsDir)
	overrideString(lookup, "DOCIFY_STATE_PATH", &cfg.StatePath)
	overrideString(lookup, "DOCIFY_REPORT_PATH", &cfg.ReportPath)
	overrideString(lookup, "DOCIFY_LLM_BASE_URL", &cfg.LLM.BaseURL)
	overrideString(lookup, "DOCIFY_LLM_MODEL", &cfg.LLM.Model)
	overrideString(lookup, "DOCIFY_PUBLISHER", &cfg.Publishing.Provider)
	overrideString(lookup, "DOCIFY_BASE_SHA", &cfg.Runtime.BaseSHA)
	overrideString(lookup, "DOCIFY_HEAD_SHA", &cfg.Runtime.HeadSHA)
	overrideString(lookup, "DOCIFY_GITHUB_REPOSITORY", &cfg.Runtime.GitHubRepository)
	overrideString(lookup, "DOCIFY_BASE_BRANCH", &cfg.Runtime.BaseBranch)
	cfg.Runtime.LLMCredentialPresent = environmentPresent(lookup, "DOCIFY_LLM_API_KEY")
	cfg.Runtime.GitHubCredentialPresent = environmentPresent(lookup, "DOCIFY_GITHUB_TOKEN")
}

func overrideString(lookup environmentLookup, name string, destination *string) {
	if value, ok := lookup(name); ok {
		*destination = strings.TrimSpace(value)
	}
}

func environmentPresent(lookup environmentLookup, name string) bool {
	value, ok := lookup(name)
	return ok && strings.TrimSpace(value) != ""
}

func validate(cfg Config) error {
	if cfg.Version != CurrentVersion {
		return fmt.Errorf("version: unsupported value %d (want %d)", cfg.Version, CurrentVersion)
	}
	if strings.TrimSpace(cfg.DocsDir) == "" {
		return fmt.Errorf("docs_dir: must not be empty")
	}
	if strings.TrimSpace(cfg.StatePath) == "" {
		return fmt.Errorf("state_path: must not be empty")
	}
	if len(cfg.Source.Include) == 0 {
		return fmt.Errorf("source.include: must contain at least one pattern")
	}
	if cfg.Source.MaxFileBytes <= 0 {
		return fmt.Errorf("source.max_file_bytes: must be greater than zero")
	}
	if cfg.Components.Strategy != "inferred" && cfg.Components.Strategy != "explicit" {
		return fmt.Errorf("components.strategy: must be %q or %q", "inferred", "explicit")
	}
	if cfg.Components.Strategy == "explicit" && len(cfg.Components.Roots) == 0 {
		return fmt.Errorf("components.roots: must not be empty for explicit strategy")
	}
	componentLimits := []struct {
		name  string
		value int64
	}{
		{name: "max_context_bytes", value: cfg.Components.MaxContextBytes},
		{name: "max_batch_bytes", value: cfg.Components.MaxBatchBytes},
		{name: "max_supporting_bytes", value: cfg.Components.MaxSupportingBytes},
		{name: "max_manifest_bytes", value: cfg.Components.MaxManifestBytes},
		{name: "max_diff_bytes", value: cfg.Components.MaxDiffBytes},
		{name: "max_request_bytes", value: cfg.Components.MaxRequestBytes},
	}
	for _, limit := range componentLimits {
		if limit.value <= 0 {
			return fmt.Errorf("components.%s: must be greater than zero", limit.name)
		}
	}
	if cfg.Components.MaxBatchBytes > cfg.Components.MaxContextBytes {
		return fmt.Errorf("components.max_batch_bytes: must not exceed max_context_bytes")
	}
	seenRoots := make(map[string]struct{}, len(cfg.Components.Roots))
	for index, root := range cfg.Components.Roots {
		root = strings.TrimSpace(root)
		if root == "" {
			return fmt.Errorf("components.roots[%d]: must not be empty", index)
		}
		if _, exists := seenRoots[root]; exists {
			return fmt.Errorf("components.roots[%d]: duplicate root %q", index, root)
		}
		seenRoots[root] = struct{}{}
	}
	if cfg.Documentation.Profile != "codebase-summary" {
		return fmt.Errorf("documentation.profile: unsupported value %q", cfg.Documentation.Profile)
	}
	switch cfg.Documentation.Audience {
	case "mixed", "human", "ai-assistant":
	default:
		return fmt.Errorf("documentation.audience: unsupported value %q", cfg.Documentation.Audience)
	}
	if cfg.LLM.Provider != "openai-compatible" {
		return fmt.Errorf("llm.provider: unsupported value %q", cfg.LLM.Provider)
	}
	if cfg.LLM.APIMode != "chat_completions" && cfg.LLM.APIMode != "responses" {
		return fmt.Errorf("llm.api_mode: unsupported value %q", cfg.LLM.APIMode)
	}
	if cfg.LLM.Temperature < 0 || cfg.LLM.Temperature > 2 {
		return fmt.Errorf("llm.temperature: must be between 0 and 2")
	}
	if cfg.LLM.MaxOutputTokens <= 0 {
		return fmt.Errorf("llm.max_output_tokens: must be greater than zero")
	}
	switch cfg.LLM.StructuredOutputMode {
	case "auto", "json_schema", "prompt_json":
	default:
		return fmt.Errorf("llm.structured_output_mode: unsupported value %q", cfg.LLM.StructuredOutputMode)
	}
	timeout, err := time.ParseDuration(cfg.LLM.Timeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("llm.timeout: must be a positive duration")
	}
	if cfg.LLM.Retries < 0 {
		return fmt.Errorf("llm.retries: must not be negative")
	}
	if cfg.LLM.Concurrency <= 0 {
		return fmt.Errorf("llm.concurrency: must be greater than zero")
	}
	if cfg.Publishing.Provider != "worktree" && cfg.Publishing.Provider != "github-pr" {
		return fmt.Errorf("publishing.provider: must be %q or %q", "worktree", "github-pr")
	}
	return nil
}
