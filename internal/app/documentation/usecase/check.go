package usecase

import (
	"context"
	"fmt"

	documentationmodel "docify-repo/internal/app/documentation/model"
)

// Check reports whether the installed documentation is current without ever installing
// output. It first detects tampering or missing owned documents against prior state with
// no model call. When generation is genuinely required it builds a candidate into memory
// and compares it to the installed tree, returning a stale error if they differ.
func (u *Usecase) Check(ctx context.Context, input documentationmodel.CheckInput) (documentationmodel.ResultSummary, error) {
	if u.gitSource == nil || u.worktree == nil || u.state == nil {
		return documentationmodel.ResultSummary{}, fmt.Errorf("check repositories are not configured")
	}
	if u.output == nil {
		return documentationmodel.ResultSummary{}, fmt.Errorf("check requires a configured output repository")
	}

	planInput := planInputFromCheck(input)
	if err := validateSourcePolicy(planInput.SourcePolicy); err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	if err := validateComponentPolicy(planInput.ComponentPolicy); err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	if err := validateGenerationPolicy(planInput.GenerationPolicy); err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}

	// Complete any transaction a previous run left interrupted so the comparison reflects a
	// consistent installed tree. This never generates output.
	if err := u.output.Recover(ctx, planInput.WorkingDirectory); err != nil {
		return documentationmodel.ResultSummary{}, outputValidationError{fmt.Sprintf("recover interrupted transaction: %v", err)}
	}

	snapshot, err := u.resolveSnapshot(ctx, planInput)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	scan, err := u.scan(ctx, snapshot.root, snapshot.entries, planInput.SourcePolicy, snapshot.reader)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	components, ownedFiles, err := discoverComponents(scan.Files, planInput.ComponentPolicy, planInput.SourcePolicy.DocsDir)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	applyDecisionOwners(scan.Decisions, ownedFiles)

	plan, err := buildGenerationPlan(planInput, components, ownedFiles, snapshot.state, snapshot.rawChanges, snapshot.fullFallback)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}

	inspection, err := u.inspectInstalled(ctx, planInput, snapshot.state)
	if err != nil {
		return documentationmodel.ResultSummary{}, err
	}

	summary := u.baseSummary("check", scan, plan)

	// check makes no model call. Staleness is fully determined by two deterministic facts:
	// whether the installed output still matches prior state (integrity), and whether the
	// current inputs would change any generated document or the recorded state (a non-noop
	// plan). A non-noop plan always changes at least the committed state, so a later sync
	// would produce a diff — that is exactly what check must catch.
	if !inspection.integrityOK {
		// Nothing installed and nothing to document is a legitimately current empty state;
		// anything else means the committed output cannot be proven current.
		if plan.Noop && len(components) == 0 && len(inspection.existing.GeneratedPaths) == 0 {
			return u.finishCheck(ctx, planInput, summary, &inspection, "current", nil)
		}
		return u.finishCheck(ctx, planInput, summary, &inspection, "stale", staleError{reason: inspection.reason})
	}
	if plan.Noop {
		return u.finishCheck(ctx, planInput, summary, &inspection, "current", nil)
	}
	return u.finishCheck(ctx, planInput, summary, &inspection, "stale", staleError{reason: "generated documentation is out of date"})
}

// finishCheck sets the terminal status, writes the run report, and returns the check
// result. A stale outcome carries a populated summary alongside the stale error so the
// handler can still print it before the process exits nonzero.
func (u *Usecase) finishCheck(ctx context.Context, input documentationmodel.PlanInput, summary documentationmodel.ResultSummary, inspection *installedInspection, status string, checkErr error) (documentationmodel.ResultSummary, error) {
	summary.Status = status
	if reportErr := u.writeRunReport(ctx, input, summary, inspection); reportErr != nil {
		return documentationmodel.ResultSummary{}, reportErr
	}
	return summary, checkErr
}

func planInputFromCheck(input documentationmodel.CheckInput) documentationmodel.PlanInput {
	return documentationmodel.PlanInput{
		WorkingDirectory:  input.WorkingDirectory,
		Output:            input.Output,
		ReportPath:        input.ReportPath,
		BaseSHA:           input.BaseSHA,
		HeadSHA:           input.HeadSHA,
		Full:              input.Full,
		AllowFullFallback: input.AllowFullFallback,
		SourcePolicy:      input.SourcePolicy,
		ComponentPolicy:   input.ComponentPolicy,
		GenerationPolicy:  input.GenerationPolicy,
	}
}

// staleError reports that installed documentation is not current. It exposes a stable,
// non-secret reason and a dedicated exit code so CI can distinguish staleness from
// configuration, source, model, and output-validation failures.
type staleError struct {
	reason string
}

func (e staleError) Error() string {
	if e.reason == "" {
		return "generated documentation is stale"
	}
	return "generated documentation is stale: " + e.reason
}

func (e staleError) ExitCode() int { return 2 }
