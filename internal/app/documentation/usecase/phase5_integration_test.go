package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

// exitCoder mirrors the transport's exit-code extraction so tests can assert typed errors
// without importing the transport package.
type exitCoder interface{ ExitCode() int }

func bootstrap(t *testing.T, application interface {
	Sync(context.Context, documentationmodel.SyncInput) (documentationmodel.ResultSummary, error)
}, dir string) {
	t.Helper()
	if _, err := application.Sync(context.Background(), syncInput(dir)); err != nil {
		t.Fatalf("bootstrap Sync() error = %v", err)
	}
}

func TestSyncIncrementalModifyTouchesOnlyAffectedArtifacts(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	callsAfterBootstrap := generator.calls.Load()

	docsRoot := filepath.Join(dir, "docs", "generated")
	before := snapshotTree(t, docsRoot)
	beforeState := readFileString(t, filepath.Join(dir, ".docify", "state.json"))

	// Regenerate only services/api by changing its triggering source. The variant makes the
	// regenerated dossier differ so the change is observable in the output bytes.
	generator.variant = "2"
	writeFile(t, dir, "services/api/service.go", "package api\n\nfunc Handle() { /* v2 */ }\n")
	// Stage only the changed source; staging the whole worktree would track the generated
	// tree and perturb the scan counts rendered into codebase_info.md.
	runGit(t, dir, "add", "services/api/service.go")

	result, err := application.Sync(context.Background(), syncInput(dir))
	if err != nil {
		t.Fatalf("incremental Sync() error = %v", err)
	}
	if result.Status != "synced" {
		t.Fatalf("status = %q, want synced", result.Status)
	}
	if delta := generator.calls.Load() - callsAfterBootstrap; delta != 1 {
		t.Fatalf("incremental sync made %d model calls, want exactly 1 (only the changed component)", delta)
	}

	after := snapshotTree(t, docsRoot)
	changed := changedFiles(before, after)
	wantChanged := map[string]bool{
		"components.md":                    true,
		"components/services/api/index.md": true,
	}
	for path := range changed {
		if !wantChanged[path] {
			t.Errorf("incremental sync changed unexpected document %q", path)
		}
	}
	for path := range wantChanged {
		if !changed[path] {
			t.Errorf("incremental sync did not change expected document %q", path)
		}
	}
	// The unrelated component and every deterministic index stay byte-for-byte identical.
	for _, unchanged := range []string{
		"index.md", "codebase_info.md", "architecture.md", "interfaces.md",
		"data_models.md", "workflows.md", "dependencies.md", "review_notes.md",
		"components/@root/index.md",
	} {
		if before[unchanged] != after[unchanged] {
			t.Errorf("incremental sync altered unrelated document %q", unchanged)
		}
	}
	if got := readFileString(t, filepath.Join(dir, ".docify", "state.json")); got == beforeState {
		t.Error("incremental sync did not update state")
	}
}

func TestSyncComponentDeletionRequiresNoModelCall(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	callsAfterBootstrap := generator.calls.Load()

	docsRoot := filepath.Join(dir, "docs", "generated")
	before := snapshotTree(t, docsRoot)

	// Remove the only triggering file of services/api so the component disappears.
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash("services/api/service.go"))); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	runGit(t, dir, "add", "services/api/service.go")

	result, err := application.Sync(context.Background(), syncInput(dir))
	if err != nil {
		t.Fatalf("deletion Sync() error = %v", err)
	}
	if result.Status != "synced" {
		t.Fatalf("status = %q, want synced", result.Status)
	}
	if delta := generator.calls.Load() - callsAfterBootstrap; delta != 0 {
		t.Fatalf("component deletion made %d model calls, want 0", delta)
	}

	if _, err := os.Stat(filepath.Join(docsRoot, filepath.FromSlash("components/services/api/index.md"))); !os.IsNotExist(err) {
		t.Errorf("deleted component page still present: %v", err)
	}
	after := snapshotTree(t, docsRoot)
	if before["components/@root/index.md"] != after["components/@root/index.md"] {
		t.Error("deletion altered the unrelated @root component page")
	}
	if strings.Contains(after["components.md"], `key="services/api"`) {
		t.Error("components.md still owns a section for the deleted component")
	}
	if !strings.Contains(after["components.md"], `key="@root"`) {
		t.Error("components.md lost the retained @root section")
	}

	var state struct {
		Components map[string]json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal([]byte(readFileString(t, filepath.Join(dir, ".docify", "state.json"))), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if _, ok := state.Components["services/api"]; ok {
		t.Error("state still records the deleted component")
	}
	if _, ok := state.Components["@root"]; !ok {
		t.Error("state dropped the retained @root component")
	}
}

func TestSyncTestOnlyChangeIsNoop(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	callsAfterBootstrap := generator.calls.Load()
	before := snapshotTree(t, filepath.Join(dir, "docs", "generated"))

	// A change confined to a non-triggering test file must not regenerate anything.
	writeFile(t, dir, "services/api/service_test.go", "package api\n\n// touched\n")
	runGit(t, dir, "add", "services/api/service_test.go")

	result, err := application.Sync(context.Background(), syncInput(dir))
	if err != nil {
		t.Fatalf("test-only Sync() error = %v", err)
	}
	if result.Status != "noop" {
		t.Fatalf("status = %q, want noop", result.Status)
	}
	if delta := generator.calls.Load() - callsAfterBootstrap; delta != 0 {
		t.Fatalf("test-only change made %d model calls, want 0", delta)
	}
	after := snapshotTree(t, filepath.Join(dir, "docs", "generated"))
	for path, content := range before {
		if after[path] != content {
			t.Errorf("test-only change altered %q", path)
		}
	}
}

func TestCheckReportsCurrentAfterSync(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	callsAfterBootstrap := generator.calls.Load()

	result, err := application.Check(context.Background(), checkInput(dir))
	if err != nil {
		t.Fatalf("Check() error = %v, want current", err)
	}
	if result.Status != "current" {
		t.Fatalf("status = %q, want current", result.Status)
	}
	if delta := generator.calls.Load() - callsAfterBootstrap; delta != 0 {
		t.Fatalf("check made %d model calls, want 0", delta)
	}
}

func TestCheckReportsStaleForUnsyncedChange(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	callsAfterBootstrap := generator.calls.Load()
	docsRoot := filepath.Join(dir, "docs", "generated")
	before := snapshotTree(t, docsRoot)

	// A triggering change that has not been synced makes the committed docs stale.
	writeFile(t, dir, "services/api/service.go", "package api\n\nfunc Handle() { /* pending */ }\n")
	runGit(t, dir, "add", "services/api/service.go")

	result, err := application.Check(context.Background(), checkInput(dir))
	if err == nil {
		t.Fatal("Check() error = nil, want a stale error")
	}
	var coder exitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 2 {
		t.Fatalf("stale error = %v, want exit code 2", err)
	}
	if result.Status != "stale" {
		t.Fatalf("status = %q, want stale", result.Status)
	}
	if delta := generator.calls.Load() - callsAfterBootstrap; delta != 0 {
		t.Fatalf("check made %d model calls, want 0", delta)
	}
	// check never installs: the committed tree is untouched.
	after := snapshotTree(t, docsRoot)
	for path, content := range before {
		if after[path] != content {
			t.Errorf("check altered committed document %q", path)
		}
	}
	if len(after) != len(before) {
		t.Error("check changed the installed file set")
	}
}

func TestCheckReportFailureDoesNotMaskStaleResult(t *testing.T) {
	application, _, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	writeFile(t, dir, "services/api/service.go", "package api\n\nfunc Handle() { /* pending */ }\n")
	runGit(t, dir, "add", "services/api/service.go")
	writeFile(t, dir, ".reports", "not a directory\n")
	input := checkInput(dir)
	input.SourcePolicy.ReportPath = ".reports/report.json"

	result, err := application.Check(context.Background(), input)
	var coder exitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 2 {
		t.Fatalf("Check() error = %v, want primary stale exit code 2", err)
	}
	if result.Status != "stale" {
		t.Fatalf("status = %q, want stale", result.Status)
	}
}

func TestCheckDetectsTamperWithoutModelCall(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	callsAfterBootstrap := generator.calls.Load()

	// A hand edit to an installed generated document, with no source change at all.
	tampered := filepath.Join(dir, "docs", "generated", "index.md")
	original := readFileString(t, tampered)
	if err := os.WriteFile(tampered, []byte(original+"\nedited by hand\n"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	result, err := application.Check(context.Background(), checkInput(dir))
	if err == nil {
		t.Fatal("Check() error = nil, want a stale error for tampered output")
	}
	var coder exitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 2 {
		t.Fatalf("stale error = %v, want exit code 2", err)
	}
	if result.Status != "stale" {
		t.Fatalf("status = %q, want stale", result.Status)
	}
	if delta := generator.calls.Load() - callsAfterBootstrap; delta != 0 {
		t.Fatalf("tamper detection made %d model calls, want 0", delta)
	}
}

func TestSyncFullRecoveryRebuildsAfterStateLoss(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)

	// Losing state without --full is unrecoverable: ownership of the existing docs cannot
	// be proven, so the run must refuse rather than clobber possibly handwritten files.
	if err := os.Remove(filepath.Join(dir, ".docify", "state.json")); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	_, err := application.Sync(context.Background(), syncInput(dir))
	if err == nil {
		t.Fatal("Sync() without --full succeeded after state loss, want an ownership refusal")
	}
	if !strings.Contains(err.Error(), "ownership") && !strings.Contains(err.Error(), "full") {
		t.Fatalf("error = %v, want an ownership-recovery failure that mentions --full", err)
	}

	// An explicit full recovery rebuilds and re-adopts the generated tree.
	input := syncInput(dir)
	input.Full = true
	callsBeforeRecovery := generator.calls.Load()
	result, err := application.Sync(context.Background(), input)
	if err != nil {
		t.Fatalf("Sync(--full) recovery error = %v", err)
	}
	if result.Status != "synced" {
		t.Fatalf("status = %q, want synced", result.Status)
	}
	if generator.calls.Load() <= callsBeforeRecovery {
		t.Fatal("full recovery did not regenerate any component")
	}
	if _, err := os.Stat(filepath.Join(dir, ".docify", "state.json")); err != nil {
		t.Errorf("state was not rebuilt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs", "generated", "index.md")); err != nil {
		t.Errorf("documentation was not rebuilt: %v", err)
	}
}

func TestSyncProtectsInvalidStateUntilExplicitFullRecovery(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	statePath := filepath.Join(dir, ".docify", "state.json")
	writeFile(t, dir, ".docify/state.json", "foreign state\n")

	_, err := application.Sync(context.Background(), syncInput(dir))
	if err == nil || !strings.Contains(err.Error(), "state file exists") {
		t.Fatalf("Sync() error = %v, want unproven state-path refusal", err)
	}
	if generator.calls.Load() != 0 {
		t.Fatalf("normal recovery made %d model calls before refusing the state path", generator.calls.Load())
	}
	if got := readFileString(t, statePath); got != "foreign state\n" {
		t.Fatalf("normal recovery changed foreign state to %q", got)
	}

	input := syncInput(dir)
	input.Full = true
	result, err := application.Sync(context.Background(), input)
	if err != nil {
		t.Fatalf("Sync(--full) error = %v", err)
	}
	if result.Status != "synced" || result.Plan.StateStatus != "invalid" {
		t.Fatalf("result = %+v, want explicit recovery from invalid state", result)
	}
	var state sharedmodel.State
	if err := json.Unmarshal([]byte(readFileString(t, statePath)), &state); err != nil || state.SchemaVersion == 0 {
		t.Fatalf("recovered state is invalid: %+v, %v", state, err)
	}
}

func TestSyncGenerationIncompatibilityRetainsOwnershipProof(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	statePath := filepath.Join(dir, ".docify", "state.json")

	var state sharedmodel.State
	if err := json.Unmarshal([]byte(readFileString(t, statePath)), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	state.GeneratorVersion = "older-generator"
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("encode incompatible state: %v", err)
	}
	if err := os.WriteFile(statePath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write incompatible state: %v", err)
	}

	callsBefore := generator.calls.Load()
	result, err := application.Sync(context.Background(), syncInput(dir))
	if err != nil {
		t.Fatalf("Sync() error = %v, want regeneration with retained ownership proof", err)
	}
	if result.Plan.FullReason != "state_incompatible" || result.Status != "synced" {
		t.Fatalf("result = %+v, want successful incompatible-state regeneration", result)
	}
	if generator.calls.Load() <= callsBefore {
		t.Fatal("generation incompatibility did not regenerate components")
	}
}

func TestSyncFullRecoveryStillRefusesForeignFile(t *testing.T) {
	application, _, dir := newSyncTestbed(t)
	bootstrap(t, application, dir)
	if err := os.Remove(filepath.Join(dir, ".docify", "state.json")); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	// A file the tool never produces sits under the generated root.
	writeFile(t, dir, "docs/generated/FOREIGN.md", "# keep me\n")

	input := syncInput(dir)
	input.Full = true
	if _, err := application.Sync(context.Background(), input); err == nil {
		t.Fatal("Sync(--full) overwrote/deleted a foreign file, want a refusal")
	}
	if got := readFileString(t, filepath.Join(dir, "docs", "generated", "FOREIGN.md")); got != "# keep me\n" {
		t.Error("full recovery altered a foreign file it does not own")
	}
}

func TestSyncWritesStructuredRunReport(t *testing.T) {
	application, _, dir := newSyncTestbed(t)
	input := syncInput(dir)
	input.SourcePolicy.ReportPath = ".docify/report.json"

	if _, err := application.Sync(context.Background(), input); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	reportData := readFileString(t, filepath.Join(dir, ".docify", "report.json"))
	if strings.Contains(reportData, dir) {
		t.Error("run report leaks the absolute repository path")
	}

	var report documentationmodel.RunReport
	if err := json.Unmarshal([]byte(reportData), &report); err != nil {
		t.Fatalf("decode run report: %v", err)
	}
	if report.SchemaVersion != 5 || report.Command != "sync" || report.Status != "synced" {
		t.Errorf("report header = %+v, want sync/synced schema 5", report)
	}
	if report.GenerationStrategy != "dossier" || report.PlannedLLM.Primary == 0 || report.PlannedLLM.MaximumHTTPAttempts == 0 {
		t.Errorf("report planning metadata = strategy %q calls %+v", report.GenerationStrategy, report.PlannedLLM)
	}
	if report.Mode != "full" || report.FullReason != "state_missing" {
		t.Errorf("report plan = mode %q reason %q, want full/state_missing", report.Mode, report.FullReason)
	}
	seen := make(map[string]string, len(report.AffectedComponents))
	for _, affected := range report.AffectedComponents {
		seen[affected.Key] = affected.Action
	}
	if seen["@root"] != "create" || seen["services/api"] != "create" {
		t.Errorf("affected components = %+v, want @root and services/api created", report.AffectedComponents)
	}
	if len(report.Documents.Added) == 0 {
		t.Error("report records no added documents for a bootstrap")
	}
	if !report.Validation.OutputValidated {
		t.Error("report does not record that output was validated")
	}
}

func TestPlanRunReportPreservesFragmentCostBoundsWithoutModelCalls(t *testing.T) {
	application, generator, dir := newSyncTestbed(t)
	sync := syncInput(dir)
	sync.GenerationPolicy.GenerationStrategy = "fragments"
	sync.ComponentPolicy.MaxRequestBytes = 500_000
	sync.SourcePolicy.ReportPath = ".docify/plan-report.json"
	input := documentationmodel.PlanInput{
		WorkingDirectory: sync.WorkingDirectory,
		SourcePolicy:     sync.SourcePolicy, ComponentPolicy: sync.ComponentPolicy, GenerationPolicy: sync.GenerationPolicy,
	}
	result, err := application.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if generator.calls.Load() != 0 {
		t.Fatalf("Plan() made %d model calls", generator.calls.Load())
	}
	var report documentationmodel.RunReport
	data := readFileString(t, filepath.Join(dir, ".docify", "plan-report.json"))
	if !strings.Contains(data, `"fragment_fallback_components": []`) {
		t.Fatalf("plan report has unstable fallback component shape:\n%s", data)
	}
	if err := json.Unmarshal([]byte(data), &report); err != nil {
		t.Fatalf("decode plan report: %v", err)
	}
	if report.SchemaVersion != 5 || report.Command != "plan" || report.GenerationStrategy != "fragments" ||
		report.PlannedLLM.Fragment == 0 || report.PlannedLLM.MaximumLogical <= report.PlannedLLM.Primary ||
		report.PlannedLLM.MaximumHTTPAttempts <= report.PlannedLLM.MaximumLogical {
		t.Fatalf("plan report = %+v, result calls = %+v", report, result.Plan.Calls)
	}
}

// checkInput mirrors syncInput for the check command.
func checkInput(dir string) documentationmodel.CheckInput {
	sync := syncInput(dir)
	return documentationmodel.CheckInput{
		WorkingDirectory: sync.WorkingDirectory,
		Output:           sync.Output,
		SourcePolicy:     sync.SourcePolicy,
		ComponentPolicy:  sync.ComponentPolicy,
		GenerationPolicy: sync.GenerationPolicy,
	}
}

// changedFiles returns the set of paths whose content differs between two snapshots,
// relative to the shared generated root.
func changedFiles(before, after map[string]string) map[string]bool {
	changed := make(map[string]bool)
	for path, content := range after {
		if before[path] != content {
			changed[path] = true
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed[path] = true
		}
	}
	return changed
}
