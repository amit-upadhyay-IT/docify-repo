package usecase_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	documentationmodel "docify-repo/internal/app/documentation/model"
	"docify-repo/internal/app/documentation/usecase"
	sharedmodel "docify-repo/internal/model"
	filesystemrepository "docify-repo/internal/repository/filesystem"
	gitrepository "docify-repo/internal/repository/git"
)

// scriptedGenerator returns a deterministic valid dossier for every request, citing the
// first path in the request's allowed evidence set so local reference validation passes.
// It counts calls so tests can assert idempotent no-LLM behavior, and can be switched to
// always return invalid JSON to exercise failure paths.
type scriptedGenerator struct {
	calls   atomic.Int64
	invalid bool
	// variant is appended to the purpose so a regenerated component produces different
	// bytes on demand, letting incremental tests observe which documents actually change.
	// It is set before a Sync/Check call and read by worker goroutines the call starts, so
	// goroutine creation provides the happens-before edge (no data race).
	variant string
}

func (g *scriptedGenerator) Generate(_ context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	g.calls.Add(1)
	if g.invalid {
		return sharedmodel.GenerationResponse{Body: []byte(`{"not":"a dossier"}`), FinishReason: "stop"}, nil
	}
	payload := decodeAllowed(request)
	evidence := "unknown"
	if len(payload.Repository.AllowedEvidencePaths) > 0 {
		evidence = payload.Repository.AllowedEvidencePaths[0]
	}
	purpose := "Deterministic test dossier for the component."
	if g.variant != "" {
		purpose += " Revision " + g.variant + "."
	}
	dossier := sharedmodel.ComponentDossier{
		Title:       "Component " + payload.Target.ComponentKey,
		Purpose:     purpose,
		SourcePaths: []string{evidence},
		Architecture: []sharedmodel.ArchitectureItem{{
			Title: "Primary responsibility", Description: "Owns behavior in the component.",
			SourcePaths: []string{evidence},
		}},
		Interfaces:   []sharedmodel.InterfaceItem{},
		DataModels:   []sharedmodel.DataModelItem{},
		Workflows:    []sharedmodel.WorkflowItem{},
		Dependencies: []sharedmodel.DependencyItem{},
		ReviewGaps:   []sharedmodel.ReviewGap{},
		Diagrams: []sharedmodel.Diagram{{
			Type: sharedmodel.DiagramFlowchart, Title: "Flow", SourcePaths: []string{evidence},
			Nodes: []sharedmodel.FlowchartNode{{Key: "a", Label: "Start"}, {Key: "b", Label: "End"}},
			Edges: []sharedmodel.FlowchartEdge{{From: "a", To: "b", Label: "next"}},
		}},
	}
	body, err := json.Marshal(dossier)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	return sharedmodel.GenerationResponse{Body: body, FinishReason: "stop", Usage: sharedmodel.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, Present: true}}, nil
}

// requestView mirrors the fields the generator needs from the JSON payload.
type requestView struct {
	Target struct {
		ComponentKey string `json:"component_key"`
	} `json:"target"`
	Repository struct {
		AllowedEvidencePaths []string `json:"allowed_evidence_paths"`
	} `json:"repository"`
}

func decodeAllowed(request sharedmodel.GenerationRequest) requestView {
	var view requestView
	for _, message := range request.Messages {
		start := strings.IndexByte(message.Content, '{')
		if start < 0 {
			continue
		}
		if err := json.Unmarshal([]byte(message.Content[start:]), &view); err == nil && view.Target.ComponentKey != "" {
			return view
		}
	}
	return view
}

func newSyncTestbed(t *testing.T) (*usecase.Usecase, *scriptedGenerator, string) {
	t.Helper()
	dir := gitTempDir(t)
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	hardenRepo(t, dir)
	writeFile(t, dir, "go.mod", "module example.test/app\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "services/api/service.go", "package api\n\nfunc Handle() {}\n")
	writeFile(t, dir, "services/api/service_test.go", "package api\n")
	runGit(t, dir, "add", "--all")

	generator := &scriptedGenerator{}
	application := usecase.New(
		gitrepository.New(gitrepository.Options{WorkingDirectory: dir}),
		filesystemrepository.NewSourceRepository(),
		filesystemrepository.NewStateRepository(),
		generator,
		filesystemrepository.NewOutputRepository(),
	)
	return application, generator, dir
}

func syncInput(dir string) documentationmodel.SyncInput {
	return documentationmodel.SyncInput{
		WorkingDirectory: dir,
		Output:           documentationmodel.OutputModeHuman,
		Publisher:        "worktree",
		SourcePolicy: documentationmodel.SourcePolicy{
			DocsDir:      "docs/generated",
			StatePath:    ".docify/state.json",
			Include:      []string{"**/*"},
			MaxFileBytes: 1 << 20,
			Tests:        documentationmodel.SourceBehavior{IncludeAsContext: true},
		},
		ComponentPolicy: documentationmodel.ComponentPolicy{
			Strategy: "inferred", MaxContextBytes: 120_000, MaxBatchBytes: 80_000,
			MaxSupportingBytes: 20_000, MaxManifestBytes: 20_000, MaxDiffBytes: 40_000, MaxRequestBytes: 200_000,
		},
		GenerationPolicy: documentationmodel.GenerationPolicy{
			Profile: "codebase-summary", Audience: "mixed", Mermaid: true,
			Provider: "openai-compatible", APIMode: "chat_completions", Model: "test-model",
			Temperature: 0, MaxOutputTokens: 8192, StructuredOutputMode: "auto",
		},
	}
}

func TestSyncBootstrapInstallsKnowledgeBase(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)

	result, err := application.Sync(context.Background(), syncInput(dir))
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Status != "synced" || result.Generation == nil {
		t.Fatalf("result = %+v, want a synced generation outcome", result)
	}
	if generator.calls.Load() == 0 {
		t.Fatal("expected the generator to be called during bootstrap")
	}

	required := []string{
		"docs/generated/index.md", "docs/generated/codebase_info.md", "docs/generated/components.md",
		"docs/generated/architecture.md", "docs/generated/interfaces.md", "docs/generated/data_models.md",
		"docs/generated/workflows.md", "docs/generated/dependencies.md", "docs/generated/review_notes.md",
		"docs/generated/components/@root/index.md", "docs/generated/components/services/api/index.md",
		".docify/state.json",
	}
	for _, relative := range required {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(relative))); err != nil {
			t.Errorf("missing generated file %q: %v", relative, err)
		}
	}

	// No generated file may leak the absolute working directory.
	for _, relative := range required {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relative)))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), dir) {
			t.Errorf("generated file %q leaks the absolute repository path", relative)
		}
	}

	// The transaction working directory must be cleaned up after success.
	if _, err := os.Stat(filepath.Join(dir, ".docify", "tx")); !os.IsNotExist(err) {
		t.Errorf("transaction directory was not cleaned up: %v", err)
	}

	// State is valid JSON with the expected component set.
	stateData, err := os.ReadFile(filepath.Join(dir, ".docify", "state.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var state sharedmodel.State
	if err := json.Unmarshal(stateData, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if _, ok := state.Components["@root"]; !ok {
		t.Errorf("state missing @root component: %+v", state.Components)
	}
	if _, ok := state.Components["services/api"]; !ok {
		t.Errorf("state missing services/api component: %+v", state.Components)
	}
}

func TestSyncSecondRunIsNoopWithoutGeneration(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	if _, err := application.Sync(context.Background(), syncInput(dir)); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	before := snapshotTree(t, filepath.Join(dir, "docs", "generated"))
	callsAfterFirst := generator.calls.Load()

	result, err := application.Sync(context.Background(), syncInput(dir))
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if result.Status != "noop" {
		t.Fatalf("second sync status = %q, want noop", result.Status)
	}
	if generator.calls.Load() != callsAfterFirst {
		t.Fatalf("second sync made %d additional generator calls, want 0", generator.calls.Load()-callsAfterFirst)
	}
	after := snapshotTree(t, filepath.Join(dir, "docs", "generated"))
	if len(before) != len(after) {
		t.Fatalf("second sync changed the generated file set: %d vs %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("second sync changed %q", path)
		}
	}
}

func TestSyncDeterministicAcrossRepositories(t *testing.T) {
	first, _, dirA := newSyncTestbed(t)
	if _, err := first.Sync(context.Background(), syncInput(dirA)); err != nil {
		t.Fatalf("Sync() A error = %v", err)
	}
	second, _, dirB := newSyncTestbed(t)
	if _, err := second.Sync(context.Background(), syncInput(dirB)); err != nil {
		t.Fatalf("Sync() B error = %v", err)
	}
	treeA := snapshotTree(t, filepath.Join(dirA, "docs", "generated"))
	treeB := snapshotTree(t, filepath.Join(dirB, "docs", "generated"))
	if len(treeA) != len(treeB) {
		t.Fatalf("document counts differ: %d vs %d", len(treeA), len(treeB))
	}
	for path, content := range treeA {
		if treeB[path] != content {
			t.Errorf("document %q differs between identical repositories", path)
		}
	}
}

func TestSyncFailedGenerationLeavesOutputUnchanged(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	if _, err := application.Sync(context.Background(), syncInput(dir)); err != nil {
		t.Fatalf("bootstrap Sync() error = %v", err)
	}
	before := snapshotTree(t, filepath.Join(dir, "docs", "generated"))
	beforeState := readFileString(t, filepath.Join(dir, ".docify", "state.json"))

	// Change a source file so the next sync is not a no-op, then make generation fail.
	writeFile(t, dir, "services/api/service.go", "package api\n\nfunc Handle() { /* changed */ }\n")
	runGit(t, dir, "add", "--all")
	generator.invalid = true

	_, err := application.Sync(context.Background(), syncInput(dir))
	if err == nil {
		t.Fatal("expected the failed generation to return an error")
	}

	after := snapshotTree(t, filepath.Join(dir, "docs", "generated"))
	for path, content := range before {
		if after[path] != content {
			t.Errorf("failed sync altered installed document %q", path)
		}
	}
	if len(after) != len(before) {
		t.Errorf("failed sync changed the installed file set")
	}
	if got := readFileString(t, filepath.Join(dir, ".docify", "state.json")); got != beforeState {
		t.Error("failed sync altered installed state")
	}
	if _, err := os.Stat(filepath.Join(dir, ".docify", "tx")); !os.IsNotExist(err) {
		t.Errorf("failed sync left a transaction directory: %v", err)
	}
}

func TestSyncRefusesToOverwriteHandwrittenOutput(t *testing.T) {
	application, _, dir := newSyncTestbed(t)
	// A handwritten file exists under the generated root with no prior state.
	writeFile(t, dir, "docs/generated/handwritten.md", "# Do not delete\n")

	_, err := application.Sync(context.Background(), syncInput(dir))
	if err == nil {
		t.Fatal("expected sync to refuse installing over unowned output")
	}
	if !strings.Contains(err.Error(), "ownership") && !strings.Contains(err.Error(), "prove") {
		t.Fatalf("error = %v, want an ownership-recovery failure", err)
	}
	if got := readFileString(t, filepath.Join(dir, "docs", "generated", "handwritten.md")); got != "# Do not delete\n" {
		t.Error("sync altered the handwritten file")
	}
}

// ---- helpers -----------------------------------------------------------------

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append(hardenedGitArgs(dir), args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, output)
	}
}

// hardenedGitArgs disables background Git maintenance in test repositories so no gc,
// commit-graph, or fsmonitor process lingers to race t.TempDir cleanup.
func hardenedGitArgs(dir string) []string {
	return []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.fsmonitor=false", "-C", dir}
}

// gitTempDir is a temporary directory for a Git repository whose cleanup tolerates the brief
// window in which a just-finished push leaves a receive-pack object quarantine behind. The
// built-in t.TempDir cleanup fails hard on that transient directory; this one retries.
func gitTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "docify-git-")
	if err != nil {
		t.Fatalf("create temporary git directory: %v", err)
	}
	t.Cleanup(func() {
		for attempt := 0; attempt < 40; attempt++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

// hardenRepo persists the maintenance-off configuration into a repository so operations run
// by the production code (which does not pass the -c flags) also spawn no background gc,
// including receive-side auto-gc on a bare push target.
func hardenRepo(t *testing.T, dir string) {
	t.Helper()
	for key, value := range map[string]string{
		"gc.auto":          "0",
		"maintenance.auto": "false",
		"core.fsmonitor":   "false",
		"receive.autogc":   "false",
	} {
		runGit(t, dir, "config", key, value)
	}
}

func writeFile(t *testing.T, dir, relative, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", relative, err)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(data)
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(relative)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot tree: %v", err)
	}
	return tree
}
