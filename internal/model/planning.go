package model

type ChangeStatus string

const (
	ChangeAdded    ChangeStatus = "added"
	ChangeModified ChangeStatus = "modified"
	ChangeDeleted  ChangeStatus = "deleted"
	ChangeRenamed  ChangeStatus = "renamed"
)

type RawChange struct {
	Status     ChangeStatus `json:"status"`
	OldPath    string       `json:"old_path,omitempty"`
	NewPath    string       `json:"new_path,omitempty"`
	Similarity int          `json:"similarity,omitempty"`
}

type Change struct {
	Status                  ChangeStatus `json:"status"`
	OldPath                 string       `json:"old_path,omitempty"`
	NewPath                 string       `json:"new_path,omitempty"`
	OldRole                 SourceRole   `json:"old_role,omitempty"`
	NewRole                 SourceRole   `json:"new_role,omitempty"`
	OldComponentKey         string       `json:"old_component_key,omitempty"`
	NewComponentKey         string       `json:"new_component_key,omitempty"`
	OldRootComponent        bool         `json:"old_root_component,omitempty"`
	NewRootComponent        bool         `json:"new_root_component,omitempty"`
	OldTriggersRegeneration bool         `json:"old_triggers_regeneration,omitempty"`
	NewTriggersRegeneration bool         `json:"new_triggers_regeneration,omitempty"`
	Similarity              int          `json:"similarity,omitempty"`
}

type Component struct {
	Key              string
	RootComponent    bool
	Document         string
	TriggeringFiles  []SourceFile
	SupportingFiles  []SourceFile
	RelevantManifest []SourceFile
}

type ComponentSummary struct {
	Key               string   `json:"key"`
	RootComponent     bool     `json:"root_component,omitempty"`
	Document          string   `json:"document"`
	TriggeringPaths   []string `json:"triggering_paths"`
	SupportingPaths   []string `json:"supporting_paths"`
	ManifestPaths     []string `json:"manifest_paths"`
	TriggeringBytes   int64    `json:"triggering_bytes"`
	SupportingBytes   int64    `json:"supporting_bytes"`
	ManifestBytes     int64    `json:"manifest_bytes"`
	OmittedSupporting int      `json:"omitted_supporting_paths,omitempty"`
	OmittedManifests  int      `json:"omitted_manifest_paths,omitempty"`
}

type ComponentAction string

const (
	ComponentCreate        ComponentAction = "create"
	ComponentRegenerate    ComponentAction = "regenerate"
	ComponentDelete        ComponentAction = "delete"
	ComponentSkipUnchanged ComponentAction = "skip_unchanged"
)

type ComponentBatch struct {
	Index              int      `json:"index"`
	Count              int      `json:"count"`
	ChunkIndex         int      `json:"chunk_index,omitempty"`
	ChunkCount         int      `json:"chunk_count,omitempty"`
	SourcePaths        []string `json:"source_paths"`
	SourceBytes        int64    `json:"source_bytes"`
	RequestBytes       int64    `json:"request_bytes"`
	ConservativeTokens int64    `json:"conservative_tokens"`
	TypicalTokens      int64    `json:"typical_tokens"`
}

// FragmentEstimate reports deterministic map-call and byte estimates for one
// fragment kind. Fallback fields are populated only when auto plans fragments as
// a contingent fast-path fallback rather than as primary work.
type FragmentEstimate struct {
	Kind                      FragmentKind `json:"kind"`
	PlannedCalls              int          `json:"planned_calls"`
	FallbackCalls             int          `json:"fallback_calls"`
	PlannedRequestBytes       int64        `json:"planned_request_bytes"`
	FallbackRequestBytes      int64        `json:"fallback_request_bytes"`
	MaximumRepairRequestBytes int64        `json:"maximum_repair_request_bytes"`
}

type AffectedComponent struct {
	Key                   string             `json:"key"`
	RootComponent         bool               `json:"root_component,omitempty"`
	Document              string             `json:"document"`
	Action                ComponentAction    `json:"action"`
	Reasons               []string           `json:"reasons"`
	ExistedBefore         bool               `json:"existed_before"`
	ExistsNow             bool               `json:"exists_now"`
	InputHash             string             `json:"input_hash,omitempty"`
	GenerationStrategy    string             `json:"generation_strategy,omitempty"`
	FragmentFallbackPlan  bool               `json:"fragment_fallback_plan,omitempty"`
	FragmentFallback      bool               `json:"fragment_fallback,omitempty"`
	Batches               []ComponentBatch   `json:"batches,omitempty"`
	Fragments             []FragmentEstimate `json:"fragments,omitempty"`
	SynthesisRequestBytes int64              `json:"synthesis_request_bytes,omitempty"`
	OverviewRequestBytes  int64              `json:"overview_reducer_request_bytes,omitempty"`
	OverviewRepairBytes   int64              `json:"overview_reducer_maximum_repair_request_bytes,omitempty"`
	DiagramRequestBytes   int64              `json:"diagram_reducer_request_bytes,omitempty"`
	DiagramRepairBytes    int64              `json:"diagram_reducer_maximum_repair_request_bytes,omitempty"`
}

type CallEstimate struct {
	Normal                         int                `json:"normal"`
	DossierFastPath                int                `json:"dossier_fast_path"`
	Batch                          int                `json:"batch"`
	Synthesis                      int                `json:"synthesis"`
	Fragment                       int                `json:"fragment"`
	OverviewReducer                int                `json:"overview_reducer"`
	DiagramReducer                 int                `json:"diagram_reducer"`
	Fragments                      []FragmentEstimate `json:"fragments,omitempty"`
	Primary                        int                `json:"primary"`
	TypicalLogical                 int                `json:"typical_logical"`
	TypicalTruncationFallbackCalls int                `json:"typical_truncation_fallback_calls"`
	MaximumRepair                  int                `json:"maximum_repair"`
	MaximumFragmentRepairCalls     int                `json:"maximum_fragment_repair_calls"`
	MaximumTruncationFallbackCalls int                `json:"maximum_truncation_fallback_calls"`
	MaximumSourceSplitCalls        int                `json:"maximum_source_split_calls"`
	MaximumLogical                 int                `json:"maximum_logical"`
	MaximumTransportFallback       int                `json:"maximum_transport_fallback"`
	StructuredModesAttempted       int                `json:"structured_modes_attempted"`
	TransportRetries               int                `json:"transport_retries"`
	MaximumHTTPAttempts            int                `json:"maximum_http_attempts"`
	RequestBytes                   int64              `json:"request_bytes"`
	FallbackRequestBytes           int64              `json:"fallback_request_bytes"`
	OverviewRequestBytes           int64              `json:"overview_reducer_request_bytes"`
	OverviewFallbackBytes          int64              `json:"overview_reducer_fallback_request_bytes"`
	OverviewRepairBytes            int64              `json:"overview_reducer_maximum_repair_request_bytes"`
	DiagramRequestBytes            int64              `json:"diagram_reducer_request_bytes"`
	DiagramFallbackBytes           int64              `json:"diagram_reducer_fallback_request_bytes"`
	DiagramRepairBytes             int64              `json:"diagram_reducer_maximum_repair_request_bytes"`
	ConservativeTokens             int64              `json:"conservative_tokens"`
	TypicalTokens                  int64              `json:"typical_tokens"`
}

type GenerationPlan struct {
	Mode               string              `json:"mode"`
	GenerationStrategy string              `json:"generation_strategy"`
	StateStatus        string              `json:"state_status"`
	BaseSHA            string              `json:"base_sha,omitempty"`
	HeadSHA            string              `json:"head_sha,omitempty"`
	FullReason         string              `json:"full_reason,omitempty"`
	Noop               bool                `json:"noop"`
	Components         []ComponentSummary  `json:"components"`
	Changes            []Change            `json:"changes"`
	AffectedComponents []AffectedComponent `json:"affected_components"`
	DeletedDocuments   []string            `json:"deleted_documents"`
	Calls              CallEstimate        `json:"calls"`
}
