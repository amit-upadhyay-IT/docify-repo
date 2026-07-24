package model

// Role is an endpoint-neutral chat role. The adapter maps it to the concrete
// OpenAI-compatible representation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one endpoint-neutral chat message. Content may include allow-listed
// repository source and is never serialized into reports, state, or logs.
type Message struct {
	Role    Role   `json:"-"`
	Content string `json:"-"`
}

// RequestKind identifies which prompt contract produced a request. It is safe,
// non-secret metadata suitable for reports and logs.
type RequestKind string

const (
	RequestComponent RequestKind = "component"
	RequestBatch     RequestKind = "batch"
	RequestSynthesis RequestKind = "synthesis"
	RequestRepair    RequestKind = "repair"
)

// StructuredOutputMode selects how the adapter constrains the response format.
type StructuredOutputMode string

const (
	StructuredOutputAuto       StructuredOutputMode = "auto"
	StructuredOutputJSONSchema StructuredOutputMode = "json_schema"
	StructuredOutputPromptJSON StructuredOutputMode = "prompt_json"
)

// APIMode selects the OpenAI-compatible surface used by the adapter.
type APIMode string

const (
	APIModeChatCompletions APIMode = "chat_completions"
	APIModeResponses       APIMode = "responses"
)

// GenerationSettings are the pinned, non-secret model parameters for one request.
type GenerationSettings struct {
	Model                string
	Temperature          float64
	MaxOutputTokens      int
	APIMode              APIMode
	StructuredOutputMode StructuredOutputMode
}

// GenerationRequest is an endpoint-neutral request the usecase hands to a Generator.
// It carries only allow-listed content and never credentials. Messages and Schema are
// excluded from serialization so no source or contract text can leak into reports.
type GenerationRequest struct {
	Kind          RequestKind        `json:"kind"`
	ComponentKey  string             `json:"component_key"`
	BatchIndex    int                `json:"batch_index"`
	BatchCount    int                `json:"batch_count"`
	PromptVersion string             `json:"prompt_version"`
	SchemaName    string             `json:"schema_name"`
	Messages      []Message          `json:"-"`
	Schema        []byte             `json:"-"`
	Settings      GenerationSettings `json:"-"`
}

// TokenUsage is provider usage when reported. Present distinguishes zero usage from
// an absent usage block.
type TokenUsage struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	Present          bool `json:"present"`
}

// GenerationResponse is the adapter's normalized result. Body may be syntactically
// invalid JSON so the usecase repair policy can run; the adapter still rejects empty,
// non-UTF-8, transport-truncated, or over-limit bodies before returning. Body is not
// serialized so model output cannot leak into reports.
type GenerationResponse struct {
	Body                 []byte               `json:"-"`
	FinishReason         string               `json:"finish_reason,omitempty"`
	ProviderRequestID    string               `json:"provider_request_id,omitempty"`
	Usage                TokenUsage           `json:"usage"`
	TransportAttempts    int                  `json:"transport_attempts"`
	StructuredOutputUsed StructuredOutputMode `json:"structured_output_used,omitempty"`
}
