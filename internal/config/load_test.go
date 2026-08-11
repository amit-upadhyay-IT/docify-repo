package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesDefaultsWithoutFile(t *testing.T) {
	cfg, err := load(t.TempDir(), emptyEnvironment)
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if !filepath.IsAbs(cfg.WorkingDirectory) {
		t.Errorf("WorkingDirectory = %q, want absolute path", cfg.WorkingDirectory)
	}
	if cfg.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", cfg.Version, CurrentVersion)
	}
	if cfg.DocsDir != "docs/generated" || cfg.StatePath != ".docify/state.json" {
		t.Errorf("output paths = %q, %q, want defaults", cfg.DocsDir, cfg.StatePath)
	}
	if len(cfg.Source.Include) != 1 || cfg.Source.Include[0] != "**/*" {
		t.Errorf("Source.Include = %v, want default", cfg.Source.Include)
	}
	if !cfg.Source.Tests.IncludeAsContext || cfg.Source.Tests.TriggerOnChange {
		t.Errorf("Source.Tests = %+v, want context-only defaults", cfg.Source.Tests)
	}
	if cfg.LLM.MaxResponseBytes != 65_536 {
		t.Errorf("LLM.MaxResponseBytes = %d, want 65536", cfg.LLM.MaxResponseBytes)
	}
	if cfg.Documentation.GenerationStrategy != "dossier" || cfg.LLM.FragmentCallLimit != 80 || cfg.LLM.FragmentSplitDepth != 3 {
		t.Errorf("fragment defaults = %q / %d / %d, want dossier / 80 / 3", cfg.Documentation.GenerationStrategy, cfg.LLM.FragmentCallLimit, cfg.LLM.FragmentSplitDepth)
	}
}

func TestLoadAcceptsAutoGenerationStrategy(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, "version: 1\ndocumentation:\n  generation_strategy: auto\n")
	cfg, err := load(directory, emptyEnvironment)
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if cfg.Documentation.GenerationStrategy != "auto" {
		t.Fatalf("generation strategy = %q, want auto", cfg.Documentation.GenerationStrategy)
	}
}

func TestLoadAppliesFileThenEnvironment(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, `
version: 1
docs_dir: docs/from-file
report_path: reports/file.json
source:
  max_file_bytes: 2048
  tests:
    trigger_on_change: true
  generated:
    include: true
  role_overrides:
    - pattern: "features/**"
      role: contract
llm:
  model: file-model
publishing:
  provider: github-pr
`)
	environment := map[string]string{
		"DOCIFY_DOCS_DIR":            "docs/from-env",
		"DOCIFY_LLM_MODEL":           "env-model",
		"DOCIFY_GENERATION_STRATEGY": "fragments",
		"DOCIFY_BASE_SHA":            "base",
		"DOCIFY_HEAD_SHA":            "head",
		"DOCIFY_LLM_API_KEY":         "secret-value",
		"DOCIFY_GITHUB_REPOSITORY":   "owner/repository",
	}

	cfg, err := load(directory, mapEnvironment(environment))
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if cfg.DocsDir != "docs/from-env" {
		t.Errorf("DocsDir = %q, want environment value", cfg.DocsDir)
	}
	if cfg.ReportPath != "reports/file.json" {
		t.Errorf("ReportPath = %q, want file value", cfg.ReportPath)
	}
	if cfg.LLM.Model != "env-model" {
		t.Errorf("LLM.Model = %q, want environment value", cfg.LLM.Model)
	}
	if cfg.Documentation.GenerationStrategy != "fragments" {
		t.Errorf("Documentation.GenerationStrategy = %q, want environment value", cfg.Documentation.GenerationStrategy)
	}
	if cfg.Source.MaxFileBytes != 2048 || !cfg.Source.Tests.TriggerOnChange {
		t.Errorf("Source = %+v, want merged file values", cfg.Source)
	}
	if !cfg.Source.Generated.IncludeAsContext {
		t.Error("source.generated.include alias was not applied")
	}
	if len(cfg.Source.Exclude) == 0 {
		t.Error("default source exclusions were not preserved")
	}
	if !cfg.Runtime.LLMCredentialPresent {
		t.Error("LLM credential presence = false, want true")
	}
	if cfg.Runtime.BaseSHA != "base" || cfg.Runtime.HeadSHA != "head" {
		t.Errorf("runtime range = %q..%q, want base..head", cfg.Runtime.BaseSHA, cfg.Runtime.HeadSHA)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, "version: 1\nunknown_key: value\n")

	_, err := load(directory, emptyEnvironment)
	if err == nil || !strings.Contains(err.Error(), "field unknown_key not found") {
		t.Fatalf("load() error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, "version: 2\n")

	_, err := load(directory, emptyEnvironment)
	if err == nil || !strings.Contains(err.Error(), "unsupported value 2") {
		t.Fatalf("load() error = %v, want version error", err)
	}
}

func TestLoadRejectsInvalidNestedValue(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, "version: 1\nllm:\n  timeout: forever\n")

	_, err := load(directory, emptyEnvironment)
	if err == nil || !strings.Contains(err.Error(), "llm.timeout") {
		t.Fatalf("load() error = %v, want timeout validation error", err)
	}
}

func TestLoadRejectsInvalidMaxResponseBytes(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, "version: 1\nllm:\n  max_response_bytes: 0\n")

	_, err := load(directory, emptyEnvironment)
	if err == nil || !strings.Contains(err.Error(), "llm.max_response_bytes") {
		t.Fatalf("load() error = %v, want response-byte validation error", err)
	}
}

func TestLoadRejectsInvalidFragmentPolicy(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "strategy", content: "version: 1\ndocumentation:\n  generation_strategy: adaptive\n", want: "generation_strategy"},
		{name: "call limit", content: "version: 1\nllm:\n  fragment_call_limit_per_component: 0\n", want: "fragment_call_limit_per_component"},
		{name: "split depth", content: "version: 1\nllm:\n  fragment_split_depth: -1\n", want: "fragment_split_depth"},
		{name: "split depth maximum", content: "version: 1\nllm:\n  fragment_split_depth: 17\n", want: "fragment_split_depth"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeConfig(t, directory, test.content)
			_, err := load(directory, emptyEnvironment)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadAcceptsEmptyConfigFile(t *testing.T) {
	directory := t.TempDir()
	writeConfig(t, directory, "")
	if _, err := load(directory, emptyEnvironment); err != nil {
		t.Fatalf("load() error = %v", err)
	}
}

func TestLoadRejectsInvalidComponentLimitsAndRoots(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "batch larger than context", content: "version: 1\ncomponents:\n  max_context_bytes: 10\n  max_batch_bytes: 11\n", want: "max_batch_bytes"},
		{name: "duplicate root", content: "version: 1\ncomponents:\n  roots: [services/api, services/api]\n", want: "duplicate root"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeConfig(t, directory, test.content)
			_, err := load(directory, emptyEnvironment)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	cfg := defaults()
	if cfg.WorkingDirectory != "." {
		t.Fatalf("WorkingDirectory = %q, want %q", cfg.WorkingDirectory, ".")
	}
	if cfg.Components.MaxRequestBytes != 500_000 {
		t.Fatalf("Components.MaxRequestBytes = %d, want 500000", cfg.Components.MaxRequestBytes)
	}
}

func writeConfig(t *testing.T, directory, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, configFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func emptyEnvironment(string) (string, bool) {
	return "", false
}

func mapEnvironment(values map[string]string) environmentLookup {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}
