package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	calls                atomic.Int64
	fragments            atomic.Int64
	synthesis            atomic.Int64
	inFlight             atomic.Int64
	maximum              atomic.Int64
	delay                time.Duration
	invalid              bool
	diagramInvalid       bool
	truncated            bool
	truncateDossier      bool
	truncateArchitecture bool
	mutate               func()
	mutateOnce           sync.Once
	// variant is appended to the purpose so a regenerated component produces different
	// bytes on demand, letting incremental tests observe which documents actually change.
	// It is set before a Sync/Check call and read by worker goroutines the call starts, so
	// goroutine creation provides the happens-before edge (no data race).
	variant string
}

func (g *scriptedGenerator) Generate(ctx context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	g.calls.Add(1)
	current := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	for {
		maximum := g.maximum.Load()
		if current <= maximum || g.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	if g.delay > 0 {
		select {
		case <-ctx.Done():
			return sharedmodel.GenerationResponse{}, ctx.Err()
		case <-time.After(g.delay):
		}
	}
	if request.Kind == sharedmodel.RequestFragment {
		g.fragments.Add(1)
	}
	if request.Kind == sharedmodel.RequestSynthesis {
		g.synthesis.Add(1)
	}
	if g.mutate != nil {
		g.mutateOnce.Do(g.mutate)
	}
	if g.truncateArchitecture && request.Kind == sharedmodel.RequestFragment && request.FragmentKind == sharedmodel.FragmentArchitecture {
		return sharedmodel.GenerationResponse{}, &sharedmodel.CompletionError{
			Category: sharedmodel.CompletionFailureTruncated, RequestKind: request.Kind, ComponentKey: request.ComponentKey,
			FragmentKind: request.FragmentKind, SourceBatchIndex: request.SourceBatchIndex, SourceBatchCount: request.SourceBatchCount,
			SourceChunkIndex: request.SourceChunkIndex, SourceChunkCount: request.SourceChunkCount, SourceSplitPath: request.SourceSplitPath,
			FinishReason: "length", StructuredOutputUsed: sharedmodel.StructuredOutputJSONSchema, TransportAttempts: 1,
		}
	}
	if g.truncated || g.truncateDossier && request.Kind == sharedmodel.RequestComponent {
		return sharedmodel.GenerationResponse{}, &sharedmodel.CompletionError{
			Category: sharedmodel.CompletionFailureTruncated, RequestKind: request.Kind,
			ComponentKey: request.ComponentKey, BatchIndex: request.BatchIndex, BatchCount: request.BatchCount,
			FragmentKind: request.FragmentKind, SourceBatchIndex: request.SourceBatchIndex, SourceBatchCount: request.SourceBatchCount,
			SourceChunkIndex: request.SourceChunkIndex, SourceChunkCount: request.SourceChunkCount,
			SourceSplitPath: request.SourceSplitPath,
			FinishReason:    "length", ProviderRequestID: "safe-request-id",
			StructuredOutputUsed: sharedmodel.StructuredOutputJSONSchema, TransportAttempts: 1,
		}
	}
	if g.invalid {
		return sharedmodel.GenerationResponse{Body: []byte(`{"not":"a dossier"}`), FinishReason: "stop"}, nil
	}
	payload := decodeAllowed(request)
	evidence := "unknown"
	if len(payload.Repository.AllowedEvidencePaths) > 0 {
		evidence = payload.Repository.AllowedEvidencePaths[0]
	}
	if request.Kind == sharedmodel.RequestFragment && len(payload.SourceFiles) > 0 {
		evidence = payload.SourceFiles[0].Path
	}
	if request.Kind == sharedmodel.RequestOverview {
		return sharedmodel.GenerationResponse{Body: []byte(`{"title":"Fragment component","purpose":"Deterministic fragment documentation."}`), FinishReason: "stop"}, nil
	}
	if request.Kind == sharedmodel.RequestDiagram {
		if g.diagramInvalid {
			return sharedmodel.GenerationResponse{Body: []byte(`{}`), FinishReason: "stop"}, nil
		}
		return sharedmodel.GenerationResponse{Body: []byte(`{"complete":true,"items":[]}`), FinishReason: "stop"}, nil
	}
	if request.Kind == sharedmodel.RequestFragment || request.Kind == sharedmodel.RequestRepair && request.FragmentKind != "" {
		if g.diagramInvalid && request.FragmentKind == sharedmodel.FragmentDiagrams {
			return sharedmodel.GenerationResponse{Body: []byte(`{}`), FinishReason: "stop"}, nil
		}
		var body []byte
		var err error
		switch request.FragmentKind {
		case sharedmodel.FragmentOverviewCandidate:
			body, err = json.Marshal(sharedmodel.OverviewCandidate{Title: "Fragment candidate", Purpose: "Candidate fragment documentation.", SourcePaths: []string{evidence}})
		case sharedmodel.FragmentArchitecture:
			body, err = json.Marshal(sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{{
				Title:       fmt.Sprintf("Primary responsibility batch %d chunk %d", request.SourceBatchIndex, request.SourceChunkIndex),
				Description: "Owns behavior in the component.", SourcePaths: []string{evidence},
			}}})
		default:
			body = []byte(`{"complete":true,"items":[]}`)
		}
		if err != nil {
			return sharedmodel.GenerationResponse{}, err
		}
		return sharedmodel.GenerationResponse{Body: body, FinishReason: "stop"}, nil
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

func TestSyncFragmentModeRendersMultiBatchWithoutSynthesis(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	writeFile(t, dir, "services/api/worker.go", "package api\n\nfunc Work() {}\n")
	runGit(t, dir, "add", "--all")
	input := syncInput(dir)
	input.GenerationPolicy.GenerationStrategy = "fragments"
	input.GenerationPolicy.FragmentCallLimit = 200
	input.ComponentPolicy.MaxBatchBytes = 40
	input.ComponentPolicy.MaxRequestBytes = 500_000
	input.Concurrency = 2
	generator.delay = 2 * time.Millisecond

	result, err := application.Sync(context.Background(), input)
	if err != nil {
		t.Fatalf("Sync() fragment mode error = %v", err)
	}
	if result.Generation == nil || result.Generation.FragmentCalls == 0 || result.Generation.OverviewReducerCalls == 0 || result.Generation.DiagramReducerCalls == 0 {
		t.Fatalf("generation = %+v, want fragment and reducer calls", result.Generation)
	}
	if generator.fragments.Load() == 0 || generator.synthesis.Load() != 0 || result.Plan.Calls.Synthesis != 0 {
		t.Fatalf("fragment calls=%d synthesis calls=%d plan=%+v", generator.fragments.Load(), generator.synthesis.Load(), result.Plan.Calls)
	}
	if generator.maximum.Load() > 2 {
		t.Fatalf("maximum in-flight fragment calls = %d, want at most 2", generator.maximum.Load())
	}
	multiBatch := false
	for _, affected := range result.Plan.AffectedComponents {
		if affected.Key == "services/api" && len(affected.Batches) >= 2 {
			multiBatch = true
		}
	}
	if !multiBatch {
		t.Fatalf("affected components = %+v, want multi-batch services/api", result.Plan.AffectedComponents)
	}
	for _, path := range []string{"architecture.md", "interfaces.md", "data_models.md", "workflows.md", "dependencies.md", "review_notes.md"} {
		if _, err := os.Stat(filepath.Join(dir, "docs/generated", path)); err != nil {
			t.Fatalf("generated topic %q: %v", path, err)
		}
	}
	architecture, err := os.ReadFile(filepath.Join(dir, "docs/generated/architecture.md"))
	if err != nil {
		t.Fatalf("read architecture topic: %v", err)
	}
	for _, expected := range []string{"Primary responsibility batch 1 chunk 1", "Primary responsibility batch 2 chunk 1", "services/api/service.go", "services/api/worker.go"} {
		if !strings.Contains(string(architecture), expected) {
			t.Fatalf("architecture topic omitted multi-batch fact or evidence %q:\n%s", expected, architecture)
		}
	}
}

func TestSyncAutoFallsBackFromTruncatedFastPaths(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	input := syncInput(dir)
	input.GenerationPolicy.GenerationStrategy = "auto"
	input.ComponentPolicy.MaxRequestBytes = 500_000
	input.SourcePolicy.ReportPath = "reports/auto.json"
	generator.truncateDossier = true

	result, err := application.Sync(context.Background(), input)
	if err != nil {
		t.Fatalf("Sync() auto fallback error = %v", err)
	}
	if result.Status != "synced" || result.Generation == nil || result.Generation.FragmentFallbacks == 0 ||
		result.Generation.FragmentCalls == 0 || result.Generation.TransportAttempts == 0 {
		t.Fatalf("result = %+v, want recovered fragment fallback", result)
	}
	if generator.synthesis.Load() != 0 {
		t.Fatalf("synthesis calls = %d, auto fallback must not retry through dossier synthesis", generator.synthesis.Load())
	}
	for _, affected := range result.Plan.AffectedComponents {
		if affected.Action != sharedmodel.ComponentDelete &&
			(affected.GenerationStrategy != "dossier" || !affected.FragmentFallbackPlan || !affected.FragmentFallback) {
			t.Fatalf("affected component = %+v, want planned and actual fast-path fallback", affected)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "docs/generated/index.md")); err != nil {
		t.Fatalf("generated index missing after recovered truncation: %v", err)
	}
	reportData, err := os.ReadFile(filepath.Join(dir, "reports/auto.json"))
	if err != nil {
		t.Fatalf("read auto run report: %v", err)
	}
	var report documentationmodel.RunReport
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("decode auto run report: %v", err)
	}
	if report.LLM.FragmentFallbacks != result.Generation.FragmentFallbacks || report.LLM.FragmentCalls == 0 {
		t.Fatalf("report LLM = %+v, want adaptive fallback counters", report.LLM)
	}
	if len(report.LLM.FragmentFallbackComponents) != report.LLM.FragmentFallbacks {
		t.Fatalf("report fallback components = %v, want one identity per fallback", report.LLM.FragmentFallbackComponents)
	}
	for _, affected := range report.AffectedComponents {
		if affected.GenerationStrategy != "dossier" || !affected.FragmentFallbackPlan || !affected.FragmentFallback {
			t.Fatalf("report affected component = %+v, want recorded fast-path decision", affected)
		}
	}
}

func TestSyncAutoFallbackFailureReportsAdaptiveProgress(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	input := syncInput(dir)
	input.GenerationPolicy.GenerationStrategy = "auto"
	input.GenerationPolicy.FragmentSplitDepth = 1
	input.ComponentPolicy.MaxRequestBytes = 500_000
	input.SourcePolicy.ReportPath = "reports/auto-failure.json"
	generator.truncateDossier = true
	generator.truncateArchitecture = true

	result, err := application.Sync(context.Background(), input)
	if err == nil || result.Status != "generation_failed" || result.Generation == nil {
		t.Fatalf("result=%+v error=%v, want failed adaptive generation", result, err)
	}
	if result.Generation.FragmentFallbacks == 0 || result.Generation.FragmentSourceSplits == 0 || result.Failure == nil || result.Failure.Category != "truncated" {
		t.Fatalf("result = %+v, want safe fallback, split, and truncation diagnostics", result)
	}
	if result.Generation.FragmentSourceSplitCalls == 0 ||
		result.Generation.FragmentSourceSplitCalls > result.Plan.Calls.MaximumSourceSplitCalls {
		t.Fatalf("split calls = %d, planned maximum = %d", result.Generation.FragmentSourceSplitCalls, result.Plan.Calls.MaximumSourceSplitCalls)
	}
	reportData, readErr := os.ReadFile(filepath.Join(dir, "reports/auto-failure.json"))
	if readErr != nil {
		t.Fatalf("read adaptive failure report: %v", readErr)
	}
	var report documentationmodel.RunReport
	if decodeErr := json.Unmarshal(reportData, &report); decodeErr != nil {
		t.Fatalf("decode adaptive failure report: %v", decodeErr)
	}
	if report.LLM.FragmentFallbacks == 0 || report.LLM.FragmentSourceSplits == 0 ||
		report.LLM.FragmentSourceSplitCalls != result.Generation.FragmentSourceSplitCalls ||
		report.Failure == nil || report.Failure.SourceSplitPath == "" {
		t.Fatalf("report = %+v, want recursive adaptive failure metadata", report)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".docify/state.json")); !os.IsNotExist(statErr) {
		t.Fatalf("state exists after adaptive failure: %v", statErr)
	}
}

func TestSyncFragmentFailureLeavesOutputUntouched(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	generator.invalid = true
	input := syncInput(dir)
	input.GenerationPolicy.GenerationStrategy = "fragments"
	input.GenerationPolicy.FragmentCallLimit = 200
	input.ComponentPolicy.MaxRequestBytes = 500_000

	result, err := application.Sync(context.Background(), input)
	if err == nil {
		t.Fatal("Sync() succeeded with invalid required fragments")
	}
	if result.Status != "generation_failed" || result.Failure == nil || result.Failure.FragmentKind == "" {
		t.Fatalf("result = %+v, want safe fragment failure metadata", result)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "docs/generated")); !os.IsNotExist(statErr) {
		t.Fatalf("generated output exists after failed fragment run: %v", statErr)
	}
}

func TestSyncDiagramReducerFailureRemainsPublishable(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	generator.diagramInvalid = true
	input := syncInput(dir)
	input.GenerationPolicy.GenerationStrategy = "fragments"
	input.GenerationPolicy.FragmentCallLimit = 200
	input.ComponentPolicy.MaxRequestBytes = 500_000

	result, err := application.Sync(context.Background(), input)
	if err != nil {
		t.Fatalf("Sync() with diagram fallback error = %v", err)
	}
	if result.Status != "synced" || result.Generation == nil || result.Generation.DiagramReducerCalls == 0 || result.Generation.RepairCalls == 0 {
		t.Fatalf("result = %+v, want installed diagram fallback", result)
	}
	review, err := os.ReadFile(filepath.Join(dir, "docs/generated/review_notes.md"))
	if err != nil {
		t.Fatalf("read review notes: %v", err)
	}
	if !strings.Contains(string(review), "Assembly omitted diagrams after 1 bounded fallback event") {
		t.Fatalf("review notes omit diagram fallback:\n%s", review)
	}
	if _, err := os.Stat(filepath.Join(dir, ".docify/state.json")); err != nil {
		t.Fatalf("state was not installed after optional diagram failure: %v", err)
	}
}

// requestView mirrors the fields the generator needs from the JSON payload.
type requestView struct {
	Target struct {
		ComponentKey string `json:"component_key"`
	} `json:"target"`
	Repository struct {
		AllowedEvidencePaths []string `json:"allowed_evidence_paths"`
	} `json:"repository"`
	SourceFiles []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"source_files"`
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
			Temperature: 0, MaxOutputTokens: 8192, MaxResponseBytes: 65_536, StructuredOutputMode: "auto", TransportRetries: 2,
			GenerationStrategy: "dossier", FragmentCallLimit: 80, FragmentSplitDepth: 3,
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

func TestSyncTruncationWritesSafeFailureReportWithoutInstalling(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	generator.truncated = true
	input := syncInput(dir)
	input.Output = documentationmodel.OutputModeJSON
	input.SourcePolicy.ReportPath = ".docify/report.json"

	result, err := application.Sync(context.Background(), input)
	if err == nil {
		t.Fatal("Sync() error = nil, want typed truncation failure")
	}
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) {
		t.Fatalf("Sync() error = %v, want CompletionError", err)
	}
	if result.Status != "generation_failed" || result.Failure == nil || result.Failure.Category != "truncated" {
		t.Fatalf("result = %+v, want safe truncated failure summary", result)
	}
	if result.Generation == nil || result.Generation.TransportAttempts == 0 {
		t.Fatalf("completed transport attempts = %+v, want failed adapter call accounted", result.Generation)
	}
	if result.Generation == nil || result.Generation.NormalCalls < 1 {
		t.Fatalf("generation counts = %+v, want completed logical calls", result.Generation)
	}

	reportData := readFileString(t, filepath.Join(dir, ".docify", "report.json"))
	if strings.Contains(reportData, "partial") || strings.Contains(reportData, dir) {
		t.Fatalf("failure report contains unsafe data: %s", reportData)
	}
	var report documentationmodel.RunReport
	if err := json.Unmarshal([]byte(reportData), &report); err != nil {
		t.Fatalf("decode failure report: %v", err)
	}
	if report.SchemaVersion != 5 || report.Status != "generation_failed" || report.Failure == nil {
		t.Fatalf("failure report = %+v", report)
	}
	if report.Failure.ProviderRequestID != "safe-request-id" || report.Failure.TransportAttempts != 1 {
		t.Fatalf("failure metadata = %+v", report.Failure)
	}
	if _, err := os.Stat(filepath.Join(dir, ".docify", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("failed generation installed state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "generated")); !os.IsNotExist(err) {
		t.Fatalf("failed generation installed documentation: %v", err)
	}
}

func TestSyncReportFailureDoesNotMaskPrimaryGenerationFailure(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	generator.truncated = true
	input := syncInput(dir)
	writeFile(t, dir, ".reports", "not a directory\n")
	input.SourcePolicy.ReportPath = ".reports/report.json"

	result, err := application.Sync(context.Background(), input)
	if err == nil {
		t.Fatal("Sync() error = nil, want truncation")
	}
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) || completionErr.Category != sharedmodel.CompletionFailureTruncated {
		t.Fatalf("Sync() error = %v, want primary typed truncation", err)
	}
	if result.Failure == nil || result.Failure.Category != "truncated" {
		t.Fatalf("failure metadata = %+v", result.Failure)
	}
}

func TestSyncRefusesOutputChangedDuringGeneration(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	if _, err := application.Sync(context.Background(), syncInput(dir)); err != nil {
		t.Fatalf("bootstrap Sync() error = %v", err)
	}
	stateBefore := readFileString(t, filepath.Join(dir, ".docify", "state.json"))
	indexPath := filepath.Join(dir, "docs", "generated", "index.md")
	originalIndex := readFileString(t, indexPath)
	writeFile(t, dir, "services/api/service.go", "package api\n\nfunc Handle() { /* changed */ }\n")
	runGit(t, dir, "add", "--all")
	generator.mutate = func() {
		_ = os.WriteFile(indexPath, []byte(originalIndex+"\nconcurrent edit\n"), 0o600)
	}

	input := syncInput(dir)
	input.SourcePolicy.ReportPath = ".docify/report.json"
	result, err := application.Sync(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "changed during generation") {
		t.Fatalf("Sync() error = %v, want concurrent-output refusal", err)
	}
	if result.Status != "generation_failed" || result.Failure == nil || result.Failure.Category != "output_validation" {
		t.Fatalf("result = %+v, want safe output-validation failure", result)
	}
	if result.Generation == nil || result.Generation.NormalCalls < 1 {
		t.Fatalf("generation outcome = %+v, want completed call counts", result.Generation)
	}
	if got := readFileString(t, indexPath); got != originalIndex+"\nconcurrent edit\n" {
		t.Fatal("failed sync overwrote the concurrent edit")
	}
	if got := readFileString(t, filepath.Join(dir, ".docify", "state.json")); got != stateBefore {
		t.Fatal("failed sync replaced state after concurrent output change")
	}
	var report documentationmodel.RunReport
	if err := json.Unmarshal([]byte(readFileString(t, filepath.Join(dir, ".docify", "report.json"))), &report); err != nil {
		t.Fatalf("decode failure report: %v", err)
	}
	if report.Failure == nil || report.Failure.Category != "output_validation" {
		t.Fatalf("failure report = %+v", report)
	}
	if report.Validation.OutputValidated {
		t.Fatal("failed run report claims output validation succeeded")
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
