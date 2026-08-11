package usecase_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"docify-repo/internal/app/documentation/usecase"
	sharedmodel "docify-repo/internal/model"
	filesystemrepository "docify-repo/internal/repository/filesystem"
	gitrepository "docify-repo/internal/repository/git"
)

// qualificationGenerator returns the same known facts through the complete-dossier
// and fragment contracts. The fixture can therefore compare coverage and rendering
// without treating natural wording differences from a live model as correctness.
type qualificationGenerator struct {
	calls     atomic.Int64
	fragments atomic.Int64
}

func (g *qualificationGenerator) Generate(_ context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	g.calls.Add(1)
	if request.Kind == sharedmodel.RequestFragment {
		g.fragments.Add(1)
	}

	payload := decodeAllowed(request)
	if err := verifyQualificationSource(request, payload); err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	evidence := make([]string, 0, len(payload.SourceFiles))
	for _, source := range payload.SourceFiles {
		evidence = append(evidence, source.Path)
	}
	if len(evidence) == 0 {
		evidence = append(evidence, payload.Repository.AllowedEvidencePaths...)
	}
	sort.Strings(evidence)
	if len(evidence) > 2 {
		evidence = evidence[:2]
	}

	dossier := qualificationDossier(request.ComponentKey, evidence)
	var value any
	switch request.Kind {
	case sharedmodel.RequestOverview:
		value = sharedmodel.ComponentOverview{Title: dossier.Title, Purpose: dossier.Purpose}
	case sharedmodel.RequestDiagram:
		value = sharedmodel.DiagramsFragment{Complete: true, Items: dossier.Diagrams}
	case sharedmodel.RequestFragment:
		switch request.FragmentKind {
		case sharedmodel.FragmentOverviewCandidate:
			value = sharedmodel.OverviewCandidate{Title: dossier.Title, Purpose: dossier.Purpose, SourcePaths: evidence}
		case sharedmodel.FragmentArchitecture:
			value = sharedmodel.ArchitectureFragment{Complete: true, Items: dossier.Architecture}
		case sharedmodel.FragmentInterfaces:
			value = sharedmodel.InterfacesFragment{Complete: true, Items: dossier.Interfaces}
		case sharedmodel.FragmentDataModels:
			value = sharedmodel.DataModelsFragment{Complete: true, Items: dossier.DataModels}
		case sharedmodel.FragmentWorkflows:
			value = sharedmodel.WorkflowsFragment{Complete: true, Items: dossier.Workflows}
		case sharedmodel.FragmentDependencies:
			value = sharedmodel.DependenciesFragment{Complete: true, Items: dossier.Dependencies}
		case sharedmodel.FragmentReviewGaps:
			value = sharedmodel.ReviewGapsFragment{Complete: true, Items: dossier.ReviewGaps}
		default:
			return sharedmodel.GenerationResponse{}, fmt.Errorf("unexpected qualification fragment %q", request.FragmentKind)
		}
	default:
		value = dossier
	}
	body, err := json.Marshal(value)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	return sharedmodel.GenerationResponse{
		Body: body, FinishReason: "stop", TransportAttempts: 1,
		StructuredOutputUsed: sharedmodel.StructuredOutputJSONSchema,
	}, nil
}

func verifyQualificationSource(request sharedmodel.GenerationRequest, payload requestView) error {
	if request.ComponentKey != "services/api" || request.Kind != sharedmodel.RequestComponent && request.Kind != sharedmodel.RequestFragment {
		return nil
	}
	var source strings.Builder
	for _, file := range payload.SourceFiles {
		source.WriteString(file.Content)
	}
	required := []string{"package api"}
	if request.Kind == sharedmodel.RequestComponent {
		required = append(required, "type Request", "HandleRequest", "EncodeResponse", "encoding/json", "TODO: define timeout behavior")
	} else {
		switch request.FragmentKind {
		case sharedmodel.FragmentArchitecture, sharedmodel.FragmentInterfaces:
			required = append(required, "HandleRequest")
		case sharedmodel.FragmentDataModels:
			required = append(required, "type Request")
		case sharedmodel.FragmentWorkflows:
			required = append(required, "HandleRequest", "EncodeResponse")
		case sharedmodel.FragmentDependencies:
			required = append(required, "encoding/json")
		case sharedmodel.FragmentReviewGaps:
			required = append(required, "TODO: define timeout behavior")
		}
	}
	for _, marker := range required {
		if !strings.Contains(source.String(), marker) {
			return fmt.Errorf("qualification request %s/%s omitted source marker %q", request.Kind, request.FragmentKind, marker)
		}
	}
	return nil
}

func qualificationDossier(componentKey string, evidence []string) sharedmodel.ComponentDossier {
	servicePath := qualificationEvidencePath(evidence, "service.go")
	workerPath := qualificationEvidencePath(evidence, "worker.go")
	return sharedmodel.ComponentDossier{
		Title:       "Qualified " + componentKey,
		Purpose:     "Documents request handling with stable repository evidence.",
		SourcePaths: append([]string(nil), evidence...),
		Architecture: []sharedmodel.ArchitectureItem{{
			Title: "Request boundary", Description: "Separates request handling from response encoding.", SourcePaths: qualificationPaths(servicePath, workerPath),
		}},
		Interfaces: []sharedmodel.InterfaceItem{{
			Name: "HandleRequest", Kind: "function", Direction: "inbound", Description: "Accepts and validates one request.", SourcePaths: qualificationPaths(servicePath),
		}},
		DataModels: []sharedmodel.DataModelItem{{
			Name: "Request", Kind: "request", Description: "Carries the request identifier.",
			Fields:        []sharedmodel.DataField{{Name: "ID", Type: "string", Description: "Identifies the request."}},
			Relationships: []sharedmodel.DataRelationship{}, SourcePaths: qualificationPaths(servicePath),
		}},
		Workflows: []sharedmodel.WorkflowItem{{
			Name: "Process request", Description: "Validates a request before encoding its response.",
			Steps:       []sharedmodel.WorkflowStep{{Actor: "Caller", Action: "invokes", Target: "Handler"}, {Actor: "Handler", Action: "encodes", Target: "Response"}},
			SourcePaths: qualificationPaths(servicePath, workerPath),
		}},
		Dependencies: []sharedmodel.DependencyItem{{
			Name: "encoding/json", Kind: "library", Purpose: "Encodes the response payload.", SourcePaths: qualificationPaths(workerPath),
		}},
		ReviewGaps: []sharedmodel.ReviewGap{{
			Kind: "uncertainty", Description: "Timeout behavior is not represented in the fixture.", Recommendation: "Confirm timeout handling before release.", SourcePaths: qualificationPaths(servicePath),
		}},
		Diagrams: []sharedmodel.Diagram{{
			Type: sharedmodel.DiagramFlowchart, Title: "Request handling", SourcePaths: append([]string(nil), evidence...),
			Nodes: []sharedmodel.FlowchartNode{{Key: "caller", Label: "Caller"}, {Key: "handler", Label: "Handler"}},
			Edges: []sharedmodel.FlowchartEdge{{From: "caller", To: "handler", Label: "request"}},
		}},
	}
}

func qualificationEvidencePath(evidence []string, suffix string) string {
	for _, path := range evidence {
		if strings.HasSuffix(path, suffix) {
			return path
		}
	}
	if len(evidence) > 0 {
		return evidence[0]
	}
	return ""
}

func qualificationPaths(paths ...string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func TestFragmentQualificationPreservesKnownFactsAndDeterminism(t *testing.T) {
	dossierDocs, dossierCalls := runQualificationStrategy(t, "dossier")
	fragmentDocs, fragmentCalls := runQualificationStrategy(t, "fragments")
	repeatedFragmentDocs, _ := runQualificationStrategy(t, "fragments")

	if dossierCalls.fragments != 0 {
		t.Fatalf("dossier path made %d fragment calls", dossierCalls.fragments)
	}
	if fragmentCalls.fragments == 0 || fragmentCalls.total <= dossierCalls.total {
		t.Fatalf("fragment calls = %+v, dossier calls = %+v; qualification did not exercise fragment fan-out", fragmentCalls, dossierCalls)
	}
	compareDocumentTrees(t, fragmentDocs, repeatedFragmentDocs)

	for _, relative := range []string{"components/services/api/index.md", "architecture.md", "interfaces.md", "data_models.md", "workflows.md", "dependencies.md", "review_notes.md"} {
		if _, ok := dossierDocs[relative]; !ok {
			t.Errorf("dossier output missing %q", relative)
		}
		if _, ok := fragmentDocs[relative]; !ok {
			t.Errorf("fragment output missing %q", relative)
		}
	}

	knownFacts := []string{"Request boundary", "HandleRequest", "Request", "Process request", "encoding/json", "Timeout behavior", "Request handling"}
	for _, fact := range knownFacts {
		if !treeContains(dossierDocs, fact) {
			t.Errorf("dossier output lost known fact %q", fact)
		}
		if !treeContains(fragmentDocs, fact) {
			t.Errorf("fragment output lost known fact %q", fact)
		}
	}

	componentPage := fragmentDocs["components/services/api/index.md"]
	for _, evidence := range []string{"services/api/service.go", "services/api/worker.go"} {
		if !strings.Contains(componentPage, evidence) {
			t.Errorf("fragment component page does not cite %q", evidence)
		}
	}
	for _, fact := range []string{"Request boundary", "HandleRequest", "Carries the request identifier", "Process request", "encoding/json", "Timeout behavior is not represented"} {
		if count := strings.Count(componentPage, fact); count != 1 {
			t.Errorf("fragment component page contains %q %d times, want one", fact, count)
		}
	}
	for _, diagramPart := range []string{"flowchart", `n0["Caller"]`, `n1["Handler"]`, `n0 -->|"request"| n1`} {
		if !strings.Contains(componentPage, diagramPart) {
			t.Errorf("fragment component page diagram is missing %q", diagramPart)
		}
	}
	if !strings.Contains(componentPage, "services/api/service.go") || !strings.Contains(componentPage, "services/api/worker.go") {
		t.Error("fragment component page is missing the qualified request-handling diagram")
	}
}

type qualificationCallCounts struct {
	total     int64
	fragments int64
}

func runQualificationStrategy(t *testing.T, strategy string) (map[string]string, qualificationCallCounts) {
	t.Helper()
	_, _, dir := newSyncTestbed(t)
	writeFile(t, dir, "services/api/service.go", `package api

type Request struct { ID string }

// TODO: define timeout behavior.
func HandleRequest(request Request) ([]byte, error) {
	return EncodeResponse(request)
}
`)
	writeFile(t, dir, "services/api/worker.go", `package api

import "encoding/json"

func EncodeResponse(request Request) ([]byte, error) {
	return json.Marshal(request)
}
`)
	runGit(t, dir, "add", "--all")

	generator := &qualificationGenerator{}
	application := usecase.New(
		gitrepository.New(gitrepository.Options{WorkingDirectory: dir}),
		filesystemrepository.NewSourceRepository(),
		filesystemrepository.NewStateRepository(),
		generator,
		filesystemrepository.NewOutputRepository(),
	)
	input := syncInput(dir)
	input.GenerationPolicy.GenerationStrategy = strategy
	input.GenerationPolicy.FragmentCallLimit = 200
	input.GenerationPolicy.FragmentSplitDepth = 0
	input.ComponentPolicy.Strategy = "explicit"
	input.ComponentPolicy.Roots = []string{"services/api"}
	input.ComponentPolicy.MaxRequestBytes = 500_000
	input.Concurrency = 4

	if _, err := application.Sync(context.Background(), input); err != nil {
		t.Fatalf("Sync() with %s strategy: %v", strategy, err)
	}
	beforeNoop := generator.calls.Load()
	if result, err := application.Sync(context.Background(), input); err != nil || result.Status != "noop" {
		t.Fatalf("second Sync() with %s strategy = status %q, error %v; want noop", strategy, result.Status, err)
	}
	if generator.calls.Load() != beforeNoop {
		t.Fatalf("second Sync() with %s strategy made %d model calls", strategy, generator.calls.Load()-beforeNoop)
	}

	return snapshotTree(t, filepath.Join(dir, "docs", "generated")), qualificationCallCounts{
		total: generator.calls.Load(), fragments: generator.fragments.Load(),
	}
}

func compareDocumentTrees(t *testing.T, first, second map[string]string) {
	t.Helper()
	if len(first) != len(second) {
		t.Fatalf("document count differs between repeated fragment runs: %d vs %d", len(first), len(second))
	}
	for path, content := range first {
		if second[path] != content {
			t.Errorf("repeated fragment output differs for %q", path)
		}
	}
}

func treeContains(tree map[string]string, value string) bool {
	for _, content := range tree {
		if strings.Contains(content, value) {
			return true
		}
	}
	return false
}
