package usecase

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strconv"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

const (
	stateSchemaVersion          = 1
	generatorVersion            = "0.2.0"
	plannerVersion              = "v2"
	promptVersion               = "v2"
	outputSchemaVersion         = "v1"
	inputHashVersion            = "v2"
	responseJSONExpansionFactor = 3
)

type configImpact struct {
	paths      bool
	source     bool
	components bool
	context    bool
	generation bool
	unknown    bool
}

type componentRef struct {
	key      string
	root     bool
	document string
}

func buildGenerationPlan(
	input documentationmodel.PlanInput,
	components []sharedmodel.Component,
	files []sharedmodel.SourceFile,
	loadedState sharedmodel.StateLoadResult,
	rawChanges []sharedmodel.RawChange,
	fullFallback bool,
) (sharedmodel.GenerationPlan, error) {
	stateStatus, compatible := stateCompatibility(loadedState)
	currentHashes, configHash, err := configurationHashes(input)
	if err != nil {
		return sharedmodel.GenerationPlan{}, err
	}
	impacts := configImpact{}
	if compatible {
		impacts = compareConfiguration(loadedState.State, currentHashes, configHash)
	}

	fullReason := ""
	switch {
	case input.Full:
		fullReason = "explicit_full"
	case fullFallback:
		fullReason = "base_revision_unavailable"
	case loadedState.Missing:
		fullReason = "state_missing"
	case loadedState.Invalid:
		fullReason = "state_invalid"
	case !compatible:
		fullReason = "state_incompatible"
	case impacts.paths:
		fullReason = "output_paths_changed"
	}

	currentFiles := sourceFileMap(files)
	if input.BaseSHA == "" && compatible {
		rawChanges = localChanges(loadedState.State.Files, currentFiles)
	}
	changes := normalizeChanges(rawChanges, loadedState.State.Files, currentFiles)

	currentComponents := make(map[string]sharedmodel.Component, len(components))
	refs := make(map[string]componentRef, len(components)+len(loadedState.State.Components))
	for _, component := range components {
		identity := componentIdentity(component.Key, component.RootComponent)
		currentComponents[identity] = component
		refs[identity] = componentRef{key: component.Key, root: component.RootComponent, document: component.Document}
	}
	oldComponents := make(map[string]sharedmodel.StateComponent, len(loadedState.State.Components))
	for key, component := range loadedState.State.Components {
		identity := componentIdentity(key, component.RootComponent)
		oldComponents[identity] = component
		refs[identity] = componentRef{key: key, root: component.RootComponent, document: component.Document}
	}

	selected := make(map[string]map[string]struct{})
	selectComponent := func(key string, root bool, reason string) {
		identity := componentIdentity(key, root)
		if _, known := refs[identity]; !known {
			return
		}
		if selected[identity] == nil {
			selected[identity] = make(map[string]struct{})
		}
		selected[identity][reason] = struct{}{}
	}

	if fullReason != "" {
		for _, component := range components {
			selectComponent(component.Key, component.RootComponent, fullReason)
		}
		for identity, component := range oldComponents {
			if _, exists := currentComponents[identity]; !exists {
				ref := refs[identity]
				selectComponent(ref.key, component.RootComponent, fullReason)
			}
		}
	} else {
		selectChangedComponents(changes, selectComponent)
		selectOwnershipChanges(loadedState.State.Files, currentFiles, selectComponent)
		if impacts.source {
			selectFileDifferences(loadedState.State.Files, currentFiles, true, selectComponent)
		}
		if impacts.components {
			selectOwnershipChanges(loadedState.State.Files, currentFiles, selectComponent)
		}
		if impacts.context {
			selectAllCurrent(components, "context_configuration_changed", selectComponent)
		}
		if impacts.generation {
			selectAllCurrent(components, "generation_configuration_changed", selectComponent)
		}
		if impacts.unknown {
			selectAllCurrent(components, "configuration_changed", selectComponent)
		}
	}

	bundle := prompt.CodebaseSummaryV1()
	fragmentBundle := prompt.CodebaseSummaryV2()
	settings := generationSettings(input.GenerationPolicy)
	catalog := componentCatalog(components)
	componentSummaries := make([]sharedmodel.ComponentSummary, 0, len(components))
	selectedSupporting := make(map[string][]sharedmodel.SourceFile, len(components))
	selectedManifests := make(map[string][]sharedmodel.SourceFile, len(components))
	for _, component := range components {
		identity := componentIdentity(component.Key, component.RootComponent)
		supporting, omittedSupporting := boundedBytes(component.SupportingFiles, input.ComponentPolicy.MaxSupportingBytes)
		manifests, omittedManifests := boundedBytes(component.RelevantManifest, input.ComponentPolicy.MaxManifestBytes)
		selectedSupporting[identity] = supporting
		selectedManifests[identity] = manifests
		componentSummaries = append(componentSummaries, summarizeComponent(component, supporting, omittedSupporting, manifests, omittedManifests))
	}

	identities := make([]string, 0, len(selected))
	for identity := range selected {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	affected := make([]sharedmodel.AffectedComponent, 0, len(identities))
	deletedDocuments := make([]string, 0)
	calls := sharedmodel.CallEstimate{}
	fragmentTotals := make(map[sharedmodel.FragmentKind]sharedmodel.FragmentEstimate)
	for _, identity := range identities {
		ref := refs[identity]
		component, existsNow := currentComponents[identity]
		oldComponent, existedBefore := oldComponents[identity]
		reasons := sortedReasonSet(selected[identity])
		affectedComponent := sharedmodel.AffectedComponent{
			Key:                ref.key,
			RootComponent:      ref.root,
			Document:           ref.document,
			Reasons:            reasons,
			ExistedBefore:      existedBefore,
			ExistsNow:          existsNow,
			GenerationStrategy: input.GenerationPolicy.GenerationStrategy,
		}
		if !existsNow {
			if existedBefore {
				affectedComponent.Action = sharedmodel.ComponentDelete
				deletedDocuments = append(deletedDocuments, oldComponent.Document)
				affected = append(affected, affectedComponent)
			}
			continue
		}
		if existedBefore {
			affectedComponent.Action = sharedmodel.ComponentRegenerate
		} else {
			affectedComponent.Action = sharedmodel.ComponentCreate
		}
		relevantChanges := changesForComponent(changes, component.Key, component.RootComponent)
		supporting := selectedSupporting[identity]
		manifests := selectedManifests[identity]
		affectedComponent.InputHash = componentInputHash(component, supporting, manifests, catalog, relevantChanges, configHash)
		if fullReason == "" && existedBefore && oldComponent.InputHash == affectedComponent.InputHash {
			affectedComponent.Action = sharedmodel.ComponentSkipUnchanged
			affected = append(affected, affectedComponent)
			continue
		}
		kind := changeKind(relevantChanges, fullReason)
		strategy := input.GenerationPolicy.GenerationStrategy
		var requestPlan componentRequestPlan
		var fragmentPlan fragmentComponentPlan
		if strategy == "auto" {
			planned, feasible, err := planComponentFastPath(bundle, settings, component, supporting, manifests, catalog, relevantChanges, kind, input)
			if err != nil {
				return sharedmodel.GenerationPlan{}, err
			}
			if feasible {
				strategy = "dossier"
				requestPlan = componentRequestPlan{normal: true, requests: []plannedRequest{planned}}
				// Auto must prove its fragment fallback before issuing the fast-path call.
				fragmentPlan, err = planFragmentGeneration(fragmentBundle, settings, component, supporting, manifests, catalog, relevantChanges, kind, input)
				if err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
				if fragmentPlan.maximumCalls+2 > input.GenerationPolicy.FragmentCallLimit {
					return sharedmodel.GenerationPlan{}, fmt.Errorf("component %q auto generation requires at most %d baseline logical calls, exceeding the %d-call limit", component.Key, fragmentPlan.maximumCalls+2, input.GenerationPolicy.FragmentCallLimit)
				}
			} else {
				strategy = "fragments"
			}
		}
		affectedComponent.GenerationStrategy = strategy
		if strategy == "fragments" {
			if len(fragmentPlan.sourceScopes) == 0 {
				fragmentPlan, err = planFragmentGeneration(fragmentBundle, settings, component, supporting, manifests, catalog, relevantChanges, kind, input)
			}
			if err != nil {
				return sharedmodel.GenerationPlan{}, err
			}
			batches := make([]sharedmodel.ComponentBatch, 0, len(fragmentPlan.sourceScopes))
			for _, scope := range fragmentPlan.sourceScopes {
				batch := fragmentBatchEstimate(scope, fragmentPlan.mapRequests)
				batches = append(batches, batch)
				calls.RequestBytes = addInt64Saturated(calls.RequestBytes, batch.RequestBytes)
			}
			affectedComponent.Batches = batches
			affectedComponent.Fragments = fragmentPlanEstimates(fragmentPlan, false)
			affectedComponent.OverviewRequestBytes = fragmentPlan.overviewRequestMax
			affectedComponent.OverviewRepairBytes = fragmentPlan.overviewRepairMax
			affectedComponent.DiagramRequestBytes = fragmentPlan.diagramRequestMax
			affectedComponent.DiagramRepairBytes = fragmentPlan.diagramRepairMax
			mergeFragmentEstimates(fragmentTotals, affectedComponent.Fragments)
			calls.Fragment += len(fragmentPlan.mapRequests)
			calls.OverviewReducer++
			calls.DiagramReducer++
			calls.RequestBytes = addInt64Saturated(calls.RequestBytes,
				addInt64Saturated(fragmentPlan.overviewRequestMax, fragmentPlan.diagramRequestMax))
			calls.OverviewRequestBytes = addInt64Saturated(calls.OverviewRequestBytes, fragmentPlan.overviewRequestMax)
			calls.DiagramRequestBytes = addInt64Saturated(calls.DiagramRequestBytes, fragmentPlan.diagramRequestMax)
			calls.OverviewRepairBytes = maxInt64(calls.OverviewRepairBytes, fragmentPlan.overviewRepairMax)
			calls.DiagramRepairBytes = maxInt64(calls.DiagramRepairBytes, fragmentPlan.diagramRepairMax)
			maximumRepairs := input.GenerationPolicy.FragmentCallLimit / 2
			if err := addMaximumCallEstimate(&calls, maximumRepairs, input.GenerationPolicy.FragmentCallLimit, component.Key); err != nil {
				return sharedmodel.GenerationPlan{}, err
			}
			if err := addCheckedCallEstimate(&calls.MaximumFragmentRepairCalls, maximumRepairs, component.Key); err != nil {
				return sharedmodel.GenerationPlan{}, err
			}
			splitCalls, err := maximumFragmentSourceSplitCalls(fragmentPlan, input.GenerationPolicy.FragmentSplitDepth,
				input.GenerationPolicy.FragmentCallLimit, 0, component.Key)
			if err != nil {
				return sharedmodel.GenerationPlan{}, err
			}
			if err := addCheckedCallEstimate(&calls.MaximumSourceSplitCalls, splitCalls, component.Key); err != nil {
				return sharedmodel.GenerationPlan{}, err
			}
		} else {
			if len(requestPlan.requests) == 0 {
				requestPlan, err = planComponentGeneration(bundle, settings, component, supporting, manifests, catalog, relevantChanges, kind, input)
				if err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
			}
			batches := make([]sharedmodel.ComponentBatch, 0, len(requestPlan.requests))
			for _, planned := range requestPlan.requests {
				batches = append(batches, batchEstimate(planned))
			}
			affectedComponent.Batches = batches
			affectedComponent.SynthesisRequestBytes = requestPlan.synthesisBytes
			if requestPlan.normal {
				calls.Normal++
				calls.DossierFastPath++
			} else {
				calls.Batch += len(batches)
				calls.Synthesis++
			}
			for _, batch := range batches {
				calls.RequestBytes = addInt64Saturated(calls.RequestBytes, batch.RequestBytes)
			}
			calls.RequestBytes = addInt64Saturated(calls.RequestBytes, requestPlan.synthesisBytes)
			if input.GenerationPolicy.GenerationStrategy == "auto" {
				affectedComponent.FragmentFallbackPlan = true
				affectedComponent.Fragments = fragmentPlanEstimates(fragmentPlan, true)
				affectedComponent.OverviewRequestBytes = fragmentPlan.overviewRequestMax
				affectedComponent.OverviewRepairBytes = fragmentPlan.overviewRepairMax
				affectedComponent.DiagramRequestBytes = fragmentPlan.diagramRequestMax
				affectedComponent.DiagramRepairBytes = fragmentPlan.diagramRepairMax
				mergeFragmentEstimates(fragmentTotals, affectedComponent.Fragments)
				calls.FallbackRequestBytes = addInt64Saturated(calls.FallbackRequestBytes, fragmentPlanRequestBytes(fragmentPlan))
				calls.OverviewFallbackBytes = addInt64Saturated(calls.OverviewFallbackBytes, fragmentPlan.overviewRequestMax)
				calls.DiagramFallbackBytes = addInt64Saturated(calls.DiagramFallbackBytes, fragmentPlan.diagramRequestMax)
				calls.OverviewRepairBytes = maxInt64(calls.OverviewRepairBytes, fragmentPlan.overviewRepairMax)
				calls.DiagramRepairBytes = maxInt64(calls.DiagramRepairBytes, fragmentPlan.diagramRepairMax)
				maximumRepairs := input.GenerationPolicy.FragmentCallLimit / 2
				if err := addMaximumCallEstimate(&calls, maximumRepairs, input.GenerationPolicy.FragmentCallLimit, component.Key); err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
				maximumFragmentRepairs := (input.GenerationPolicy.FragmentCallLimit - 1) / 2
				if err := addCheckedCallEstimate(&calls.MaximumFragmentRepairCalls, maximumFragmentRepairs, component.Key); err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
				if err := addCheckedCallEstimate(&calls.MaximumTruncationFallbackCalls, input.GenerationPolicy.FragmentCallLimit-1, component.Key); err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
				splitCalls, err := maximumFragmentSourceSplitCalls(fragmentPlan, input.GenerationPolicy.FragmentSplitDepth,
					input.GenerationPolicy.FragmentCallLimit, 1, component.Key)
				if err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
				if err := addCheckedCallEstimate(&calls.MaximumSourceSplitCalls, splitCalls, component.Key); err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
			} else {
				componentPrimary := len(requestPlan.requests)
				if !requestPlan.normal {
					componentPrimary++
				}
				if err := addMaximumCallEstimate(&calls, componentPrimary, 2*componentPrimary, component.Key); err != nil {
					return sharedmodel.GenerationPlan{}, err
				}
			}
		}
		affected = append(affected, affectedComponent)
	}
	sort.Strings(deletedDocuments)
	calls.Fragments = orderedFragmentEstimates(fragmentTotals)
	calls.Primary = calls.Normal + calls.Batch + calls.Synthesis + calls.Fragment + calls.OverviewReducer + calls.DiagramReducer
	calls.TypicalLogical = calls.Primary
	if input.GenerationPolicy.StructuredOutputMode == "auto" {
		calls.MaximumTransportFallback = calls.MaximumLogical
		calls.StructuredModesAttempted = 2
	} else {
		calls.StructuredModesAttempted = 1
	}
	calls.TransportRetries = input.GenerationPolicy.TransportRetries
	const maximumInt64 = int64(^uint64(0) >> 1)
	if calls.RequestBytes == maximumInt64 || calls.FallbackRequestBytes == maximumInt64 {
		return sharedmodel.GenerationPlan{}, fmt.Errorf("planned request byte estimate exceeds integer capacity")
	}
	maximumAttempts, err := maximumHTTPAttempts(calls.MaximumLogical, input.GenerationPolicy.StructuredOutputMode, input.GenerationPolicy.TransportRetries)
	if err != nil {
		return sharedmodel.GenerationPlan{}, err
	}
	calls.MaximumHTTPAttempts = maximumAttempts
	calls.ConservativeTokens = calls.RequestBytes
	calls.TypicalTokens = divideRoundUp(calls.RequestBytes, 4)

	mode := "incremental"
	if fullReason != "" {
		mode = "full"
	}
	return sharedmodel.GenerationPlan{
		Mode:               mode,
		GenerationStrategy: input.GenerationPolicy.GenerationStrategy,
		StateStatus:        stateStatus,
		BaseSHA:            input.BaseSHA,
		HeadSHA:            input.HeadSHA,
		FullReason:         fullReason,
		Noop:               calls.Primary == 0 && len(deletedDocuments) == 0,
		Components:         componentSummaries,
		Changes:            changes,
		AffectedComponents: affected,
		DeletedDocuments:   deletedDocuments,
		Calls:              calls,
	}, nil
}

func addMaximumCallEstimate(calls *sharedmodel.CallEstimate, repairs, logical int, componentKey string) error {
	maximumInt := int(^uint(0) >> 1)
	if repairs < 0 || logical < 0 || calls.MaximumRepair > maximumInt-repairs || calls.MaximumLogical > maximumInt-logical {
		return fmt.Errorf("component %q maximum logical call estimate exceeds integer capacity", componentKey)
	}
	calls.MaximumRepair += repairs
	calls.MaximumLogical += logical
	return nil
}

func addCheckedCallEstimate(destination *int, value int, componentKey string) error {
	maximumInt := int(^uint(0) >> 1)
	if value < 0 || *destination > maximumInt-value {
		return fmt.Errorf("component %q maximum logical call estimate exceeds integer capacity", componentKey)
	}
	*destination += value
	return nil
}

func maximumHTTPAttempts(logical int, structuredMode string, retries int) (int, error) {
	if logical == 0 {
		return 0, nil
	}
	modes := 1
	if structuredMode == "auto" {
		modes = 2
	}
	perLogical, err := checkedMultiplyInt(modes, retries+1)
	if err != nil {
		return 0, fmt.Errorf("maximum HTTP attempt estimate exceeds integer capacity")
	}
	attempts, err := checkedMultiplyInt(logical, perLogical)
	if err != nil {
		return 0, fmt.Errorf("maximum HTTP attempt estimate exceeds integer capacity")
	}
	return attempts, nil
}

func checkedMultiplyInt(left, right int) (int, error) {
	maximumInt := int(^uint(0) >> 1)
	if left < 0 || right < 0 || left != 0 && right > maximumInt/left {
		return 0, fmt.Errorf("integer multiplication overflow")
	}
	return left * right, nil
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func addInt64Saturated(left, right int64) int64 {
	const maximumInt64 = int64(^uint64(0) >> 1)
	if left < 0 || right < 0 || left > maximumInt64-right {
		return maximumInt64
	}
	return left + right
}

func multiplyInt64Saturated(left, right int64) int64 {
	const maximumInt64 = int64(^uint64(0) >> 1)
	if left < 0 || right < 0 || left != 0 && right > maximumInt64/left {
		return maximumInt64
	}
	return left * right
}

func fragmentPlanEstimates(plan fragmentComponentPlan, fallback bool) []sharedmodel.FragmentEstimate {
	estimates := make(map[sharedmodel.FragmentKind]sharedmodel.FragmentEstimate)
	for _, planned := range plan.mapRequests {
		estimate := estimates[planned.scope.Kind]
		estimate.Kind = planned.scope.Kind
		requestBytes := requestContentBytes(planned.request)
		if fallback {
			estimate.FallbackCalls++
			estimate.FallbackRequestBytes = addInt64Saturated(estimate.FallbackRequestBytes, requestBytes)
		} else {
			estimate.PlannedCalls++
			estimate.PlannedRequestBytes = addInt64Saturated(estimate.PlannedRequestBytes, requestBytes)
		}
		if planned.repairRequestBytes > estimate.MaximumRepairRequestBytes {
			estimate.MaximumRepairRequestBytes = planned.repairRequestBytes
		}
		estimates[planned.scope.Kind] = estimate
	}
	return orderedFragmentEstimates(estimates)
}

func orderedFragmentEstimates(estimates map[sharedmodel.FragmentKind]sharedmodel.FragmentEstimate) []sharedmodel.FragmentEstimate {
	result := make([]sharedmodel.FragmentEstimate, 0, len(estimates))
	for _, kind := range fragmentMapKinds() {
		if estimate, ok := estimates[kind]; ok {
			result = append(result, estimate)
		}
	}
	return result
}

func mergeFragmentEstimates(destination map[sharedmodel.FragmentKind]sharedmodel.FragmentEstimate, source []sharedmodel.FragmentEstimate) {
	for _, estimate := range source {
		merged := destination[estimate.Kind]
		merged.Kind = estimate.Kind
		merged.PlannedCalls += estimate.PlannedCalls
		merged.FallbackCalls += estimate.FallbackCalls
		merged.PlannedRequestBytes = addInt64Saturated(merged.PlannedRequestBytes, estimate.PlannedRequestBytes)
		merged.FallbackRequestBytes = addInt64Saturated(merged.FallbackRequestBytes, estimate.FallbackRequestBytes)
		if estimate.MaximumRepairRequestBytes > merged.MaximumRepairRequestBytes {
			merged.MaximumRepairRequestBytes = estimate.MaximumRepairRequestBytes
		}
		destination[estimate.Kind] = merged
	}
}

func fragmentPlanRequestBytes(plan fragmentComponentPlan) int64 {
	result := addInt64Saturated(plan.overviewRequestMax, plan.diagramRequestMax)
	for _, planned := range plan.mapRequests {
		result = addInt64Saturated(result, requestContentBytes(planned.request))
	}
	return result
}

// maximumFragmentSourceSplitCalls reports child fragment calls, not split
// events. A failing run can spend the remaining component budget on one early
// required root before later map requests or reducers execute, so only the
// strategy prefix and that root call can be reserved from the strict ceiling.
func maximumFragmentSourceSplitCalls(plan fragmentComponentPlan, depth, callLimit, prefixCalls int, componentKey string) (int, error) {
	if depth <= 0 {
		return 0, nil
	}
	requiredRoots := 0
	for _, planned := range plan.mapRequests {
		if requiredFragmentKind(planned.scope.Kind) {
			requiredRoots++
		}
	}
	descendantCallsPerRoot := (1 << (depth + 1)) - 2
	theoreticalCalls, err := checkedMultiplyInt(requiredRoots, descendantCallsPerRoot)
	if err != nil {
		return 0, fmt.Errorf("component %q maximum source-split estimate exceeds integer capacity", componentKey)
	}
	remaining := callLimit - prefixCalls - 1
	if remaining <= 0 {
		return 0, nil
	}
	if theoreticalCalls > remaining {
		theoreticalCalls = remaining
	}
	return theoreticalCalls, nil
}

func stateCompatibility(result sharedmodel.StateLoadResult) (string, bool) {
	if result.Missing {
		return "missing", false
	}
	if result.Invalid {
		return "invalid", false
	}
	state := result.State
	if state.SchemaVersion != stateSchemaVersion || state.GeneratorVersion != generatorVersion ||
		state.PlannerVersion != plannerVersion || state.PromptVersion != promptVersion || state.RenderVersion != renderVersion ||
		state.OutputSchemaVersion != outputSchemaVersion {
		return "incompatible", false
	}
	return "compatible", true
}

func validateGenerationPolicy(policy documentationmodel.GenerationPolicy) error {
	if policy.Profile != "codebase-summary" {
		return fmt.Errorf("unsupported documentation profile %q", policy.Profile)
	}
	switch policy.Audience {
	case "mixed", "human", "ai-assistant":
	default:
		return fmt.Errorf("unsupported documentation audience %q", policy.Audience)
	}
	switch policy.GenerationStrategy {
	case "auto", "dossier", "fragments":
	default:
		return fmt.Errorf("unsupported documentation generation strategy %q", policy.GenerationStrategy)
	}
	if policy.Provider != "openai-compatible" {
		return fmt.Errorf("unsupported LLM provider %q", policy.Provider)
	}
	if policy.APIMode != "chat_completions" && policy.APIMode != "responses" {
		return fmt.Errorf("unsupported LLM API mode %q", policy.APIMode)
	}
	if policy.Temperature < 0 || policy.Temperature > 2 || policy.MaxOutputTokens <= 0 || policy.MaxResponseBytes <= 0 {
		return fmt.Errorf("invalid LLM generation limits")
	}
	if policy.FragmentCallLimit <= 0 {
		return fmt.Errorf("invalid fragment call limit")
	}
	if policy.TransportRetries < 0 {
		return fmt.Errorf("invalid transport retry limit")
	}
	if policy.FragmentSplitDepth < 0 || policy.FragmentSplitDepth > sharedmodel.MaximumFragmentSplitDepth {
		return fmt.Errorf("invalid fragment split depth")
	}
	switch policy.StructuredOutputMode {
	case "auto", "json_schema", "prompt_json":
	default:
		return fmt.Errorf("unsupported structured output mode %q", policy.StructuredOutputMode)
	}
	return nil
}

func configurationHashes(input documentationmodel.PlanInput) (sharedmodel.StateConfigHashes, string, error) {
	pathsHash, err := hashJSON(struct {
		DocsDir   string `json:"docs_dir"`
		StatePath string `json:"state_path"`
	}{input.SourcePolicy.DocsDir, input.SourcePolicy.StatePath})
	if err != nil {
		return sharedmodel.StateConfigHashes{}, "", err
	}
	sourceHash, err := hashJSON(struct {
		Version       string                            `json:"classification_version"`
		Include       []string                          `json:"include"`
		Exclude       []string                          `json:"exclude"`
		MaxFileBytes  int64                             `json:"max_file_bytes"`
		Tests         documentationmodel.SourceBehavior `json:"tests"`
		Generated     documentationmodel.SourceBehavior `json:"generated"`
		Fixtures      documentationmodel.SourceBehavior `json:"fixtures"`
		RoleOverrides []documentationmodel.RoleOverride `json:"role_overrides"`
	}{classificationVersion, input.SourcePolicy.Include, input.SourcePolicy.Exclude, input.SourcePolicy.MaxFileBytes,
		input.SourcePolicy.Tests, input.SourcePolicy.Generated, input.SourcePolicy.Fixtures, input.SourcePolicy.RoleOverrides})
	if err != nil {
		return sharedmodel.StateConfigHashes{}, "", err
	}
	componentsHash, err := hashJSON(struct {
		Version  string   `json:"discovery_version"`
		Strategy string   `json:"strategy"`
		Roots    []string `json:"roots"`
	}{componentDiscoveryVersion, input.ComponentPolicy.Strategy, input.ComponentPolicy.Roots})
	if err != nil {
		return sharedmodel.StateConfigHashes{}, "", err
	}
	contextHash, err := hashJSON(struct {
		MaxContextBytes    int64 `json:"max_context_bytes"`
		MaxBatchBytes      int64 `json:"max_batch_bytes"`
		MaxSupportingBytes int64 `json:"max_supporting_bytes"`
		MaxManifestBytes   int64 `json:"max_manifest_bytes"`
		MaxDiffBytes       int64 `json:"max_diff_bytes"`
		MaxRequestBytes    int64 `json:"max_request_bytes"`
	}{input.ComponentPolicy.MaxContextBytes, input.ComponentPolicy.MaxBatchBytes, input.ComponentPolicy.MaxSupportingBytes,
		input.ComponentPolicy.MaxManifestBytes, input.ComponentPolicy.MaxDiffBytes, input.ComponentPolicy.MaxRequestBytes})
	if err != nil {
		return sharedmodel.StateConfigHashes{}, "", err
	}
	generationHash, err := generationConfigurationHash(input.GenerationPolicy, fragmentMergeVersion)
	if err != nil {
		return sharedmodel.StateConfigHashes{}, "", err
	}
	hashes := sharedmodel.StateConfigHashes{
		Paths: pathsHash, Source: sourceHash, Components: componentsHash, Context: contextHash, Generation: generationHash,
	}
	aggregate, err := hashJSON(hashes)
	return hashes, aggregate, err
}

func generationConfigurationHash(policy documentationmodel.GenerationPolicy, mergeVersion string) (string, error) {
	return hashJSON(struct {
		PromptVersion       string                              `json:"prompt_version"`
		PromptContentHash   string                              `json:"prompt_content_hash"`
		OutputSchemaVersion string                              `json:"output_schema_version"`
		MergeVersion        string                              `json:"merge_version"`
		Policy              documentationmodel.GenerationPolicy `json:"policy"`
	}{promptVersion, generationPromptContentHash(policy.GenerationStrategy), outputSchemaVersion, mergeVersion, policy})
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode deterministic hash input: %w", err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest), nil
}

func compareConfiguration(state sharedmodel.State, current sharedmodel.StateConfigHashes, aggregate string) configImpact {
	if state.ConfigHash == aggregate {
		return configImpact{}
	}
	previous := state.ConfigHashes
	if previous.Paths == "" || previous.Source == "" || previous.Components == "" || previous.Context == "" || previous.Generation == "" {
		return configImpact{unknown: true}
	}
	return configImpact{
		paths: previous.Paths != current.Paths, source: previous.Source != current.Source,
		components: previous.Components != current.Components, context: previous.Context != current.Context,
		generation: previous.Generation != current.Generation,
	}
}

func sourceFileMap(files []sharedmodel.SourceFile) map[string]sharedmodel.SourceFile {
	result := make(map[string]sharedmodel.SourceFile, len(files))
	for _, file := range files {
		result[file.Path] = file
	}
	return result
}

func localChanges(oldFiles map[string]sharedmodel.StateFile, currentFiles map[string]sharedmodel.SourceFile) []sharedmodel.RawChange {
	paths := make(map[string]struct{}, len(oldFiles)+len(currentFiles))
	for repositoryPath := range oldFiles {
		paths[repositoryPath] = struct{}{}
	}
	for repositoryPath := range currentFiles {
		paths[repositoryPath] = struct{}{}
	}
	sortedPaths := make([]string, 0, len(paths))
	for repositoryPath := range paths {
		sortedPaths = append(sortedPaths, repositoryPath)
	}
	sort.Strings(sortedPaths)
	changes := make([]sharedmodel.RawChange, 0)
	for _, repositoryPath := range sortedPaths {
		oldFile, oldExists := oldFiles[repositoryPath]
		currentFile, currentExists := currentFiles[repositoryPath]
		switch {
		case !oldExists:
			changes = append(changes, sharedmodel.RawChange{Status: sharedmodel.ChangeAdded, NewPath: repositoryPath})
		case !currentExists:
			changes = append(changes, sharedmodel.RawChange{Status: sharedmodel.ChangeDeleted, OldPath: repositoryPath})
		case stateFileChanged(oldFile, currentFile):
			changes = append(changes, sharedmodel.RawChange{Status: sharedmodel.ChangeModified, NewPath: repositoryPath})
		}
	}
	return changes
}

func normalizeChanges(raw []sharedmodel.RawChange, oldFiles map[string]sharedmodel.StateFile, currentFiles map[string]sharedmodel.SourceFile) []sharedmodel.Change {
	changes := make([]sharedmodel.Change, 0, len(raw))
	for _, candidate := range raw {
		change := sharedmodel.Change{Status: candidate.Status, OldPath: candidate.OldPath, NewPath: candidate.NewPath, Similarity: candidate.Similarity}
		if oldFile, exists := oldFiles[candidate.OldPath]; exists {
			change.OldRole = oldFile.Role
			change.OldComponentKey = oldFile.ComponentKey
			change.OldRootComponent = oldFile.RootComponent
			change.OldTriggersRegeneration = oldFile.TriggersRegeneration
		}
		if currentFile, exists := currentFiles[candidate.NewPath]; exists {
			change.NewRole = currentFile.Role
			change.NewComponentKey = currentFile.ComponentKey
			change.NewRootComponent = currentFile.RootComponent
			change.NewTriggersRegeneration = currentFile.TriggersRegeneration
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(left, right int) bool {
		if changes[left].OldPath != changes[right].OldPath {
			return changes[left].OldPath < changes[right].OldPath
		}
		if changes[left].NewPath != changes[right].NewPath {
			return changes[left].NewPath < changes[right].NewPath
		}
		return changes[left].Status < changes[right].Status
	})
	return changes
}

func selectChangedComponents(changes []sharedmodel.Change, selectComponent func(string, bool, string)) {
	for _, change := range changes {
		switch change.Status {
		case sharedmodel.ChangeAdded:
			if change.NewTriggersRegeneration {
				selectComponent(change.NewComponentKey, change.NewRootComponent, "source_added")
			}
		case sharedmodel.ChangeModified:
			if change.OldTriggersRegeneration {
				selectComponent(change.OldComponentKey, change.OldRootComponent, "source_modified")
			}
			if change.NewTriggersRegeneration {
				selectComponent(change.NewComponentKey, change.NewRootComponent, "source_modified")
			}
		case sharedmodel.ChangeDeleted:
			if change.OldTriggersRegeneration {
				selectComponent(change.OldComponentKey, change.OldRootComponent, "source_deleted")
			}
		case sharedmodel.ChangeRenamed:
			if change.OldTriggersRegeneration {
				selectComponent(change.OldComponentKey, change.OldRootComponent, "source_renamed")
			}
			if change.NewTriggersRegeneration {
				selectComponent(change.NewComponentKey, change.NewRootComponent, "source_renamed")
			}
		}
	}
}

func selectOwnershipChanges(oldFiles map[string]sharedmodel.StateFile, currentFiles map[string]sharedmodel.SourceFile, selectComponent func(string, bool, string)) {
	for repositoryPath, oldFile := range oldFiles {
		currentFile, exists := currentFiles[repositoryPath]
		if !exists || oldFile.ComponentKey == currentFile.ComponentKey && oldFile.RootComponent == currentFile.RootComponent {
			continue
		}
		selectComponent(oldFile.ComponentKey, oldFile.RootComponent, "ownership_changed")
		selectComponent(currentFile.ComponentKey, currentFile.RootComponent, "ownership_changed")
	}
}

func selectFileDifferences(oldFiles map[string]sharedmodel.StateFile, currentFiles map[string]sharedmodel.SourceFile, includeSupporting bool, selectComponent func(string, bool, string)) {
	for repositoryPath, oldFile := range oldFiles {
		currentFile, exists := currentFiles[repositoryPath]
		if exists && !stateFileChanged(oldFile, currentFile) {
			continue
		}
		if includeSupporting || oldFile.TriggersRegeneration {
			selectComponent(oldFile.ComponentKey, oldFile.RootComponent, "source_policy_changed")
		}
		if exists && (includeSupporting || currentFile.TriggersRegeneration) {
			selectComponent(currentFile.ComponentKey, currentFile.RootComponent, "source_policy_changed")
		}
	}
	for repositoryPath, currentFile := range currentFiles {
		if _, exists := oldFiles[repositoryPath]; exists {
			continue
		}
		if includeSupporting || currentFile.TriggersRegeneration {
			selectComponent(currentFile.ComponentKey, currentFile.RootComponent, "source_policy_changed")
		}
	}
}

func stateFileChanged(oldFile sharedmodel.StateFile, currentFile sharedmodel.SourceFile) bool {
	return oldFile.SourceHash != currentFile.SourceHash || oldFile.Role != currentFile.Role ||
		oldFile.TriggersRegeneration != currentFile.TriggersRegeneration || oldFile.ComponentKey != currentFile.ComponentKey ||
		oldFile.RootComponent != currentFile.RootComponent
}

func selectAllCurrent(components []sharedmodel.Component, reason string, selectComponent func(string, bool, string)) {
	for _, component := range components {
		selectComponent(component.Key, component.RootComponent, reason)
	}
}

func componentCatalog(components []sharedmodel.Component) []string {
	catalog := make([]string, 0, len(components))
	for _, component := range components {
		catalog = append(catalog, component.Key)
	}
	sort.Strings(catalog)
	return catalog
}

// boundedBytes selects files in their supplied order while their cumulative size
// stays within limit, returning the selected files and the number omitted. It is
// used for supplemental context (supporting tests and relevant manifests) that may
// be trimmed, never for triggering source, which must never be silently dropped.
func boundedBytes(files []sharedmodel.SourceFile, limit int64) ([]sharedmodel.SourceFile, int) {
	selected := make([]sharedmodel.SourceFile, 0, len(files))
	var selectedBytes int64
	for _, file := range files {
		if file.Size < 0 || selectedBytes > limit || file.Size > limit-selectedBytes {
			continue
		}
		selected = append(selected, file)
		selectedBytes = addInt64Saturated(selectedBytes, file.Size)
	}
	return selected, len(files) - len(selected)
}

func summarizeComponent(component sharedmodel.Component, supporting []sharedmodel.SourceFile, omittedSupporting int, manifests []sharedmodel.SourceFile, omittedManifests int) sharedmodel.ComponentSummary {
	summary := sharedmodel.ComponentSummary{
		Key: component.Key, RootComponent: component.RootComponent, Document: component.Document,
		OmittedSupporting: omittedSupporting, OmittedManifests: omittedManifests,
	}
	for _, file := range component.TriggeringFiles {
		summary.TriggeringPaths = append(summary.TriggeringPaths, file.Path)
		summary.TriggeringBytes = addInt64Saturated(summary.TriggeringBytes, file.Size)
	}
	for _, file := range supporting {
		summary.SupportingPaths = append(summary.SupportingPaths, file.Path)
		summary.SupportingBytes = addInt64Saturated(summary.SupportingBytes, file.Size)
	}
	for _, file := range manifests {
		summary.ManifestPaths = append(summary.ManifestPaths, file.Path)
		summary.ManifestBytes = addInt64Saturated(summary.ManifestBytes, file.Size)
	}
	return summary
}

func changesForComponent(changes []sharedmodel.Change, key string, root bool) []sharedmodel.Change {
	result := make([]sharedmodel.Change, 0)
	for _, change := range changes {
		oldMatch := change.OldComponentKey == key && change.OldRootComponent == root
		newMatch := change.NewComponentKey == key && change.NewRootComponent == root
		if oldMatch || newMatch {
			result = append(result, change)
		}
	}
	return result
}

func componentInputHash(component sharedmodel.Component, supporting, manifests []sharedmodel.SourceFile, catalog []string, changes []sharedmodel.Change, configHash string) string {
	digest := sha256.New()
	writeHashField(digest, inputHashVersion)
	writeHashField(digest, generatorVersion)
	writeHashField(digest, plannerVersion)
	writeHashField(digest, promptVersion)
	writeHashField(digest, renderVersion)
	writeHashField(digest, outputSchemaVersion)
	writeHashField(digest, fragmentMergeVersion)
	writeHashField(digest, configHash)
	writeHashField(digest, component.Key)
	writeHashField(digest, strconv.FormatBool(component.RootComponent))

	// Each variable-length section is prefixed with its element count so that no two
	// different partitions of files, manifests, or changes can hash to the same value.
	writeHashField(digest, strconv.Itoa(len(catalog)))
	for _, key := range catalog {
		writeHashField(digest, key)
	}
	source := append(append([]sharedmodel.SourceFile(nil), component.TriggeringFiles...), supporting...)
	writeHashField(digest, strconv.Itoa(len(source)))
	for _, file := range source {
		writeHashFile(digest, file)
	}
	writeHashField(digest, strconv.Itoa(len(manifests)))
	for _, file := range manifests {
		writeHashFile(digest, file)
	}
	writeHashField(digest, strconv.Itoa(len(changes)))
	for _, change := range changes {
		data, _ := json.Marshal(change)
		writeHashField(digest, string(data))
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil))
}

func writeHashFile(destination hash.Hash, file sharedmodel.SourceFile) {
	writeHashField(destination, file.Path)
	writeHashField(destination, string(file.Role))
	writeHashField(destination, file.SourceHash)
	writeHashField(destination, string(file.Data))
}

func writeHashField(destination hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write([]byte(value))
}

// plannedRequest pairs a built request with the exact source group it covers, so both
// the plan estimate and later generation share one deterministic split.
type plannedRequest struct {
	request sharedmodel.GenerationRequest
	source  []sharedmodel.SourceFile
}

// componentRequestPlan is the full set of requests one affected component needs. When
// normal is true it holds a single component request; otherwise it holds one request
// per stable batch plus the worst-case synthesis size.
type componentRequestPlan struct {
	normal         bool
	requests       []plannedRequest
	synthesisBytes int64
}

type fragmentSourceScope struct {
	source          []sharedmodel.SourceFile
	batchIndex      int
	batchCount      int
	chunkIndex      int
	chunkCount      int
	splitPath       string
	allowedEvidence []string
}

func (s fragmentSourceScope) coverageScope(component sharedmodel.Component, kind sharedmodel.FragmentKind) fragmentScope {
	return fragmentScope{
		ComponentKey: component.Key, RootComponent: component.RootComponent, Kind: kind,
		SourceBatchIndex: s.batchIndex, SourceBatchCount: s.batchCount,
		SourceChunkIndex: s.chunkIndex, SourceChunkCount: s.chunkCount,
		SplitPath: s.splitPath, AllowedEvidence: append([]string(nil), s.allowedEvidence...),
	}
}

type plannedFragmentRequest struct {
	request            sharedmodel.GenerationRequest
	scope              fragmentScope
	source             []sharedmodel.SourceFile
	repairRequestBytes int64
}

type fragmentComponentPlan struct {
	sourceScopes       []fragmentSourceScope
	mapRequests        []plannedFragmentRequest
	overviewRequestMax int64
	overviewRepairMax  int64
	diagramRequestMax  int64
	diagramRepairMax   int64
	maximumCalls       int
}

type fragmentSourceBatch struct {
	chunks [][]sharedmodel.SourceFile
}

type fragmentPlanSizeError struct {
	batchIndex int
	chunkIndex int
	size       int64
}

func (e fragmentPlanSizeError) Error() string {
	return fmt.Sprintf("fragment source scope %d/%d request is %d bytes", e.batchIndex, e.chunkIndex, e.size)
}

// planComponentGeneration is the single source of truth for how a component is split
// into requests. It is used by the planner to estimate calls and by generation to send
// them, so a component that plans as normal always generates as normal.
func planComponentGeneration(bundle prompt.Bundle, settings sharedmodel.GenerationSettings, component sharedmodel.Component, supporting, manifests []sharedmodel.SourceFile, catalog []string, changes []sharedmodel.Change, kind string, input documentationmodel.PlanInput) (componentRequestPlan, error) {
	planned, feasible, err := planComponentFastPath(bundle, settings, component, supporting, manifests, catalog, changes, kind, input)
	if err != nil {
		return componentRequestPlan{}, err
	}
	if feasible {
		return componentRequestPlan{normal: true, requests: []plannedRequest{planned}}, nil
	}

	groups := make([][]sharedmodel.SourceFile, 0)
	for _, file := range component.TriggeringFiles {
		if file.Size > input.ComponentPolicy.MaxBatchBytes {
			return componentRequestPlan{}, fmt.Errorf("component %q file %q exceeds the %d-byte batch limit", component.Key, file.Path, input.ComponentPolicy.MaxBatchBytes)
		}
		if len(groups) == 0 || addInt64Saturated(sourceGroupBytes(groups[len(groups)-1]), file.Size) > input.ComponentPolicy.MaxBatchBytes {
			groups = append(groups, []sharedmodel.SourceFile{file})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], file)
		}
	}
	if len(groups) < 2 {
		return componentRequestPlan{}, fmt.Errorf("component %q request exceeds the %d-byte request limit and cannot be split further", component.Key, input.ComponentPolicy.MaxRequestBytes)
	}
	synthesisBytes := synthesisWorstCaseBytes(bundle, settings, component, catalog, len(groups), input.GenerationPolicy.MaxResponseBytes)
	if synthesisBytes == int64(^uint64(0)>>1) {
		return componentRequestPlan{}, fmt.Errorf("component %q worst-case synthesis request estimate exceeds integer capacity", component.Key)
	}
	if synthesisBytes > input.ComponentPolicy.MaxRequestBytes {
		return componentRequestPlan{}, fmt.Errorf("component %q requires %d batches whose worst-case synthesis request is %d bytes, exceeding the %d-byte request limit", component.Key, len(groups), synthesisBytes, input.ComponentPolicy.MaxRequestBytes)
	}
	requests := make([]plannedRequest, 0, len(groups))
	for index, group := range groups {
		request, err := buildComponentRequest(bundle, settings, component, group, supporting, manifests, catalog, changes, kind, index+1, len(groups))
		if err != nil {
			return componentRequestPlan{}, err
		}
		if requestContentBytes(request) > input.ComponentPolicy.MaxRequestBytes {
			return componentRequestPlan{}, fmt.Errorf("component %q batch %d request is %d bytes, exceeding the %d-byte request limit", component.Key, index+1, requestContentBytes(request), input.ComponentPolicy.MaxRequestBytes)
		}
		requests = append(requests, plannedRequest{request: request, source: group})
	}
	return componentRequestPlan{normal: false, requests: requests, synthesisBytes: synthesisBytes}, nil
}

// planComponentFastPath decides whether one full-dossier request fits without
// constructing the legacy batch/synthesis path. Auto mode uses the same primitive.
func planComponentFastPath(bundle prompt.Bundle, settings sharedmodel.GenerationSettings, component sharedmodel.Component, supporting, manifests []sharedmodel.SourceFile, catalog []string, changes []sharedmodel.Change, kind string, input documentationmodel.PlanInput) (plannedRequest, bool, error) {
	var sourceBytes int64
	for _, file := range component.TriggeringFiles {
		sourceBytes = addInt64Saturated(sourceBytes, file.Size)
	}
	if sourceBytes > input.ComponentPolicy.MaxContextBytes {
		return plannedRequest{}, false, nil
	}
	request, err := buildComponentRequest(bundle, settings, component, component.TriggeringFiles, supporting, manifests, catalog, changes, kind, 1, 1)
	if err != nil {
		return plannedRequest{}, false, err
	}
	if requestContentBytes(request) > input.ComponentPolicy.MaxRequestBytes {
		return plannedRequest{}, false, nil
	}
	return plannedRequest{request: request, source: component.TriggeringFiles}, true, nil
}

func planFragmentGeneration(
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	input documentationmodel.PlanInput,
) (fragmentComponentPlan, error) {
	if len(component.TriggeringFiles) == 0 {
		return fragmentComponentPlan{}, fmt.Errorf("component %q has no triggering source for fragment generation", component.Key)
	}
	if err := validateFragmentProfile(settings.MaxOutputTokens, input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes); err != nil {
		return fragmentComponentPlan{}, err
	}
	batches, err := initialFragmentSourceBatches(component.TriggeringFiles, input.ComponentPolicy.MaxBatchBytes)
	if err != nil {
		return fragmentComponentPlan{}, fmt.Errorf("component %q: %w", component.Key, err)
	}

	var plan fragmentComponentPlan
	for {
		plan.sourceScopes = flattenFragmentSourceScopes(batches, supporting, manifests)
		primaryCalls := len(plan.sourceScopes)*(len(requiredFragmentKinds())+1) + 2
		plan.maximumCalls = primaryCalls * 2
		if input.GenerationPolicy.FragmentCallLimit < plan.maximumCalls {
			return fragmentComponentPlan{}, fmt.Errorf("component %q fragment generation requires at most %d logical calls, exceeding the %d-call limit", component.Key, plan.maximumCalls, input.GenerationPolicy.FragmentCallLimit)
		}
		plan.mapRequests, err = buildFragmentMapRequests(bundle, settings, component, supporting, manifests, catalog, changes, changeKind, plan.sourceScopes, input)
		if err == nil {
			break
		}
		var sizeErr fragmentPlanSizeError
		if !errorsAsFragmentPlanSize(err, &sizeErr) {
			return fragmentComponentPlan{}, err
		}
		if err := refineFragmentSourceBatch(&batches, sizeErr.batchIndex, sizeErr.chunkIndex); err != nil {
			return fragmentComponentPlan{}, fmt.Errorf("component %q fragment request exceeds the %d-byte request limit and cannot be split at a newline: %w", component.Key, input.ComponentPolicy.MaxRequestBytes, err)
		}
	}

	plan.overviewRequestMax, plan.overviewRepairMax, plan.diagramRequestMax, plan.diagramRepairMax, err =
		validateReducerRequestBounds(bundle, settings, len(plan.sourceScopes), input)
	if err != nil {
		return fragmentComponentPlan{}, fmt.Errorf("component %q: %w", component.Key, err)
	}
	return plan, nil
}

func fragmentMapKinds() []sharedmodel.FragmentKind {
	return append([]sharedmodel.FragmentKind{sharedmodel.FragmentOverviewCandidate}, requiredFragmentKinds()...)
}

func buildFragmentMapRequests(
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	scopes []fragmentSourceScope,
	input documentationmodel.PlanInput,
) ([]plannedFragmentRequest, error) {
	requests := make([]plannedFragmentRequest, 0, len(scopes)*len(fragmentMapKinds()))
	for _, sourceScope := range scopes {
		for _, fragmentKind := range fragmentMapKinds() {
			request, err := buildFragmentRequestUnchecked(bundle, settings, component, fragmentKind, sourceScope.source, supporting, manifests,
				catalog, changes, changeKind, sourceScope.batchIndex, sourceScope.batchCount, sourceScope.chunkIndex, sourceScope.chunkCount)
			if err != nil {
				return nil, err
			}
			if err := validateFragmentScope(component, sourceScope.source, supporting, manifests, catalog, sourceScope.batchIndex, sourceScope.batchCount, sourceScope.chunkIndex, sourceScope.chunkCount); err != nil {
				return nil, err
			}
			if size := requestContentBytes(request); size > input.ComponentPolicy.MaxRequestBytes {
				return nil, fragmentPlanSizeError{batchIndex: sourceScope.batchIndex, chunkIndex: sourceScope.chunkIndex, size: size}
			}
			repairBytes, err := fragmentRepairWorstCaseForRequest(bundle, request)
			if err != nil {
				return nil, err
			}
			if repairBytes > input.ComponentPolicy.MaxRequestBytes {
				return nil, fmt.Errorf("component %q %s fragment worst-case repair request is %d bytes, exceeding the %d-byte request limit", component.Key, fragmentKind, repairBytes, input.ComponentPolicy.MaxRequestBytes)
			}
			scope := sourceScope.coverageScope(component, fragmentKind)
			request.SourceSplitPath = scope.SplitPath
			requests = append(requests, plannedFragmentRequest{
				request: request, scope: scope, source: append([]sharedmodel.SourceFile(nil), sourceScope.source...),
				repairRequestBytes: repairBytes,
			})
		}
	}
	return requests, nil
}

func initialFragmentSourceBatches(files []sharedmodel.SourceFile, maxBatchBytes int64) ([]fragmentSourceBatch, error) {
	batches := make([]fragmentSourceBatch, 0)
	regular := make([]sharedmodel.SourceFile, 0)
	var regularBytes int64
	flush := func() {
		if len(regular) == 0 {
			return
		}
		batches = append(batches, fragmentSourceBatch{chunks: [][]sharedmodel.SourceFile{regular}})
		regular = nil
		regularBytes = 0
	}
	for _, file := range files {
		if file.Size > maxBatchBytes {
			flush()
			chunks, err := splitSourceFileByLimit(file, maxBatchBytes)
			if err != nil {
				return nil, err
			}
			chunkGroups := make([][]sharedmodel.SourceFile, len(chunks))
			for index := range chunks {
				chunkGroups[index] = []sharedmodel.SourceFile{chunks[index]}
			}
			batches = append(batches, fragmentSourceBatch{chunks: chunkGroups})
			continue
		}
		if len(regular) > 0 && addInt64Saturated(regularBytes, file.Size) > maxBatchBytes {
			flush()
		}
		regular = append(regular, file)
		regularBytes = addInt64Saturated(regularBytes, file.Size)
	}
	flush()
	return batches, nil
}

func flattenFragmentSourceScopes(batches []fragmentSourceBatch, supporting, manifests []sharedmodel.SourceFile) []fragmentSourceScope {
	result := make([]fragmentSourceScope, 0)
	for batchIndex, batch := range batches {
		for chunkIndex, source := range batch.chunks {
			result = append(result, fragmentSourceScope{
				source: append([]sharedmodel.SourceFile(nil), source...), batchIndex: batchIndex + 1, batchCount: len(batches),
				chunkIndex: chunkIndex + 1, chunkCount: len(batch.chunks), splitPath: fmt.Sprintf("b%d/c%d", batchIndex+1, chunkIndex+1),
				allowedEvidence: allowedEvidencePaths(source, supporting, manifests),
			})
		}
	}
	return result
}

func refineFragmentSourceBatch(batches *[]fragmentSourceBatch, batchIndex, chunkIndex int) error {
	if batchIndex < 1 || batchIndex > len(*batches) {
		return fmt.Errorf("invalid batch index")
	}
	batch := (*batches)[batchIndex-1]
	if chunkIndex < 1 || chunkIndex > len(batch.chunks) {
		return fmt.Errorf("invalid chunk index")
	}
	source := batch.chunks[chunkIndex-1]
	if len(source) > 1 {
		midpoint := len(source) / 2
		left := fragmentSourceBatch{chunks: [][]sharedmodel.SourceFile{append([]sharedmodel.SourceFile(nil), source[:midpoint]...)}}
		right := fragmentSourceBatch{chunks: [][]sharedmodel.SourceFile{append([]sharedmodel.SourceFile(nil), source[midpoint:]...)}}
		replacement := []fragmentSourceBatch{left, right}
		updated := append([]fragmentSourceBatch(nil), (*batches)[:batchIndex-1]...)
		updated = append(updated, replacement...)
		updated = append(updated, (*batches)[batchIndex:]...)
		*batches = updated
		return nil
	}
	if len(source) != 1 {
		return fmt.Errorf("empty source scope")
	}
	left, right, err := splitSourceFileAtNewline(source[0])
	if err != nil {
		return err
	}
	chunks := make([][]sharedmodel.SourceFile, 0, len(batch.chunks)+1)
	chunks = append(chunks, batch.chunks[:chunkIndex-1]...)
	chunks = append(chunks, []sharedmodel.SourceFile{left}, []sharedmodel.SourceFile{right})
	chunks = append(chunks, batch.chunks[chunkIndex:]...)
	(*batches)[batchIndex-1].chunks = chunks
	return nil
}

func splitSourceFileByLimit(file sharedmodel.SourceFile, limit int64) ([]sharedmodel.SourceFile, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("fragment batch limit must be positive")
	}
	if int64(len(file.Data)) <= limit {
		return []sharedmodel.SourceFile{file}, nil
	}
	chunks := make([]sharedmodel.SourceFile, 0, divideRoundUp(int64(len(file.Data)), limit))
	for start := 0; start < len(file.Data); {
		end := start + int(limit)
		if end >= len(file.Data) {
			end = len(file.Data)
		} else {
			newline := bytes.LastIndexByte(file.Data[start:end], '\n')
			if newline < 0 {
				return nil, fmt.Errorf("file %q has no newline within the %d-byte fragment batch limit", file.Path, limit)
			}
			end = start + newline + 1
		}
		if end <= start {
			return nil, fmt.Errorf("file %q cannot be split into non-empty newline chunks", file.Path)
		}
		chunks = append(chunks, sourceFileChunk(file, file.Data[start:end]))
		start = end
	}
	return chunks, nil
}

func splitSourceFileAtNewline(file sharedmodel.SourceFile) (sharedmodel.SourceFile, sharedmodel.SourceFile, error) {
	if len(file.Data) < 2 {
		return sharedmodel.SourceFile{}, sharedmodel.SourceFile{}, fmt.Errorf("file %q has reached the minimum chunk size", file.Path)
	}
	midpoint := len(file.Data) / 2
	boundary := -1
	if before := bytes.LastIndexByte(file.Data[:midpoint], '\n'); before >= 0 {
		boundary = before + 1
	} else if after := bytes.IndexByte(file.Data[midpoint:], '\n'); after >= 0 {
		boundary = midpoint + after + 1
	}
	if boundary <= 0 || boundary >= len(file.Data) {
		return sharedmodel.SourceFile{}, sharedmodel.SourceFile{}, fmt.Errorf("file %q has no internal newline boundary", file.Path)
	}
	return sourceFileChunk(file, file.Data[:boundary]), sourceFileChunk(file, file.Data[boundary:]), nil
}

func sourceFileChunk(file sharedmodel.SourceFile, data []byte) sharedmodel.SourceFile {
	chunk := file
	chunk.Data = append([]byte(nil), data...)
	chunk.Size = int64(len(data))
	return chunk
}

func errorsAsFragmentPlanSize(err error, target *fragmentPlanSizeError) bool {
	value, ok := err.(fragmentPlanSizeError)
	if ok {
		*target = value
	}
	return ok
}

const reducerProjectionItems = 8

const runtimeFragmentOverlapBytes = 1024

// splitRuntimeFragmentSource narrows one failed runtime scope. Multi-file scopes
// split at a stable midpoint. Single-file scopes overlap by one bounded source line
// when possible, falling back to the newline delimiter as the minimum overlap.
func splitRuntimeFragmentSource(source []sharedmodel.SourceFile) ([][]sharedmodel.SourceFile, bool) {
	if len(source) > 1 {
		midpoint := len(source) / 2
		return [][]sharedmodel.SourceFile{
			append([]sharedmodel.SourceFile(nil), source[:midpoint]...),
			append([]sharedmodel.SourceFile(nil), source[midpoint:]...),
		}, true
	}
	if len(source) != 1 {
		return nil, false
	}
	left, right, ok := splitSourceFileAtNewlineWithOverlap(source[0])
	if !ok {
		return nil, false
	}
	return [][]sharedmodel.SourceFile{{left}, {right}}, true
}

func splitSourceFileAtNewlineWithOverlap(file sharedmodel.SourceFile) (sharedmodel.SourceFile, sharedmodel.SourceFile, bool) {
	left, _, err := splitSourceFileAtNewline(file)
	if err != nil {
		return sharedmodel.SourceFile{}, sharedmodel.SourceFile{}, false
	}
	boundary := len(left.Data)
	overlapStart := boundary - 1 // The split newline is always a safe one-byte overlap.
	if previous := bytes.LastIndexByte(file.Data[:boundary-1], '\n'); previous >= 0 {
		lineStart := previous + 1
		if lineStart > 0 && boundary-lineStart <= runtimeFragmentOverlapBytes {
			overlapStart = lineStart
		}
	}
	if overlapStart <= 0 || overlapStart >= boundary {
		return sharedmodel.SourceFile{}, sharedmodel.SourceFile{}, false
	}
	right := sourceFileChunk(file, file.Data[overlapStart:])
	if len(left.Data) >= len(file.Data) || len(right.Data) >= len(file.Data) {
		return sharedmodel.SourceFile{}, sharedmodel.SourceFile{}, false
	}
	return left, right, true
}

func validateReducerRequestBounds(bundle prompt.FragmentBundle, settings sharedmodel.GenerationSettings, sourceScopeCount int, input documentationmodel.PlanInput) (int64, int64, int64, int64, error) {
	component := sharedmodel.Component{Key: maximumJSONExpansionString(fragmentMaxPath)}
	candidates := make([]sharedmodel.OverviewCandidate, sourceScopeCount)
	for index := range candidates {
		candidates[index] = sharedmodel.OverviewCandidate{Title: repeated(fragmentMaxTitle), Purpose: repeated(fragmentMaxLongText), SourcePaths: maximumDistinctPaths(fragmentMaxSourcePaths)}
	}
	sections := make([]overviewSectionProjection, 0, len(requiredFragmentKinds()))
	for _, kind := range requiredFragmentKinds() {
		sections = append(sections, overviewSectionProjection{Kind: kind, Count: assembledSectionLimit(kind), Names: maximumDistinctStrings(reducerProjectionItems, fragmentMaxName)})
	}
	maximumEvidence := sourceScopeCount * fragmentMaxSourcePaths * (1 + fragmentMaxArchitectureItems + fragmentMaxInterfaceItems +
		fragmentMaxDataModelItems + fragmentMaxWorkflowItems + fragmentMaxDependencyItems + fragmentMaxReviewGapItems)
	overview, err := buildOverviewReducerRequest(bundle, settings, component, candidates, maximumDistinctPaths(maximumEvidence), sections,
		input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	overviewRepair, err := fragmentRepairWorstCaseForRequest(bundle, overview)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	diagramProjection := maximumDiagramProjectionInput()
	diagram, err := buildDiagramReducerRequest(bundle, settings, component, diagramProjection, maximumDistinctPaths(reducerProjectionItems*fragmentMaxSourcePaths*3),
		input.GenerationPolicy.MaxResponseBytes, input.ComponentPolicy.MaxRequestBytes)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	diagramRepair, err := fragmentRepairWorstCaseForRequest(bundle, diagram)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return requestContentBytes(overview), overviewRepair, requestContentBytes(diagram), diagramRepair, nil
}

func assembledSectionLimit(kind sharedmodel.FragmentKind) int {
	switch kind {
	case sharedmodel.FragmentArchitecture:
		return assembledMaxArchitectureItems
	case sharedmodel.FragmentInterfaces:
		return assembledMaxInterfaceItems
	case sharedmodel.FragmentDataModels:
		return assembledMaxDataModelItems
	case sharedmodel.FragmentWorkflows:
		return assembledMaxWorkflowItems
	case sharedmodel.FragmentDependencies:
		return assembledMaxDependencyItems
	case sharedmodel.FragmentReviewGaps:
		return assembledMaxReviewGapItems
	default:
		return 0
	}
}

func maximumDistinctPaths(count int) []string {
	result := make([]string, count)
	for index := range result {
		prefix := strconv.Itoa(index) + ":"
		result[index] = prefix + maximumJSONExpansionString(fragmentMaxPath-len(prefix))
	}
	return result
}

func maximumDistinctStrings(count, length int) []string {
	result := make([]string, count)
	for index := range result {
		prefix := strconv.Itoa(index) + ":"
		result[index] = prefix + repeated(length-len(prefix))
	}
	return result
}

func maximumDiagramProjectionInput() diagramProjection {
	projection := diagramProjection{}
	for index := 0; index < reducerProjectionItems; index++ {
		paths := maximumDistinctPaths(fragmentMaxSourcePaths)
		projection.Architecture = append(projection.Architecture, diagramArchitectureProjection{Title: repeated(fragmentMaxTitle), SourcePaths: paths})
		relationships := make([]sharedmodel.DataRelationship, fragmentMaxRelationships)
		for relationship := range relationships {
			relationships[relationship] = sharedmodel.DataRelationship{Target: repeated(fragmentMaxName), Kind: "implements", Description: repeated(fragmentMaxShortText)}
		}
		projection.DataModels = append(projection.DataModels, diagramDataModelProjection{Name: repeated(fragmentMaxName), Relationships: relationships, SourcePaths: paths})
		steps := make([]sharedmodel.WorkflowStep, fragmentMaxSteps)
		for step := range steps {
			steps[step] = sharedmodel.WorkflowStep{Actor: repeated(fragmentMaxName), Action: repeated(fragmentMaxShortText), Target: repeated(fragmentMaxName)}
		}
		projection.Workflows = append(projection.Workflows, diagramWorkflowProjection{Name: repeated(fragmentMaxName), Steps: steps, SourcePaths: paths})
	}
	return projection
}

// batchEstimate derives the reportable estimate for one planned request. The request
// bytes are measured from the exact request the generator will rebuild.
func batchEstimate(planned plannedRequest) sharedmodel.ComponentBatch {
	requestBytes := requestContentBytes(planned.request)
	return sharedmodel.ComponentBatch{
		Index: planned.request.BatchIndex, Count: planned.request.BatchCount,
		SourcePaths: sourcePaths(planned.source), SourceBytes: sourceGroupBytes(planned.source),
		RequestBytes: requestBytes, ConservativeTokens: requestBytes, TypicalTokens: divideRoundUp(requestBytes, 4),
	}
}

func fragmentBatchEstimate(scope fragmentSourceScope, requests []plannedFragmentRequest) sharedmodel.ComponentBatch {
	var requestBytes int64
	for _, planned := range requests {
		if planned.scope.SourceBatchIndex == scope.batchIndex && planned.scope.SourceChunkIndex == scope.chunkIndex {
			requestBytes = addInt64Saturated(requestBytes, requestContentBytes(planned.request))
		}
	}
	return sharedmodel.ComponentBatch{
		Index: scope.batchIndex, Count: scope.batchCount, ChunkIndex: scope.chunkIndex, ChunkCount: scope.chunkCount,
		SourcePaths: sourcePaths(scope.source), SourceBytes: sourceGroupBytes(scope.source), RequestBytes: requestBytes,
		ConservativeTokens: requestBytes, TypicalTokens: divideRoundUp(requestBytes, 4),
	}
}

func generationPromptContentHash(strategy string) string {
	switch strategy {
	case "fragments":
		return prompt.CodebaseSummaryV2().ContentHash()
	case "auto":
		content := prompt.CodebaseSummaryV1().ContentHash() + "\x00" + prompt.CodebaseSummaryV2().ContentHash()
		digest := sha256.Sum256([]byte(content))
		return fmt.Sprintf("sha256:%x", digest)
	default:
		return prompt.CodebaseSummaryV1().ContentHash()
	}
}

// synthesisWorstCaseBytes bounds the synthesis request before any paid call: encoded
// system and synthesis messages, the trusted prompt-JSON schema instruction, fixed
// envelope headroom, and one response-ceiling-sized dossier per batch. HTML escaping is
// disabled, and a 3x factor conservatively covers nested quote/backslash escaping plus
// Go's mandatory U+2028/U+2029 escaping.
func synthesisWorstCaseBytes(bundle prompt.Bundle, settings sharedmodel.GenerationSettings, component sharedmodel.Component, catalog []string, batchCount int, maxResponseBytes int64) int64 {
	skeleton := synthesisPayload{
		Task:          "component_synthesis",
		PromptVersion: bundle.Identifier(),
		Target:        planningTarget{ComponentKey: component.Key, BatchCount: batchCount},
		Repository:    planningRepository{ComponentKeys: catalog, TargetSourcePaths: sourcePaths(component.TriggeringFiles)},
		Limits:        defaultLimits(),
	}
	envelope, _ := marshalRequestJSON(skeleton)
	request := sharedmodel.GenerationRequest{
		Kind: sharedmodel.RequestSynthesis, ComponentKey: component.Key, BatchCount: batchCount,
		PromptVersion: bundle.Identifier(), SchemaName: "component_dossier", Schema: bundle.Schema(), Settings: settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: bundle.Synthesis() + "\n\n" + string(envelope)},
		},
	}
	responseBytes := multiplyInt64Saturated(int64(batchCount), responseJSONExpansionFactor)
	responseBytes = multiplyInt64Saturated(responseBytes, maxResponseBytes)
	return addInt64Saturated(requestContentBytes(request), responseBytes)
}

func sourcePaths(files []sharedmodel.SourceFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func sourceGroupBytes(files []sharedmodel.SourceFile) int64 {
	var total int64
	for _, file := range files {
		total = addInt64Saturated(total, file.Size)
	}
	return total
}

func sortedReasonSet(reasons map[string]struct{}) []string {
	result := make([]string, 0, len(reasons))
	for reason := range reasons {
		result = append(result, reason)
	}
	sort.Strings(result)
	return result
}

func divideRoundUp(value, divisor int64) int64 {
	if value == 0 {
		return 0
	}
	quotient := value / divisor
	if value%divisor != 0 {
		quotient++
	}
	return quotient
}
