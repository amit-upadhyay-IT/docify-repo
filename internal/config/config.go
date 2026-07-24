package config

const CurrentVersion = 1

// Config contains merged non-secret configuration and runtime invocation data.
type Config struct {
	Version          int                 `yaml:"version"`
	DocsDir          string              `yaml:"docs_dir"`
	StatePath        string              `yaml:"state_path"`
	ReportPath       string              `yaml:"report_path"`
	Source           SourceConfig        `yaml:"source"`
	Components       ComponentsConfig    `yaml:"components"`
	Documentation    DocumentationConfig `yaml:"documentation"`
	LLM              LLMConfig           `yaml:"llm"`
	Publishing       PublishingConfig    `yaml:"publishing"`
	WorkingDirectory string              `yaml:"-"`
	Runtime          RuntimeConfig       `yaml:"-"`
}

type SourceConfig struct {
	Include       []string       `yaml:"include"`
	Exclude       []string       `yaml:"exclude"`
	MaxFileBytes  int64          `yaml:"max_file_bytes"`
	Tests         SourceBehavior `yaml:"tests"`
	Generated     SourceBehavior `yaml:"generated"`
	Fixtures      SourceBehavior `yaml:"fixtures"`
	RoleOverrides []RoleOverride `yaml:"role_overrides"`
}

type SourceBehavior struct {
	IncludeAsContext bool  `yaml:"include_as_context"`
	TriggerOnChange  bool  `yaml:"trigger_on_change"`
	Include          *bool `yaml:"include,omitempty"`
}

type RoleOverride struct {
	Pattern string `yaml:"pattern"`
	Role    string `yaml:"role"`
}

type ComponentsConfig struct {
	Strategy           string   `yaml:"strategy"`
	Roots              []string `yaml:"roots"`
	MaxContextBytes    int64    `yaml:"max_context_bytes"`
	MaxBatchBytes      int64    `yaml:"max_batch_bytes"`
	MaxSupportingBytes int64    `yaml:"max_supporting_bytes"`
	MaxManifestBytes   int64    `yaml:"max_manifest_bytes"`
	MaxDiffBytes       int64    `yaml:"max_diff_bytes"`
	MaxRequestBytes    int64    `yaml:"max_request_bytes"`
}

type DocumentationConfig struct {
	Profile  string `yaml:"profile"`
	Audience string `yaml:"audience"`
	Mermaid  bool   `yaml:"mermaid"`
}

type LLMConfig struct {
	Provider             string  `yaml:"provider"`
	BaseURL              string  `yaml:"base_url"`
	APIMode              string  `yaml:"api_mode"`
	Model                string  `yaml:"model"`
	Temperature          float64 `yaml:"temperature"`
	MaxOutputTokens      int     `yaml:"max_output_tokens"`
	StructuredOutputMode string  `yaml:"structured_output_mode"`
	Timeout              string  `yaml:"timeout"`
	Retries              int     `yaml:"retries"`
	Concurrency          int     `yaml:"concurrency"`
}

type PublishingConfig struct {
	Provider string `yaml:"provider"`
	Branch   string `yaml:"branch"`
}

// RuntimeConfig contains non-secret environment values and credential presence.
type RuntimeConfig struct {
	BaseSHA                 string
	HeadSHA                 string
	GitHubRepository        string
	BaseBranch              string
	LLMCredentialPresent    bool
	GitHubCredentialPresent bool
}
