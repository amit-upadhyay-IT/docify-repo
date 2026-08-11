package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

// defaultConcurrency is used when the invocation supplies no positive concurrency.
const defaultConcurrency = 2

// Sync builds the same plan as Plan, and when generation is required it generates and
// validates every component dossier, renders the complete knowledge base locally,
// validates the candidate output and ownership, and installs it through the recoverable
// worktree transaction. A failed model call or validation never alters installed output.
// The github-pr publisher reuses this generation core on a prepared documentation branch.
func (u *Usecase) Sync(ctx context.Context, input documentationmodel.SyncInput) (documentationmodel.ResultSummary, error) {
	if u.gitSource == nil || u.worktree == nil || u.state == nil {
		return documentationmodel.ResultSummary{}, fmt.Errorf("sync repositories are not configured")
	}
	publisher := input.Publisher
	if publisher == "" {
		publisher = "worktree"
	}
	if u.generator == nil || u.output == nil {
		return documentationmodel.ResultSummary{}, fmt.Errorf("sync requires a configured generator and output repository")
	}
	if publisher == "github-pr" {
		return u.publishGitHub(ctx, input)
	}
	if publisher != "worktree" {
		return documentationmodel.ResultSummary{}, unavailableError{command: "sync --publisher " + publisher}
	}
	if input.BaseSHA != "" || input.HeadSHA != "" {
		return documentationmodel.ResultSummary{}, sourceError{err: fmt.Errorf("worktree sync does not accept base/head SHAs yet; omit them to sync the current worktree")}
	}

	planInput := planInputFromSync(input)
	generation, err := u.prepareGeneration(ctx, "sync", planInput)
	if err != nil {
		return documentationmodel.ResultSummary{}, err
	}
	if generation.plan.Noop {
		generation.summary.Status = "noop"
		if err := u.writeRunReport(ctx, planInput, generation.summary, nil); err != nil {
			return documentationmodel.ResultSummary{}, err
		}
		return generation.summary, nil
	}

	candidate, inspection, err := u.generateCandidate(ctx, generation)
	if err != nil {
		return u.finishGenerationFailure(ctx, planInput, generation.summary, candidate, &inspection, err), err
	}
	inspection, err = u.revalidateInstallationOwnership(ctx, generation, inspection)
	if err != nil {
		return u.finishGenerationFailure(ctx, planInput, generation.summary, candidate, &inspection, err), err
	}
	if err := u.installCandidate(ctx, planInput, candidate, inspection); err != nil {
		failure := outputValidationError{fmt.Sprintf("install generated output: %v", err)}
		return u.finishGenerationFailure(ctx, planInput, generation.summary, candidate, &inspection, failure), failure
	}
	generation.summary.Status = "synced"
	generation.summary.Generation = candidate.outcome()
	markFragmentFallbacks(&generation.summary.Plan, candidate.fallbackComponents)
	if err := u.writeRunReport(ctx, planInput, generation.summary, &inspection); err != nil {
		return documentationmodel.ResultSummary{}, err
	}
	return generation.summary, nil
}

// preparedGeneration bundles the shared results of planning a run: the resolved snapshot,
// scan, discovered components, the deterministic plan, and the plan-derived summary. Both
// worktree sync and the GitHub publisher build on it.
type preparedGeneration struct {
	planInput  documentationmodel.PlanInput
	snapshot   resolvedSnapshot
	scan       scanResult
	components []sharedmodel.Component
	ownedFiles []sharedmodel.SourceFile
	plan       sharedmodel.GenerationPlan
	summary    documentationmodel.ResultSummary
}

// prepareGeneration validates policies, finalizes any interrupted transaction, resolves the
// snapshot, scans, discovers components, and builds the deterministic generation plan. It
// makes no model call and installs nothing.
func (u *Usecase) prepareGeneration(ctx context.Context, command string, planInput documentationmodel.PlanInput) (preparedGeneration, error) {
	if err := validateSourcePolicy(planInput.SourcePolicy); err != nil {
		return preparedGeneration{}, sourceError{err: err}
	}
	if err := validateComponentPolicy(planInput.ComponentPolicy); err != nil {
		return preparedGeneration{}, sourceError{err: err}
	}
	if err := validateGenerationPolicy(planInput.GenerationPolicy); err != nil {
		return preparedGeneration{}, sourceError{err: err}
	}

	// Recover any transaction interrupted by a previous run before touching output.
	if err := u.output.Recover(ctx, planInput.WorkingDirectory, planInput.SourcePolicy.DocsDir, planInput.SourcePolicy.StatePath); err != nil {
		return preparedGeneration{}, outputValidationError{fmt.Sprintf("recover interrupted transaction: %v", err)}
	}

	snapshot, err := u.resolveSnapshot(ctx, planInput)
	if err != nil {
		return preparedGeneration{}, sourceError{err: err}
	}
	scan, err := u.scan(ctx, snapshot.root, snapshot.entries, planInput.SourcePolicy, snapshot.reader)
	if err != nil {
		return preparedGeneration{}, sourceError{err: err}
	}
	components, ownedFiles, err := discoverComponents(scan.Files, planInput.ComponentPolicy, planInput.SourcePolicy.DocsDir)
	if err != nil {
		return preparedGeneration{}, sourceError{err: err}
	}
	applyDecisionOwners(scan.Decisions, ownedFiles)

	plan, err := buildGenerationPlan(planInput, components, ownedFiles, snapshot.state, snapshot.rawChanges, snapshot.fullFallback)
	if err != nil {
		return preparedGeneration{}, sourceError{err: err}
	}
	return preparedGeneration{
		planInput:  planInput,
		snapshot:   snapshot,
		scan:       scan,
		components: components,
		ownedFiles: ownedFiles,
		plan:       plan,
		summary:    u.baseSummary(command, scan, plan),
	}, nil
}

// generateCandidate inspects the installed tree and builds the validated candidate output.
// It performs the bounded model calls the plan requires but installs nothing.
func (u *Usecase) generateCandidate(ctx context.Context, generation preparedGeneration) (candidateOutput, installedInspection, error) {
	inspection, err := u.inspectInstalled(ctx, generation.planInput, generation.snapshot.state)
	if err != nil {
		return candidateOutput{}, installedInspection{}, err
	}
	if err := validateStatePathOwnership(inspection.existing, inspection.stateOwned, generation.planInput.Full); err != nil {
		return candidateOutput{}, inspection, err
	}
	if err := validateGeneratedOwnershipPrerequisite(inspection.existing, inspection.provenState, generation.planInput.Full); err != nil {
		return candidateOutput{}, inspection, err
	}
	candidate, err := u.buildCandidate(ctx, generation.planInput, generation.components, generation.ownedFiles,
		generation.scan.Decisions, generation.snapshot, generation.plan, inspection)
	if err != nil {
		return candidate, inspection, err
	}
	return candidate, inspection, nil
}

func (u *Usecase) revalidateInstallationOwnership(ctx context.Context, generation preparedGeneration, expected installedInspection) (installedInspection, error) {
	currentState, err := u.state.Load(ctx, generation.snapshot.root, generation.planInput.SourcePolicy.StatePath)
	if err != nil {
		return installedInspection{}, outputValidationError{fmt.Sprintf("revalidate state ownership: %v", err)}
	}
	if !reflect.DeepEqual(currentState, generation.snapshot.state) {
		return installedInspection{}, outputValidationError{"generated output or state changed during generation; refusing to install"}
	}
	current, err := u.inspectInstalled(ctx, generation.planInput, currentState)
	if err != nil {
		return installedInspection{}, err
	}
	if !reflect.DeepEqual(current.existing, expected.existing) ||
		!reflect.DeepEqual(current.content, expected.content) || current.stateHash != expected.stateHash {
		return installedInspection{}, outputValidationError{"generated output or state changed during generation; refusing to install"}
	}
	return current, nil
}

// candidateOutput is a fully rendered, validated documentation set ready to install or
// compare. Building it never mutates installed output.
type candidateOutput struct {
	rendered           renderedOutput
	stateData          []byte
	stats              []componentGenerationStats
	fallbackComponents []string
	deletes            []string
	diff               sharedmodel.OutputDiff
	fullMode           bool
	generated          int
}

// buildCandidate generates the dossiers the plan requires, renders the complete knowledge
// base (reusing verified installed bytes for unaffected components in incremental mode),
// validates it, and resolves ownership. It performs no install. A failed model call or
// validation therefore leaves installed output untouched.
func (u *Usecase) buildCandidate(
	ctx context.Context,
	input documentationmodel.PlanInput,
	components []sharedmodel.Component,
	files []sharedmodel.SourceFile,
	decisions []sharedmodel.SourceDecision,
	snapshot resolvedSnapshot,
	plan sharedmodel.GenerationPlan,
	inspection installedInspection,
) (candidateOutput, error) {
	fullMode := plan.FullReason != ""
	if !fullMode && !inspection.integrityOK {
		return candidateOutput{}, outputValidationError{fmt.Sprintf(
			"installed documentation cannot be verified for an incremental update (%s); rerun with --full to rebuild", inspection.reason)}
	}

	bundle := prompt.CodebaseSummaryV1()
	fragmentBundle := prompt.CodebaseSummaryV2()
	settings := generationSettings(input.GenerationPolicy)
	catalog := componentCatalog(components)
	configHashes, configHash, err := configurationHashes(input)
	if err != nil {
		return candidateOutput{}, sourceError{err: err}
	}
	_, compatible := stateCompatibility(snapshot.state)
	currentFiles := sourceFileMap(files)
	rawChanges := snapshot.rawChanges
	if input.BaseSHA == "" && compatible {
		rawChanges = localChanges(snapshot.state.State.Files, currentFiles)
	}
	changes := normalizeChanges(rawChanges, snapshot.state.State.Files, currentFiles)

	boundedSupporting := make(map[string][]sharedmodel.SourceFile, len(components))
	boundedManifests := make(map[string][]sharedmodel.SourceFile, len(components))
	for _, component := range components {
		identity := componentIdentity(component.Key, component.RootComponent)
		supporting, _ := boundedBytes(component.SupportingFiles, input.ComponentPolicy.MaxSupportingBytes)
		manifests, _ := boundedBytes(component.RelevantManifest, input.ComponentPolicy.MaxManifestBytes)
		boundedSupporting[identity] = supporting
		boundedManifests[identity] = manifests
	}

	var fresh map[string]bool
	var reuseSections map[sectionID]string
	var reusePages map[string]string
	if !fullMode {
		fresh = freshComponentSet(plan)
		reuseSections, reusePages, err = buildReuse(input.SourcePolicy.DocsDir, inspection.content)
		if err != nil {
			return candidateOutput{}, outputValidationError{err.Error()}
		}
	}

	// Only components that must be regenerated are sent to the model. In full mode fresh is
	// nil, which selects every component.
	generateIndices := make([]int, 0, len(components))
	for index, component := range components {
		if fresh == nil || fresh[componentIdentity(component.Key, component.RootComponent)] {
			generateIndices = append(generateIndices, index)
		}
	}

	concurrency := input.Concurrency
	if concurrency < 1 {
		concurrency = defaultConcurrency
	}
	limitedGenerator := newLLMCallLimiter(u.generator, concurrency)
	plannedStrategies := make(map[string]string, len(plan.AffectedComponents))
	for _, affected := range plan.AffectedComponents {
		if affected.ExistsNow && affected.Action != sharedmodel.ComponentSkipUnchanged {
			plannedStrategies[componentIdentity(affected.Key, affected.RootComponent)] = affected.GenerationStrategy
		}
	}
	// Workers write only their own slice index; the dossier map is assembled after the
	// barrier so no two goroutines touch shared state concurrently.
	producedDossiers := make([]sharedmodel.ComponentDossier, len(components))
	stats := make([]componentGenerationStats, len(components))
	err = runBounded(ctx, concurrency, len(generateIndices), func(ctx context.Context, position int) error {
		index := generateIndices[position]
		component := components[index]
		identity := componentIdentity(component.Key, component.RootComponent)
		relevant := changesForComponent(changes, component.Key, component.RootComponent)
		kind := changeKind(relevant, plan.FullReason)
		var dossier sharedmodel.ComponentDossier
		var componentStats componentGenerationStats
		var err error
		strategy := input.GenerationPolicy.GenerationStrategy
		if strategy == "auto" {
			var ok bool
			strategy, ok = plannedStrategies[identity]
			if !ok {
				return fmt.Errorf("component %q has no planned auto generation strategy", component.Key)
			}
		}
		if strategy == "fragments" {
			dossier, componentStats, err = generateComponentFragments(ctx, limitedGenerator, fragmentBundle, settings, component,
				boundedSupporting[identity], boundedManifests[identity], catalog, relevant, kind, input, concurrency)
		} else if input.GenerationPolicy.GenerationStrategy == "auto" {
			dossier, componentStats, err = generateAutoComponentDossier(ctx, limitedGenerator, bundle, fragmentBundle, settings, component,
				boundedSupporting[identity], boundedManifests[identity], catalog, relevant, kind, input, concurrency)
		} else {
			dossier, componentStats, err = generateComponentDossier(ctx, limitedGenerator, bundle, settings, component,
				boundedSupporting[identity], boundedManifests[identity], catalog, relevant, kind, input, concurrency)
		}
		stats[index] = componentStats
		if err != nil {
			return err
		}
		producedDossiers[index] = dossier
		return nil
	})
	if err != nil {
		completed := limitedGenerator.snapshot()
		for _, componentStats := range stats {
			completed.Fallback += componentStats.Fallback
			completed.Split += componentStats.Split
			completed.SplitCalls += componentStats.SplitCalls
			completed.Saturated += componentStats.Saturated
			completed.OverviewFallback += componentStats.OverviewFallback
			completed.DiagramFallback += componentStats.DiagramFallback
		}
		return candidateOutput{stats: []componentGenerationStats{completed}, fallbackComponents: fallbackComponentKeys(components, stats)}, err
	}
	failedAfterGeneration := candidateOutput{
		stats: stats, generated: len(generateIndices), fallbackComponents: fallbackComponentKeys(components, stats),
	}
	dossiers := make(map[string]sharedmodel.ComponentDossier, len(generateIndices))
	for _, index := range generateIndices {
		component := components[index]
		dossiers[componentIdentity(component.Key, component.RootComponent)] = producedDossiers[index]
	}

	rendered, err := renderDocumentation(renderInput{
		docsDir:         input.SourcePolicy.DocsDir,
		audience:        input.GenerationPolicy.Audience,
		mermaidEnabled:  input.GenerationPolicy.Mermaid,
		components:      components,
		dossiers:        dossiers,
		decisions:       decisions,
		files:           files,
		trackedPaths:    len(snapshot.entries),
		freshComponents: fresh,
		reuseSections:   reuseSections,
		reusePages:      reusePages,
	})
	if err != nil {
		return failedAfterGeneration, outputValidationError{err.Error()}
	}

	state := buildState(input, components, files, changes, catalog, configHashes, configHash, boundedSupporting, boundedManifests, rendered)
	if err := validateCandidateOutput(input.SourcePolicy.DocsDir, rendered, state); err != nil {
		return failedAfterGeneration, err
	}

	candidatePaths := make(map[string]struct{}, len(rendered.docs))
	for _, doc := range rendered.docs {
		candidatePaths[doc.path] = struct{}{}
	}
	priorOwned := make(map[string]struct{}, len(snapshot.state.State.GeneratedPaths))
	for _, ownedPath := range snapshot.state.State.GeneratedPaths {
		priorOwned[ownedPath] = struct{}{}
	}
	decision, err := resolveOwnership(inspection.existing, priorOwned, inspection.stateOwned, inspection.provenState, candidatePaths, input.Full)
	if err != nil {
		return failedAfterGeneration, err
	}

	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return failedAfterGeneration, fmt.Errorf("encode state: %w", err)
	}
	stateData = append(stateData, '\n')

	return candidateOutput{
		rendered:           rendered,
		stateData:          stateData,
		stats:              stats,
		fallbackComponents: fallbackComponentKeys(components, stats),
		deletes:            decision.deletes,
		diff:               classifyDiff(rendered, snapshot.state.State.GeneratedContentHashes, decision.deletes),
		fullMode:           fullMode,
		generated:          len(generateIndices),
	}, nil
}

// installCandidate writes the changed documents and state and removes deleted documents as
// one recoverable transaction. Full generation writes every document for a clean rebuild;
// an incremental run writes only the documents whose bytes actually changed, so a single
// component change touches only its own artifacts.
func (u *Usecase) installCandidate(ctx context.Context, input documentationmodel.PlanInput, candidate candidateOutput, inspection installedInspection) error {
	writeSet := make(map[string]struct{})
	if candidate.fullMode {
		for _, doc := range candidate.rendered.docs {
			writeSet[doc.path] = struct{}{}
		}
	} else {
		for _, added := range candidate.diff.Added {
			writeSet[added] = struct{}{}
		}
		for _, changed := range candidate.diff.Changed {
			writeSet[changed] = struct{}{}
		}
	}

	transaction := sharedmodel.OutputTransaction{Deletes: append([]string(nil), candidate.deletes...)}
	sort.Strings(transaction.Deletes)
	writes := make([]sharedmodel.RenderedDocument, 0, len(writeSet)+1)
	for _, doc := range candidate.rendered.docs {
		if _, ok := writeSet[doc.path]; !ok {
			continue
		}
		data := []byte(doc.content)
		writes = append(writes, sharedmodel.RenderedDocument{
			Path: doc.path, Data: data, ContentHash: contentHash(data),
			ComponentKeys: append([]string(nil), doc.componentKeys...), Deterministic: doc.deterministic,
		})
	}
	sort.Slice(writes, func(left, right int) bool { return writes[left].Path < writes[right].Path })
	writes = append(writes, sharedmodel.RenderedDocument{
		Path: input.SourcePolicy.StatePath, Data: candidate.stateData, ContentHash: contentHash(candidate.stateData),
	})
	transaction.Writes = writes
	preconditions := make(map[string]sharedmodel.OutputPrecondition, len(inspection.existing.GeneratedPaths)+len(writes)+1)
	for _, existingPath := range inspection.existing.GeneratedPaths {
		data, ok := inspection.content[existingPath]
		if !ok {
			return outputValidationError{fmt.Sprintf("installed output %q disappeared before installation", existingPath)}
		}
		preconditions[existingPath] = sharedmodel.OutputPrecondition{
			Path: existingPath, MustExist: true, ContentHash: contentHash(data),
		}
	}
	preconditions[input.SourcePolicy.StatePath] = sharedmodel.OutputPrecondition{
		Path: input.SourcePolicy.StatePath, MustExist: inspection.existing.StateExists, ContentHash: inspection.stateHash,
	}
	for _, write := range writes {
		if _, exists := preconditions[write.Path]; !exists {
			preconditions[write.Path] = sharedmodel.OutputPrecondition{Path: write.Path}
		}
	}
	transaction.Preconditions = make([]sharedmodel.OutputPrecondition, 0, len(preconditions))
	for _, precondition := range preconditions {
		transaction.Preconditions = append(transaction.Preconditions, precondition)
	}
	sort.Slice(transaction.Preconditions, func(left, right int) bool {
		return transaction.Preconditions[left].Path < transaction.Preconditions[right].Path
	})
	return u.output.Install(ctx, input.WorkingDirectory, transaction)
}

// outcome summarizes a candidate for the result summary and run report. It carries only
// safe counts, provider usage, and repository-relative path lists.
func (candidate candidateOutput) outcome() *documentationmodel.GenerationOutcome {
	outcome := &documentationmodel.GenerationOutcome{GeneratedComponents: candidate.generated}
	for _, componentStats := range candidate.stats {
		outcome.NormalCalls += componentStats.Normal
		outcome.BatchCalls += componentStats.Batch
		outcome.SynthesisCalls += componentStats.Synthesis
		outcome.FragmentCalls += componentStats.Fragment
		outcome.OverviewReducerCalls += componentStats.Overview
		outcome.DiagramReducerCalls += componentStats.Diagram
		outcome.RepairCalls += componentStats.Repair
		outcome.FragmentFallbacks += componentStats.Fallback
		outcome.FragmentSourceSplits += componentStats.Split
		outcome.FragmentSourceSplitCalls += componentStats.SplitCalls
		outcome.SaturatedScopes += componentStats.Saturated
		outcome.OverviewFallbacks += componentStats.OverviewFallback
		outcome.DiagramFallbacks += componentStats.DiagramFallback
		outcome.TransportAttempts += componentStats.TransportAttempts
		if componentStats.Usage.Present {
			outcome.Usage.Present = true
			outcome.Usage.PromptTokens += componentStats.Usage.PromptTokens
			outcome.Usage.CompletionTokens += componentStats.Usage.CompletionTokens
			outcome.Usage.TotalTokens += componentStats.Usage.TotalTokens
		}
	}
	outcome.Diff = candidate.diff
	outcome.FragmentFallbackComponents = append([]string{}, candidate.fallbackComponents...)
	written := len(candidate.diff.Added) + len(candidate.diff.Changed)
	if candidate.fullMode {
		written = len(candidate.rendered.docs)
	}
	outcome.InstalledPaths = written
	outcome.DeletedPaths = len(candidate.deletes)
	return outcome
}

func fallbackComponentKeys(components []sharedmodel.Component, stats []componentGenerationStats) []string {
	keys := make([]string, 0)
	for index, componentStats := range stats {
		if componentStats.Fallback > 0 && index < len(components) {
			keys = append(keys, components[index].Key)
		}
	}
	return sortedUnique(keys)
}

// finishGenerationFailure produces diagnostics without changing installed output. Report
// failures are intentionally ignored so they cannot replace the primary generation error.
func (u *Usecase) finishGenerationFailure(
	ctx context.Context,
	input documentationmodel.PlanInput,
	summary documentationmodel.ResultSummary,
	candidate candidateOutput,
	inspection *installedInspection,
	err error,
) documentationmodel.ResultSummary {
	summary.Status = "generation_failed"
	outcome := candidate.outcome()
	outcome.Diff = sharedmodel.OutputDiff{Added: []string{}, Changed: []string{}, Deleted: []string{}, Unchanged: []string{}}
	outcome.InstalledPaths = 0
	outcome.DeletedPaths = 0
	summary.Generation = outcome
	markFragmentFallbacks(&summary.Plan, candidate.fallbackComponents)
	summary.Failure = generationFailure(err)
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = u.writeRunReport(reportCtx, input, summary, inspection)
	return summary
}

func markFragmentFallbacks(plan *sharedmodel.GenerationPlan, components []string) {
	fallbacks := make(map[string]struct{}, len(components))
	for _, component := range components {
		fallbacks[component] = struct{}{}
	}
	for index := range plan.AffectedComponents {
		_, plan.AffectedComponents[index].FragmentFallback = fallbacks[plan.AffectedComponents[index].Key]
	}
}

func generationFailure(err error) *documentationmodel.GenerationFailure {
	var completionErr *sharedmodel.CompletionError
	if errors.As(err, &completionErr) {
		return &documentationmodel.GenerationFailure{
			Category:             string(completionErr.Category),
			ComponentKey:         completionErr.ComponentKey,
			RequestKind:          completionErr.RequestKind,
			BatchIndex:           completionErr.BatchIndex,
			BatchCount:           completionErr.BatchCount,
			FragmentKind:         completionErr.FragmentKind,
			SourceBatchIndex:     completionErr.SourceBatchIndex,
			SourceBatchCount:     completionErr.SourceBatchCount,
			SourceChunkIndex:     completionErr.SourceChunkIndex,
			SourceChunkCount:     completionErr.SourceChunkCount,
			SourceSplitPath:      completionErr.SourceSplitPath,
			FinishReason:         completionErr.FinishReason,
			ProviderRequestID:    completionErr.ProviderRequestID,
			StructuredOutputUsed: completionErr.StructuredOutputUsed,
			TransportAttempts:    completionErr.TransportAttempts,
		}
	}
	var transportErr *sharedmodel.TransportError
	if errors.As(err, &transportErr) {
		return &documentationmodel.GenerationFailure{
			Category:             "transport",
			ComponentKey:         transportErr.ComponentKey,
			RequestKind:          transportErr.RequestKind,
			BatchIndex:           transportErr.BatchIndex,
			BatchCount:           transportErr.BatchCount,
			FragmentKind:         transportErr.FragmentKind,
			SourceBatchIndex:     transportErr.SourceBatchIndex,
			SourceBatchCount:     transportErr.SourceBatchCount,
			SourceChunkIndex:     transportErr.SourceChunkIndex,
			SourceChunkCount:     transportErr.SourceChunkCount,
			SourceSplitPath:      transportErr.SourceSplitPath,
			StructuredOutputUsed: transportErr.StructuredOutputUsed,
			TransportAttempts:    transportErr.TransportAttempts,
		}
	}
	var validationErr dossierValidationError
	if errors.As(err, &validationErr) {
		return &documentationmodel.GenerationFailure{
			Category: "schema_validation", ComponentKey: validationErr.componentKey,
			RequestKind: validationErr.kind, FragmentKind: validationErr.fragmentKind,
			SourceBatchIndex: validationErr.batchIndex, SourceBatchCount: validationErr.batchCount,
			SourceChunkIndex: validationErr.chunkIndex, SourceChunkCount: validationErr.chunkCount,
			SourceSplitPath: validationErr.splitPath,
			ValidationCodes: append([]string(nil), validationErr.codes...),
		}
	}
	var limitErr fragmentCallLimitError
	if errors.As(err, &limitErr) {
		return &documentationmodel.GenerationFailure{Category: "call_limit", ComponentKey: limitErr.componentKey}
	}
	var outputErr outputValidationError
	if errors.As(err, &outputErr) {
		return &documentationmodel.GenerationFailure{Category: "output_validation"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &documentationmodel.GenerationFailure{Category: "cancelled"}
	}
	return &documentationmodel.GenerationFailure{Category: "generation"}
}

// baseSummary builds the shared plan-derived counts for a sync summary.
func (u *Usecase) baseSummary(command string, scan scanResult, plan sharedmodel.GenerationPlan) documentationmodel.ResultSummary {
	summary := documentationmodel.ResultSummary{
		Command:      command,
		TrackedPaths: len(scan.Decisions),
		Files:        scan.Decisions,
		Plan:         plan,
	}
	for _, decision := range scan.Decisions {
		if decision.IncludedAsContext {
			summary.IncludedPaths++
		} else {
			summary.ExcludedPaths++
		}
		if decision.TriggersRegeneration {
			summary.TriggeringPaths++
		}
	}
	return summary
}

func planInputFromSync(input documentationmodel.SyncInput) documentationmodel.PlanInput {
	return documentationmodel.PlanInput{
		WorkingDirectory:  input.WorkingDirectory,
		Output:            input.Output,
		ReportPath:        input.ReportPath,
		BaseSHA:           input.BaseSHA,
		HeadSHA:           input.HeadSHA,
		Full:              input.Full,
		AllowFullFallback: input.AllowFullFallback,
		Concurrency:       input.Concurrency,
		SourcePolicy:      input.SourcePolicy,
		ComponentPolicy:   input.ComponentPolicy,
		GenerationPolicy:  input.GenerationPolicy,
	}
}
