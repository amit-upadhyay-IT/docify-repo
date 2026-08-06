package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

// scriptedGenerator is a deterministic stub. respond decides the body for each call by
// request kind and per-kind call index, so tests can script repair and batch flows.
type scriptedGenerator struct {
	mu       sync.Mutex
	calls    []sharedmodel.GenerationRequest
	respond  func(request sharedmodel.GenerationRequest, kindIndex int) []byte
	generate func(request sharedmodel.GenerationRequest, kindIndex int) (sharedmodel.GenerationResponse, error)
}

func (g *scriptedGenerator) Generate(_ context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	g.mu.Lock()
	kindIndex := 0
	for _, previous := range g.calls {
		if previous.Kind == request.Kind {
			kindIndex++
		}
	}
	g.calls = append(g.calls, request)
	g.mu.Unlock()
	if g.generate != nil {
		return g.generate(request, kindIndex)
	}
	return sharedmodel.GenerationResponse{Body: g.respond(request, kindIndex)}, nil
}

func (g *scriptedGenerator) countKind(kind sharedmodel.RequestKind) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	count := 0
	for _, request := range g.calls {
		if request.Kind == kind {
			count++
		}
	}
	return count
}

func (g *scriptedGenerator) countFragment(kind sharedmodel.FragmentKind) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	count := 0
	for _, request := range g.calls {
		if request.Kind == sharedmodel.RequestFragment && request.FragmentKind == kind {
			count++
		}
	}
	return count
}

func dossierBody(t *testing.T, title string, evidence ...string) []byte {
	t.Helper()
	dossier := sharedmodel.ComponentDossier{
		Title: title, Purpose: "Purpose prose.", SourcePaths: append([]string{}, evidence...),
		Architecture: []sharedmodel.ArchitectureItem{}, Interfaces: []sharedmodel.InterfaceItem{},
		DataModels: []sharedmodel.DataModelItem{}, Workflows: []sharedmodel.WorkflowItem{},
		Dependencies: []sharedmodel.DependencyItem{}, ReviewGaps: []sharedmodel.ReviewGap{}, Diagrams: []sharedmodel.Diagram{},
	}
	if len(evidence) > 0 {
		dossier.Architecture = []sharedmodel.ArchitectureItem{{Title: "Boundary", Description: "Owns behavior.", SourcePaths: evidence}}
	}
	data, err := json.Marshal(dossier)
	if err != nil {
		t.Fatalf("marshal dossier: %v", err)
	}
	return data
}

func normalComponent() sharedmodel.Component {
	return sharedmodel.Component{
		Key: "services/api", Document: "components/services-api/index.md",
		TriggeringFiles: []sharedmodel.SourceFile{planSource("services/api/a.go", sharedmodel.RoleProductionSource, true, "package api")},
	}
}

func TestGenerateComponentDossierNormalSingleCall(t *testing.T) {
	component := normalComponent()
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		return dossierBody(t, "API", "services/api/a.go")
	}}
	dossier, stats, err := generateComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(),
		generationSettings(testPlanInput().GenerationPolicy), component, nil, nil, []string{"services/api"}, nil, "full", testPlanInput(), 2)
	if err != nil {
		t.Fatalf("generateComponentDossier() error = %v", err)
	}
	if stats.Normal != 1 || stats.Repair != 0 || stats.Batch != 0 || stats.Synthesis != 0 {
		t.Fatalf("stats = %+v, want a single normal call", stats)
	}
	if dossier.Title != "API" || len(generator.calls) != 1 {
		t.Fatalf("dossier=%q calls=%d, want one normal request", dossier.Title, len(generator.calls))
	}
}

func TestGenerateComponentDossierRepairsOnce(t *testing.T) {
	component := normalComponent()
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		if request.Kind == sharedmodel.RequestRepair {
			return dossierBody(t, "API", "services/api/a.go")
		}
		return []byte(`{}`) // missing required fields -> repair-eligible
	}}
	_, stats, err := generateComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(),
		generationSettings(testPlanInput().GenerationPolicy), component, nil, nil, []string{"services/api"}, nil, "full", testPlanInput(), 2)
	if err != nil {
		t.Fatalf("generateComponentDossier() error = %v", err)
	}
	if stats.Repair != 1 || len(generator.calls) != 2 {
		t.Fatalf("stats=%+v calls=%d, want exactly one repair", stats, len(generator.calls))
	}
	if generator.calls[1].Kind != sharedmodel.RequestRepair {
		t.Fatalf("second call kind = %s, want repair", generator.calls[1].Kind)
	}
}

func TestGenerateComponentDossierFailsAfterOneRepair(t *testing.T) {
	component := normalComponent()
	generator := &scriptedGenerator{respond: func(sharedmodel.GenerationRequest, int) []byte {
		return []byte(`{}`) // always invalid
	}}
	_, _, err := generateComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(),
		generationSettings(testPlanInput().GenerationPolicy), component, nil, nil, []string{"services/api"}, nil, "full", testPlanInput(), 2)
	var validationErr dossierValidationError
	if err == nil || !asValidationError(err, &validationErr) {
		t.Fatalf("error = %v, want dossierValidationError", err)
	}
	if len(generator.calls) != 2 {
		t.Fatalf("calls = %d, want exactly two (one primary + one repair)", len(generator.calls))
	}
}

func TestGenerateComponentDossierBatchesThenSynthesizes(t *testing.T) {
	input := testPlanInput()
	input.ComponentPolicy.MaxContextBytes = 100
	input.ComponentPolicy.MaxBatchBytes = 70
	component := sharedmodel.Component{
		Key: "services/api", Document: "components/services-api/index.md",
		TriggeringFiles: []sharedmodel.SourceFile{
			planSource("services/api/a.go", sharedmodel.RoleProductionSource, true, strings.Repeat("a", 60)),
			planSource("services/api/b.go", sharedmodel.RoleProductionSource, true, strings.Repeat("b", 60)),
		},
	}
	generator := &scriptedGenerator{generate: func(request sharedmodel.GenerationRequest, _ int) (sharedmodel.GenerationResponse, error) {
		var body []byte
		switch request.Kind {
		case sharedmodel.RequestBatch:
			if request.BatchIndex == 1 {
				body = dossierBody(t, "API batch 1", "services/api/a.go")
			} else {
				body = dossierBody(t, "API batch 2", "services/api/b.go")
			}
		case sharedmodel.RequestSynthesis:
			body = dossierBody(t, "API", "services/api/a.go", "services/api/b.go")
		default:
			body = []byte(`{}`)
		}
		return sharedmodel.GenerationResponse{Body: body, TransportAttempts: 2}, nil
	}}
	dossier, stats, err := generateComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{"services/api"}, nil, "full", input, 2)
	if err != nil {
		t.Fatalf("generateComponentDossier() error = %v", err)
	}
	if stats.Batch != 2 || stats.Synthesis != 1 || stats.Normal != 0 || stats.TransportAttempts != 6 {
		t.Fatalf("stats = %+v, want two batches and one synthesis", stats)
	}
	if dossier.Title != "API" {
		t.Fatalf("final dossier title = %q, want synthesized API", dossier.Title)
	}
	if got := generator.countKind(sharedmodel.RequestBatch); got != 2 {
		t.Fatalf("batch calls = %d, want 2", got)
	}
}

func TestGenerateComponentDossierRejectsSynthesisEvidenceOutsideUnion(t *testing.T) {
	input := testPlanInput()
	input.ComponentPolicy.MaxContextBytes = 100
	input.ComponentPolicy.MaxBatchBytes = 70
	component := sharedmodel.Component{
		Key: "services/api", Document: "components/services-api/index.md",
		TriggeringFiles: []sharedmodel.SourceFile{
			planSource("services/api/a.go", sharedmodel.RoleProductionSource, true, strings.Repeat("a", 60)),
			planSource("services/api/b.go", sharedmodel.RoleProductionSource, true, strings.Repeat("b", 60)),
		},
	}
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		switch request.Kind {
		case sharedmodel.RequestBatch:
			// Batches establish no evidence, so the union is empty.
			return dossierBody(t, "batch")
		case sharedmodel.RequestSynthesis, sharedmodel.RequestRepair:
			// Synthesis fabricates a citation to a path no batch established.
			return dossierBody(t, "API", "services/api/unseen.go")
		default:
			return []byte(`{}`)
		}
	}}
	_, _, err := generateComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{"services/api"}, nil, "full", input, 2)
	if err == nil {
		t.Fatal("expected synthesis to fail when citing evidence outside the batch union")
	}
}

func TestGenerateAutoComponentFallsBackFromTruncatedFastPath(t *testing.T) {
	input := fragmentTestPlanInput()
	input.GenerationPolicy.GenerationStrategy = "auto"
	component := normalComponent()
	generator := &scriptedGenerator{generate: func(request sharedmodel.GenerationRequest, _ int) (sharedmodel.GenerationResponse, error) {
		if request.Kind == sharedmodel.RequestComponent {
			return sharedmodel.GenerationResponse{}, truncatedCompletion(request)
		}
		return sharedmodel.GenerationResponse{Body: successfulFragmentResponse(t, request, "services/api/a.go")}, nil
	}}

	_, stats, err := generateAutoComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(), prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateAutoComponentDossier() error = %v", err)
	}
	if generator.countKind(sharedmodel.RequestComponent) != 1 || generator.countKind(sharedmodel.RequestSynthesis) != 0 {
		t.Fatalf("calls = %+v, want one fast path and no dossier synthesis", generator.calls)
	}
	if stats.Normal != 1 || stats.Fallback != 1 || stats.Fragment == 0 {
		t.Fatalf("stats = %+v, want one truncation fallback into fragments", stats)
	}
}

func TestGenerateAutoComponentFallsBackFromTruncatedFastPathRepair(t *testing.T) {
	input := fragmentTestPlanInput()
	input.GenerationPolicy.GenerationStrategy = "auto"
	component := normalComponent()
	generator := &scriptedGenerator{generate: func(request sharedmodel.GenerationRequest, _ int) (sharedmodel.GenerationResponse, error) {
		switch {
		case request.Kind == sharedmodel.RequestComponent:
			return sharedmodel.GenerationResponse{Body: []byte(`{}`)}, nil
		case request.Kind == sharedmodel.RequestRepair && request.FragmentKind == "":
			return sharedmodel.GenerationResponse{}, truncatedCompletion(request)
		default:
			return sharedmodel.GenerationResponse{Body: successfulFragmentResponse(t, request, "services/api/a.go")}, nil
		}
	}}

	_, stats, err := generateAutoComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(), prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateAutoComponentDossier() error = %v", err)
	}
	if stats.Normal != 1 || stats.Repair != 1 || stats.Fallback != 1 || generator.countKind(sharedmodel.RequestComponent) != 1 {
		t.Fatalf("stats=%+v calls=%+v, want repair truncation fallback without a second dossier", stats, generator.calls)
	}
}

func TestGenerateComponentFragmentsMapsReducesAndAssemblesWithoutSynthesis(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		return successfulFragmentResponse(t, request, "services/api/a.go")
	}}

	dossier, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if dossier.Title != "API" || dossier.Purpose != "Documents the API component." {
		t.Fatalf("overview = %q / %q", dossier.Title, dossier.Purpose)
	}
	if len(dossier.SourcePaths) != 1 || dossier.SourcePaths[0] != "services/api/a.go" {
		t.Fatalf("source paths = %v, want validated fragment evidence only", dossier.SourcePaths)
	}
	if stats.Fragment != len(fragmentMapKinds()) || stats.Overview != 1 || stats.Diagram != 1 || stats.Repair != 0 {
		t.Fatalf("stats = %+v, want map and reducer calls", stats)
	}
	if generator.countKind(sharedmodel.RequestSynthesis) != 0 || generator.countKind(sharedmodel.RequestFragment) != len(fragmentMapKinds()) {
		t.Fatalf("calls = %+v, fragment mode must not synthesize a dossier", generator.calls)
	}
}

func TestGenerateComponentFragmentsRepairsOneInvalidRequiredFragment(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	var architectureCalls int
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		if request.FragmentKind == sharedmodel.FragmentArchitecture {
			architectureCalls++
			if request.Kind == sharedmodel.RequestFragment {
				return []byte(`{}`)
			}
		}
		return successfulFragmentResponse(t, request, "services/api/a.go")
	}}

	_, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if stats.Repair != 1 || architectureCalls != 2 {
		t.Fatalf("stats=%+v architecture calls=%d, want one repair", stats, architectureCalls)
	}
}

func TestGenerateComponentFragmentsUsesReducerFallbacks(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		if request.Kind == sharedmodel.RequestOverview || request.Kind == sharedmodel.RequestDiagram || request.Kind == sharedmodel.RequestRepair {
			return []byte(`{}`)
		}
		return successfulFragmentResponse(t, request, "services/api/a.go")
	}}

	dossier, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if dossier.Title != "API candidate" || len(dossier.Diagrams) != 0 {
		t.Fatalf("fallback dossier = %+v", dossier)
	}
	if !containsGapDescription(dossier.ReviewGaps, "Assembly used a deterministic component overview") || !containsGapDescription(dossier.ReviewGaps, "Assembly omitted diagrams") {
		t.Fatalf("review gaps = %+v, want explicit reducer fallback notices", dossier.ReviewGaps)
	}
	if stats.Repair != 2 || stats.OverviewFallback != 1 || stats.DiagramFallback != 1 {
		t.Fatalf("stats = %+v, want one repair and fallback per failed reducer", stats)
	}
}

func TestGenerateComponentFragmentsFallsBackWhenOverviewCandidatesFail(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		if request.FragmentKind == sharedmodel.FragmentOverviewCandidate {
			return []byte(`{}`)
		}
		return successfulFragmentResponse(t, request, "services/api/a.go")
	}}

	dossier, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if dossier.Title != "Api" || stats.Overview != 0 || generator.countKind(sharedmodel.RequestOverview) != 0 {
		t.Fatalf("fallback dossier=%q stats=%+v overview calls=%d", dossier.Title, stats, generator.countKind(sharedmodel.RequestOverview))
	}
	if !containsGapDescription(dossier.ReviewGaps, "Assembly used a deterministic component overview") {
		t.Fatalf("review gaps = %+v, want overview fallback notice", dossier.ReviewGaps)
	}
}

func TestGenerateComponentFragmentsUsesOnlyCitedEvidence(t *testing.T) {
	input := fragmentTestPlanInput()
	component := sharedmodel.Component{
		Key: "services/api",
		TriggeringFiles: []sharedmodel.SourceFile{
			planSource("services/api/a.go", sharedmodel.RoleProductionSource, true, "package api\n"),
			planSource("services/api/b.go", sharedmodel.RoleProductionSource, true, "package api\n"),
		},
	}
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		return successfulFragmentResponse(t, request, "services/api/a.go")
	}}
	dossier, _, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if !equalStrings(dossier.SourcePaths, []string{"services/api/a.go"}) {
		t.Fatalf("source paths = %v, want cited evidence union without uncited b.go", dossier.SourcePaths)
	}
}

func TestGenerateComponentFragmentsRetainsSaturationAtMinimumScope(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		if request.Kind == sharedmodel.RequestFragment && request.FragmentKind == sharedmodel.FragmentArchitecture {
			fragment := sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{
				{Title: "Boundary one", Description: "Owns one responsibility.", SourcePaths: []string{"services/api/a.go"}},
				{Title: "Boundary two", Description: "Owns another responsibility.", SourcePaths: []string{"services/api/a.go"}},
			}}
			body, _ := json.Marshal(fragment)
			return body
		}
		return successfulFragmentResponse(t, request, "services/api/a.go")
	}}

	dossier, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if stats.Saturated != 1 || !containsGapDescription(dossier.ReviewGaps, "Fragment coverage reached a bounded item limit") {
		t.Fatalf("stats=%+v gaps=%+v, want retained saturation notice", stats, dossier.ReviewGaps)
	}
}

func TestGenerateComponentFragmentsRetainsSaturationAtMaximumDepth(t *testing.T) {
	input := fragmentTestPlanInput()
	input.GenerationPolicy.FragmentSplitDepth = 0
	component := normalComponent()
	component.TriggeringFiles[0] = planSource("services/api/a.go", sharedmodel.RoleProductionSource, true,
		"line one\nline two\nline three\n")
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		if request.Kind == sharedmodel.RequestFragment && request.FragmentKind == sharedmodel.FragmentArchitecture {
			fragment := sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{
				{Title: "Boundary one", Description: "Owns one responsibility.", SourcePaths: []string{"services/api/a.go"}},
				{Title: "Boundary two", Description: "Owns another responsibility.", SourcePaths: []string{"services/api/a.go"}},
			}}
			body, _ := json.Marshal(fragment)
			return body
		}
		return successfulFragmentResponse(t, request, "services/api/a.go")
	}}

	dossier, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if stats.Split != 0 || stats.Saturated != 1 || !containsGapDescription(dossier.ReviewGaps, "Fragment coverage reached a bounded item limit") {
		t.Fatalf("stats=%+v gaps=%+v, want depth-exhausted saturation success", stats, dossier.ReviewGaps)
	}
}

func TestGenerateComponentFragmentsSplitsOnlyTruncatedRequiredScope(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	component.TriggeringFiles[0] = planSource("services/api/a.go", sharedmodel.RoleProductionSource, true,
		"package api\nfunc A() {}\nfunc B() {}\n")
	generator := &scriptedGenerator{generate: func(request sharedmodel.GenerationRequest, _ int) (sharedmodel.GenerationResponse, error) {
		if request.Kind == sharedmodel.RequestFragment && request.FragmentKind == sharedmodel.FragmentArchitecture && request.SourceChunkCount == 1 {
			return sharedmodel.GenerationResponse{}, truncatedCompletion(request)
		}
		return sharedmodel.GenerationResponse{Body: successfulFragmentResponse(t, request, "services/api/a.go")}, nil
	}}

	_, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if stats.Split != 1 || generator.countFragment(sharedmodel.FragmentArchitecture) != 3 {
		t.Fatalf("stats=%+v architecture calls=%d, want parent plus two children", stats, generator.countFragment(sharedmodel.FragmentArchitecture))
	}
	for _, kind := range requiredFragmentKinds()[1:] {
		if got := generator.countFragment(kind); got != 1 {
			t.Fatalf("%s calls = %d, want unaffected root scope only", kind, got)
		}
	}
}

func TestGenerateComponentFragmentsRestartsAfterTruncatedRepair(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	component.TriggeringFiles[0] = planSource("services/api/a.go", sharedmodel.RoleProductionSource, true,
		"package api\nfunc A() {}\nfunc B() {}\n")
	generator := &scriptedGenerator{generate: func(request sharedmodel.GenerationRequest, _ int) (sharedmodel.GenerationResponse, error) {
		if request.FragmentKind == sharedmodel.FragmentArchitecture && request.SourceChunkCount == 1 {
			if request.Kind == sharedmodel.RequestFragment {
				return sharedmodel.GenerationResponse{Body: []byte(`{}`)}, nil
			}
			if request.Kind == sharedmodel.RequestRepair {
				return sharedmodel.GenerationResponse{}, truncatedCompletion(request)
			}
		}
		return sharedmodel.GenerationResponse{Body: successfulFragmentResponse(t, request, "services/api/a.go")}, nil
	}}

	_, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 4)
	if err != nil {
		t.Fatalf("generateComponentFragments() error = %v", err)
	}
	if stats.Split != 1 || stats.Repair != 1 || generator.countFragment(sharedmodel.FragmentArchitecture) != 3 {
		t.Fatalf("stats=%+v architecture calls=%d, want repair truncation restart under child scopes", stats, generator.countFragment(sharedmodel.FragmentArchitecture))
	}
}

func TestGenerateComponentFragmentsFailsTruncationAtSplitDepth(t *testing.T) {
	input := fragmentTestPlanInput()
	input.GenerationPolicy.FragmentSplitDepth = 1
	component := normalComponent()
	component.TriggeringFiles[0] = planSource("services/api/a.go", sharedmodel.RoleProductionSource, true,
		"line one\nline two\nline three\nline four\n")
	generator := &scriptedGenerator{generate: func(request sharedmodel.GenerationRequest, _ int) (sharedmodel.GenerationResponse, error) {
		if request.Kind == sharedmodel.RequestFragment && request.FragmentKind == sharedmodel.FragmentArchitecture {
			return sharedmodel.GenerationResponse{}, truncatedCompletion(request)
		}
		return sharedmodel.GenerationResponse{Body: successfulFragmentResponse(t, request, "services/api/a.go")}, nil
	}}

	_, stats, err := generateComponentFragments(context.Background(), generator, prompt.CodebaseSummaryV2(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{component.Key}, nil, "full", input, 1)
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) || completionErr.Category != sharedmodel.CompletionFailureTruncated {
		t.Fatalf("error = %v, want typed truncation exhaustion", err)
	}
	if completionErr.SourceSplitPath != "b1/c1/0" || completionErr.SourceChunkIndex != 1 || completionErr.SourceChunkCount != 2 {
		t.Fatalf("completion metadata = %+v, want stable recursive split identity", completionErr)
	}
	if stats.Split != 1 || stats.Saturated != 0 || generator.countKind(sharedmodel.RequestOverview) != 0 {
		t.Fatalf("stats=%+v calls=%+v, truncation must not degrade to retained coverage", stats, generator.calls)
	}
}

func TestAdaptiveFragmentSplittingStopsAtComponentCallLimit(t *testing.T) {
	input := fragmentTestPlanInput()
	component := normalComponent()
	component.TriggeringFiles[0] = planSource("services/api/a.go", sharedmodel.RoleProductionSource, true,
		"line one\nline two\nline three\nline four\n")
	plan, err := planFragmentGeneration(prompt.CodebaseSummaryV2(), generationSettings(input.GenerationPolicy), component, nil, nil,
		[]string{component.Key}, nil, "full", input)
	if err != nil {
		t.Fatalf("planFragmentGeneration() error = %v", err)
	}
	var architecture plannedFragmentRequest
	for _, planned := range plan.mapRequests {
		if planned.scope.Kind == sharedmodel.FragmentArchitecture {
			architecture = planned
			break
		}
	}
	generator := &scriptedGenerator{respond: func(sharedmodel.GenerationRequest, int) []byte {
		fragment := sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{
			{Title: "Boundary one", Description: "Owns one responsibility.", SourcePaths: []string{"services/api/a.go"}},
			{Title: "Boundary two", Description: "Owns another responsibility.", SourcePaths: []string{"services/api/a.go"}},
		}}
		body, _ := json.Marshal(fragment)
		return body
	}}
	budget := &componentCallBudget{generator: generator, componentKey: component.Key, limit: 2}
	stats := componentGenerationStats{}
	_, err = generateRequiredFragmentAdaptive(context.Background(), budget, prompt.CodebaseSummaryV2(), generationSettings(input.GenerationPolicy),
		component, nil, nil, []string{component.Key}, nil, "full", architecture, 0, input, &stats)
	var limitErr fragmentCallLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error = %v, want deterministic component call limit", err)
	}
	if len(generator.calls) != 2 || stats.Split != 2 || stats.Saturated != 0 {
		t.Fatalf("underlying calls=%d stats=%+v, want bounded adaptive split attempts", len(generator.calls), stats)
	}
}

func successfulFragmentResponse(t *testing.T, request sharedmodel.GenerationRequest, evidence string) []byte {
	t.Helper()
	switch request.Kind {
	case sharedmodel.RequestOverview:
		return []byte(`{"title":"API","purpose":"Documents the API component."}`)
	case sharedmodel.RequestDiagram:
		return []byte(`{"complete":true,"items":[]}`)
	case sharedmodel.RequestRepair, sharedmodel.RequestFragment:
		switch request.FragmentKind {
		case sharedmodel.FragmentOverviewCandidate:
			body, _ := json.Marshal(sharedmodel.OverviewCandidate{Title: "API candidate", Purpose: "Candidate purpose.", SourcePaths: []string{evidence}})
			return body
		case sharedmodel.FragmentArchitecture:
			return []byte(`{"complete":true,"items":[]}`)
		case sharedmodel.FragmentInterfaces:
			return []byte(`{"complete":true,"items":[]}`)
		case sharedmodel.FragmentDataModels:
			return []byte(`{"complete":true,"items":[]}`)
		case sharedmodel.FragmentWorkflows:
			return []byte(`{"complete":true,"items":[]}`)
		case sharedmodel.FragmentDependencies:
			return []byte(`{"complete":true,"items":[]}`)
		case sharedmodel.FragmentReviewGaps:
			return []byte(`{"complete":true,"items":[]}`)
		case sharedmodel.FragmentDiagrams:
			return []byte(`{"complete":true,"items":[]}`)
		}
	}
	return []byte(`{}`)
}

func fragmentTestPlanInput() documentationmodel.PlanInput {
	input := testPlanInput()
	input.GenerationPolicy.GenerationStrategy = "fragments"
	input.GenerationPolicy.MaxOutputTokens = fragmentMinimumOutputTokens
	input.GenerationPolicy.FragmentCallLimit = 80
	return input
}

func truncatedCompletion(request sharedmodel.GenerationRequest) error {
	return &sharedmodel.CompletionError{
		Category: sharedmodel.CompletionFailureTruncated, RequestKind: request.Kind, ComponentKey: request.ComponentKey,
		BatchIndex: request.BatchIndex, BatchCount: request.BatchCount, FragmentKind: request.FragmentKind,
		SourceBatchIndex: request.SourceBatchIndex, SourceBatchCount: request.SourceBatchCount,
		SourceChunkIndex: request.SourceChunkIndex, SourceChunkCount: request.SourceChunkCount,
		SourceSplitPath: request.SourceSplitPath,
		FinishReason:    "length", StructuredOutputUsed: sharedmodel.StructuredOutputJSONSchema, TransportAttempts: 1,
	}
}

type concurrencyProbeGenerator struct {
	inFlight int32
	maximum  int32
	calls    int32
}

func (g *concurrencyProbeGenerator) Generate(ctx context.Context, _ sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	current := atomic.AddInt32(&g.inFlight, 1)
	atomic.AddInt32(&g.calls, 1)
	for {
		maximum := atomic.LoadInt32(&g.maximum)
		if current <= maximum || atomic.CompareAndSwapInt32(&g.maximum, maximum, current) {
			break
		}
	}
	defer atomic.AddInt32(&g.inFlight, -1)
	select {
	case <-ctx.Done():
		return sharedmodel.GenerationResponse{}, ctx.Err()
	case <-time.After(10 * time.Millisecond):
		return sharedmodel.GenerationResponse{}, nil
	}
}

func TestLLMCallLimiterBoundsNestedWork(t *testing.T) {
	probe := &concurrencyProbeGenerator{}
	limiter := newLLMCallLimiter(probe, 3)
	err := runBounded(context.Background(), 6, 6, func(ctx context.Context, _ int) error {
		return runBounded(ctx, 6, 6, func(ctx context.Context, index int) error {
			kind := sharedmodel.RequestBatch
			if index == 0 {
				kind = sharedmodel.RequestRepair
			}
			_, err := limiter.Generate(ctx, sharedmodel.GenerationRequest{Kind: kind})
			return err
		})
	})
	if err != nil {
		t.Fatalf("nested work error = %v", err)
	}
	if got := atomic.LoadInt32(&probe.maximum); got > 3 {
		t.Fatalf("maximum in-flight Generate calls = %d, want at most 3", got)
	}
	if got := atomic.LoadInt32(&probe.calls); got != 36 {
		t.Fatalf("Generate calls = %d, want 36", got)
	}
	stats := limiter.snapshot()
	if stats.Batch != 30 || stats.Repair != 6 {
		t.Fatalf("completed call stats = %+v", stats)
	}
}

func TestComponentCallBudgetCountsFragmentsRepairsAndReducersTogether(t *testing.T) {
	generator := &scriptedGenerator{respond: func(sharedmodel.GenerationRequest, int) []byte { return []byte(`{}`) }}
	budget := &componentCallBudget{generator: generator, componentKey: "services/api", limit: 3}
	for _, kind := range []sharedmodel.RequestKind{sharedmodel.RequestFragment, sharedmodel.RequestRepair, sharedmodel.RequestOverview} {
		if _, err := budget.Generate(context.Background(), sharedmodel.GenerationRequest{Kind: kind}); err != nil {
			t.Fatalf("budgeted %s call error = %v", kind, err)
		}
	}
	_, err := budget.Generate(context.Background(), sharedmodel.GenerationRequest{Kind: sharedmodel.RequestDiagram})
	if _, ok := err.(fragmentCallLimitError); !ok {
		t.Fatalf("fourth call error = %v, want fragmentCallLimitError", err)
	}
	if len(generator.calls) != 3 {
		t.Fatalf("underlying calls = %d, want exactly the budgeted 3", len(generator.calls))
	}
}

func asValidationError(err error, target *dossierValidationError) bool {
	if converted, ok := err.(dossierValidationError); ok {
		*target = converted
		return true
	}
	return false
}
