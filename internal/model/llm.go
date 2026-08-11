package model

import "fmt"

const MaximumFragmentSplitDepth = 16

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
	RequestFragment  RequestKind = "fragment"
	RequestOverview  RequestKind = "overview_reducer"
	RequestDiagram   RequestKind = "diagram_reducer"
	RequestRepair    RequestKind = "repair"
)

// FragmentKind identifies one bounded section response contract. It is safe,
// non-secret metadata suitable for requests, reports, and failure diagnostics.
type FragmentKind string

const (
	FragmentOverviewCandidate FragmentKind = "overview_candidate"
	FragmentArchitecture      FragmentKind = "architecture"
	FragmentInterfaces        FragmentKind = "interfaces"
	FragmentDataModels        FragmentKind = "data_models"
	FragmentWorkflows         FragmentKind = "workflows"
	FragmentDependencies      FragmentKind = "dependencies"
	FragmentReviewGaps        FragmentKind = "review_gaps"
	FragmentDiagrams          FragmentKind = "diagrams"
)

// FragmentKinds returns every supported fragment kind in stable execution order.
func FragmentKinds() []FragmentKind {
	return []FragmentKind{
		FragmentOverviewCandidate,
		FragmentArchitecture,
		FragmentInterfaces,
		FragmentDataModels,
		FragmentWorkflows,
		FragmentDependencies,
		FragmentReviewGaps,
		FragmentDiagrams,
	}
}

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
	Kind             RequestKind        `json:"kind"`
	ComponentKey     string             `json:"component_key"`
	BatchIndex       int                `json:"batch_index"`
	BatchCount       int                `json:"batch_count"`
	FragmentKind     FragmentKind       `json:"fragment_kind,omitempty"`
	SourceBatchIndex int                `json:"source_batch_index,omitempty"`
	SourceBatchCount int                `json:"source_batch_count,omitempty"`
	SourceChunkIndex int                `json:"source_chunk_index,omitempty"`
	SourceChunkCount int                `json:"source_chunk_count,omitempty"`
	SourceSplitPath  string             `json:"source_split_path,omitempty"`
	PromptVersion    string             `json:"prompt_version"`
	SchemaName       string             `json:"schema_name"`
	Messages         []Message          `json:"-"`
	Schema           []byte             `json:"-"`
	Settings         GenerationSettings `json:"-"`
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

// CompletionFailureCategory identifies a response-envelope or completion failure
// without retaining provider or model prose.
type CompletionFailureCategory string

const (
	CompletionFailureTruncated        CompletionFailureCategory = "truncated"
	CompletionFailureIncomplete       CompletionFailureCategory = "incomplete"
	CompletionFailureEmpty            CompletionFailureCategory = "empty"
	CompletionFailureInvalidEnvelope  CompletionFailureCategory = "invalid_envelope"
	CompletionFailureInvalidUTF8      CompletionFailureCategory = "invalid_utf8"
	CompletionFailureResponseTooLarge CompletionFailureCategory = "response_too_large"
)

// CompletionError is a safe typed failure for a provider response that cannot be
// treated as complete model output. It deliberately contains no partial response body.
type CompletionError struct {
	Category             CompletionFailureCategory `json:"category"`
	RequestKind          RequestKind               `json:"request_kind,omitempty"`
	ComponentKey         string                    `json:"component_key,omitempty"`
	BatchIndex           int                       `json:"batch_index,omitempty"`
	BatchCount           int                       `json:"batch_count,omitempty"`
	FragmentKind         FragmentKind              `json:"fragment_kind,omitempty"`
	SourceBatchIndex     int                       `json:"source_batch_index,omitempty"`
	SourceBatchCount     int                       `json:"source_batch_count,omitempty"`
	SourceChunkIndex     int                       `json:"source_chunk_index,omitempty"`
	SourceChunkCount     int                       `json:"source_chunk_count,omitempty"`
	SourceSplitPath      string                    `json:"source_split_path,omitempty"`
	FinishReason         string                    `json:"finish_reason,omitempty"`
	ProviderRequestID    string                    `json:"provider_request_id,omitempty"`
	StructuredOutputUsed StructuredOutputMode      `json:"structured_output_used,omitempty"`
	TransportAttempts    int                       `json:"transport_attempts"`
	ResponseLimitBytes   int64                     `json:"response_limit_bytes,omitempty"`
}

func (e *CompletionError) Error() string {
	target := "llm response"
	if e.ComponentKey != "" {
		target = fmt.Sprintf("llm %s request for %q", e.RequestKind, e.ComponentKey)
	}
	switch e.Category {
	case CompletionFailureTruncated:
		return target + " was truncated at the output token limit"
	case CompletionFailureIncomplete:
		return target + " did not complete"
	case CompletionFailureEmpty:
		return target + " contained no content"
	case CompletionFailureInvalidEnvelope:
		return target + " envelope is invalid"
	case CompletionFailureInvalidUTF8:
		return target + " content is not valid UTF-8"
	case CompletionFailureResponseTooLarge:
		return fmt.Sprintf("%s content exceeds the %d-byte limit", target, e.ResponseLimitBytes)
	default:
		return target + " failed completion validation"
	}
}

// ExitCode classifies completion failures with other intentional generation failures.
func (e *CompletionError) ExitCode() int { return 5 }

// TransportError is a typed network failure. Cause is available for normal error
// unwrapping but excluded from structured diagnostics.
type TransportError struct {
	RequestKind          RequestKind          `json:"request_kind,omitempty"`
	ComponentKey         string               `json:"component_key,omitempty"`
	BatchIndex           int                  `json:"batch_index,omitempty"`
	BatchCount           int                  `json:"batch_count,omitempty"`
	FragmentKind         FragmentKind         `json:"fragment_kind,omitempty"`
	SourceBatchIndex     int                  `json:"source_batch_index,omitempty"`
	SourceBatchCount     int                  `json:"source_batch_count,omitempty"`
	SourceChunkIndex     int                  `json:"source_chunk_index,omitempty"`
	SourceChunkCount     int                  `json:"source_chunk_count,omitempty"`
	SourceSplitPath      string               `json:"source_split_path,omitempty"`
	StructuredOutputUsed StructuredOutputMode `json:"structured_output_used,omitempty"`
	TransportAttempts    int                  `json:"transport_attempts"`
	Cause                error                `json:"-"`
}

func (e *TransportError) Error() string {
	if e.ComponentKey != "" {
		return fmt.Sprintf("llm %s request for %q failed during transport: %v", e.RequestKind, e.ComponentKey, e.Cause)
	}
	return fmt.Sprintf("llm transport failure: %v", e.Cause)
}

func (e *TransportError) Unwrap() error { return e.Cause }

const promptJSONSchemaPrefix = "Trusted JSON output contract: return JSON only and conform exactly to this schema. Do not repeat the schema.\n"

// PromptJSONSchemaMessage is the single schema-injection primitive used by the
// adapter and request-size planning for prompt-JSON structured output.
func PromptJSONSchemaMessage(schema []byte) Message {
	return Message{Role: RoleSystem, Content: promptJSONSchemaPrefix + string(schema)}
}
