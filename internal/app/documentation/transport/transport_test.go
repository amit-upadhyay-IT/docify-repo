package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	documentationmodel "docify-repo/internal/app/documentation/model"
	"docify-repo/internal/config"
)

func TestCommandHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "root", args: []string{"--help"}, want: "Generate and synchronize repository documentation"},
		{name: "sync", args: []string{"sync", "--help"}, want: "Synchronize generated documentation"},
		{name: "check", args: []string{"check", "--help"}, want: "Check whether generated documentation is current"},
		{name: "plan", args: []string{"plan", "--help"}, want: "Plan documentation changes without generating output"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			application := New(config.Config{WorkingDirectory: t.TempDir()}, &stdout, &stderr)
			command := application.command()
			command.SetArgs(test.args)

			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("ExecuteContext() error = %v", err)
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Errorf("stdout = %q, want text %q", stdout.String(), test.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestCommandDispatchesThroughHandler(t *testing.T) {
	application := New(config.Config{WorkingDirectory: t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{})
	command := application.command()
	command.SetArgs([]string{"sync"})

	// A bare working directory has no configured docs_dir, so sync dispatches through the
	// handler and usecase and returns a typed source/configuration error (exit code 4).
	err := command.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docs_dir") {
		t.Fatalf("ExecuteContext() error = %v, want a dispatched source policy error", err)
	}
	if ExitCode(err) != 4 {
		t.Fatalf("ExitCode() = %d, want 4", ExitCode(err))
	}
}

func TestExitCodeFindsWrappedTypedError(t *testing.T) {
	application := New(config.Config{WorkingDirectory: t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{})
	command := application.command()
	command.SetArgs([]string{"sync"})
	err := command.ExecuteContext(context.Background())

	if got := ExitCode(fmt.Errorf("command failed: %w", err)); got != 4 {
		t.Fatalf("ExitCode() = %d, want 4", got)
	}
}

func TestCommandRejectsIncompleteRangeBeforeUsecase(t *testing.T) {
	application := New(config.Config{WorkingDirectory: t.TempDir()}, &bytes.Buffer{}, &bytes.Buffer{})
	command := application.command()
	command.SetArgs([]string{"check", "--base-sha", "abc123"})

	err := command.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "must be supplied together") {
		t.Fatalf("ExecuteContext() error = %v, want incomplete range error", err)
	}
}

func TestPlanProducesCredentialFreeJSONScan(t *testing.T) {
	directory := t.TempDir()
	runTestGit(t, directory, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("# Repository\n"), 0o600); err != nil {
		t.Fatalf("write prose: %v", err)
	}
	runTestGit(t, directory, "add", "--all")

	cfg := config.Config{
		WorkingDirectory: directory,
		DocsDir:          "docs/generated",
		StatePath:        ".docify/state.json",
		Source: config.SourceConfig{
			Include:      []string{"**/*"},
			MaxFileBytes: 1024,
			Tests:        config.SourceBehavior{IncludeAsContext: true},
		},
		Components: config.ComponentsConfig{
			Strategy: "inferred", MaxContextBytes: 120_000, MaxBatchBytes: 80_000,
			MaxSupportingBytes: 20_000, MaxManifestBytes: 20_000, MaxDiffBytes: 40_000, MaxRequestBytes: 200_000,
		},
		Documentation: config.DocumentationConfig{Profile: "codebase-summary", Audience: "mixed", Mermaid: true, GenerationStrategy: "dossier"},
		LLM: config.LLMConfig{
			Provider: "openai-compatible", APIMode: "chat_completions", MaxOutputTokens: 8192, MaxResponseBytes: 65_536,
			StructuredOutputMode: "auto", FragmentCallLimit: 80,
		},
		Publishing: config.PublishingConfig{Provider: "worktree"},
	}
	var stdout bytes.Buffer
	application := New(cfg, &stdout, &bytes.Buffer{})
	command := application.command()
	command.SetArgs([]string{"plan", "--output", "json"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}

	var result documentationmodel.ResultSummary
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode stdout: %v: %s", err, stdout.String())
	}
	if result.Status != "plan_complete" || result.TrackedPaths != 2 || result.IncludedPaths != 2 || result.TriggeringPaths != 1 {
		t.Errorf("result = %+v, want completed two-file plan", result)
	}
	if result.Plan.Mode != "full" || result.Plan.FullReason != "state_missing" || result.Plan.Calls.Primary != 1 {
		t.Errorf("plan = %+v, want one-call full bootstrap", result.Plan)
	}
	if strings.Contains(stdout.String(), directory) {
		t.Error("JSON output contains machine-specific repository path")
	}
}

func TestEndpointIdentityHashNormalizesWithoutExposingEndpoint(t *testing.T) {
	first := endpointIdentityHash(" HTTPS://Example.COM/v1/ ")
	second := endpointIdentityHash("https://example.com/v1")
	if first != second {
		t.Fatalf("normalized endpoint hashes differ: %q != %q", first, second)
	}
	if first == endpointIdentityHash("https://example.com/v2") {
		t.Fatal("different endpoint paths should have different hashes")
	}
	if strings.Contains(first, "example.com") {
		t.Fatalf("endpoint hash exposes endpoint: %q", first)
	}
}

func TestHandleAskpassAnswersCredentialPrompts(t *testing.T) {
	environment := map[string]string{
		"DOCIFY_INTERNAL_ASKPASS": "1",
		"DOCIFY_GITHUB_TOKEN":     "secret-token",
	}
	lookup := func(name string) (string, bool) {
		value, ok := environment[name]
		return value, ok
	}

	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "username", prompt: "Username for 'https://github.com':", want: "x-access-token\n"},
		{name: "password", prompt: "Password for 'https://github.com':", want: "secret-token\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			code, handled := HandleAskpass([]string{"docify-repo", test.prompt}, lookup, &out)
			if !handled || code != 0 {
				t.Fatalf("HandleAskpass() = (%d, %t), want handled with code 0", code, handled)
			}
			if out.String() != test.want {
				t.Errorf("askpass output = %q, want %q", out.String(), test.want)
			}
		})
	}

	// Without the marker, a normal command invocation is not treated as an askpass callback.
	if code, handled := HandleAskpass([]string{"docify-repo", "sync"}, func(string) (string, bool) { return "", false }, &bytes.Buffer{}); handled || code != 0 {
		t.Errorf("HandleAskpass() = (%d, %t) for a normal invocation, want (0, false)", code, handled)
	}
}

func runTestGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", arguments[0], err, output)
	}
}
