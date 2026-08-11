package usecase

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func TestAssembledDossierWirePreservesRequiredNullableFields(t *testing.T) {
	path := "services/payments/service.go"
	dossier := sharedmodel.ComponentDossier{
		Title: "Payments", Purpose: "Processes payments.", SourcePaths: []string{path},
		Architecture: []sharedmodel.ArchitectureItem{}, Interfaces: []sharedmodel.InterfaceItem{}, DataModels: []sharedmodel.DataModelItem{},
		Workflows: []sharedmodel.WorkflowItem{{
			Name: "Process", Description: "Processes one payment.",
			Steps: []sharedmodel.WorkflowStep{{Actor: "Caller", Action: "Submits a payment."}}, SourcePaths: []string{path},
		}},
		Dependencies: []sharedmodel.DependencyItem{{Name: "helpers", Kind: "library", Purpose: "Provides helpers.", SourcePaths: []string{path}}},
		ReviewGaps:   []sharedmodel.ReviewGap{},
		Diagrams: []sharedmodel.Diagram{
			{Type: sharedmodel.DiagramFlowchart, Title: "Flow", SourcePaths: []string{path}, Nodes: []sharedmodel.FlowchartNode{{Key: "a", Label: "Start"}, {Key: "b", Label: "End"}}, Edges: []sharedmodel.FlowchartEdge{{From: "a", To: "b"}}},
			{Type: sharedmodel.DiagramSequence, Title: "Calls", SourcePaths: []string{path}, Participants: []sharedmodel.SequenceParticipant{}, Messages: []sharedmodel.SequenceMessage{}},
			{Type: sharedmodel.DiagramClass, Title: "Types", SourcePaths: []string{path}, Classes: []sharedmodel.ClassNode{{Key: "a", Label: "A", Members: []string{}}, {Key: "b", Label: "B", Members: []string{}}}, Relationships: []sharedmodel.ClassRelationship{{From: "a", To: "b", Kind: "association"}}},
		},
	}
	body, err := json.Marshal(dossier)
	if err != nil {
		t.Fatalf("marshal dossier: %v", err)
	}
	result := validateDossier(body, []string{path}, []string{"services/payments"})
	if !result.valid() {
		t.Fatalf("assembled wire issues = %+v", result.issues)
	}
}

func TestDiagramMarshalPreservesUnionShapeAndContamination(t *testing.T) {
	clean, err := json.Marshal(sharedmodel.Diagram{Type: sharedmodel.DiagramFlowchart, Title: "Flow", SourcePaths: []string{"src/flow.go"}})
	if err != nil {
		t.Fatalf("marshal clean diagram: %v", err)
	}
	var cleanObject map[string]json.RawMessage
	if err := json.Unmarshal(clean, &cleanObject); err != nil {
		t.Fatalf("decode clean diagram: %v", err)
	}
	if string(cleanObject["nodes"]) != "[]" || string(cleanObject["edges"]) != "[]" {
		t.Fatalf("active arrays were not materialized: %s", clean)
	}
	if _, exists := cleanObject["messages"]; exists {
		t.Fatalf("inactive nil array was emitted: %s", clean)
	}

	contaminated, err := json.Marshal(sharedmodel.Diagram{
		Type: sharedmodel.DiagramFlowchart, Title: "Flow", SourcePaths: []string{"src/flow.go"}, Messages: []sharedmodel.SequenceMessage{},
	})
	if err != nil {
		t.Fatalf("marshal contaminated diagram: %v", err)
	}
	var contaminatedObject map[string]json.RawMessage
	if err := json.Unmarshal(contaminated, &contaminatedObject); err != nil {
		t.Fatalf("decode contaminated diagram: %v", err)
	}
	if string(contaminatedObject["messages"]) != "[]" {
		t.Fatalf("explicit inactive array was hidden: %s", contaminated)
	}
}

func TestCoverageLedgerRequiresEveryKindAndTerminalScope(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	planned := testPlannedScopes(component, 1)
	if _, err := newFragmentCoverageLedger(component, planned[:len(planned)-1]); err == nil || !strings.Contains(err.Error(), "coverage_required_kind_missing") {
		t.Fatalf("missing-kind error = %v", err)
	}

	ledger, err := newFragmentCoverageLedger(component, planned)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	for _, scope := range planned[:len(planned)-1] {
		if err := ledger.record(scope, emptyTestFragment(t, scope), false); err != nil {
			t.Fatalf("record %s: %v", scope.Kind, err)
		}
	}
	if _, err := ledger.verify(component); err == nil || !strings.Contains(err.Error(), "coverage_scope_uncovered") {
		t.Fatalf("uncovered-scope error = %v", err)
	}
	if err := ledger.record(planned[len(planned)-1], emptyTestFragment(t, planned[len(planned)-1]), false); err != nil {
		t.Fatalf("record final empty fragment: %v", err)
	}
	summary, err := ledger.verify(component)
	if err != nil {
		t.Fatalf("verify complete ledger: %v", err)
	}
	if len(summary.fragments) != len(requiredFragmentKinds()) {
		t.Fatalf("covered fragments = %d, want %d", len(summary.fragments), len(requiredFragmentKinds()))
	}
}

func TestCoverageLedgerRejectsMissingBatchAndChunkIndexes(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	missingBatch := testPlannedScopes(component, 1)
	for index := range missingBatch {
		missingBatch[index].SourceBatchCount = 2
	}
	if _, err := newFragmentCoverageLedger(component, missingBatch); err == nil || !strings.Contains(err.Error(), "coverage_batch_missing") {
		t.Fatalf("missing-batch error = %v", err)
	}

	missingChunk := testPlannedScopes(component, 1)
	for index := range missingChunk {
		missingChunk[index].SourceChunkCount = 2
	}
	if _, err := newFragmentCoverageLedger(component, missingChunk); err == nil || !strings.Contains(err.Error(), "coverage_chunk_missing") {
		t.Fatalf("missing-chunk error = %v", err)
	}
}

func TestCoverageLedgerRequiresEveryReplacementChild(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	planned := testPlannedScopes(component, 1)
	ledger, err := newFragmentCoverageLedger(component, planned)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	parent := plannedScope(t, planned, sharedmodel.FragmentArchitecture)
	children := []fragmentScope{
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 1, SourceChunkIndex: 1, SourceChunkCount: 2, SplitPath: "b1/c1/0", AllowedEvidence: parent.AllowedEvidence},
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 1, SourceChunkIndex: 2, SourceChunkCount: 2, SplitPath: "b1/c1/1", AllowedEvidence: parent.AllowedEvidence},
	}
	if err := ledger.replace(parent, children); err != nil {
		t.Fatalf("replace parent: %v", err)
	}
	for _, scope := range planned {
		if scope.Kind == sharedmodel.FragmentArchitecture {
			continue
		}
		if err := ledger.record(scope, emptyTestFragment(t, scope), false); err != nil {
			t.Fatalf("record %s: %v", scope.Kind, err)
		}
	}
	if err := ledger.record(children[0], emptyTestFragment(t, children[0]), false); err != nil {
		t.Fatalf("record child: %v", err)
	}
	if _, err := ledger.verify(component); err == nil || !strings.Contains(err.Error(), "coverage_scope_uncovered") {
		t.Fatalf("missing-child error = %v", err)
	}
	if err := ledger.record(children[1], emptyTestFragment(t, children[1]), false); err != nil {
		t.Fatalf("record second child: %v", err)
	}
	summary, err := ledger.verify(component)
	if err != nil {
		t.Fatalf("verify replacement: %v", err)
	}
	if len(summary.fragments) != len(requiredFragmentKinds())+1 {
		t.Fatalf("terminal fragments = %d, want %d", len(summary.fragments), len(requiredFragmentKinds())+1)
	}
}

func TestCoverageLedgerRejectsMalformedReplacementAndSwappedResult(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	planned := testPlannedScopes(component, 2)
	ledger, err := newFragmentCoverageLedger(component, planned)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	parent := plannedScope(t, planned, sharedmodel.FragmentArchitecture)
	badChildren := []fragmentScope{
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 2, SourceChunkIndex: 1, SourceChunkCount: 2, SplitPath: parent.SplitPath + "/0"},
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 2, SourceChunkIndex: 2, SourceChunkCount: 2, SplitPath: parent.SplitPath + "/wrong"},
	}
	if err := ledger.replace(parent, badChildren); err == nil || !strings.Contains(err.Error(), "coverage_child_lineage_invalid") {
		t.Fatalf("malformed replacement error = %v", err)
	}
	expandedChildren := []fragmentScope{
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 2, SourceChunkIndex: 1, SourceChunkCount: 2, SplitPath: parent.SplitPath + "/0", AllowedEvidence: append(append([]string(nil), parent.AllowedEvidence...), "src/unplanned.go")},
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 2, SourceChunkIndex: 2, SourceChunkCount: 2, SplitPath: parent.SplitPath + "/1", AllowedEvidence: parent.AllowedEvidence},
	}
	if err := ledger.replace(parent, expandedChildren); err == nil || !strings.Contains(err.Error(), "coverage_child_evidence_expansion") {
		t.Fatalf("expanded child evidence error = %v", err)
	}
	erasedChildren := []fragmentScope{
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 2, SourceChunkIndex: 1, SourceChunkCount: 2, SplitPath: parent.SplitPath + "/0", AllowedEvidence: []string{}},
		{ComponentKey: component.Key, Kind: parent.Kind, SourceBatchIndex: 1, SourceBatchCount: 2, SourceChunkIndex: 2, SourceChunkCount: 2, SplitPath: parent.SplitPath + "/1", AllowedEvidence: []string{}},
	}
	if err := ledger.replace(parent, erasedChildren); err == nil || !strings.Contains(err.Error(), "coverage_child_evidence_missing") {
		t.Fatalf("erased child evidence error = %v", err)
	}

	first := plannedScopeForBatch(t, planned, sharedmodel.FragmentArchitecture, 1)
	second := plannedScopeForBatch(t, planned, sharedmodel.FragmentArchitecture, 2)
	swapped := emptyTestFragment(t, second)
	if err := ledger.record(first, swapped, false); err == nil || !strings.Contains(err.Error(), "coverage_fragment_scope_mismatch") {
		t.Fatalf("swapped-scope error = %v", err)
	}
	broaderScope := first
	broaderScope.AllowedEvidence = append(broaderScope.AllowedEvidence, "src/unplanned.go")
	broader := emptyTestFragment(t, broaderScope)
	if err := ledger.record(first, broader, false); err == nil || !strings.Contains(err.Error(), "coverage_fragment_evidence_mismatch") {
		t.Fatalf("mismatched-evidence error = %v", err)
	}
}

func TestCoverageLedgerSeparatesUnresolvedAndRetainedSaturation(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	planned := testPlannedScopes(component, 1)
	for index := range planned {
		planned[index].AllowedEvidence = []string{"src/one.go", "src/two.go"}
	}
	ledger, err := newFragmentCoverageLedger(component, planned)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	architectureScope := plannedScope(t, planned, sharedmodel.FragmentArchitecture)
	saturated := testListFragment(t, architectureScope, sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{
		{Title: "One", Description: "Owns one concern.", SourcePaths: []string{"src/one.go"}},
		{Title: "Two", Description: "Owns another concern.", SourcePaths: []string{"src/two.go"}},
	}}, []string{"src/one.go", "src/two.go"})
	if err := ledger.record(architectureScope, saturated, false); err == nil || !strings.Contains(err.Error(), "coverage_saturation_unresolved") {
		t.Fatalf("unresolved saturation error = %v", err)
	}
	if err := ledger.record(architectureScope, saturated, true); err != nil {
		t.Fatalf("retain saturation: %v", err)
	}
	for _, scope := range planned {
		if scope.Kind == sharedmodel.FragmentArchitecture {
			continue
		}
		if err := ledger.record(scope, emptyTestFragment(t, scope), false); err != nil {
			t.Fatalf("record %s: %v", scope.Kind, err)
		}
	}
	summary, err := ledger.verify(component)
	if err != nil {
		t.Fatalf("verify retained saturation: %v", err)
	}
	if summary.saturatedScopes != 1 || !reflect.DeepEqual(summary.saturationEvidence, []string{"src/one.go", "src/two.go"}) {
		t.Fatalf("saturation summary = %+v", summary)
	}
}

func TestAssembleComponentDossierIsPermutationInvariant(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	forward := assembledPermutationFixture(t, component, false)
	reverse := assembledPermutationFixture(t, component, true)
	forwardBody, _ := json.Marshal(forward.dossier)
	reverseBody, _ := json.Marshal(reverse.dossier)
	if string(forwardBody) != string(reverseBody) {
		t.Fatalf("assembly changed with input order:\nforward=%s\nreverse=%s", forwardBody, reverseBody)
	}
	if !reflect.DeepEqual(forward.stats, reverse.stats) {
		t.Fatalf("stats changed with input order: forward=%+v reverse=%+v", forward.stats, reverse.stats)
	}
	if len(forward.dossier.Architecture) != 2 {
		t.Fatalf("architecture items = %d, want exact duplicate plus conflict", len(forward.dossier.Architecture))
	}
	if !reflect.DeepEqual(forward.dossier.Architecture[0].SourcePaths, []string{"src/000.go", "src/001.go"}) {
		t.Fatalf("deduplicated evidence = %v", forward.dossier.Architecture[0].SourcePaths)
	}
	if forward.stats.ConflictIdentities != 1 {
		t.Fatalf("conflicts = %d, want 1", forward.stats.ConflictIdentities)
	}
	if forward.stats.OverviewFallbacks != 0 || containsGapDescription(forward.dossier.ReviewGaps, "Assembly used a deterministic component overview") {
		t.Fatalf("validated overview incorrectly reported fallback: stats=%+v gaps=%+v", forward.stats, forward.dossier.ReviewGaps)
	}
	if forward.dossier.Title != "Alpha" {
		t.Fatalf("stable overview title = %q, want Alpha", forward.dossier.Title)
	}
}

func TestCoverageStoresSealedValidationWithoutMutableAliases(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	var original fragmentValidation
	ledger := completeTestLedger(t, component, 1, false, func(scope fragmentScope) fragmentValidation {
		if scope.Kind != sharedmodel.FragmentArchitecture {
			return emptyTestFragment(t, scope)
		}
		value := sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{{
			Title: "Boundary", Description: "Owns processing.", SourcePaths: []string{"src/000.go"},
		}}}
		body, _ := json.Marshal(value)
		allowed := []string{"src/000.go"}
		original = validateScopedFragment(scope, body, allowed, []string{component.Key})
		allowed[0] = "src/mutated-before-record.go"
		return original
	})
	mutated := original.value.(sharedmodel.ArchitectureFragment)
	mutated.Items[0].SourcePaths[0] = "src/mutated.go"
	original.value = mutated
	original.evidenceUsed[0] = "src/mutated.go"

	dossier, _, err := assembleComponentDossier(fragmentAssemblyInput{Component: component, Catalog: []string{component.Key}, Coverage: ledger})
	if err != nil {
		t.Fatalf("assemble sealed fragment: %v", err)
	}
	if !reflect.DeepEqual(dossier.Architecture[0].SourcePaths, []string{"src/000.go"}) {
		t.Fatalf("sealed evidence changed through alias: %v", dossier.Architecture[0].SourcePaths)
	}
}

func TestMergeAppliesEvidenceAndSectionLimitsDeterministically(t *testing.T) {
	stats := fragmentAssemblyStats{SectionItemsOmitted: make(map[sharedmodel.FragmentKind]int)}
	exact := make([]sharedmodel.ArchitectureItem, assembledMaxItemSourcePaths+1)
	for index := range exact {
		exact[index] = sharedmodel.ArchitectureItem{Title: "Boundary", Description: "Owns processing.", SourcePaths: []string{fmt.Sprintf("src/%03d.go", index)}}
	}
	merged, conflicts, _, err := mergeFragmentItems(exact, assembledMaxArchitectureItems, sharedmodel.FragmentArchitecture, architectureMergeSpec(), &stats)
	if err != nil {
		t.Fatalf("merge exact items: %v", err)
	}
	if len(merged) != 1 || len(merged[0].SourcePaths) != assembledMaxItemSourcePaths || conflicts != 0 {
		t.Fatalf("exact merge result = items:%d paths:%d conflicts:%d", len(merged), len(merged[0].SourcePaths), conflicts)
	}
	if stats.ItemsWithEvidenceOverflow != 1 || stats.ItemEvidencePathsOmitted != 1 {
		t.Fatalf("evidence overflow stats = %+v", stats)
	}

	stats = fragmentAssemblyStats{SectionItemsOmitted: make(map[sharedmodel.FragmentKind]int)}
	dense := make([]sharedmodel.ArchitectureItem, assembledMaxArchitectureItems+1)
	for index := range dense {
		dense[index] = sharedmodel.ArchitectureItem{Title: fmt.Sprintf("Boundary %03d", index), Description: "Owns processing.", SourcePaths: []string{"src/common.go"}}
	}
	dense[len(dense)-1].SourcePaths = []string{"src/a.go", "src/b.go", "src/c.go"}
	merged, _, _, err = mergeFragmentItems(dense, assembledMaxArchitectureItems, sharedmodel.FragmentArchitecture, architectureMergeSpec(), &stats)
	if err != nil {
		t.Fatalf("merge dense section: %v", err)
	}
	if len(merged) != assembledMaxArchitectureItems || stats.SectionItemsOmitted[sharedmodel.FragmentArchitecture] != 1 {
		t.Fatalf("section overflow = items:%d stats:%+v", len(merged), stats)
	}
	foundPrioritized := false
	for _, item := range merged {
		if item.Title == "Boundary 100" {
			foundPrioritized = true
		}
	}
	if !foundPrioritized {
		t.Fatal("evidence-supported overflow item was not retained")
	}
}

func TestTopLevelEvidencePriorityIsBoundedAndStable(t *testing.T) {
	evidence := make([]string, assembledMaxSourcePaths+1)
	for index := range evidence {
		evidence[index] = fmt.Sprintf("src/%03d.go", index)
	}
	counts := map[string]int{"src/199.go": 5, "src/200.go": 5}
	stats := fragmentAssemblyStats{}
	selected := selectTopLevelPaths(reverseStrings(evidence), counts, &stats)
	if len(selected) != assembledMaxSourcePaths || stats.TopLevelSourcePathsOmitted != 1 {
		t.Fatalf("top-level selection = paths:%d stats:%+v", len(selected), stats)
	}
	if selected[0] != "src/199.go" || selected[1] != "src/200.go" {
		t.Fatalf("priority order starts %v, want support then lexical", selected[:2])
	}
	for _, path := range selected {
		if path == "src/198.go" {
			t.Fatal("lower-priority lexical tail was retained")
		}
	}
}

func TestAssemblyNoticeTemplatesAreValidAndCapacityIsReserved(t *testing.T) {
	if len(assemblyNoticeTemplates) != assemblyNoticeSlots || assembledModelGapLimit+assemblyNoticeSlots != assembledMaxReviewGapItems {
		t.Fatalf("notice capacity invariant failed: templates=%d slots=%d model=%d total=%d", len(assemblyNoticeTemplates), assemblyNoticeSlots, assembledModelGapLimit, assembledMaxReviewGapItems)
	}
	notices := []sharedmodel.ReviewGap{
		assemblyNotice(assemblyNoticeConflict, nil, 1),
		assemblyNotice(assemblyNoticeSaturation, nil, 1),
		assemblyNotice(assemblyNoticeSection, nil, 1),
		assemblyNotice(assemblyNoticeEvidence, nil, 1, 1),
		assemblyNotice(assemblyNoticeOverview, nil, 1),
		assemblyNotice(assemblyNoticeDiagram, nil, 1),
	}
	dossier := sharedmodel.ComponentDossier{
		Title: "Component", Purpose: "Documents the component.", SourcePaths: []string{},
		Architecture: []sharedmodel.ArchitectureItem{}, Interfaces: []sharedmodel.InterfaceItem{}, DataModels: []sharedmodel.DataModelItem{},
		Workflows: []sharedmodel.WorkflowItem{}, Dependencies: []sharedmodel.DependencyItem{}, ReviewGaps: notices, Diagrams: []sharedmodel.Diagram{},
	}
	body, _ := json.Marshal(dossier)
	if validation := validateDossier(body, nil, nil); !validation.valid() {
		t.Fatalf("notice validation issues = %+v", validation.issues)
	}

	modelGaps := make([]sharedmodel.ReviewGap, assembledMaxReviewGapItems)
	for index := range modelGaps {
		modelGaps[index] = sharedmodel.ReviewGap{Kind: "uncertainty", Description: fmt.Sprintf("Question %03d remains unresolved.", index), Recommendation: "Inspect the relevant implementation.", SourcePaths: []string{}}
	}
	stats := fragmentAssemblyStats{SectionItemsOmitted: make(map[sharedmodel.FragmentKind]int)}
	bounded, _, _, err := mergeFragmentItems(modelGaps, assembledModelGapLimit, sharedmodel.FragmentReviewGaps, reviewGapMergeSpec(), &stats)
	if err != nil {
		t.Fatalf("merge review gaps: %v", err)
	}
	if len(bounded)+len(notices) != assembledMaxReviewGapItems || stats.ModelReviewGapsOmitted != assemblyNoticeSlots {
		t.Fatalf("review-gap reservation = model:%d notices:%d stats:%+v", len(bounded), len(notices), stats)
	}
}

func TestDenseAssemblyBoundsAllNoticeCategories(t *testing.T) {
	component := sharedmodel.Component{Key: "services/dense"}
	const sourceScopes = assembledMaxSourcePaths + 1
	ledger := completeTestLedger(t, component, sourceScopes, true, func(scope fragmentScope) fragmentValidation {
		path := fmt.Sprintf("src/%03d.go", scope.SourceBatchIndex-1)
		switch scope.Kind {
		case sharedmodel.FragmentArchitecture:
			items := []sharedmodel.ArchitectureItem{{Title: "Boundary", Description: "Owns dense processing.", SourcePaths: []string{path}}}
			if scope.SourceBatchIndex == 1 {
				items = append(items, sharedmodel.ArchitectureItem{Title: "Boundary", Description: "Coordinates dense processing.", SourcePaths: []string{path}})
			}
			return testListFragment(t, scope, sharedmodel.ArchitectureFragment{Complete: true, Items: items}, []string{path})
		case sharedmodel.FragmentReviewGaps:
			if scope.SourceBatchIndex <= 50 {
				first := (scope.SourceBatchIndex - 1) * 2
				items := []sharedmodel.ReviewGap{
					{Kind: "uncertainty", Description: fmt.Sprintf("Question %03d remains unresolved.", first), Recommendation: "Inspect the relevant implementation.", SourcePaths: []string{path}},
					{Kind: "uncertainty", Description: fmt.Sprintf("Question %03d remains unresolved.", first+1), Recommendation: "Inspect the relevant implementation.", SourcePaths: []string{path}},
				}
				return testListFragment(t, scope, sharedmodel.ReviewGapsFragment{Complete: true, Items: items}, []string{path})
			}
		}
		return emptyTestFragment(t, scope)
	})
	dossier, stats, err := assembleComponentDossier(fragmentAssemblyInput{
		Component: component, Catalog: []string{component.Key}, Coverage: ledger, DiagramFallback: true,
	})
	if err != nil {
		t.Fatalf("assemble dense fixture: %v", err)
	}
	if len(dossier.ReviewGaps) != assembledMaxReviewGapItems {
		t.Fatalf("review gaps = %d, want %d", len(dossier.ReviewGaps), assembledMaxReviewGapItems)
	}
	if len(dossier.SourcePaths) != assembledMaxSourcePaths || len(dossier.Architecture[0].SourcePaths) != assembledMaxItemSourcePaths {
		t.Fatalf("bounded evidence = top:%d item:%d", len(dossier.SourcePaths), len(dossier.Architecture[0].SourcePaths))
	}
	if stats.ConflictIdentities != 1 || stats.SaturatedScopes != 51 || stats.ModelReviewGapsOmitted != 6 || stats.ItemEvidencePathsOmitted != 1 || stats.NoticeEvidencePathsOmitted != 1 || stats.TopLevelSourcePathsOmitted != 1 || stats.OverviewFallbacks != 1 || stats.DiagramFallbacks != 1 {
		t.Fatalf("dense assembly stats = %+v", stats)
	}
	for _, prefix := range []string{
		"Assembly retained conflicting claims",
		"Fragment coverage reached",
		"Assembly omitted 6 items",
		"Assembly omitted 2 item citations and 1 top-level source paths",
		"Assembly used a deterministic component overview",
		"Assembly omitted diagrams",
	} {
		if !containsGapDescription(dossier.ReviewGaps, prefix) {
			t.Fatalf("dense assembly missing notice %q", prefix)
		}
	}
}

func TestAssemblyUsesSafeOverviewFallbackAndFinalValidation(t *testing.T) {
	component := sharedmodel.Component{Key: "services/<legacy>"}
	ledger := completeTestLedger(t, component, 1, false, nil)
	dossier, _, err := assembleComponentDossier(fragmentAssemblyInput{Component: component, Catalog: []string{component.Key}, Coverage: ledger})
	if err != nil {
		t.Fatalf("assemble fallback dossier: %v", err)
	}
	if dossier.Title != "Legacy" || dossier.Purpose != "Documentation for the services/%3Clegacy%3E component." {
		t.Fatalf("fallback overview = %q / %q", dossier.Title, dossier.Purpose)
	}

	contaminated := dossier
	contaminated.Architecture = []sharedmodel.ArchitectureItem{{Title: "Boundary", Description: "Owns processing.", SourcePaths: []string{"src/not-validated.go"}}}
	invalid, err := validateAssembledDossier(contaminated, nil, []string{component.Key})
	if err == nil || !strings.Contains(err.Error(), "final_validation") || !strings.Contains(err.Error(), issueUnknownEvidence) {
		t.Fatalf("contamination error = %v", err)
	}
	if !reflect.DeepEqual(invalid, sharedmodel.ComponentDossier{}) {
		t.Fatalf("invalid assembly returned dossier: %+v", invalid)
	}
	invalidEnum := dossier
	invalidEnum.Interfaces = []sharedmodel.InterfaceItem{{Name: "Entry", Kind: "teleport", Direction: "inbound", Description: "Accepts work.", SourcePaths: []string{"src/entry.go"}}}
	if _, err := validateAssembledDossier(invalidEnum, []string{"src/entry.go"}, []string{component.Key}); err == nil || !strings.Contains(err.Error(), issueInvalidEnum) {
		t.Fatalf("invalid-enum final validation error = %v", err)
	}

	longComponent := sharedmodel.Component{Key: "services/" + strings.Repeat("a-", 250)}
	if title := fallbackComponentTitle(longComponent); len(title) > schemaMaxTitle {
		t.Fatalf("fallback title is %d bytes, limit %d", len(title), schemaMaxTitle)
	}
}

func TestSaturatedDiagramFragmentIsRetainedWithNotice(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	ledger := completeTestLedger(t, component, 1, false, nil)
	path := "src/flow.go"
	diagram := sharedmodel.DiagramsFragment{Complete: true, Items: []sharedmodel.Diagram{{
		Type: sharedmodel.DiagramFlowchart, Title: "Flow", SourcePaths: []string{path},
		Nodes: []sharedmodel.FlowchartNode{{Key: "start", Label: "Start"}}, Edges: []sharedmodel.FlowchartEdge{},
	}}}
	body, _ := json.Marshal(diagram)
	diagramScope := fragmentScope{
		ComponentKey: component.Key, Kind: sharedmodel.FragmentDiagrams,
		SourceBatchIndex: 1, SourceBatchCount: 1, SourceChunkIndex: 1, SourceChunkCount: 1,
		SplitPath: "diagram", AllowedEvidence: []string{path},
	}
	validation := validateScopedFragment(diagramScope, body, []string{path}, []string{component.Key})
	if !validation.valid() || !validation.saturated {
		t.Fatalf("diagram validation = %+v", validation)
	}
	dossier, stats, err := assembleComponentDossier(fragmentAssemblyInput{
		Component: component, Catalog: []string{component.Key}, Coverage: ledger, DiagramFragments: []fragmentValidation{validation},
	})
	if err != nil {
		t.Fatalf("assemble diagram fallback: %v", err)
	}
	if len(dossier.Diagrams) != 1 || stats.DiagramFallbacks != 0 || stats.SaturatedScopes != 1 || !containsGapDescription(dossier.ReviewGaps, "Fragment coverage reached") {
		t.Fatalf("diagram saturation result = diagrams:%d stats:%+v gaps:%+v", len(dossier.Diagrams), stats, dossier.ReviewGaps)
	}
}

func TestAssemblyRejectsCrossComponentOptionalFragments(t *testing.T) {
	component := sharedmodel.Component{Key: "services/payments"}
	other := sharedmodel.Component{Key: "services/other"}
	ledger := completeTestLedger(t, component, 1, false, nil)
	overview := testOverviewCandidate(t, other, sharedmodel.OverviewCandidate{
		Title: "Other", Purpose: "Documents another component.", SourcePaths: []string{"src/other.go"},
	}, []string{"src/other.go"}, []string{other.Key})
	if _, _, err := assembleComponentDossier(fragmentAssemblyInput{
		Component: component, Catalog: []string{component.Key, other.Key}, Coverage: ledger, OverviewCandidates: []fragmentValidation{overview},
	}); err == nil || !strings.Contains(err.Error(), "overview_candidate_scope_mismatch") {
		t.Fatalf("cross-component overview error = %v", err)
	}

	diagram := sharedmodel.DiagramsFragment{Complete: true, Items: []sharedmodel.Diagram{{
		Type: sharedmodel.DiagramFlowchart, Title: "Other flow", SourcePaths: []string{"src/other.go"},
		Nodes: []sharedmodel.FlowchartNode{}, Edges: []sharedmodel.FlowchartEdge{},
	}}}
	body, _ := json.Marshal(diagram)
	diagramScope := fragmentScope{
		ComponentKey: other.Key, Kind: sharedmodel.FragmentDiagrams,
		SourceBatchIndex: 1, SourceBatchCount: 1, SourceChunkIndex: 1, SourceChunkCount: 1,
		SplitPath: "diagram", AllowedEvidence: []string{"src/other.go"},
	}
	validation := validateScopedFragment(diagramScope, body, diagramScope.AllowedEvidence, []string{component.Key, other.Key})
	if _, _, err := assembleComponentDossier(fragmentAssemblyInput{
		Component: component, Catalog: []string{component.Key, other.Key}, Coverage: ledger, DiagramFragments: []fragmentValidation{validation},
	}); err == nil || !strings.Contains(err.Error(), "diagram_fragment_scope_mismatch") {
		t.Fatalf("cross-component diagram error = %v", err)
	}
}

type assembledFixture struct {
	dossier sharedmodel.ComponentDossier
	stats   fragmentAssemblyStats
}

func assembledPermutationFixture(t *testing.T, component sharedmodel.Component, reverse bool) assembledFixture {
	t.Helper()
	ledger := completeTestLedger(t, component, 3, reverse, func(scope fragmentScope) fragmentValidation {
		path := fmt.Sprintf("src/%03d.go", scope.SourceBatchIndex-1)
		switch scope.Kind {
		case sharedmodel.FragmentArchitecture:
			item := sharedmodel.ArchitectureItem{Title: "Boundary", Description: "Owns processing.", SourcePaths: []string{path}}
			if scope.SourceBatchIndex == 3 {
				item.Description = "Coordinates processing."
			}
			return testListFragment(t, scope, sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{item}}, []string{path})
		case sharedmodel.FragmentInterfaces:
			item := sharedmodel.InterfaceItem{Name: "Handle", Kind: "function", Direction: "inbound", Description: "Handles one request.", SourcePaths: []string{path}}
			return testListFragment(t, scope, sharedmodel.InterfacesFragment{Complete: true, Items: []sharedmodel.InterfaceItem{item}}, []string{path})
		case sharedmodel.FragmentDataModels:
			item := sharedmodel.DataModelItem{Name: "Request", Kind: "request", Description: "Carries request data.", Fields: []sharedmodel.DataField{}, Relationships: []sharedmodel.DataRelationship{}, SourcePaths: []string{path}}
			return testListFragment(t, scope, sharedmodel.DataModelsFragment{Complete: true, Items: []sharedmodel.DataModelItem{item}}, []string{path})
		case sharedmodel.FragmentWorkflows:
			item := sharedmodel.WorkflowItem{Name: "Handle request", Description: "Handles one request.", Steps: []sharedmodel.WorkflowStep{{Actor: "Caller", Action: "Submits work."}}, SourcePaths: []string{path}}
			return testListFragment(t, scope, sharedmodel.WorkflowsFragment{Complete: true, Items: []sharedmodel.WorkflowItem{item}}, []string{path})
		case sharedmodel.FragmentDependencies:
			item := sharedmodel.DependencyItem{Name: "helpers", Kind: "library", Purpose: "Provides helpers.", SourcePaths: []string{path}}
			return testListFragment(t, scope, sharedmodel.DependenciesFragment{Complete: true, Items: []sharedmodel.DependencyItem{item}}, []string{path})
		case sharedmodel.FragmentReviewGaps:
			item := sharedmodel.ReviewGap{Kind: "uncertainty", Description: "One behavior remains uncertain.", Recommendation: "Inspect the implementation.", SourcePaths: []string{path}}
			return testListFragment(t, scope, sharedmodel.ReviewGapsFragment{Complete: true, Items: []sharedmodel.ReviewGap{item}}, []string{path})
		}
		return emptyTestFragment(t, scope)
	})
	candidates := []fragmentValidation{
		testOverviewCandidate(t, component, sharedmodel.OverviewCandidate{Title: "Beta", Purpose: "Documents beta behavior.", SourcePaths: []string{"src/001.go"}}, []string{"src/001.go"}, []string{component.Key}),
		testOverviewCandidate(t, component, sharedmodel.OverviewCandidate{Title: "Alpha", Purpose: "Documents alpha behavior.", SourcePaths: []string{"src/000.go"}}, []string{"src/000.go"}, []string{component.Key}),
	}
	if reverse {
		candidates = reverseValidations(candidates)
	}
	dossier, stats, err := assembleComponentDossier(fragmentAssemblyInput{
		Component: component, Catalog: []string{component.Key}, Coverage: ledger, OverviewCandidates: candidates,
	})
	if err != nil {
		t.Fatalf("assemble permutation: %v", err)
	}
	return assembledFixture{dossier: dossier, stats: stats}
}

func completeTestLedger(t *testing.T, component sharedmodel.Component, sourceScopes int, reverse bool, validationFor func(fragmentScope) fragmentValidation) *fragmentCoverageLedger {
	t.Helper()
	planned := testPlannedScopes(component, sourceScopes)
	if reverse {
		reverseScopes(planned)
	}
	ledger, err := newFragmentCoverageLedger(component, planned)
	if err != nil {
		t.Fatalf("new complete ledger: %v", err)
	}
	for _, scope := range planned {
		validation := emptyTestFragment(t, scope)
		if validationFor != nil {
			validation = validationFor(scope)
		}
		if err := ledger.record(scope, validation, validation.saturated); err != nil {
			t.Fatalf("record %s batch %d: %v", scope.Kind, scope.SourceBatchIndex, err)
		}
	}
	return ledger
}

func testPlannedScopes(component sharedmodel.Component, sourceScopes int) []fragmentScope {
	result := make([]fragmentScope, 0, sourceScopes*len(requiredFragmentKinds()))
	for batch := 1; batch <= sourceScopes; batch++ {
		for _, kind := range requiredFragmentKinds() {
			result = append(result, fragmentScope{
				ComponentKey: component.Key, RootComponent: component.RootComponent, Kind: kind,
				SourceBatchIndex: batch, SourceBatchCount: sourceScopes, SourceChunkIndex: 1, SourceChunkCount: 1,
				SplitPath: fmt.Sprintf("b%d/c1", batch), AllowedEvidence: []string{fmt.Sprintf("src/%03d.go", batch-1)},
			})
		}
	}
	return result
}

func emptyTestFragment(t *testing.T, scope fragmentScope) fragmentValidation {
	t.Helper()
	switch scope.Kind {
	case sharedmodel.FragmentArchitecture:
		return testListFragment(t, scope, sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{}}, nil)
	case sharedmodel.FragmentInterfaces:
		return testListFragment(t, scope, sharedmodel.InterfacesFragment{Complete: true, Items: []sharedmodel.InterfaceItem{}}, nil)
	case sharedmodel.FragmentDataModels:
		return testListFragment(t, scope, sharedmodel.DataModelsFragment{Complete: true, Items: []sharedmodel.DataModelItem{}}, nil)
	case sharedmodel.FragmentWorkflows:
		return testListFragment(t, scope, sharedmodel.WorkflowsFragment{Complete: true, Items: []sharedmodel.WorkflowItem{}}, nil)
	case sharedmodel.FragmentDependencies:
		return testListFragment(t, scope, sharedmodel.DependenciesFragment{Complete: true, Items: []sharedmodel.DependencyItem{}}, nil)
	case sharedmodel.FragmentReviewGaps:
		return testListFragment(t, scope, sharedmodel.ReviewGapsFragment{Complete: true, Items: []sharedmodel.ReviewGap{}}, nil)
	default:
		return fragmentValidation{kind: scope.Kind, issues: []sharedmodel.ValidationIssue{{Code: issueInvalidValue}}}
	}
}

func testListFragment(t *testing.T, scope fragmentScope, value any, _ []string) fragmentValidation {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s test fragment: %v", scope.Kind, err)
	}
	result := validateScopedFragment(scope, body, scope.AllowedEvidence, []string{scope.ComponentKey, "services/billing"})
	if !result.valid() {
		t.Fatalf("validate %s test fragment: %+v", scope.Kind, result.issues)
	}
	return result
}

func testOverviewCandidate(t *testing.T, component sharedmodel.Component, value sharedmodel.OverviewCandidate, evidence, catalog []string) fragmentValidation {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal overview candidate: %v", err)
	}
	scope := fragmentScope{
		ComponentKey: component.Key, RootComponent: component.RootComponent, Kind: sharedmodel.FragmentOverviewCandidate,
		SourceBatchIndex: 1, SourceBatchCount: 1, SourceChunkIndex: 1, SourceChunkCount: 1,
		SplitPath: "overview", AllowedEvidence: evidence,
	}
	result := validateScopedFragment(scope, body, evidence, catalog)
	if !result.valid() {
		t.Fatalf("validate overview candidate: %+v", result.issues)
	}
	return result
}

func plannedScope(t *testing.T, scopes []fragmentScope, kind sharedmodel.FragmentKind) fragmentScope {
	t.Helper()
	for _, scope := range scopes {
		if scope.Kind == kind {
			return scope
		}
	}
	t.Fatalf("planned scope %s not found", kind)
	return fragmentScope{}
}

func plannedScopeForBatch(t *testing.T, scopes []fragmentScope, kind sharedmodel.FragmentKind, batch int) fragmentScope {
	t.Helper()
	for _, scope := range scopes {
		if scope.Kind == kind && scope.SourceBatchIndex == batch {
			return scope
		}
	}
	t.Fatalf("planned scope %s batch %d not found", kind, batch)
	return fragmentScope{}
}

func containsGapDescription(gaps []sharedmodel.ReviewGap, prefix string) bool {
	for _, gap := range gaps {
		if strings.HasPrefix(gap.Description, prefix) {
			return true
		}
	}
	return false
}

func reverseScopes(values []fragmentScope) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseStrings(values []string) []string {
	result := append([]string(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func reverseValidations(values []fragmentValidation) []fragmentValidation {
	result := append([]fragmentValidation(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
