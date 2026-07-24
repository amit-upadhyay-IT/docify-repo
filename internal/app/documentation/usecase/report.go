package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

const runReportSchemaVersion = 1

// writeRunReport writes the structured run report to the configured report path when one
// is set and an output repository is available. It is a run artifact installed
// independently of the documentation transaction, so a report write failure is surfaced
// but never leaves documentation partially installed (it runs after install).
func (u *Usecase) writeRunReport(ctx context.Context, input documentationmodel.PlanInput, summary documentationmodel.ResultSummary, inspection *installedInspection) error {
	reportPath := input.SourcePolicy.ReportPath
	if reportPath == "" || u.output == nil {
		return nil
	}
	report := buildRunReport(summary, inspection)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run report: %w", err)
	}
	data = append(data, '\n')
	if err := u.output.WriteReport(ctx, input.WorkingDirectory, reportPath, data); err != nil {
		return outputValidationError{fmt.Sprintf("write run report: %v", err)}
	}
	return nil
}

// buildRunReport assembles the safe, structured report from a completed result summary and
// the optional installed-output inspection. It copies only repository-relative paths,
// stable action labels, counts, and provider usage.
func buildRunReport(summary documentationmodel.ResultSummary, inspection *installedInspection) documentationmodel.RunReport {
	report := documentationmodel.RunReport{
		SchemaVersion: runReportSchemaVersion,
		Command:       summary.Command,
		Status:        summary.Status,
		Mode:          summary.Plan.Mode,
		StateStatus:   summary.Plan.StateStatus,
		Noop:          summary.Plan.Noop,
		BaseSHA:       summary.Plan.BaseSHA,
		HeadSHA:       summary.Plan.HeadSHA,
		FullReason:    summary.Plan.FullReason,
		TrackedPaths:  summary.TrackedPaths,
		IncludedPaths: make([]string, 0),
		ExcludedPaths: make([]string, 0),
		Documents:     sharedmodel.OutputDiff{Added: []string{}, Changed: []string{}, Deleted: []string{}, Unchanged: []string{}},
	}

	for _, decision := range summary.Files {
		if decision.IncludedAsContext {
			report.IncludedPaths = append(report.IncludedPaths, decision.Path)
		} else {
			report.ExcludedPaths = append(report.ExcludedPaths, decision.Path)
		}
	}
	sort.Strings(report.IncludedPaths)
	sort.Strings(report.ExcludedPaths)

	report.AffectedComponents = make([]documentationmodel.ReportAffectedComponent, 0, len(summary.Plan.AffectedComponents))
	report.DeletedComponents = make([]string, 0)
	for _, affected := range summary.Plan.AffectedComponents {
		if affected.Action == sharedmodel.ComponentDelete {
			report.DeletedComponents = append(report.DeletedComponents, affected.Key)
			continue
		}
		report.AffectedComponents = append(report.AffectedComponents, documentationmodel.ReportAffectedComponent{
			Key:           affected.Key,
			RootComponent: affected.RootComponent,
			Action:        string(affected.Action),
			Reasons:       append([]string(nil), affected.Reasons...),
		})
	}
	sort.Strings(report.DeletedComponents)

	if outcome := summary.Generation; outcome != nil {
		report.LLM = documentationmodel.ReportLLM{
			NormalCalls:    outcome.NormalCalls,
			BatchCalls:     outcome.BatchCalls,
			SynthesisCalls: outcome.SynthesisCalls,
			RepairCalls:    outcome.RepairCalls,
			Usage:          outcome.Usage,
		}
		report.Documents = normalizeDiff(outcome.Diff)
		report.Validation.OutputValidated = true
	}

	if inspection != nil {
		report.Validation.IntegrityChecked = inspection.provenState
		report.Validation.IntegrityOK = inspection.integrityOK
		if !inspection.integrityOK {
			report.Validation.Detail = inspection.reason
		}
	}
	return report
}

// normalizeDiff ensures every diff slice is non-nil so the report renders stable JSON.
func normalizeDiff(diff sharedmodel.OutputDiff) sharedmodel.OutputDiff {
	result := sharedmodel.OutputDiff{
		Added:     append([]string{}, diff.Added...),
		Changed:   append([]string{}, diff.Changed...),
		Deleted:   append([]string{}, diff.Deleted...),
		Unchanged: append([]string{}, diff.Unchanged...),
	}
	sort.Strings(result.Added)
	sort.Strings(result.Changed)
	sort.Strings(result.Deleted)
	sort.Strings(result.Unchanged)
	return result
}
