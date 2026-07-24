package usecase

import (
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
	stateSchemaVersion        = 1
	generatorVersion          = "0.1.0"
	plannerVersion            = "v1"
	promptVersion             = "v1"
	outputSchemaVersion       = "v1"
	inputHashVersion          = "v1"
	schemaMaximumDossierBytes = 32 << 10
	synthesisEnvelopeBytes    = 1024
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
	for _, identity := range identities {
		ref := refs[identity]
		component, existsNow := currentComponents[identity]
		oldComponent, existedBefore := oldComponents[identity]
		reasons := sortedReasonSet(selected[identity])
		affectedComponent := sharedmodel.AffectedComponent{
			Key:           ref.key,
			RootComponent: ref.root,
			Document:      ref.document,
			Reasons:       reasons,
			ExistedBefore: existedBefore,
			ExistsNow:     existsNow,
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
		requestPlan, err := planComponentGeneration(bundle, settings, component, supporting, manifests, catalog, relevantChanges, kind, input)
		if err != nil {
			return sharedmodel.GenerationPlan{}, err
		}
		batches := make([]sharedmodel.ComponentBatch, 0, len(requestPlan.requests))
		for _, planned := range requestPlan.requests {
			batches = append(batches, batchEstimate(planned))
		}
		affectedComponent.Batches = batches
		affectedComponent.SynthesisRequestBytes = requestPlan.synthesisBytes
		if requestPlan.normal {
			calls.Normal++
		} else {
			calls.Batch += len(batches)
			calls.Synthesis++
		}
		for _, batch := range batches {
			calls.RequestBytes += batch.RequestBytes
		}
		calls.RequestBytes += requestPlan.synthesisBytes
		affected = append(affected, affectedComponent)
	}
	sort.Strings(deletedDocuments)
	calls.Primary = calls.Normal + calls.Batch + calls.Synthesis
	calls.MaximumRepair = calls.Primary
	if input.GenerationPolicy.StructuredOutputMode == "auto" {
		calls.MaximumTransportFallback = calls.Primary
	}
	calls.ConservativeTokens = calls.RequestBytes
	calls.TypicalTokens = divideRoundUp(calls.RequestBytes, 4)

	mode := "incremental"
	if fullReason != "" {
		mode = "full"
	}
	return sharedmodel.GenerationPlan{
		Mode:               mode,
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

func stateCompatibility(result sharedmodel.StateLoadResult) (string, bool) {
	if result.Missing {
		return "missing", false
	}
	state := result.State
	if state.SchemaVersion != stateSchemaVersion || state.PlannerVersion != plannerVersion || state.PromptVersion != promptVersion ||
		state.OutputSchemaVersion != "" && state.OutputSchemaVersion != outputSchemaVersion {
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
	if policy.Provider != "openai-compatible" {
		return fmt.Errorf("unsupported LLM provider %q", policy.Provider)
	}
	if policy.APIMode != "chat_completions" && policy.APIMode != "responses" {
		return fmt.Errorf("unsupported LLM API mode %q", policy.APIMode)
	}
	if policy.Temperature < 0 || policy.Temperature > 2 || policy.MaxOutputTokens <= 0 {
		return fmt.Errorf("invalid LLM generation limits")
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
	generationHash, err := hashJSON(struct {
		PromptVersion       string                              `json:"prompt_version"`
		PromptContentHash   string                              `json:"prompt_content_hash"`
		OutputSchemaVersion string                              `json:"output_schema_version"`
		Policy              documentationmodel.GenerationPolicy `json:"policy"`
	}{promptVersion, prompt.CodebaseSummaryV1().ContentHash(), outputSchemaVersion, input.GenerationPolicy})
	if err != nil {
		return sharedmodel.StateConfigHashes{}, "", err
	}
	hashes := sharedmodel.StateConfigHashes{
		Paths: pathsHash, Source: sourceHash, Components: componentsHash, Context: contextHash, Generation: generationHash,
	}
	aggregate, err := hashJSON(hashes)
	return hashes, aggregate, err
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
		if selectedBytes+file.Size > limit {
			continue
		}
		selected = append(selected, file)
		selectedBytes += file.Size
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
		summary.TriggeringBytes += file.Size
	}
	for _, file := range supporting {
		summary.SupportingPaths = append(summary.SupportingPaths, file.Path)
		summary.SupportingBytes += file.Size
	}
	for _, file := range manifests {
		summary.ManifestPaths = append(summary.ManifestPaths, file.Path)
		summary.ManifestBytes += file.Size
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
	writeHashField(digest, plannerVersion)
	writeHashField(digest, promptVersion)
	writeHashField(digest, outputSchemaVersion)
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

// planComponentGeneration is the single source of truth for how a component is split
// into requests. It is used by the planner to estimate calls and by generation to send
// them, so a component that plans as normal always generates as normal.
func planComponentGeneration(bundle prompt.Bundle, settings sharedmodel.GenerationSettings, component sharedmodel.Component, supporting, manifests []sharedmodel.SourceFile, catalog []string, changes []sharedmodel.Change, kind string, input documentationmodel.PlanInput) (componentRequestPlan, error) {
	var sourceBytes int64
	for _, file := range component.TriggeringFiles {
		sourceBytes += file.Size
	}
	if sourceBytes <= input.ComponentPolicy.MaxContextBytes {
		request, err := buildComponentRequest(bundle, settings, component, component.TriggeringFiles, supporting, manifests, catalog, changes, kind, 1, 1)
		if err != nil {
			return componentRequestPlan{}, err
		}
		if requestContentBytes(request) <= input.ComponentPolicy.MaxRequestBytes {
			return componentRequestPlan{normal: true, requests: []plannedRequest{{request: request, source: component.TriggeringFiles}}}, nil
		}
	}

	groups := make([][]sharedmodel.SourceFile, 0)
	for _, file := range component.TriggeringFiles {
		if file.Size > input.ComponentPolicy.MaxBatchBytes {
			return componentRequestPlan{}, fmt.Errorf("component %q file %q exceeds the %d-byte batch limit", component.Key, file.Path, input.ComponentPolicy.MaxBatchBytes)
		}
		if len(groups) == 0 || sourceGroupBytes(groups[len(groups)-1])+file.Size > input.ComponentPolicy.MaxBatchBytes {
			groups = append(groups, []sharedmodel.SourceFile{file})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], file)
		}
	}
	if len(groups) < 2 {
		return componentRequestPlan{}, fmt.Errorf("component %q request exceeds the %d-byte request limit and cannot be split further", component.Key, input.ComponentPolicy.MaxRequestBytes)
	}
	synthesisBytes := synthesisWorstCaseBytes(bundle, component, catalog, len(groups))
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

// synthesisWorstCaseBytes bounds the synthesis request before any paid call: system
// prompt, synthesis instructions, schema, the fixed catalogs envelope, and one
// maximum-size dossier per batch. It is a safe upper bound, not the eventual size.
func synthesisWorstCaseBytes(bundle prompt.Bundle, component sharedmodel.Component, catalog []string, batchCount int) int64 {
	skeleton := synthesisPayload{
		Task:          "component_synthesis",
		PromptVersion: bundle.Identifier(),
		Target:        planningTarget{ComponentKey: component.Key, BatchCount: batchCount},
		Repository:    planningRepository{ComponentKeys: catalog, TargetSourcePaths: sourcePaths(component.TriggeringFiles)},
		Limits:        defaultLimits(),
	}
	envelope, _ := json.Marshal(skeleton)
	base := int64(len(bundle.System()) + len(bundle.Synthesis()) + len(bundle.Schema()) + len(envelope))
	return base + int64(synthesisEnvelopeBytes) + int64(batchCount)*int64(schemaMaximumDossierBytes)
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
		total += file.Size
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
	return (value + divisor - 1) / divisor
}
