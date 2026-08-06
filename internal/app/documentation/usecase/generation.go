package usecase

import (
	"context"
	"errors"
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
	Normal            int
	Batch             int
	Synthesis         int
	Fragment          int
	Overview          int
	Diagram           int
	Repair            int
	Fallback          int
	Split             int
	SplitCalls        int
	Saturated         int
	OverviewFallback  int
	DiagramFallback   int
	TransportAttempts int
	Usage             sharedmodel.TokenUsage
}

func (s *componentGenerationStats) recordCall(kind sharedmodel.RequestKind) {
	switch kind {
	case sharedmodel.RequestComponent:
		s.Normal++
	case sharedmodel.RequestBatch:
		s.Batch++
	case sharedmodel.RequestSynthesis:
		s.Synthesis++
	case sharedmodel.RequestFragment:
		s.Fragment++
	case sharedmodel.RequestOverview:
		s.Overview++
	case sharedmodel.RequestDiagram:
		s.Diagram++
	case sharedmodel.RequestRepair:
		s.Repair++
	}
}

func (s *componentGenerationStats) merge(other componentGenerationStats) {
	s.Normal += other.Normal
	s.Batch += other.Batch
	s.Synthesis += other.Synthesis
	s.Fragment += other.Fragment
	s.Overview += other.Overview
	s.Diagram += other.Diagram
	s.Repair += other.Repair
	s.Fallback += other.Fallback
	s.Split += other.Split
	s.SplitCalls += other.SplitCalls
	s.Saturated += other.Saturated
	s.OverviewFallback += other.OverviewFallback
	s.DiagramFallback += other.DiagramFallback
	s.TransportAttempts += other.TransportAttempts
	if other.Usage.Present {
		s.Usage.Present = true
		s.Usage.PromptTokens += other.Usage.PromptTokens
		s.Usage.CompletionTokens += other.Usage.CompletionTokens
		s.Usage.TotalTokens += other.Usage.TotalTokens
	}
}

func (s *componentGenerationStats) record(response sharedmodel.GenerationResponse) {
	s.TransportAttempts += response.TransportAttempts
	if response.Usage.Present {
		s.Usage.Present = true
		s.Usage.PromptTokens += response.Usage.PromptTokens
		s.Usage.CompletionTokens += response.Usage.CompletionTokens
		s.Usage.TotalTokens += response.Usage.TotalTokens
	}
}

// llmCallLimiter applies one orchestration-wide concurrency budget immediately around
// every Generator.Generate invocation. Nested component and batch workers share this
// instance and never hold a permit while spawning or waiting for other work.
type llmCallLimiter struct {
	generator Generator
	permits   chan struct{}

	mu        sync.Mutex
	completed componentGenerationStats
}

func newLLMCallLimiter(generator Generator, concurrency int) *llmCallLimiter {
	if concurrency < 1 {
		concurrency = 1
	}
	return &llmCallLimiter{generator: generator, permits: make(chan struct{}, concurrency)}
}

func (l *llmCallLimiter) Generate(ctx context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	select {
	case l.permits <- struct{}{}:
	case <-ctx.Done():
		return sharedmodel.GenerationResponse{}, ctx.Err()
	}
	response, err := l.generator.Generate(ctx, request)
	<-l.permits
	if err != nil && response.TransportAttempts == 0 {
		response.TransportAttempts = errorTransportAttempts(err)
	}

	l.mu.Lock()
	l.completed.recordCall(request.Kind)
	l.completed.record(response)
	l.mu.Unlock()
	return response, err
}

func errorTransportAttempts(err error) int {
	var completionErr *sharedmodel.CompletionError
	if errors.As(err, &completionErr) {
		return completionErr.TransportAttempts
	}
	var transportErr *sharedmodel.TransportError
	if errors.As(err, &transportErr) {
		return transportErr.TransportAttempts
	}
	return 0
}

func (l *llmCallLimiter) snapshot() componentGenerationStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.completed
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
		stats.Normal++
		validation, err := generateWithRepair(ctx, generator, bundle, planned.request, allowed, catalog, &stats)
		if err != nil {
			return sharedmodel.ComponentDossier{}, stats, err
		}
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
		stats.TransportAttempts += batchStats[index].TransportAttempts
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
	stats.Synthesis++
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	return validation.dossier, stats, nil
}

// generateAutoComponentDossier runs the one-request dossier fast path under the
// component budget and switches to fragment mode only for typed truncation.
func generateAutoComponentDossier(
	ctx context.Context,
	generator Generator,
	bundle prompt.Bundle,
	fragmentBundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	kind string,
	input documentationmodel.PlanInput,
	concurrency int,
) (sharedmodel.ComponentDossier, componentGenerationStats, error) {
	budgeted := &componentCallBudget{generator: generator, componentKey: component.Key, limit: input.GenerationPolicy.FragmentCallLimit}
	dossier, stats, err := generateComponentDossier(ctx, budgeted, bundle, settings, component, supporting, manifests, catalog, changes, kind, input, concurrency)
	if err == nil || !isTruncationError(err) {
		return dossier, stats, err
	}
	stats.Fallback++
	fragmented, fragmentStats, fragmentErr := generateComponentFragmentsWithBudget(ctx, budgeted, fragmentBundle, settings, component,
		supporting, manifests, catalog, changes, kind, input, concurrency)
	stats.merge(fragmentStats)
	if fragmentErr != nil {
		return sharedmodel.ComponentDossier{}, stats, fragmentErr
	}
	return fragmented, stats, nil
}

func isTruncationError(err error) bool {
	var completionErr *sharedmodel.CompletionError
	return errors.As(err, &completionErr) && completionErr.Category == sharedmodel.CompletionFailureTruncated
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
	stats.record(response)
	if err != nil {
		return dossierValidation{}, err
	}
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
	stats.record(repairResponse)
	if err != nil {
		return dossierValidation{}, err
	}
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
	fragmentKind sharedmodel.FragmentKind
	batchIndex   int
	batchCount   int
	chunkIndex   int
	chunkCount   int
	splitPath    string
	codes        []string
}

func (e dossierValidationError) Error() string {
	return fmt.Sprintf("component %q %s response failed validation after one repair: %s",
		e.componentKey, e.kind, strings.Join(e.codes, ","))
}

func (e dossierValidationError) ExitCode() int { return 5 }
