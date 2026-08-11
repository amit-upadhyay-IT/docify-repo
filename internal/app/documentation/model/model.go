package model

import sharedmodel "docify-repo/internal/model"

// OutputMode controls command summary formatting.
type OutputMode string

const (
	OutputModeHuman OutputMode = "human"
	OutputModeJSON  OutputMode = "json"
)

type RawSyncOptions struct {
	WorkingDirectory  string
	Output            string
	ReportPath        string
	BaseSHA           string
	HeadSHA           string
	Publisher         string
	Full              bool
	AllowFullFallback bool
	Concurrency       int
	SourcePolicy      SourcePolicy
	ComponentPolicy   ComponentPolicy
	GenerationPolicy  GenerationPolicy
	GitHubRepository  string
	BaseBranch        string
	Branch            string
	LLMCredential     bool
	GitHubCredential  bool
}

type RawCheckOptions struct {
	WorkingDirectory  string
	Output            string
	ReportPath        string
	BaseSHA           string
	HeadSHA           string
	Full              bool
	AllowFullFallback bool
	SourcePolicy      SourcePolicy
	ComponentPolicy   ComponentPolicy
	GenerationPolicy  GenerationPolicy
}

type RawPlanOptions struct {
	WorkingDirectory  string
	Output            string
	ReportPath        string
	BaseSHA           string
	HeadSHA           string
	Full              bool
	AllowFullFallback bool
	SourcePolicy      SourcePolicy
	ComponentPolicy   ComponentPolicy
	GenerationPolicy  GenerationPolicy
}

type SyncInput struct {
	WorkingDirectory        string
	Output                  OutputMode
	ReportPath              string
	BaseSHA                 string
	HeadSHA                 string
	Publisher               string
	Full                    bool
	AllowFullFallback       bool
	Concurrency             int
	SourcePolicy            SourcePolicy
	ComponentPolicy         ComponentPolicy
	GenerationPolicy        GenerationPolicy
	GitHubRepository        string
	BaseBranch              string
	Branch                  string
	GitHubCredentialPresent bool
}

type CheckInput struct {
	WorkingDirectory  string
	Output            OutputMode
	ReportPath        string
	BaseSHA           string
	HeadSHA           string
	Full              bool
	AllowFullFallback bool
	SourcePolicy      SourcePolicy
	ComponentPolicy   ComponentPolicy
	GenerationPolicy  GenerationPolicy
}

type PlanInput struct {
	WorkingDirectory  string
	Output            OutputMode
	ReportPath        string
	BaseSHA           string
	HeadSHA           string
	Full              bool
	AllowFullFallback bool
	Concurrency       int
	SourcePolicy      SourcePolicy
	ComponentPolicy   ComponentPolicy
	GenerationPolicy  GenerationPolicy
}

type ResultSummary struct {
	Command         string                       `json:"command"`
	Status          string                       `json:"status"`
	TrackedPaths    int                          `json:"tracked_paths"`
	IncludedPaths   int                          `json:"included_paths"`
	TriggeringPaths int                          `json:"triggering_paths"`
	ExcludedPaths   int                          `json:"excluded_paths"`
	Files           []sharedmodel.SourceDecision `json:"files"`
	Plan            sharedmodel.GenerationPlan   `json:"plan"`
	Generation      *GenerationOutcome           `json:"generation,omitempty"`
	Failure         *GenerationFailure           `json:"failure,omitempty"`
}

// GenerationOutcome records the safe, non-secret result of a sync that actually
// generated and installed output. It carries call counts, provider usage, and the
// output diff, never source or model prose.
type GenerationOutcome struct {
	NormalCalls                int                    `json:"normal_calls"`
	BatchCalls                 int                    `json:"batch_calls"`
	SynthesisCalls             int                    `json:"synthesis_calls"`
	FragmentCalls              int                    `json:"fragment_calls"`
	OverviewReducerCalls       int                    `json:"overview_reducer_calls"`
	DiagramReducerCalls        int                    `json:"diagram_reducer_calls"`
	RepairCalls                int                    `json:"repair_calls"`
	FragmentFallbacks          int                    `json:"fragment_fallbacks"`
	FragmentFallbackComponents []string               `json:"fragment_fallback_components"`
	FragmentSourceSplits       int                    `json:"fragment_source_splits"`
	FragmentSourceSplitCalls   int                    `json:"fragment_source_split_calls"`
	SaturatedScopes            int                    `json:"retained_saturated_scopes"`
	OverviewFallbacks          int                    `json:"overview_fallbacks"`
	DiagramFallbacks           int                    `json:"diagram_fallbacks"`
	TransportAttempts          int                    `json:"transport_attempts"`
	Usage                      sharedmodel.TokenUsage `json:"usage"`
	Diff                       sharedmodel.OutputDiff `json:"diff"`
	InstalledPaths             int                    `json:"installed_paths"`
	DeletedPaths               int                    `json:"deleted_paths"`
	GeneratedComponents        int                    `json:"generated_components"`
}

// GenerationFailure is safe diagnostic metadata for a failed generation run. It
// contains no error prose, source, prompt, schema, or model response.
type GenerationFailure struct {
	Category             string                           `json:"category"`
	ComponentKey         string                           `json:"component_key,omitempty"`
	RequestKind          sharedmodel.RequestKind          `json:"request_kind,omitempty"`
	BatchIndex           int                              `json:"batch_index,omitempty"`
	BatchCount           int                              `json:"batch_count,omitempty"`
	FragmentKind         sharedmodel.FragmentKind         `json:"fragment_kind,omitempty"`
	SourceBatchIndex     int                              `json:"source_batch_index,omitempty"`
	SourceBatchCount     int                              `json:"source_batch_count,omitempty"`
	SourceChunkIndex     int                              `json:"source_chunk_index,omitempty"`
	SourceChunkCount     int                              `json:"source_chunk_count,omitempty"`
	SourceSplitPath      string                           `json:"source_split_path,omitempty"`
	FinishReason         string                           `json:"finish_reason,omitempty"`
	ProviderRequestID    string                           `json:"provider_request_id,omitempty"`
	StructuredOutputUsed sharedmodel.StructuredOutputMode `json:"structured_output_used,omitempty"`
	TransportAttempts    int                              `json:"transport_attempts,omitempty"`
	ValidationCodes      []string                         `json:"validation_codes,omitempty"`
}

// RunReport is the structured, machine-readable summary written to the configured report
// path. It records the change range, path decisions, affected components with reasons,
// LLM call counts and usage, the document diff, and validation results. It never contains
// source text, prompts, model responses, or credentials.
type RunReport struct {
	SchemaVersion      int                       `json:"schema_version"`
	Command            string                    `json:"command"`
	Status             string                    `json:"status"`
	Mode               string                    `json:"mode"`
	StateStatus        string                    `json:"state_status"`
	Noop               bool                      `json:"noop"`
	BaseSHA            string                    `json:"base_sha,omitempty"`
	HeadSHA            string                    `json:"head_sha,omitempty"`
	FullReason         string                    `json:"full_reason,omitempty"`
	GenerationStrategy string                    `json:"generation_strategy"`
	PlannedLLM         sharedmodel.CallEstimate  `json:"planned_llm"`
	TrackedPaths       int                       `json:"tracked_paths"`
	IncludedPaths      []string                  `json:"included_paths"`
	ExcludedPaths      []string                  `json:"excluded_paths"`
	AffectedComponents []ReportAffectedComponent `json:"affected_components"`
	DeletedComponents  []string                  `json:"deleted_components"`
	LLM                ReportLLM                 `json:"llm"`
	Documents          sharedmodel.OutputDiff    `json:"documents"`
	Validation         ReportValidation          `json:"validation"`
	Failure            *GenerationFailure        `json:"failure,omitempty"`
}

// ReportAffectedComponent names one planned component action and why it was selected.
type ReportAffectedComponent struct {
	Key                  string   `json:"key"`
	RootComponent        bool     `json:"root_component,omitempty"`
	Action               string   `json:"action"`
	Reasons              []string `json:"reasons"`
	GenerationStrategy   string   `json:"generation_strategy,omitempty"`
	FragmentFallbackPlan bool     `json:"fragment_fallback_plan,omitempty"`
	FragmentFallback     bool     `json:"fragment_fallback,omitempty"`
}

// ReportLLM records the number of model calls made and provider-reported token usage.
type ReportLLM struct {
	NormalCalls                int                    `json:"normal_calls"`
	BatchCalls                 int                    `json:"batch_calls"`
	SynthesisCalls             int                    `json:"synthesis_calls"`
	FragmentCalls              int                    `json:"fragment_calls"`
	OverviewReducerCalls       int                    `json:"overview_reducer_calls"`
	DiagramReducerCalls        int                    `json:"diagram_reducer_calls"`
	RepairCalls                int                    `json:"repair_calls"`
	FragmentFallbacks          int                    `json:"fragment_fallbacks"`
	FragmentFallbackComponents []string               `json:"fragment_fallback_components"`
	FragmentSourceSplits       int                    `json:"fragment_source_splits"`
	FragmentSourceSplitCalls   int                    `json:"fragment_source_split_calls"`
	SaturatedScopes            int                    `json:"retained_saturated_scopes"`
	OverviewFallbacks          int                    `json:"overview_fallbacks"`
	DiagramFallbacks           int                    `json:"diagram_fallbacks"`
	TransportAttempts          int                    `json:"transport_attempts"`
	Usage                      sharedmodel.TokenUsage `json:"usage"`
}

// ReportValidation records the local validation and installed-output integrity results.
type ReportValidation struct {
	OutputValidated  bool   `json:"output_validated"`
	IntegrityChecked bool   `json:"integrity_checked"`
	IntegrityOK      bool   `json:"integrity_ok"`
	Detail           string `json:"detail,omitempty"`
}

type SourcePolicy struct {
	DocsDir       string
	StatePath     string
	ReportPath    string
	Include       []string
	Exclude       []string
	MaxFileBytes  int64
	Tests         SourceBehavior
	Generated     SourceBehavior
	Fixtures      SourceBehavior
	RoleOverrides []RoleOverride
}

type SourceBehavior struct {
	IncludeAsContext bool
	TriggerOnChange  bool
}

type RoleOverride struct {
	Pattern string
	Role    sharedmodel.SourceRole
}

type ComponentPolicy struct {
	Strategy           string
	Roots              []string
	MaxContextBytes    int64
	MaxBatchBytes      int64
	MaxSupportingBytes int64
	MaxManifestBytes   int64
	MaxDiffBytes       int64
	MaxRequestBytes    int64
}

type GenerationPolicy struct {
	Profile              string
	Audience             string
	Mermaid              bool
	GenerationStrategy   string
	Provider             string
	APIMode              string
	Model                string
	Temperature          float64
	MaxOutputTokens      int
	MaxResponseBytes     int64
	EndpointHash         string
	StructuredOutputMode string
	TransportRetries     int
	FragmentCallLimit    int
	FragmentSplitDepth   int
}
