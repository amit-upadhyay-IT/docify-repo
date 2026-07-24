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
	SourcePaths        []string `json:"source_paths"`
	SourceBytes        int64    `json:"source_bytes"`
	RequestBytes       int64    `json:"request_bytes"`
	ConservativeTokens int64    `json:"conservative_tokens"`
	TypicalTokens      int64    `json:"typical_tokens"`
}

type AffectedComponent struct {
	Key                   string           `json:"key"`
	RootComponent         bool             `json:"root_component,omitempty"`
	Document              string           `json:"document"`
	Action                ComponentAction  `json:"action"`
	Reasons               []string         `json:"reasons"`
	ExistedBefore         bool             `json:"existed_before"`
	ExistsNow             bool             `json:"exists_now"`
	InputHash             string           `json:"input_hash,omitempty"`
	Batches               []ComponentBatch `json:"batches,omitempty"`
	SynthesisRequestBytes int64            `json:"synthesis_request_bytes,omitempty"`
}

type CallEstimate struct {
	Normal                   int   `json:"normal"`
	Batch                    int   `json:"batch"`
	Synthesis                int   `json:"synthesis"`
	Primary                  int   `json:"primary"`
	MaximumRepair            int   `json:"maximum_repair"`
	MaximumTransportFallback int   `json:"maximum_transport_fallback"`
	RequestBytes             int64 `json:"request_bytes"`
	ConservativeTokens       int64 `json:"conservative_tokens"`
	TypicalTokens            int64 `json:"typical_tokens"`
}

type GenerationPlan struct {
	Mode               string              `json:"mode"`
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
