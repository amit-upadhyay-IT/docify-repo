package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	documentationmodel "docify-repo/internal/app/documentation/model"
	"docify-repo/internal/app/documentation/usecase"
)

type Handler struct {
	usecase *usecase.Usecase
	output  io.Writer
}

func New(documentationUsecase *usecase.Usecase, output io.Writer) *Handler {
	return &Handler{usecase: documentationUsecase, output: output}
}

func (h *Handler) Sync(ctx context.Context, options documentationmodel.RawSyncOptions) error {
	common, err := normalizeCommon(options.WorkingDirectory, options.Output, options.ReportPath, options.BaseSHA, options.HeadSHA)
	if err != nil {
		return err
	}

	input := documentationmodel.SyncInput{
		WorkingDirectory:        common.workingDirectory,
		Output:                  common.output,
		ReportPath:              common.reportPath,
		BaseSHA:                 common.baseSHA,
		HeadSHA:                 common.headSHA,
		Publisher:               strings.ToLower(strings.TrimSpace(options.Publisher)),
		Full:                    options.Full,
		AllowFullFallback:       options.AllowFullFallback,
		Concurrency:             options.Concurrency,
		SourcePolicy:            normalizeSourcePolicy(options.SourcePolicy, common.reportPath),
		ComponentPolicy:         normalizeComponentPolicy(options.ComponentPolicy),
		GenerationPolicy:        options.GenerationPolicy,
		GitHubRepository:        strings.TrimSpace(options.GitHubRepository),
		BaseBranch:              strings.TrimSpace(options.BaseBranch),
		Branch:                  strings.TrimSpace(options.Branch),
		GitHubCredentialPresent: options.GitHubCredential,
	}
	result, err := h.usecase.Sync(ctx, input)
	if err != nil {
		return err
	}

	return h.writeResult(result, input.Output)
}

func (h *Handler) Check(ctx context.Context, options documentationmodel.RawCheckOptions) error {
	common, err := normalizeCommon(options.WorkingDirectory, options.Output, options.ReportPath, options.BaseSHA, options.HeadSHA)
	if err != nil {
		return err
	}

	input := documentationmodel.CheckInput{
		WorkingDirectory:  common.workingDirectory,
		Output:            common.output,
		ReportPath:        common.reportPath,
		BaseSHA:           common.baseSHA,
		HeadSHA:           common.headSHA,
		Full:              options.Full,
		AllowFullFallback: options.AllowFullFallback,
		SourcePolicy:      normalizeSourcePolicy(options.SourcePolicy, common.reportPath),
		ComponentPolicy:   normalizeComponentPolicy(options.ComponentPolicy),
		GenerationPolicy:  options.GenerationPolicy,
	}
	result, err := h.usecase.Check(ctx, input)
	if err != nil {
		// A stale result carries a populated summary; print it before returning the error
		// so CI sees the machine-readable outcome and still exits nonzero.
		if result.Command != "" {
			if writeErr := h.writeResult(result, input.Output); writeErr != nil {
				return writeErr
			}
		}
		return err
	}

	return h.writeResult(result, input.Output)
}

func (h *Handler) Plan(ctx context.Context, options documentationmodel.RawPlanOptions) error {
	common, err := normalizeCommon(options.WorkingDirectory, options.Output, options.ReportPath, options.BaseSHA, options.HeadSHA)
	if err != nil {
		return err
	}

	input := documentationmodel.PlanInput{
		WorkingDirectory:  common.workingDirectory,
		Output:            common.output,
		ReportPath:        common.reportPath,
		BaseSHA:           common.baseSHA,
		HeadSHA:           common.headSHA,
		Full:              options.Full,
		AllowFullFallback: options.AllowFullFallback,
		SourcePolicy:      normalizeSourcePolicy(options.SourcePolicy, common.reportPath),
		ComponentPolicy:   normalizeComponentPolicy(options.ComponentPolicy),
		GenerationPolicy:  options.GenerationPolicy,
	}
	result, err := h.usecase.Plan(ctx, input)
	if err != nil {
		return err
	}

	return h.writeResult(result, input.Output)
}

type commonOptions struct {
	workingDirectory string
	output           documentationmodel.OutputMode
	reportPath       string
	baseSHA          string
	headSHA          string
}

func normalizeCommon(workingDirectory, output, reportPath, baseSHA, headSHA string) (commonOptions, error) {
	baseSHA = strings.TrimSpace(baseSHA)
	headSHA = strings.TrimSpace(headSHA)
	if (baseSHA == "") != (headSHA == "") {
		return commonOptions{}, fmt.Errorf("base SHA and head SHA must be supplied together")
	}

	workingDirectory = strings.TrimSpace(workingDirectory)
	if workingDirectory == "" {
		return commonOptions{}, fmt.Errorf("working directory is required")
	}
	absoluteWorkingDirectory, err := filepath.Abs(workingDirectory)
	if err != nil {
		return commonOptions{}, fmt.Errorf("resolve working directory: %w", err)
	}

	outputMode := documentationmodel.OutputMode(strings.ToLower(strings.TrimSpace(output)))
	switch outputMode {
	case documentationmodel.OutputModeHuman, documentationmodel.OutputModeJSON:
	default:
		return commonOptions{}, fmt.Errorf("output must be %q or %q", documentationmodel.OutputModeHuman, documentationmodel.OutputModeJSON)
	}
	reportPath = strings.TrimSpace(reportPath)
	if reportPath != "" {
		reportPath = filepath.Clean(reportPath)
	}

	return commonOptions{
		workingDirectory: filepath.Clean(absoluteWorkingDirectory),
		output:           outputMode,
		reportPath:       reportPath,
		baseSHA:          baseSHA,
		headSHA:          headSHA,
	}, nil
}

func (h *Handler) writeResult(result documentationmodel.ResultSummary, mode documentationmodel.OutputMode) error {
	if mode == documentationmodel.OutputModeJSON {
		if err := json.NewEncoder(h.output).Encode(result); err != nil {
			return fmt.Errorf("write JSON result: %w", err)
		}
		return nil
	}

	if _, err := fmt.Fprintf(
		h.output,
		"command=%s status=%s mode=%s state=%s noop=%t tracked_paths=%d included_paths=%d triggering_paths=%d excluded_paths=%d components=%d affected_components=%d normal_calls=%d batch_calls=%d synthesis_calls=%d maximum_repair_calls=%d maximum_transport_fallback_calls=%d llm_calls=%d request_bytes=%d conservative_tokens=%d typical_tokens=%d\n",
		result.Command,
		result.Status,
		result.Plan.Mode,
		result.Plan.StateStatus,
		result.Plan.Noop,
		result.TrackedPaths,
		result.IncludedPaths,
		result.TriggeringPaths,
		result.ExcludedPaths,
		len(result.Plan.Components),
		len(result.Plan.AffectedComponents),
		result.Plan.Calls.Normal,
		result.Plan.Calls.Batch,
		result.Plan.Calls.Synthesis,
		result.Plan.Calls.MaximumRepair,
		result.Plan.Calls.MaximumTransportFallback,
		result.Plan.Calls.Primary,
		result.Plan.Calls.RequestBytes,
		result.Plan.Calls.ConservativeTokens,
		result.Plan.Calls.TypicalTokens,
	); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	if outcome := result.Generation; outcome != nil {
		if _, err := fmt.Fprintf(
			h.output,
			"generated_components=%d installed_paths=%d deleted_paths=%d added=%d changed=%d normal_calls=%d batch_calls=%d synthesis_calls=%d repair_calls=%d usage_present=%t prompt_tokens=%d completion_tokens=%d total_tokens=%d\n",
			outcome.GeneratedComponents,
			outcome.InstalledPaths,
			outcome.DeletedPaths,
			len(outcome.Diff.Added),
			len(outcome.Diff.Changed),
			outcome.NormalCalls,
			outcome.BatchCalls,
			outcome.SynthesisCalls,
			outcome.RepairCalls,
			outcome.Usage.Present,
			outcome.Usage.PromptTokens,
			outcome.Usage.CompletionTokens,
			outcome.Usage.TotalTokens,
		); err != nil {
			return fmt.Errorf("write generation outcome: %w", err)
		}
	}
	for _, file := range result.Files {
		if _, err := fmt.Fprintf(
			h.output,
			"path=%q role=%s component=%q included_as_context=%t triggers_regeneration=%t reason=%s size=%d\n",
			file.Path,
			file.Role,
			file.ComponentKey,
			file.IncludedAsContext,
			file.TriggersRegeneration,
			file.Reason,
			file.Size,
		); err != nil {
			return fmt.Errorf("write source decision: %w", err)
		}
	}
	for _, component := range result.Plan.Components {
		if _, err := fmt.Fprintf(
			h.output,
			"component=%q document=%q triggering_paths=%d supporting_paths=%d manifest_paths=%d triggering_bytes=%d supporting_bytes=%d manifest_bytes=%d omitted_supporting_paths=%d omitted_manifest_paths=%d\n",
			component.Key,
			component.Document,
			len(component.TriggeringPaths),
			len(component.SupportingPaths),
			len(component.ManifestPaths),
			component.TriggeringBytes,
			component.SupportingBytes,
			component.ManifestBytes,
			component.OmittedSupporting,
			component.OmittedManifests,
		); err != nil {
			return fmt.Errorf("write component summary: %w", err)
		}
	}
	for _, change := range result.Plan.Changes {
		if _, err := fmt.Fprintf(
			h.output,
			"change=%s old_path=%q new_path=%q old_component=%q new_component=%q similarity=%d\n",
			change.Status,
			change.OldPath,
			change.NewPath,
			change.OldComponentKey,
			change.NewComponentKey,
			change.Similarity,
		); err != nil {
			return fmt.Errorf("write change: %w", err)
		}
	}
	for _, affected := range result.Plan.AffectedComponents {
		if _, err := fmt.Fprintf(
			h.output,
			"affected_component=%q action=%s reasons=%q document=%q input_hash=%s batches=%d synthesis_request_bytes=%d\n",
			affected.Key,
			affected.Action,
			strings.Join(affected.Reasons, ","),
			affected.Document,
			affected.InputHash,
			len(affected.Batches),
			affected.SynthesisRequestBytes,
		); err != nil {
			return fmt.Errorf("write affected component: %w", err)
		}
		for _, batch := range affected.Batches {
			if _, err := fmt.Fprintf(
				h.output,
				"component=%q batch=%d/%d source_paths=%d source_bytes=%d request_bytes=%d conservative_tokens=%d typical_tokens=%d\n",
				affected.Key,
				batch.Index,
				batch.Count,
				len(batch.SourcePaths),
				batch.SourceBytes,
				batch.RequestBytes,
				batch.ConservativeTokens,
				batch.TypicalTokens,
			); err != nil {
				return fmt.Errorf("write component batch: %w", err)
			}
		}
	}
	for _, document := range result.Plan.DeletedDocuments {
		if _, err := fmt.Fprintf(h.output, "delete_document=%q\n", document); err != nil {
			return fmt.Errorf("write deleted document: %w", err)
		}
	}
	return nil
}

func normalizeSourcePolicy(policy documentationmodel.SourcePolicy, reportPath string) documentationmodel.SourcePolicy {
	policy.DocsDir = normalizeRepositoryPath(policy.DocsDir)
	policy.StatePath = normalizeRepositoryPath(policy.StatePath)
	policy.ReportPath = normalizeRepositoryPath(reportPath)
	policy.Include = append([]string(nil), policy.Include...)
	policy.Exclude = append([]string(nil), policy.Exclude...)
	policy.RoleOverrides = append([]documentationmodel.RoleOverride(nil), policy.RoleOverrides...)
	return policy
}

func normalizeComponentPolicy(policy documentationmodel.ComponentPolicy) documentationmodel.ComponentPolicy {
	policy.Roots = append([]string(nil), policy.Roots...)
	for index := range policy.Roots {
		policy.Roots[index] = normalizeRepositoryPath(policy.Roots[index])
	}
	return policy
}

func normalizeRepositoryPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(value))
}
