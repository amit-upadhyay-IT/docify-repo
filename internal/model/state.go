package model

type State struct {
	SchemaVersion          int                       `json:"schema_version"`
	GeneratorVersion       string                    `json:"generator_version"`
	PlannerVersion         string                    `json:"planner_version"`
	PromptVersion          string                    `json:"prompt_version"`
	RenderVersion          string                    `json:"render_version,omitempty"`
	OutputSchemaVersion    string                    `json:"output_schema_version,omitempty"`
	ConfigHash             string                    `json:"config_hash"`
	ConfigHashes           StateConfigHashes         `json:"config_hashes,omitempty"`
	GeneratedPaths         []string                  `json:"generated_paths"`
	GeneratedContentHashes map[string]string         `json:"generated_content_hashes,omitempty"`
	Files                  map[string]StateFile      `json:"files"`
	Components             map[string]StateComponent `json:"components"`
}

type StateConfigHashes struct {
	Paths      string `json:"paths,omitempty"`
	Source     string `json:"source,omitempty"`
	Components string `json:"components,omitempty"`
	Context    string `json:"context,omitempty"`
	Generation string `json:"generation,omitempty"`
}

type StateFile struct {
	SourceHash           string     `json:"source_hash"`
	Role                 SourceRole `json:"role"`
	TriggersRegeneration bool       `json:"triggers_regeneration"`
	ComponentKey         string     `json:"component_key"`
	RootComponent        bool       `json:"root_component,omitempty"`
}

type StateComponent struct {
	InputHash     string `json:"input_hash"`
	Document      string `json:"document"`
	RootComponent bool   `json:"root_component,omitempty"`
}

type StateLoadResult struct {
	Missing bool
	Invalid bool
	State   State
}
