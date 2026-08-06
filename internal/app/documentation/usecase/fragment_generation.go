package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

type componentCallBudget struct {
	generator    Generator
	componentKey string
	limit        int

	mu   sync.Mutex
	used int
}

type fragmentCallLimitError struct {
	componentKey string
	limit        int
}

func (e fragmentCallLimitError) Error() string {
	return fmt.Sprintf("component %q reached the %d-call fragment generation limit", e.componentKey, e.limit)
}

func (e fragmentCallLimitError) ExitCode() int { return 5 }

func (b *componentCallBudget) Generate(ctx context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	b.mu.Lock()
	if b.used >= b.limit {
		b.mu.Unlock()
		return sharedmodel.GenerationResponse{}, fragmentCallLimitError{componentKey: b.componentKey, limit: b.limit}
	}
	b.used++
	b.mu.Unlock()
	return b.generator.Generate(ctx, request)
}

func generateComponentFragments(
	ctx context.Context,
	generator Generator,
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	input documentationmodel.PlanInput,
	concurrency int,
) (sharedmodel.ComponentDossier, componentGenerationStats, error) {
	if generator == nil {
		return sharedmodel.ComponentDossier{}, componentGenerationStats{}, fmt.Errorf("component %q requires a configured generator", component.Key)
	}
	budgeted := &componentCallBudget{generator: generator, componentKey: component.Key, limit: input.GenerationPolicy.FragmentCallLimit}
	return generateComponentFragmentsWithBudget(ctx, budgeted, bundle, settings, component, supporting, manifests, catalog, changes, changeKind, input, concurrency)
}

type adaptiveFragmentResult struct {
	planned          plannedFragmentRequest
	validation       fragmentValidation
	retainSaturation bool
	children         []adaptiveFragmentResult
}

type fragmentMapResult struct {
	overview       fragmentValidation
	overviewFailed bool
	required       adaptiveFragmentResult
	stats          componentGenerationStats
}

func generateComponentFragmentsWithBudget(
	ctx context.Context,
	generator Generator,
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	input documentationmodel.PlanInput,
	concurrency int,
) (sharedmodel.ComponentDossier, componentGenerationStats, error) {
	stats := componentGenerationStats{}
	if generator == nil {
		return sharedmodel.ComponentDossier{}, stats, fmt.Errorf("component %q requires a configured generator", component.Key)
	}
	plan, err := planFragmentGeneration(bundle, settings, component, supporting, manifests, catalog, changes, changeKind, input)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	plannedCoverage := make([]fragmentScope, 0, len(plan.sourceScopes)*len(requiredFragmentKinds()))
	for _, planned := range plan.mapRequests {
		if requiredFragmentKind(planned.scope.Kind) {
			plannedCoverage = append(plannedCoverage, planned.scope)
		}
	}
	ledger, err := newFragmentCoverageLedger(component, plannedCoverage)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}

	results := make([]fragmentMapResult, len(plan.mapRequests))
	err = runBounded(ctx, concurrency, len(plan.mapRequests), func(ctx context.Context, index int) error {
		planned := plan.mapRequests[index]
		if planned.scope.Kind == sharedmodel.FragmentOverviewCandidate {
			validation, err := generateFragmentWithRepair(ctx, generator, bundle, planned.request, planned.scope, catalog,
				input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes, &results[index].stats)
			if err != nil {
				if optionalReducerMustFail(err) {
					return err
				}
				results[index].overviewFailed = true
				return nil
			}
			results[index].overview = validation
			return nil
		}
		result, err := generateRequiredFragmentAdaptive(ctx, generator, bundle, settings, component, supporting, manifests,
			catalog, changes, changeKind, planned, 0, input, &results[index].stats)
		results[index].required = result
		return err
	})
	for index := range results {
		stats.merge(results[index].stats)
	}
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}

	overviewCandidates := make([]fragmentValidation, 0, len(plan.sourceScopes))
	requiredValidations := make([]fragmentValidation, 0, len(plannedCoverage))
	for index, planned := range plan.mapRequests {
		if planned.scope.Kind == sharedmodel.FragmentOverviewCandidate {
			if !results[index].overviewFailed {
				overviewCandidates = append(overviewCandidates, results[index].overview)
			}
			continue
		}
		terminal, err := recordAdaptiveFragmentResult(ledger, results[index].required)
		if err != nil {
			return sharedmodel.ComponentDossier{}, stats, err
		}
		requiredValidations = append(requiredValidations, terminal...)
	}

	provisional, _, err := assembleComponentDossier(fragmentAssemblyInput{
		Component: component, Catalog: catalog, Coverage: ledger, OverviewCandidates: overviewCandidates,
	})
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}

	candidates, _, err := trustedOverviewCandidates(overviewCandidates)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	evidenceValidations := append(append([]fragmentValidation(nil), overviewCandidates...), requiredValidations...)
	evidence, err := trustedFragmentEvidence(evidenceValidations)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	var overview *overviewValidation
	overviewFallback := false
	if len(candidates) == 0 {
		overviewFallback = true
	} else {
		overviewRequest, err := buildOverviewReducerRequest(bundle, settings, component, candidates, evidence,
			overviewSections(provisional), input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes)
		if err != nil {
			return sharedmodel.ComponentDossier{}, stats, err
		}
		var reducerFallback bool
		var overviewStats componentGenerationStats
		overview, reducerFallback, overviewStats, err = generateOverviewWithRepair(ctx, generator, bundle, overviewRequest, component,
			input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes)
		stats.merge(overviewStats)
		if err != nil {
			return sharedmodel.ComponentDossier{}, stats, err
		}
		overviewFallback = overviewFallback || reducerFallback
	}
	if overviewFallback {
		stats.OverviewFallback++
	}
	projection, diagramEvidence := diagramProjectionFromDossier(provisional)
	diagramRequest, err := buildDiagramReducerRequest(bundle, settings, component, projection, diagramEvidence,
		input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	diagramScope := fragmentScope{
		ComponentKey: component.Key, RootComponent: component.RootComponent, Kind: sharedmodel.FragmentDiagrams,
		SourceBatchIndex: 1, SourceBatchCount: 1, SourceChunkIndex: 1, SourceChunkCount: 1,
		SplitPath: "diagram", AllowedEvidence: append([]string(nil), diagramEvidence...),
	}
	diagram, diagramFallback, diagramStats, err := generateOptionalDiagram(ctx, generator, bundle, diagramRequest, diagramScope, catalog,
		input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes)
	stats.merge(diagramStats)
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	diagrams := []fragmentValidation(nil)
	if !diagramFallback {
		diagrams = append(diagrams, diagram)
	} else {
		stats.DiagramFallback++
	}

	dossier, _, err := assembleComponentDossier(fragmentAssemblyInput{
		Component: component, Catalog: catalog, Coverage: ledger, OverviewCandidates: overviewCandidates,
		Overview: overview, OverviewFallback: overviewFallback, DiagramFragments: diagrams, DiagramFallback: diagramFallback,
	})
	if err != nil {
		return sharedmodel.ComponentDossier{}, stats, err
	}
	return dossier, stats, nil
}

func generateRequiredFragmentAdaptive(
	ctx context.Context,
	generator Generator,
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	planned plannedFragmentRequest,
	depth int,
	input documentationmodel.PlanInput,
	stats *componentGenerationStats,
) (adaptiveFragmentResult, error) {
	validation, err := generateFragmentWithRepair(ctx, generator, bundle, planned.request, planned.scope, catalog,
		input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes, stats)
	truncated := isTruncationError(err)
	if err != nil && !truncated {
		return adaptiveFragmentResult{}, err
	}
	if err == nil && !validation.saturated {
		return adaptiveFragmentResult{planned: planned, validation: validation}, nil
	}
	if depth >= input.GenerationPolicy.FragmentSplitDepth {
		if truncated {
			return adaptiveFragmentResult{}, err
		}
		stats.Saturated++
		return adaptiveFragmentResult{planned: planned, validation: validation, retainSaturation: true}, nil
	}

	children, available, splitErr := splitAdaptiveFragmentRequest(bundle, settings, component, supporting, manifests, catalog,
		changes, changeKind, planned, input)
	if splitErr != nil {
		return adaptiveFragmentResult{}, splitErr
	}
	if !available {
		if truncated {
			return adaptiveFragmentResult{}, err
		}
		stats.Saturated++
		return adaptiveFragmentResult{planned: planned, validation: validation, retainSaturation: true}, nil
	}
	stats.Split++
	result := adaptiveFragmentResult{planned: planned, children: make([]adaptiveFragmentResult, len(children))}
	for index, child := range children {
		generated, childErr := generateRequiredFragmentAdaptive(ctx, generator, bundle, settings, component, supporting, manifests,
			catalog, changes, changeKind, child, depth+1, input, stats)
		if childErr != nil {
			return adaptiveFragmentResult{}, childErr
		}
		result.children[index] = generated
	}
	return result, nil
}

func splitAdaptiveFragmentRequest(
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	parent plannedFragmentRequest,
	input documentationmodel.PlanInput,
) ([]plannedFragmentRequest, bool, error) {
	sources, available := splitRuntimeFragmentSource(parent.source)
	if !available {
		return nil, false, nil
	}
	children := make([]plannedFragmentRequest, len(sources))
	for index, source := range sources {
		request, err := buildFragmentRequest(bundle, settings, component, parent.scope.Kind, source, supporting, manifests, catalog,
			changes, changeKind, parent.scope.SourceBatchIndex, parent.scope.SourceBatchCount, index+1, len(sources),
			input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes)
		if err != nil {
			return nil, false, err
		}
		scope := parent.scope
		scope.SourceChunkIndex = index + 1
		scope.SourceChunkCount = len(sources)
		scope.SplitDepth = parent.scope.SplitDepth + 1
		scope.SplitPath = fmt.Sprintf("%s/%d", parent.scope.SplitPath, index)
		scope.AllowedEvidence = allowedEvidencePaths(source, supporting, manifests)
		request.SourceSplitPath = scope.SplitPath
		children[index] = plannedFragmentRequest{request: request, scope: scope, source: append([]sharedmodel.SourceFile(nil), source...)}
	}
	return children, true, nil
}

func recordAdaptiveFragmentResult(ledger *fragmentCoverageLedger, result adaptiveFragmentResult) ([]fragmentValidation, error) {
	if len(result.children) == 0 {
		if err := ledger.record(result.planned.scope, result.validation, result.retainSaturation); err != nil {
			return nil, err
		}
		return []fragmentValidation{result.validation}, nil
	}
	children := make([]fragmentScope, len(result.children))
	for index := range result.children {
		children[index] = result.children[index].planned.scope
	}
	if err := ledger.replace(result.planned.scope, children); err != nil {
		return nil, err
	}
	validations := make([]fragmentValidation, 0, len(children))
	for _, child := range result.children {
		terminal, err := recordAdaptiveFragmentResult(ledger, child)
		if err != nil {
			return nil, err
		}
		validations = append(validations, terminal...)
	}
	return validations, nil
}

func generateFragmentWithRepair(
	ctx context.Context,
	generator Generator,
	bundle prompt.FragmentBundle,
	request sharedmodel.GenerationRequest,
	scope fragmentScope,
	catalog []string,
	maxResponseBytes, maxRequestBytes int64,
	stats *componentGenerationStats,
) (fragmentValidation, error) {
	stats.Fragment++
	response, err := generator.Generate(ctx, request)
	if scope.SplitDepth > 0 && !isFragmentCallLimitError(err) {
		stats.SplitCalls++
	}
	stats.record(response)
	if err != nil {
		return fragmentValidation{}, err
	}
	validation := validateScopedFragment(scope, response.Body, scope.AllowedEvidence, catalog)
	if validation.valid() {
		return validation, nil
	}
	if !validation.repairable {
		return fragmentValidation{}, fragmentResponseValidationError(request, validation.issues)
	}
	repair, err := buildFragmentRepairRequest(bundle, request, response.Body, validation.issues, maxResponseBytes, maxRequestBytes)
	if err != nil {
		return fragmentValidation{}, err
	}
	stats.Repair++
	repairedResponse, err := generator.Generate(ctx, repair)
	stats.record(repairedResponse)
	if err != nil {
		return fragmentValidation{}, err
	}
	repaired := validateScopedFragment(scope, repairedResponse.Body, scope.AllowedEvidence, catalog)
	if repaired.valid() {
		return repaired, nil
	}
	return fragmentValidation{}, fragmentResponseValidationError(request, repaired.issues)
}

func isFragmentCallLimitError(err error) bool {
	var limitErr fragmentCallLimitError
	return errors.As(err, &limitErr)
}

func generateOverviewWithRepair(
	ctx context.Context,
	generator Generator,
	bundle prompt.FragmentBundle,
	request sharedmodel.GenerationRequest,
	component sharedmodel.Component,
	maxResponseBytes, maxRequestBytes int64,
) (*overviewValidation, bool, componentGenerationStats, error) {
	stats := componentGenerationStats{Overview: 1}
	response, err := generator.Generate(ctx, request)
	stats.record(response)
	if err != nil {
		if optionalReducerMustFail(err) {
			return nil, false, stats, err
		}
		return nil, true, stats, nil
	}
	validation := validateOverviewReduction(component, response.Body)
	if validation.valid() {
		return &validation, false, stats, nil
	}
	if len(response.Body) > fragmentResponseBytes {
		return nil, true, stats, nil
	}
	repair, err := buildFragmentRepairRequest(bundle, request, response.Body, validation.issues, maxResponseBytes, maxRequestBytes)
	if err != nil {
		return nil, true, stats, nil
	}
	stats.Repair++
	repairedResponse, err := generator.Generate(ctx, repair)
	stats.record(repairedResponse)
	if err != nil {
		if optionalReducerMustFail(err) {
			return nil, false, stats, err
		}
		return nil, true, stats, nil
	}
	repaired := validateOverviewReduction(component, repairedResponse.Body)
	if !repaired.valid() {
		return nil, true, stats, nil
	}
	return &repaired, false, stats, nil
}

func generateOptionalDiagram(
	ctx context.Context,
	generator Generator,
	bundle prompt.FragmentBundle,
	request sharedmodel.GenerationRequest,
	scope fragmentScope,
	catalog []string,
	maxResponseBytes, maxRequestBytes int64,
) (fragmentValidation, bool, componentGenerationStats, error) {
	stats := componentGenerationStats{Diagram: 1}
	validation, err := generateFragmentWithRepair(ctx, generator, bundle, request, scope, catalog, maxResponseBytes, maxRequestBytes, &stats)
	// generateFragmentWithRepair counts every primary as a map fragment; reducers
	// have their own safe statistic instead.
	stats.Fragment--
	if err != nil {
		if optionalReducerMustFail(err) {
			return fragmentValidation{}, false, stats, err
		}
		return fragmentValidation{}, true, stats, nil
	}
	if validation.saturated {
		stats.Saturated++
	}
	return validation, false, stats, nil
}

func fragmentResponseValidationError(request sharedmodel.GenerationRequest, issues []sharedmodel.ValidationIssue) dossierValidationError {
	return dossierValidationError{
		componentKey: request.ComponentKey, kind: request.Kind, fragmentKind: request.FragmentKind,
		batchIndex: request.SourceBatchIndex, batchCount: request.SourceBatchCount,
		chunkIndex: request.SourceChunkIndex, chunkCount: request.SourceChunkCount,
		splitPath: request.SourceSplitPath, codes: issueCodes(issues),
	}
}

func trustedOverviewCandidates(validations []fragmentValidation) ([]sharedmodel.OverviewCandidate, []string, error) {
	candidates := make([]sharedmodel.OverviewCandidate, 0, len(validations))
	evidence := make([]string, 0)
	for _, validation := range validations {
		trusted, err := validation.revalidateSealed()
		if err != nil {
			return nil, nil, err
		}
		candidate, ok := trusted.value.(sharedmodel.OverviewCandidate)
		if !ok {
			return nil, nil, fragmentAssemblyError{code: "fragment_value_type_mismatch"}
		}
		candidate.SourcePaths = sortedUnique(candidate.SourcePaths)
		candidates = append(candidates, candidate)
		evidence = append(evidence, trusted.evidenceUsed...)
	}
	return candidates, sortedUnique(evidence), nil
}

func trustedFragmentEvidence(validations []fragmentValidation) ([]string, error) {
	evidence := make([]string, 0)
	for _, validation := range validations {
		trusted, err := validation.revalidateSealed()
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, trusted.evidenceUsed...)
	}
	return sortedUnique(evidence), nil
}

func overviewSections(dossier sharedmodel.ComponentDossier) []overviewSectionProjection {
	return []overviewSectionProjection{
		{Kind: sharedmodel.FragmentArchitecture, Count: len(dossier.Architecture), Names: firstArchitectureNames(dossier.Architecture)},
		{Kind: sharedmodel.FragmentInterfaces, Count: len(dossier.Interfaces), Names: firstInterfaceNames(dossier.Interfaces)},
		{Kind: sharedmodel.FragmentDataModels, Count: len(dossier.DataModels), Names: firstDataModelNames(dossier.DataModels)},
		{Kind: sharedmodel.FragmentWorkflows, Count: len(dossier.Workflows), Names: firstWorkflowNames(dossier.Workflows)},
		{Kind: sharedmodel.FragmentDependencies, Count: len(dossier.Dependencies), Names: firstDependencyNames(dossier.Dependencies)},
		{Kind: sharedmodel.FragmentReviewGaps, Count: len(dossier.ReviewGaps), Names: []string{}},
	}
}

func firstArchitectureNames(items []sharedmodel.ArchitectureItem) []string {
	result := make([]string, 0, min(len(items), reducerProjectionItems))
	for _, item := range items[:min(len(items), reducerProjectionItems)] {
		result = append(result, item.Title)
	}
	return result
}

func firstInterfaceNames(items []sharedmodel.InterfaceItem) []string {
	result := make([]string, 0, min(len(items), reducerProjectionItems))
	for _, item := range items[:min(len(items), reducerProjectionItems)] {
		result = append(result, item.Name)
	}
	return result
}

func firstDataModelNames(items []sharedmodel.DataModelItem) []string {
	result := make([]string, 0, min(len(items), reducerProjectionItems))
	for _, item := range items[:min(len(items), reducerProjectionItems)] {
		result = append(result, item.Name)
	}
	return result
}

func firstWorkflowNames(items []sharedmodel.WorkflowItem) []string {
	result := make([]string, 0, min(len(items), reducerProjectionItems))
	for _, item := range items[:min(len(items), reducerProjectionItems)] {
		result = append(result, item.Name)
	}
	return result
}

func firstDependencyNames(items []sharedmodel.DependencyItem) []string {
	result := make([]string, 0, min(len(items), reducerProjectionItems))
	for _, item := range items[:min(len(items), reducerProjectionItems)] {
		result = append(result, item.Name)
	}
	return result
}

func diagramProjectionFromDossier(dossier sharedmodel.ComponentDossier) (diagramProjection, []string) {
	projection := diagramProjection{}
	evidence := make([]string, 0)
	for _, item := range dossier.Architecture[:min(len(dossier.Architecture), reducerProjectionItems)] {
		paths, _ := boundedPaths(sortedUnique(item.SourcePaths), fragmentMaxSourcePaths)
		projection.Architecture = append(projection.Architecture, diagramArchitectureProjection{Title: item.Title, SourcePaths: paths})
		evidence = append(evidence, paths...)
	}
	for _, item := range dossier.DataModels[:min(len(dossier.DataModels), reducerProjectionItems)] {
		paths, _ := boundedPaths(sortedUnique(item.SourcePaths), fragmentMaxSourcePaths)
		relationships := append([]sharedmodel.DataRelationship(nil), item.Relationships[:min(len(item.Relationships), fragmentMaxRelationships)]...)
		projection.DataModels = append(projection.DataModels, diagramDataModelProjection{Name: item.Name, Relationships: relationships, SourcePaths: paths})
		evidence = append(evidence, paths...)
	}
	for _, item := range dossier.Workflows[:min(len(dossier.Workflows), reducerProjectionItems)] {
		paths, _ := boundedPaths(sortedUnique(item.SourcePaths), fragmentMaxSourcePaths)
		steps := append([]sharedmodel.WorkflowStep(nil), item.Steps[:min(len(item.Steps), fragmentMaxSteps)]...)
		projection.Workflows = append(projection.Workflows, diagramWorkflowProjection{Name: item.Name, Steps: steps, SourcePaths: paths})
		evidence = append(evidence, paths...)
	}
	return projection, sortedUnique(evidence)
}

func optionalReducerMustFail(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var limitErr fragmentCallLimitError
	return errors.As(err, &limitErr)
}
