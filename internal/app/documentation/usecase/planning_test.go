package usecase

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

func TestBuildGenerationPlanBootstrapsMissingState(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
		planSource("services/web/main.go", sharedmodel.RoleProductionSource, true, "web"),
	})

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if plan.Mode != "full" || plan.FullReason != "state_missing" || plan.Noop {
		t.Fatalf("plan = %+v, want full bootstrap", plan)
	}
	if plan.Calls.Normal != 2 || plan.Calls.Primary != 2 || len(plan.DeletedDocuments) != 0 {
		t.Fatalf("calls = %+v, want two normal generations", plan.Calls)
	}
	if len(plan.AffectedComponents) != 2 {
		t.Fatalf("affected = %+v, want two components", plan.AffectedComponents)
	}
	for _, affected := range plan.AffectedComponents {
		if affected.Action != sharedmodel.ComponentCreate || affected.ExistedBefore {
			t.Errorf("affected %q = %+v, want create", affected.Key, affected)
		}
	}
}

func TestBuildGenerationPlanForcesFullBypassingSuppression(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
	})
	state := compatibleFixtureState(t, input, files, components)

	// Without --full an unchanged worktree is a no-op.
	baseline, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() baseline error = %v", err)
	}
	if !baseline.Noop || baseline.Calls.Primary != 0 {
		t.Fatalf("baseline = %+v, want unchanged no-op", baseline)
	}

	// --full regenerates every current component regardless of input-hash suppression.
	input.Full = true
	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() full error = %v", err)
	}
	if plan.Mode != "full" || plan.FullReason != "explicit_full" || plan.Noop {
		t.Fatalf("plan = %+v, want explicit full generation", plan)
	}
	if len(plan.AffectedComponents) != 1 || plan.AffectedComponents[0].Action != sharedmodel.ComponentRegenerate {
		t.Fatalf("affected = %+v, want forced regeneration", plan.AffectedComponents)
	}
	if plan.Calls.Primary != 1 {
		t.Errorf("calls = %+v, want one forced generation", plan.Calls)
	}
}

func TestBuildGenerationPlanRegeneratesRenameWithinOwner(t *testing.T) {
	input := testPlanInput()
	input.BaseSHA = "base"
	input.HeadSHA = "head"
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/keep.go", sharedmodel.RoleProductionSource, true, "keep"),
		planSource("services/api/new.go", sharedmodel.RoleProductionSource, true, "moved"),
	})
	state := compatibleFixtureState(t, input, files, components)
	delete(state.Files, "services/api/new.go")
	state.Files["services/api/old.go"] = sharedmodel.StateFile{
		SourceHash: hashText("moved"), Role: sharedmodel.RoleProductionSource,
		TriggersRegeneration: true, ComponentKey: "services/api",
	}
	changes := []sharedmodel.RawChange{{
		Status: sharedmodel.ChangeRenamed, OldPath: "services/api/old.go", NewPath: "services/api/new.go", Similarity: 100,
	}}

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, changes, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if plan.Mode != "incremental" || plan.Noop || len(plan.DeletedDocuments) != 0 {
		t.Fatalf("plan = %+v, want in-place rename regeneration", plan)
	}
	if len(plan.AffectedComponents) != 1 || plan.AffectedComponents[0].Key != "services/api" ||
		plan.AffectedComponents[0].Action != sharedmodel.ComponentRegenerate {
		t.Fatalf("affected = %+v, want single owner regeneration", plan.AffectedComponents)
	}
	if !containsReason(plan.AffectedComponents[0].Reasons, "source_renamed") {
		t.Errorf("reasons = %v, want source_renamed", plan.AffectedComponents[0].Reasons)
	}
}

func TestBuildGenerationPlanMovesRenameAcrossOwners(t *testing.T) {
	input := testPlanInput()
	input.BaseSHA = "base"
	input.HeadSHA = "head"
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/web/main.go", sharedmodel.RoleProductionSource, true, "moved"),
	})
	state := compatibleFixtureState(t, input, files, components)
	// Before the rename the file lived under services/api, which owned it exclusively;
	// services/web did not exist yet.
	delete(state.Files, "services/web/main.go")
	delete(state.Components, "services/web")
	state.Files["services/api/main.go"] = sharedmodel.StateFile{
		SourceHash: hashText("moved"), Role: sharedmodel.RoleProductionSource,
		TriggersRegeneration: true, ComponentKey: "services/api",
	}
	state.Components["services/api"] = sharedmodel.StateComponent{
		InputHash: hashText("old input services/api"), Document: "docs/generated/components/services/api/index.md",
	}
	changes := []sharedmodel.RawChange{{
		Status: sharedmodel.ChangeRenamed, OldPath: "services/api/main.go", NewPath: "services/web/main.go", Similarity: 100,
	}}

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, changes, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if len(plan.AffectedComponents) != 2 || plan.Calls.Primary != 1 || len(plan.DeletedDocuments) != 1 {
		t.Fatalf("plan = %+v, want old deletion and new generation", plan)
	}
	deleted := affectedByKey(t, plan.AffectedComponents, "services/api")
	created := affectedByKey(t, plan.AffectedComponents, "services/web")
	if deleted.Action != sharedmodel.ComponentDelete {
		t.Errorf("old owner action = %s, want delete", deleted.Action)
	}
	if created.Action != sharedmodel.ComponentCreate || !containsReason(created.Reasons, "source_renamed") {
		t.Errorf("new owner = %+v, want created rename", created)
	}
}

func TestBuildGenerationPlanTreatsDocsOnlyChangeAsNoop(t *testing.T) {
	input := testPlanInput()
	input.BaseSHA = "base"
	input.HeadSHA = "head"
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
	})
	state := compatibleFixtureState(t, input, files, components)
	changes := []sharedmodel.RawChange{
		{Status: sharedmodel.ChangeModified, NewPath: "docs/generated/architecture.md"},
		{Status: sharedmodel.ChangeModified, NewPath: ".docify/state.json"},
	}

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, changes, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if !plan.Noop || plan.Calls.Primary != 0 || len(plan.AffectedComponents) != 0 || len(plan.DeletedDocuments) != 0 {
		t.Fatalf("plan = %+v, want docs-only no-op", plan)
	}
}

func TestBuildGenerationPlanSkipsUnchangedComponentOnConfigImpact(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
	})

	// The canonical input hash a bootstrap would produce for this component.
	bootstrap, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() bootstrap error = %v", err)
	}
	inputHash := bootstrap.AffectedComponents[0].InputHash

	// A context-limit configuration change selects every component, but an unchanged
	// component with a matching input hash must be skipped without an LLM call.
	state := compatibleFixtureState(t, input, files, components)
	state.ConfigHash = "sha256:stale-aggregate"
	state.ConfigHashes.Context = "sha256:changed-context"
	component := state.Components["services/api"]
	component.InputHash = inputHash
	state.Components["services/api"] = component

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if len(plan.AffectedComponents) != 1 || plan.AffectedComponents[0].Action != sharedmodel.ComponentSkipUnchanged {
		t.Fatalf("affected = %+v, want skip_unchanged", plan.AffectedComponents)
	}
	if plan.Calls.Primary != 0 {
		t.Errorf("calls = %+v, want no LLM call for unchanged component", plan.Calls)
	}
}

func TestBuildGenerationPlanIsStableAcrossRuns(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
		planSource("services/web/main.go", sharedmodel.RoleProductionSource, true, "web"),
	})

	first, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() first error = %v", err)
	}
	second, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() second error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("planner output differs across runs:\nfirst  = %+v\nsecond = %+v", first, second)
	}
}

func TestBuildGenerationPlanAttachesRootManifestContext(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("go.mod", sharedmodel.RoleDependencyManifest, true, "module example\n"),
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
	})

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}

	api := summaryByKey(t, plan.Components, "services/api")
	if !reflect.DeepEqual(api.ManifestPaths, []string{"go.mod"}) || api.ManifestBytes != int64(len("module example\n")) {
		t.Errorf("services/api manifests = %+v (%d bytes), want the root go.mod as context", api.ManifestPaths, api.ManifestBytes)
	}

	// The root component owns go.mod as triggering source, so it must not also list it
	// as relevant manifest context.
	root := summaryByKey(t, plan.Components, "@root")
	if len(root.ManifestPaths) != 0 {
		t.Errorf("@root manifests = %v, want none (owned as triggering source)", root.ManifestPaths)
	}
}

func TestBuildGenerationPlanHashesRootManifestContent(t *testing.T) {
	build := func(manifest string) sharedmodel.AffectedComponent {
		t.Helper()
		input := testPlanInput()
		files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
			planSource("go.mod", sharedmodel.RoleDependencyManifest, true, manifest),
			planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
		})
		plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
		if err != nil {
			t.Fatalf("buildGenerationPlan() error = %v", err)
		}
		return affectedByKey(t, plan.AffectedComponents, "services/api")
	}

	first := build("module example\ngo 1.26\n")
	second := build("module example\ngo 1.27\n")
	if first.InputHash == second.InputHash {
		t.Errorf("input hash %s did not change when root manifest content changed", first.InputHash)
	}
}

func summaryByKey(t *testing.T, summaries []sharedmodel.ComponentSummary, key string) sharedmodel.ComponentSummary {
	t.Helper()
	for _, summary := range summaries {
		if summary.Key == key {
			return summary
		}
	}
	t.Fatalf("component summary %q not found in %+v", key, summaries)
	return sharedmodel.ComponentSummary{}
}

func containsReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func affectedByKey(t *testing.T, components []sharedmodel.AffectedComponent, key string) sharedmodel.AffectedComponent {
	t.Helper()
	for _, component := range components {
		if component.Key == key {
			return component
		}
	}
	t.Fatalf("affected component %q not found in %+v", key, components)
	return sharedmodel.AffectedComponent{}
}

func TestBuildGenerationPlanSelectsModifiedComponent(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "new source"),
	})
	state := compatibleFixtureState(t, input, files, components)
	state.Files["services/api/main.go"] = sharedmodel.StateFile{
		SourceHash: hashText("old source"), Role: sharedmodel.RoleProductionSource,
		TriggersRegeneration: true, ComponentKey: "services/api",
	}

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if plan.Mode != "incremental" || plan.Noop || plan.Calls.Primary != 1 {
		t.Fatalf("plan = %+v, want one incremental call", plan)
	}
	if len(plan.AffectedComponents) != 1 || plan.AffectedComponents[0].Action != sharedmodel.ComponentRegenerate ||
		plan.AffectedComponents[0].Reasons[0] != "source_modified" {
		t.Errorf("affected = %+v, want modified regeneration", plan.AffectedComponents)
	}
}

func TestBuildGenerationPlanIgnoresTestOnlyChange(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "source"),
		planSource("services/api/main_test.go", sharedmodel.RoleTest, false, "new test"),
	})
	state := compatibleFixtureState(t, input, files, components)
	testState := state.Files["services/api/main_test.go"]
	testState.SourceHash = hashText("old test")
	state.Files["services/api/main_test.go"] = testState

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if !plan.Noop || len(plan.AffectedComponents) != 0 || plan.Calls.Primary != 0 {
		t.Errorf("plan = %+v, want test-only no-op", plan)
	}
}

func TestBuildGenerationPlanDeletesEmptyComponentWithoutCall(t *testing.T) {
	input := testPlanInput()
	state := compatibleFixtureState(t, input, nil, nil)
	state.Files["services/api/main.go"] = sharedmodel.StateFile{
		SourceHash: hashText("old"), Role: sharedmodel.RoleProductionSource,
		TriggersRegeneration: true, ComponentKey: "services/api",
	}
	state.Components["services/api"] = sharedmodel.StateComponent{
		InputHash: hashText("input"), Document: "docs/generated/components/services/api/index.md",
	}

	plan, err := buildGenerationPlan(input, nil, nil, sharedmodel.StateLoadResult{State: state}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if plan.Noop || plan.Calls.Primary != 0 || len(plan.DeletedDocuments) != 1 {
		t.Fatalf("plan = %+v, want one call-free deletion", plan)
	}
	if len(plan.AffectedComponents) != 1 || plan.AffectedComponents[0].Action != sharedmodel.ComponentDelete {
		t.Errorf("affected = %+v, want deletion", plan.AffectedComponents)
	}
}

func TestBuildGenerationPlanDetectsOwnershipRekey(t *testing.T) {
	input := testPlanInput()
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/main.go", sharedmodel.RoleProductionSource, true, "source"),
	})
	state := compatibleFixtureState(t, input, files, nil)
	oldFile := state.Files["services/api/main.go"]
	oldFile.ComponentKey = "services"
	state.Files["services/api/main.go"] = oldFile
	state.Components["services"] = sharedmodel.StateComponent{
		InputHash: hashText("old input"), Document: "docs/generated/components/services/index.md",
	}

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{State: state}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if len(plan.AffectedComponents) != 2 || plan.Calls.Primary != 1 || len(plan.DeletedDocuments) != 1 {
		t.Fatalf("plan = %+v, want old deletion and new generation", plan)
	}
	if plan.AffectedComponents[0].Action != sharedmodel.ComponentDelete || plan.AffectedComponents[1].Action != sharedmodel.ComponentCreate {
		t.Errorf("actions = %s, %s, want delete/create", plan.AffectedComponents[0].Action, plan.AffectedComponents[1].Action)
	}
}

func TestBuildGenerationPlanCreatesStableBatchesAndSynthesis(t *testing.T) {
	input := testPlanInput()
	input.ComponentPolicy.MaxContextBytes = 100
	input.ComponentPolicy.MaxBatchBytes = 70
	input.ComponentPolicy.MaxRequestBytes = 100_000
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/a.go", sharedmodel.RoleProductionSource, true, strings.Repeat("a", 60)),
		planSource("services/api/b.go", sharedmodel.RoleProductionSource, true, strings.Repeat("b", 60)),
	})

	plan, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
	if err != nil {
		t.Fatalf("buildGenerationPlan() error = %v", err)
	}
	if plan.Calls.Batch != 2 || plan.Calls.Synthesis != 1 || plan.Calls.Primary != 3 || plan.Calls.MaximumRepair != 3 {
		t.Fatalf("calls = %+v, want two batches and synthesis", plan.Calls)
	}
	batches := plan.AffectedComponents[0].Batches
	if len(batches) != 2 || batches[0].SourcePaths[0] != "services/api/a.go" || batches[1].SourcePaths[0] != "services/api/b.go" {
		t.Errorf("batches = %+v, want stable path order", batches)
	}
}

func TestBuildGenerationPlanRejectsInfeasibleSynthesis(t *testing.T) {
	input := testPlanInput()
	input.ComponentPolicy.MaxContextBytes = 100
	input.ComponentPolicy.MaxBatchBytes = 70
	input.ComponentPolicy.MaxRequestBytes = 10_000
	files, components := ownedFixture(t, input, []sharedmodel.SourceFile{
		planSource("services/api/a.go", sharedmodel.RoleProductionSource, true, strings.Repeat("a", 60)),
		planSource("services/api/b.go", sharedmodel.RoleProductionSource, true, strings.Repeat("b", 60)),
	})

	_, err := buildGenerationPlan(input, components, files, sharedmodel.StateLoadResult{Missing: true}, nil, false)
	if err == nil || !strings.Contains(err.Error(), "worst-case synthesis request") {
		t.Fatalf("buildGenerationPlan() error = %v, want synthesis feasibility error", err)
	}
}

func ownedFixture(t *testing.T, input documentationmodel.PlanInput, files []sharedmodel.SourceFile) ([]sharedmodel.SourceFile, []sharedmodel.Component) {
	t.Helper()
	components, owned, err := discoverComponents(files, input.ComponentPolicy, input.SourcePolicy.DocsDir)
	if err != nil {
		t.Fatalf("discoverComponents() error = %v", err)
	}
	return owned, components
}

func compatibleFixtureState(t *testing.T, input documentationmodel.PlanInput, files []sharedmodel.SourceFile, components []sharedmodel.Component) sharedmodel.State {
	t.Helper()
	hashes, aggregate, err := configurationHashes(input)
	if err != nil {
		t.Fatalf("configurationHashes() error = %v", err)
	}
	state := sharedmodel.State{
		SchemaVersion: stateSchemaVersion, GeneratorVersion: generatorVersion, PlannerVersion: plannerVersion,
		PromptVersion: promptVersion, OutputSchemaVersion: outputSchemaVersion, ConfigHash: aggregate, ConfigHashes: hashes,
		Files: make(map[string]sharedmodel.StateFile), Components: make(map[string]sharedmodel.StateComponent),
	}
	for _, file := range files {
		state.Files[file.Path] = sharedmodel.StateFile{
			SourceHash: file.SourceHash, Role: file.Role, TriggersRegeneration: file.TriggersRegeneration,
			ComponentKey: file.ComponentKey, RootComponent: file.RootComponent,
		}
	}
	for _, component := range components {
		state.Components[component.Key] = sharedmodel.StateComponent{
			InputHash: hashText("old input " + component.Key), Document: component.Document, RootComponent: component.RootComponent,
		}
	}
	return state
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", digest)
}
