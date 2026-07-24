package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

// componentGenerationStats records safe, non-secret counts for one component's
// generation. It carries no source or model prose.
type componentGenerationStats struct {
	Normal    int
	Batch     int
	Synthesis int
	Repair    int
	Usage     sharedmodel.TokenUsage
}

func (s *componentGenerationStats) record(response sharedmodel.GenerationResponse) {
	if response.Usage.Present {
		s.Usage.Present = true
		s.Usage.PromptTokens += response.Usage.PromptTokens
		s.Usage.CompletionTokens += response.Usage.CompletionTokens
		s.Usage.TotalTokens += response.Usage.TotalTokens
	}
}

// generateComponentDossier produces one validated dossier for a component. A normal
// component makes one request; an oversized component makes one request per stable
// batch with bounded concurrency, then one bounded synthesis request. Every request is
// eligible for at most one schema-repair attempt.
func generateComponentDossier(
	ctx context.Context,
	generator Generator,
	bundle prompt.Bundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	kind string,
	input documentationmodel.PlanInput,
	concurrency int,
) (sharedmodel.ComponentDossier, componentGenerationStats, error) {
	stats := componentGenerationStats{}
	if generator == nil {
		return sharedmodel.ComponentDossier{}, stats, fmt.Errorf("component %q requires a configured generator", component.Key)
	}

	requestPlan, err := planComponentGeneration(bundle, settings, component, supporting, manifests, catalog, changes, kind, input)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}

	if requestPlan.normal {
		planned := requestPlan.requests[0]
		allowed := allowedEvidencePaths(planned.source, supporting, manifests)
		validation, err := generateWithRepair(ctx, generator, bundle, planned.request, allowed, catalog, &stats)
		if err != nil {
			return sharedmodel.ComponentDossier{}, stats, err
		}
		stats.Normal++
		return validation.dossier, stats, nil
	}

	batchDossiers := make([]sharedmodel.ComponentDossier, len(requestPlan.requests))
	batchStats := make([]componentGenerationStats, len(requestPlan.requests))
	evidence := make([][]string, len(requestPlan.requests))
	err = runBounded(ctx, concurrency, len(requestPlan.requests), func(ctx context.Context, index int) error {
		planned := requestPlan.requests[index]
		allowed := allowedEvidencePaths(planned.source, supporting, manifests)
		validation, err := generateWithRepair(ctx, generator, bundle, planned.request, allowed, catalog, &batchStats[index])
		if err != nil {
			return err
		}
		batchDossiers[index] = validation.dossier
		evidence[index] = validation.evidenceUsed
		return nil
	})
	// Each planned request is exactly one batch call. Fold per-batch repair counts and
	// usage into the component totals regardless of whether a batch failed.
	stats.Batch = len(requestPlan.requests)
	for index := range batchStats {
		stats.Repair += batchStats[index].Repair
		if batchStats[index].Usage.Present {
			stats.Usage.Present = true
			stats.Usage.PromptTokens += batchStats[index].Usage.PromptTokens
			stats.Usage.CompletionTokens += batchStats[index].Usage.CompletionTokens
			stats.Usage.TotalTokens += batchStats[index].Usage.TotalTokens
		}
	}
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}

	// The final dossier may cite only evidence actually established across the batches.
	unionEvidence := unionSorted(evidence)
	synthesisRequest, err := buildSynthesisRequest(bundle, settings, component, catalog, batchDossiers)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	validation, err := generateWithRepair(ctx, generator, bundle, synthesisRequest, unionEvidence, catalog, &stats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	stats.Synthesis++
	return validation.dossier, stats, nil
}

// generateWithRepair sends one request, validates the response, and on a repair-eligible
// failure sends exactly one repair request. A repair response is never repaired again.
func generateWithRepair(
	ctx context.Context,
	generator Generator,
	bundle prompt.Bundle,
	request sharedmodel.GenerationRequest,
	allowedEvidence, catalog []string,
	stats *componentGenerationStats,
) (dossierValidation, error) {
	response, err := generator.Generate(ctx, request)
	if err != nil {
		return dossierValidation{}, err
	}
	stats.record(response)
	validation := validateDossier(response.Body, allowedEvidence, catalog)
	if validation.valid() {
		return validation, nil
	}

	repairRequest, err := buildRepairRequest(bundle, request, response.Body, validation.issues)
	if err != nil {
		return dossierValidation{}, err
	}
	stats.Repair++
	repairResponse, err := generator.Generate(ctx, repairRequest)
	if err != nil {
		return dossierValidation{}, err
	}
	stats.record(repairResponse)
	repaired := validateDossier(repairResponse.Body, allowedEvidence, catalog)
	if repaired.valid() {
		return repaired, nil
	}
	return dossierValidation{}, dossierValidationError{
		componentKey: request.ComponentKey,
		kind:         request.Kind,
		codes:        issueCodes(repaired.issues),
	}
}

// runBounded runs work(index) for index in [0,count) with at most concurrency in
// flight, returning the first error and cancelling the rest. Results must be written to
// per-index storage; runBounded shares no mutable state between workers.
func runBounded(ctx context.Context, concurrency, count int, work func(context.Context, int) error) error {
	if concurrency < 1 {
		concurrency = 1
	}
	if count == 0 {
		return nil
	}
	groupCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	semaphore := make(chan struct{}, concurrency)
	var waitGroup sync.WaitGroup
	var once sync.Once
	var firstErr error

	for index := 0; index < count; index++ {
		if groupCtx.Err() != nil {
			break
		}
		semaphore <- struct{}{}
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			defer func() { <-semaphore }()
			if groupCtx.Err() != nil {
				return
			}
			if err := work(groupCtx, index); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}(index)
	}
	waitGroup.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func unionSorted(groups [][]string) []string {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, value := range group {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func issueCodes(issues []sharedmodel.ValidationIssue) []string {
	codes := make([]string, 0, len(issues))
	seen := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		if _, ok := seen[issue.Code]; ok {
			continue
		}
		seen[issue.Code] = struct{}{}
		codes = append(codes, issue.Code)
	}
	sort.Strings(codes)
	return codes
}

// dossierValidationError is a safe LLM-validation failure. It names the component,
// request kind, and stable issue codes only, never source or model prose.
type dossierValidationError struct {
	componentKey string
	kind         sharedmodel.RequestKind
	codes        []string
}

func (e dossierValidationError) Error() string {
	return fmt.Sprintf("component %q %s response failed validation after one repair: %s",
		e.componentKey, e.kind, strings.Join(e.codes, ","))
}

func (e dossierValidationError) ExitCode() int { return 5 }
