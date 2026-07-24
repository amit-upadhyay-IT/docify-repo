package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

// scriptedGenerator is a deterministic stub. respond decides the body for each call by
// request kind and per-kind call index, so tests can script repair and batch flows.
type scriptedGenerator struct {
	mu      sync.Mutex
	calls   []sharedmodel.GenerationRequest
	respond func(request sharedmodel.GenerationRequest, kindIndex int) []byte
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

func dossierBody(t *testing.T, title string, evidence ...string) []byte {
	t.Helper()
	dossier := sharedmodel.ComponentDossier{Title: title, Purpose: "Purpose prose.", SourcePaths: evidence}
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
	generator := &scriptedGenerator{respond: func(request sharedmodel.GenerationRequest, _ int) []byte {
		switch request.Kind {
		case sharedmodel.RequestBatch:
			if request.BatchIndex == 1 {
				return dossierBody(t, "API batch 1", "services/api/a.go")
			}
			return dossierBody(t, "API batch 2", "services/api/b.go")
		case sharedmodel.RequestSynthesis:
			return dossierBody(t, "API", "services/api/a.go", "services/api/b.go")
		default:
			return []byte(`{}`)
		}
	}}
	dossier, stats, err := generateComponentDossier(context.Background(), generator, prompt.CodebaseSummaryV1(),
		generationSettings(input.GenerationPolicy), component, nil, nil, []string{"services/api"}, nil, "full", input, 2)
	if err != nil {
		t.Fatalf("generateComponentDossier() error = %v", err)
	}
	if stats.Batch != 2 || stats.Synthesis != 1 || stats.Normal != 0 {
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

func asValidationError(err error, target *dossierValidationError) bool {
	if converted, ok := err.(dossierValidationError); ok {
		*target = converted
		return true
	}
	return false
}
