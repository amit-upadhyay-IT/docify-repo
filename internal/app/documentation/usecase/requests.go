package usecase

import (
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
	body, err := json.Marshal(payload)
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
	body, err := json.Marshal(payload)
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
	body, err := json.Marshal(payload)
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

// requestContentBytes is the deterministic, provider-neutral size of a request: the
// UTF-8 byte length of every message plus the response schema. It is a conservative
// upper estimate, not a claim about provider tokenization.
func requestContentBytes(request sharedmodel.GenerationRequest) int64 {
	var total int64
	for _, message := range request.Messages {
		total += int64(len(message.Content))
	}
	total += int64(len(request.Schema))
	return total
}

func requestSourceFiles(files []sharedmodel.SourceFile) []requestSourceFile {
	result := make([]requestSourceFile, 0, len(files))
	for _, file := range files {
		result = append(result, requestSourceFile{Path: file.Path, Role: file.Role, Content: string(file.Data)})
	}
	return result
}
