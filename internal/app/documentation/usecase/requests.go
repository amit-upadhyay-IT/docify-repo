package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

// requestSourceFile is one allow-listed file as sent to the model. Only path, role,
// and content are exposed; source hashes and sizes stay local.
type requestSourceFile struct {
	Path    string                 `json:"path"`
	Role    sharedmodel.SourceRole `json:"role"`
	Content string                 `json:"content"`
}

type planningTarget struct {
	ComponentKey string `json:"component_key"`
	BatchIndex   int    `json:"batch_index"`
	BatchCount   int    `json:"batch_count"`
}

type planningRepository struct {
	ComponentKeys        []string `json:"component_keys"`
	TargetSourcePaths    []string `json:"target_source_paths"`
	AllowedEvidencePaths []string `json:"allowed_evidence_paths"`
}

type changeContext struct {
	Kind          string               `json:"kind"`
	Changes       []sharedmodel.Change `json:"changes"`
	Diff          string               `json:"diff"`
	DiffTruncated bool                 `json:"diff_truncated"`
}

type payloadLimits struct {
	MaximumItemsPerSection int `json:"maximum_items_per_section"`
	MaximumDiagrams        int `json:"maximum_diagrams"`
}

// componentPayload is the untrusted JSON data embedded in a component or batch request.
type componentPayload struct {
	Task          string              `json:"task"`
	PromptVersion string              `json:"prompt_version"`
	Target        planningTarget      `json:"target"`
	Repository    planningRepository  `json:"repository"`
	Manifests     []requestSourceFile `json:"manifests"`
	SourceFiles   []requestSourceFile `json:"source_files"`
	Supporting    []requestSourceFile `json:"supporting_files"`
	ChangeContext changeContext       `json:"change_context"`
	Limits        payloadLimits       `json:"limits"`
}

// synthesisPayload is the untrusted JSON data embedded in a synthesis request. It
// contains only already-validated batch dossiers and deterministic catalogs, never
// original source.
type synthesisPayload struct {
	Task          string                         `json:"task"`
	PromptVersion string                         `json:"prompt_version"`
	Target        planningTarget                 `json:"target"`
	Repository    planningRepository             `json:"repository"`
	BatchDossiers []sharedmodel.ComponentDossier `json:"batch_dossiers"`
	Limits        payloadLimits                  `json:"limits"`
}

// repairPayload is the untrusted JSON data embedded in a repair request. It contains
// the fixed target, the bounded invalid response, and machine-generated issues only.
type repairPayload struct {
	Task             string                        `json:"task"`
	PromptVersion    string                        `json:"prompt_version"`
	OriginalKind     sharedmodel.RequestKind       `json:"original_kind"`
	Target           planningTarget                `json:"target"`
	InvalidResponse  json.RawMessage               `json:"invalid_response"`
	ValidationIssues []sharedmodel.ValidationIssue `json:"validation_issues"`
	Limits           payloadLimits                 `json:"limits"`
}

type fragmentTarget struct {
	ComponentKey     string                   `json:"component_key"`
	FragmentKind     sharedmodel.FragmentKind `json:"fragment_kind"`
	SourceBatchIndex int                      `json:"source_batch_index"`
	SourceBatchCount int                      `json:"source_batch_count"`
	SourceChunkIndex int                      `json:"source_chunk_index"`
	SourceChunkCount int                      `json:"source_chunk_count"`
}

type fragmentPayloadLimits struct {
	MaximumItems          int `json:"maximum_items,omitempty"`
	MaximumSourcePaths    int `json:"maximum_source_paths"`
	MaximumTitleBytes     int `json:"maximum_title_bytes"`
	MaximumNameBytes      int `json:"maximum_name_bytes"`
	MaximumShortTextBytes int `json:"maximum_short_text_bytes"`
	MaximumLongTextBytes  int `json:"maximum_long_text_bytes"`
	MaximumPathBytes      int `json:"maximum_path_bytes"`
	MaximumFields         int `json:"maximum_fields,omitempty"`
	MaximumRelationships  int `json:"maximum_relationships,omitempty"`
	MaximumSteps          int `json:"maximum_steps,omitempty"`
}

type fragmentPayload struct {
	Task          string                `json:"task"`
	PromptVersion string                `json:"prompt_version"`
	Target        fragmentTarget        `json:"target"`
	Repository    planningRepository    `json:"repository"`
	Manifests     []requestSourceFile   `json:"manifests"`
	SourceFiles   []requestSourceFile   `json:"source_files"`
	Supporting    []requestSourceFile   `json:"supporting_files"`
	ChangeContext changeContext         `json:"change_context"`
	Limits        fragmentPayloadLimits `json:"limits"`
}

type fragmentRepairPayload struct {
	Task             string                        `json:"task"`
	PromptVersion    string                        `json:"prompt_version"`
	Target           boundedRepairTarget           `json:"target"`
	InvalidResponse  json.RawMessage               `json:"invalid_response"`
	ValidationIssues []sharedmodel.ValidationIssue `json:"validation_issues"`
	Limits           fragmentPayloadLimits         `json:"limits"`
}

type boundedRepairTarget struct {
	ComponentKey     string                   `json:"component_key"`
	RequestKind      sharedmodel.RequestKind  `json:"request_kind"`
	FragmentKind     sharedmodel.FragmentKind `json:"fragment_kind,omitempty"`
	SourceBatchIndex int                      `json:"source_batch_index,omitempty"`
	SourceBatchCount int                      `json:"source_batch_count,omitempty"`
	SourceChunkIndex int                      `json:"source_chunk_index,omitempty"`
	SourceChunkCount int                      `json:"source_chunk_count,omitempty"`
}

type reducerTarget struct {
	ComponentKey  string `json:"component_key"`
	RootComponent bool   `json:"root_component"`
}

type overviewSectionProjection struct {
	Kind  sharedmodel.FragmentKind `json:"kind"`
	Count int                      `json:"count"`
	Names []string                 `json:"names"`
}

type overviewReducerPayload struct {
	Task          string                          `json:"task"`
	PromptVersion string                          `json:"prompt_version"`
	Target        reducerTarget                   `json:"target"`
	Candidates    []sharedmodel.OverviewCandidate `json:"overview_candidates"`
	EvidencePaths []string                        `json:"evidence_paths"`
	Sections      []overviewSectionProjection     `json:"sections"`
	Limits        fragmentPayloadLimits           `json:"limits"`
}

type diagramArchitectureProjection struct {
	Title       string   `json:"title"`
	SourcePaths []string `json:"source_paths"`
}

type diagramDataModelProjection struct {
	Name          string                         `json:"name"`
	Relationships []sharedmodel.DataRelationship `json:"relationships"`
	SourcePaths   []string                       `json:"source_paths"`
}

type diagramWorkflowProjection struct {
	Name        string                     `json:"name"`
	Steps       []sharedmodel.WorkflowStep `json:"steps"`
	SourcePaths []string                   `json:"source_paths"`
}

type diagramProjection struct {
	Architecture []diagramArchitectureProjection `json:"architecture"`
	DataModels   []diagramDataModelProjection    `json:"data_models"`
	Workflows    []diagramWorkflowProjection     `json:"workflows"`
}

type diagramReducerPayload struct {
	Task          string                `json:"task"`
	PromptVersion string                `json:"prompt_version"`
	Target        reducerTarget         `json:"target"`
	Repository    planningRepository    `json:"repository"`
	Projection    diagramProjection     `json:"validated_projection"`
	Limits        fragmentPayloadLimits `json:"limits"`
}

const providerRequestEnvelopeHeadroom = 1024

type requestPlanningMessage struct {
	Role    sharedmodel.Role `json:"role"`
	Content string           `json:"content"`
}

// requestPlanningEnvelope is provider-neutral but complete: it includes every
// variable request field, message framing, the prompt-JSON schema fallback, and
// fixed headroom for either supported provider API wrapper.
type requestPlanningEnvelope struct {
	Model                string                           `json:"model"`
	Temperature          float64                          `json:"temperature"`
	MaxOutputTokens      int                              `json:"max_output_tokens"`
	APIMode              sharedmodel.APIMode              `json:"api_mode"`
	StructuredOutputMode sharedmodel.StructuredOutputMode `json:"structured_output_mode"`
	SchemaName           string                           `json:"schema_name"`
	Schema               json.RawMessage                  `json:"schema,omitempty"`
	Messages             []requestPlanningMessage         `json:"messages"`
}

func generationSettings(policy documentationmodel.GenerationPolicy) sharedmodel.GenerationSettings {
	return sharedmodel.GenerationSettings{
		Model:                policy.Model,
		Temperature:          policy.Temperature,
		MaxOutputTokens:      policy.MaxOutputTokens,
		APIMode:              sharedmodel.APIMode(policy.APIMode),
		StructuredOutputMode: sharedmodel.StructuredOutputMode(policy.StructuredOutputMode),
	}
}

func defaultLimits() payloadLimits {
	return payloadLimits{MaximumItemsPerSection: schemaMaxItemsPerSection, MaximumDiagrams: schemaMaxDiagrams}
}

func limitsForFragment(kind sharedmodel.FragmentKind) fragmentPayloadLimits {
	return fragmentPayloadLimits{
		MaximumItems: fragmentItemLimit(string(kind)), MaximumSourcePaths: fragmentMaxSourcePaths,
		MaximumTitleBytes: fragmentMaxTitle, MaximumNameBytes: fragmentMaxName,
		MaximumShortTextBytes: fragmentMaxShortText, MaximumLongTextBytes: fragmentMaxLongText,
		MaximumPathBytes: fragmentMaxPath, MaximumFields: fragmentMaxFields,
		MaximumRelationships: fragmentMaxRelationships, MaximumSteps: fragmentMaxSteps,
	}
}

// allowedEvidencePaths returns the sorted, de-duplicated set of paths the model may
// cite for one request: exactly the source, supporting, and manifest files actually
// included, never the broader component catalog.
func allowedEvidencePaths(groups ...[]sharedmodel.SourceFile) []string {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, file := range group {
			seen[file.Path] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func changeKind(changes []sharedmodel.Change, fullReason string) string {
	if fullReason != "" || len(changes) == 0 {
		return "full"
	}
	return "incremental"
}

// buildComponentRequest constructs a deterministic component or batch request. Kind is
// RequestBatch when the component is split, RequestComponent otherwise.
func buildComponentRequest(
	bundle prompt.Bundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	source, supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	kind string,
	index, count int,
) (sharedmodel.GenerationRequest, error) {
	payload := componentPayload{
		Task:          "component_dossier",
		PromptVersion: bundle.Identifier(),
		Target:        planningTarget{ComponentKey: component.Key, BatchIndex: index, BatchCount: count},
		Repository: planningRepository{
			ComponentKeys:        catalog,
			TargetSourcePaths:    sourcePaths(component.TriggeringFiles),
			AllowedEvidencePaths: allowedEvidencePaths(source, supporting, manifests),
		},
		Manifests:     requestSourceFiles(manifests),
		SourceFiles:   requestSourceFiles(source),
		Supporting:    requestSourceFiles(supporting),
		ChangeContext: changeContext{Kind: kind, Changes: changes},
		Limits:        defaultLimits(),
	}
	body, err := marshalRequestJSON(payload)
	if err != nil {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("encode component %q request: %w", component.Key, err)
	}
	requestKind := sharedmodel.RequestComponent
	if count > 1 {
		requestKind = sharedmodel.RequestBatch
	}
	return sharedmodel.GenerationRequest{
		Kind:          requestKind,
		ComponentKey:  component.Key,
		BatchIndex:    index,
		BatchCount:    count,
		PromptVersion: bundle.Identifier(),
		SchemaName:    "component_dossier",
		Schema:        bundle.Schema(),
		Settings:      settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: bundle.Component() + "\n\n" + string(body)},
		},
	}, nil
}

// buildSynthesisRequest constructs the single bounded synthesis request from validated
// batch dossiers and deterministic catalogs only.
func buildSynthesisRequest(
	bundle prompt.Bundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	catalog []string,
	batchDossiers []sharedmodel.ComponentDossier,
) (sharedmodel.GenerationRequest, error) {
	payload := synthesisPayload{
		Task:          "component_synthesis",
		PromptVersion: bundle.Identifier(),
		Target:        planningTarget{ComponentKey: component.Key, BatchIndex: 0, BatchCount: len(batchDossiers)},
		Repository: planningRepository{
			ComponentKeys:     catalog,
			TargetSourcePaths: sourcePaths(component.TriggeringFiles),
		},
		BatchDossiers: batchDossiers,
		Limits:        defaultLimits(),
	}
	body, err := marshalRequestJSON(payload)
	if err != nil {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("encode component %q synthesis request: %w", component.Key, err)
	}
	return sharedmodel.GenerationRequest{
		Kind:          sharedmodel.RequestSynthesis,
		ComponentKey:  component.Key,
		BatchCount:    len(batchDossiers),
		PromptVersion: bundle.Identifier(),
		SchemaName:    "component_dossier",
		Schema:        bundle.Schema(),
		Settings:      settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: bundle.Synthesis() + "\n\n" + string(body)},
		},
	}, nil
}

// buildFragmentRequest constructs one bounded section request and proves the provider
// profile and complete encoded request fit before the request can be sent.
func buildFragmentRequest(
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	kind sharedmodel.FragmentKind,
	source, supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	batchIndex, batchCount, chunkIndex, chunkCount int,
	maxResponseBytes, maxRequestBytes int64,
) (sharedmodel.GenerationRequest, error) {
	if err := validateFragmentProfile(settings.MaxOutputTokens, maxResponseBytes, maxRequestBytes); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	if err := validateFragmentScope(component, source, supporting, manifests, catalog, batchIndex, batchCount, chunkIndex, chunkCount); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	request, err := buildFragmentRequestUnchecked(bundle, settings, component, kind, source, supporting, manifests, catalog, changes, changeKind, batchIndex, batchCount, chunkIndex, chunkCount)
	if err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	if err := validateFragmentRequestSize(request, maxRequestBytes); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	repairBytes, err := fragmentRepairWorstCaseForRequest(bundle, request)
	if err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	if repairBytes > maxRequestBytes {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("component %q %s fragment worst-case repair request is %d bytes, exceeding the %d-byte request limit", component.Key, kind, repairBytes, maxRequestBytes)
	}
	return request, nil
}

// buildFragmentRequestUnchecked exists only for calculating the profile's own
// worst-case repair envelope without recursively validating that profile.
func buildFragmentRequestUnchecked(
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	kind sharedmodel.FragmentKind,
	source, supporting, manifests []sharedmodel.SourceFile,
	catalog []string,
	changes []sharedmodel.Change,
	changeKind string,
	batchIndex, batchCount, chunkIndex, chunkCount int,
) (sharedmodel.GenerationRequest, error) {
	fragmentPrompt, ok := bundle.FragmentPrompt(kind)
	if !ok {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("unsupported fragment kind %q", kind)
	}
	schema, ok := bundle.FragmentSchema(kind)
	if !ok {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("missing schema for fragment kind %q", kind)
	}
	schemaName, ok := bundle.FragmentSchemaName(kind)
	if !ok {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("missing schema name for fragment kind %q", kind)
	}
	payload := fragmentPayload{
		Task: "component_fragment", PromptVersion: bundle.Identifier(),
		Target: fragmentTarget{
			ComponentKey: component.Key, FragmentKind: kind,
			SourceBatchIndex: batchIndex, SourceBatchCount: batchCount,
			SourceChunkIndex: chunkIndex, SourceChunkCount: chunkCount,
		},
		Repository: planningRepository{
			ComponentKeys: catalog, TargetSourcePaths: sourcePaths(component.TriggeringFiles),
			AllowedEvidencePaths: allowedEvidencePaths(source, supporting, manifests),
		},
		Manifests: requestSourceFiles(manifests), SourceFiles: requestSourceFiles(source), Supporting: requestSourceFiles(supporting),
		ChangeContext: changeContext{Kind: changeKind, Changes: changes}, Limits: limitsForFragment(kind),
	}
	body, err := marshalRequestJSON(payload)
	if err != nil {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("encode component %q %s fragment request: %w", component.Key, kind, err)
	}
	return sharedmodel.GenerationRequest{
		Kind: sharedmodel.RequestFragment, ComponentKey: component.Key,
		BatchIndex: batchIndex, BatchCount: batchCount, FragmentKind: kind,
		SourceBatchIndex: batchIndex, SourceBatchCount: batchCount,
		SourceChunkIndex: chunkIndex, SourceChunkCount: chunkCount,
		PromptVersion: bundle.Identifier(), SchemaName: schemaName, Schema: schema, Settings: settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: fragmentPrompt + "\n\n" + string(body)},
		},
	}, nil
}

func buildOverviewReducerRequest(
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	candidates []sharedmodel.OverviewCandidate,
	evidence []string,
	sections []overviewSectionProjection,
	maxResponseBytes, maxRequestBytes int64,
) (sharedmodel.GenerationRequest, error) {
	payload := overviewReducerPayload{
		Task: "component_overview_reducer", PromptVersion: bundle.Identifier(),
		Target:     reducerTarget{ComponentKey: component.Key, RootComponent: component.RootComponent},
		Candidates: append([]sharedmodel.OverviewCandidate(nil), candidates...), EvidencePaths: sortedUnique(evidence),
		Sections: append([]overviewSectionProjection(nil), sections...), Limits: limitsForFragment(sharedmodel.FragmentOverviewCandidate),
	}
	body, err := marshalRequestJSON(payload)
	if err != nil {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("encode component %q overview reducer request: %w", component.Key, err)
	}
	request := sharedmodel.GenerationRequest{
		Kind: sharedmodel.RequestOverview, ComponentKey: component.Key, BatchIndex: 1, BatchCount: 1,
		SourceBatchIndex: 1, SourceBatchCount: 1,
		SourceChunkIndex: 1, SourceChunkCount: 1, PromptVersion: bundle.Identifier(),
		SourceSplitPath: "overview",
		SchemaName:      bundle.OverviewSchemaName(), Schema: bundle.OverviewSchema(), Settings: settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: bundle.OverviewPrompt() + "\n\n" + string(body)},
		},
	}
	if err := validateBoundedReducerRequest(bundle, request, maxResponseBytes, maxRequestBytes); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	return request, nil
}

func buildDiagramReducerRequest(
	bundle prompt.FragmentBundle,
	settings sharedmodel.GenerationSettings,
	component sharedmodel.Component,
	projection diagramProjection,
	allowedEvidence []string,
	maxResponseBytes, maxRequestBytes int64,
) (sharedmodel.GenerationRequest, error) {
	promptText, ok := bundle.FragmentPrompt(sharedmodel.FragmentDiagrams)
	if !ok {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("missing diagram reducer prompt")
	}
	schema, ok := bundle.FragmentSchema(sharedmodel.FragmentDiagrams)
	if !ok {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("missing diagram reducer schema")
	}
	schemaName, ok := bundle.FragmentSchemaName(sharedmodel.FragmentDiagrams)
	if !ok {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("missing diagram reducer schema name")
	}
	payload := diagramReducerPayload{
		Task: "component_diagram_reducer", PromptVersion: bundle.Identifier(),
		Target: reducerTarget{ComponentKey: component.Key, RootComponent: component.RootComponent},
		Repository: planningRepository{
			ComponentKeys: []string{component.Key}, AllowedEvidencePaths: sortedUnique(allowedEvidence),
		},
		Projection: projection,
		Limits:     limitsForFragment(sharedmodel.FragmentDiagrams),
	}
	body, err := marshalRequestJSON(payload)
	if err != nil {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("encode component %q diagram reducer request: %w", component.Key, err)
	}
	request := sharedmodel.GenerationRequest{
		Kind: sharedmodel.RequestDiagram, ComponentKey: component.Key, BatchIndex: 1, BatchCount: 1,
		FragmentKind: sharedmodel.FragmentDiagrams, SourceBatchIndex: 1, SourceBatchCount: 1,
		SourceChunkIndex: 1, SourceChunkCount: 1, PromptVersion: bundle.Identifier(),
		SourceSplitPath: "diagram",
		SchemaName:      schemaName, Schema: schema, Settings: settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: promptText + "\n\n" + string(body)},
		},
	}
	if err := validateBoundedReducerRequest(bundle, request, maxResponseBytes, maxRequestBytes); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	return request, nil
}

func validateBoundedReducerRequest(bundle prompt.FragmentBundle, request sharedmodel.GenerationRequest, maxResponseBytes, maxRequestBytes int64) error {
	if err := validateFragmentProfile(request.Settings.MaxOutputTokens, maxResponseBytes, maxRequestBytes); err != nil {
		return err
	}
	if err := validateFragmentRequestSize(request, maxRequestBytes); err != nil {
		return err
	}
	repairBytes, err := fragmentRepairWorstCaseForRequest(bundle, request)
	if err != nil {
		return err
	}
	if repairBytes > maxRequestBytes {
		return fmt.Errorf("component %q %s worst-case repair request is %d bytes, exceeding the %d-byte request limit", request.ComponentKey, request.Kind, repairBytes, maxRequestBytes)
	}
	return nil
}

func validateFragmentScope(component sharedmodel.Component, source, supporting, manifests []sharedmodel.SourceFile, catalog []string, batchIndex, batchCount, chunkIndex, chunkCount int) error {
	if component.Key == "" || len(component.Key) > fragmentMaxPath {
		return fmt.Errorf("fragment component key must be between 1 and %d bytes", fragmentMaxPath)
	}
	if batchIndex <= 0 || batchCount <= 0 || batchIndex > batchCount || chunkIndex <= 0 || chunkCount <= 0 || chunkIndex > chunkCount {
		return fmt.Errorf("fragment source batch and chunk indexes must identify a bounded scope")
	}
	for _, key := range catalog {
		if key == "" || len(key) > fragmentMaxPath {
			return fmt.Errorf("fragment component catalog key must be between 1 and %d bytes", fragmentMaxPath)
		}
	}
	for _, path := range allowedEvidencePaths(source, supporting, manifests) {
		if path == "" || len(path) > fragmentMaxPath {
			return fmt.Errorf("fragment evidence path must be between 1 and %d bytes", fragmentMaxPath)
		}
	}
	return nil
}

// buildRepairRequest constructs the one permitted repair request for an original
// request. It reuses the fixed target and schema and cannot expand scope: no source,
// catalogs, or supporting content is added.
func buildRepairRequest(
	bundle prompt.Bundle,
	original sharedmodel.GenerationRequest,
	invalidBody []byte,
	issues []sharedmodel.ValidationIssue,
) (sharedmodel.GenerationRequest, error) {
	if original.Kind == sharedmodel.RequestRepair {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("a repair response cannot be repaired again")
	}
	invalid := json.RawMessage(append([]byte(nil), invalidBody...))
	if !json.Valid(invalid) {
		// The invalid body is embedded as an opaque JSON string when it is not itself
		// valid JSON, so the repair payload remains well-formed.
		encoded, err := json.Marshal(string(invalidBody))
		if err != nil {
			return sharedmodel.GenerationRequest{}, fmt.Errorf("encode invalid response for repair: %w", err)
		}
		invalid = json.RawMessage(encoded)
	}
	payload := repairPayload{
		Task:             "component_repair",
		PromptVersion:    bundle.Identifier(),
		OriginalKind:     original.Kind,
		Target:           planningTarget{ComponentKey: original.ComponentKey, BatchIndex: original.BatchIndex, BatchCount: original.BatchCount},
		InvalidResponse:  invalid,
		ValidationIssues: issues,
		Limits:           defaultLimits(),
	}
	body, err := marshalRequestJSON(payload)
	if err != nil {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("encode component %q repair request: %w", original.ComponentKey, err)
	}
	return sharedmodel.GenerationRequest{
		Kind:          sharedmodel.RequestRepair,
		ComponentKey:  original.ComponentKey,
		BatchIndex:    original.BatchIndex,
		BatchCount:    original.BatchCount,
		PromptVersion: bundle.Identifier(),
		SchemaName:    original.SchemaName,
		Schema:        original.Schema,
		Settings:      original.Settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: bundle.Repair() + "\n\n" + string(body)},
		},
	}, nil
}

// buildFragmentRepairRequest creates the one bounded repair envelope without source
// or catalogs. It preserves the original fragment scope and schema exactly.
func buildFragmentRepairRequest(
	bundle prompt.FragmentBundle,
	original sharedmodel.GenerationRequest,
	invalidBody []byte,
	issues []sharedmodel.ValidationIssue,
	maxResponseBytes, maxRequestBytes int64,
) (sharedmodel.GenerationRequest, error) {
	if err := validateFragmentProfile(original.Settings.MaxOutputTokens, maxResponseBytes, maxRequestBytes); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	repair, err := buildFragmentRepairRequestUnchecked(bundle, original, invalidBody, issues)
	if err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	if err := validateFragmentRequestSize(repair, maxRequestBytes); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	return repair, nil
}

func buildFragmentRepairRequestUnchecked(
	bundle prompt.FragmentBundle,
	original sharedmodel.GenerationRequest,
	invalidBody []byte,
	issues []sharedmodel.ValidationIssue,
) (sharedmodel.GenerationRequest, error) {
	if original.Kind == sharedmodel.RequestRepair {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("a repair response cannot be repaired again")
	}
	if original.Kind != sharedmodel.RequestFragment && original.Kind != sharedmodel.RequestOverview && original.Kind != sharedmodel.RequestDiagram {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("bounded repair requires an original fragment or reducer request")
	}
	if original.Kind != sharedmodel.RequestOverview && original.FragmentKind == "" {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("bounded repair requires a fragment contract kind")
	}
	if original.ComponentKey == "" || len(original.ComponentKey) > fragmentMaxPath {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("fragment repair component key must be between 1 and %d bytes", fragmentMaxPath)
	}
	if original.SourceBatchIndex <= 0 || original.SourceBatchCount <= 0 || original.SourceBatchIndex > original.SourceBatchCount ||
		original.SourceChunkIndex <= 0 || original.SourceChunkCount <= 0 || original.SourceChunkIndex > original.SourceChunkCount {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("fragment repair requires a valid bounded source scope")
	}
	if len(invalidBody) > fragmentResponseBytes {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("fragment repair candidate exceeds the %d-byte limit", fragmentResponseBytes)
	}
	if err := validateFragmentRepairIssues(issues); err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	invalid, err := repairRawMessage(invalidBody)
	if err != nil {
		return sharedmodel.GenerationRequest{}, err
	}
	contractKind := original.FragmentKind
	if original.Kind == sharedmodel.RequestOverview {
		contractKind = sharedmodel.FragmentOverviewCandidate
	}
	payload := fragmentRepairPayload{
		Task: "component_bounded_repair", PromptVersion: bundle.Identifier(),
		Target: boundedRepairTarget{
			ComponentKey: original.ComponentKey, RequestKind: original.Kind, FragmentKind: original.FragmentKind,
			SourceBatchIndex: original.SourceBatchIndex, SourceBatchCount: original.SourceBatchCount,
			SourceChunkIndex: original.SourceChunkIndex, SourceChunkCount: original.SourceChunkCount,
		},
		InvalidResponse: invalid, ValidationIssues: issues, Limits: limitsForFragment(contractKind),
	}
	body, err := marshalRequestJSON(payload)
	if err != nil {
		return sharedmodel.GenerationRequest{}, fmt.Errorf("encode component %q %s fragment repair request: %w", original.ComponentKey, original.FragmentKind, err)
	}
	return sharedmodel.GenerationRequest{
		Kind: sharedmodel.RequestRepair, ComponentKey: original.ComponentKey,
		BatchIndex: original.BatchIndex, BatchCount: original.BatchCount, FragmentKind: original.FragmentKind,
		SourceBatchIndex: original.SourceBatchIndex, SourceBatchCount: original.SourceBatchCount,
		SourceChunkIndex: original.SourceChunkIndex, SourceChunkCount: original.SourceChunkCount,
		SourceSplitPath: original.SourceSplitPath,
		PromptVersion:   bundle.Identifier(), SchemaName: original.SchemaName, Schema: append([]byte(nil), original.Schema...), Settings: original.Settings,
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: bundle.System()},
			{Role: sharedmodel.RoleUser, Content: bundle.Repair() + "\n\n" + string(body)},
		},
	}, nil
}

func validateFragmentRepairIssues(issues []sharedmodel.ValidationIssue) error {
	if len(issues) > maxValidationIssues {
		return fmt.Errorf("fragment repair has more than %d validation issues", maxValidationIssues)
	}
	for _, issue := range issues {
		if len(issue.Code) > fragmentRepairIssueCodeBytes || len(issue.Path) > fragmentRepairIssuePathBytes || len(issue.Message) > fragmentRepairIssueMessageBytes {
			return fmt.Errorf("fragment repair validation issue exceeds the bounded envelope")
		}
	}
	return nil
}

func repairRawMessage(invalidBody []byte) (json.RawMessage, error) {
	invalid := json.RawMessage(append([]byte(nil), invalidBody...))
	if json.Valid(invalid) {
		return invalid, nil
	}
	encoded, err := json.Marshal(string(invalidBody))
	if err != nil {
		return nil, fmt.Errorf("encode invalid response for repair: %w", err)
	}
	return json.RawMessage(encoded), nil
}

// requestContentBytes is the deterministic, provider-neutral encoded request size. It
// includes message framing, settings, metadata, the selected structured-output envelope,
// and fixed provider-envelope headroom. Auto mode reserves the larger prompt-JSON fallback.
func requestContentBytes(request sharedmodel.GenerationRequest) int64 {
	return int64(len(requestPlanningBytes(request)) + providerRequestEnvelopeHeadroom)
}

func requestPlanningBytes(request sharedmodel.GenerationRequest) []byte {
	structured := request.Settings.StructuredOutputMode
	if structured != sharedmodel.StructuredOutputJSONSchema {
		structured = sharedmodel.StructuredOutputPromptJSON
	}
	messageCapacity := len(request.Messages)
	if structured == sharedmodel.StructuredOutputPromptJSON {
		messageCapacity++
	}
	messages := make([]requestPlanningMessage, 0, messageCapacity)
	schemaInserted := false
	for _, message := range request.Messages {
		if structured == sharedmodel.StructuredOutputPromptJSON && !schemaInserted && message.Role != sharedmodel.RoleSystem {
			schema := sharedmodel.PromptJSONSchemaMessage(request.Schema)
			messages = append(messages, requestPlanningMessage{Role: schema.Role, Content: schema.Content})
			schemaInserted = true
		}
		messages = append(messages, requestPlanningMessage{Role: message.Role, Content: message.Content})
	}
	if structured == sharedmodel.StructuredOutputPromptJSON && !schemaInserted {
		schema := sharedmodel.PromptJSONSchemaMessage(request.Schema)
		messages = append(messages, requestPlanningMessage{Role: schema.Role, Content: schema.Content})
	}
	var nativeSchema json.RawMessage
	if structured == sharedmodel.StructuredOutputJSONSchema {
		nativeSchema = append(json.RawMessage(nil), request.Schema...)
	}
	envelope := requestPlanningEnvelope{
		Model: request.Settings.Model, Temperature: request.Settings.Temperature,
		MaxOutputTokens: request.Settings.MaxOutputTokens, APIMode: request.Settings.APIMode,
		StructuredOutputMode: structured, SchemaName: request.SchemaName, Schema: nativeSchema, Messages: messages,
	}
	encoded, _ := marshalRequestJSON(envelope)
	return encoded
}

func encodedMessageContentBytes(content string) int64 {
	encoded, _ := marshalRequestJSON(content)
	// Exclude the surrounding JSON string quotes; the request limit tracks encoded
	// content while fixed provider-envelope overhead is budgeted separately.
	return int64(len(encoded) - 2)
}

func marshalRequestJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func requestSourceFiles(files []sharedmodel.SourceFile) []requestSourceFile {
	result := make([]requestSourceFile, 0, len(files))
	for _, file := range files {
		result = append(result, requestSourceFile{Path: file.Path, Role: file.Role, Content: string(file.Data)})
	}
	return result
}
