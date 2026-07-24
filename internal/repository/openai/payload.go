package openai

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	sharedmodel "docify-repo/internal/model"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *schemaEnvelope `json:"json_schema,omitempty"`
}

type schemaEnvelope struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responsesRequest struct {
	Model           string         `json:"model"`
	Temperature     float64        `json:"temperature"`
	MaxOutputTokens int            `json:"max_output_tokens"`
	Input           []chatMessage  `json:"input"`
	Text            *responsesText `json:"text,omitempty"`
}

type responsesText struct {
	Format *responsesFormat `json:"format,omitempty"`
}

type responsesFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

func buildBody(request sharedmodel.GenerationRequest, structured sharedmodel.StructuredOutputMode) ([]byte, error) {
	messages := make([]chatMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, chatMessage{Role: string(message.Role), Content: message.Content})
	}

	if request.Settings.APIMode == sharedmodel.APIModeResponses {
		payload := responsesRequest{
			Model:           request.Settings.Model,
			Temperature:     request.Settings.Temperature,
			MaxOutputTokens: request.Settings.MaxOutputTokens,
			Input:           messages,
			Text:            responsesTextFormat(structured, request),
		}
		return json.Marshal(payload)
	}

	payload := chatRequest{
		Model:          request.Settings.Model,
		Temperature:    request.Settings.Temperature,
		MaxTokens:      request.Settings.MaxOutputTokens,
		Messages:       messages,
		ResponseFormat: chatResponseFormat(structured, request),
	}
	return json.Marshal(payload)
}

func chatResponseFormat(structured sharedmodel.StructuredOutputMode, request sharedmodel.GenerationRequest) *responseFormat {
	switch structured {
	case sharedmodel.StructuredOutputJSONSchema:
		return &responseFormat{
			Type: "json_schema",
			JSONSchema: &schemaEnvelope{
				Name:   schemaName(request),
				Strict: true,
				Schema: json.RawMessage(request.Schema),
			},
		}
	default:
		return &responseFormat{Type: "json_object"}
	}
}

func responsesTextFormat(structured sharedmodel.StructuredOutputMode, request sharedmodel.GenerationRequest) *responsesText {
	switch structured {
	case sharedmodel.StructuredOutputJSONSchema:
		return &responsesText{Format: &responsesFormat{
			Type: "json_schema", Name: schemaName(request), Strict: true, Schema: json.RawMessage(request.Schema),
		}}
	default:
		return &responsesText{Format: &responsesFormat{Type: "json_object"}}
	}
}

func schemaName(request sharedmodel.GenerationRequest) string {
	if request.SchemaName != "" {
		return request.SchemaName
	}
	return "component_dossier"
}

// Provider response envelopes.

type chatResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *usageBlock  `json:"usage"`
}

type chatChoice struct {
	FinishReason string      `json:"finish_reason"`
	Message      chatMessage `json:"message"`
}

type responsesResponse struct {
	ID     string          `json:"id"`
	Output []responsesItem `json:"output"`
	Status string          `json:"status"`
	Usage  *responsesUsage `json:"usage"`
}

type responsesItem struct {
	Content []responsesContent `json:"content"`
}

type responsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// normalizeResponse parses a provider envelope and extracts the model content. The
// envelope must be valid JSON; the extracted content may be invalid JSON so the
// usecase repair policy can run. Empty, non-UTF-8, transport-truncated (unparsable
// envelope), and over-limit responses fail here rather than being returned for repair.
func normalizeResponse(mode sharedmodel.APIMode, raw []byte, maxContent int64) (sharedmodel.GenerationResponse, error) {
	if mode == sharedmodel.APIModeResponses {
		return normalizeResponses(raw, maxContent)
	}
	return normalizeChat(raw, maxContent)
}

func normalizeChat(raw []byte, maxContent int64) (sharedmodel.GenerationResponse, error) {
	var envelope chatResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return sharedmodel.GenerationResponse{}, fmt.Errorf("llm response envelope is not valid JSON")
	}
	if len(envelope.Choices) == 0 {
		return sharedmodel.GenerationResponse{}, fmt.Errorf("llm response contained no choices")
	}
	choice := envelope.Choices[0]
	content, err := boundedContent(choice.Message.Content, choice.FinishReason, maxContent)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	response := sharedmodel.GenerationResponse{
		Body:              content,
		FinishReason:      choice.FinishReason,
		ProviderRequestID: envelope.ID,
	}
	if envelope.Usage != nil {
		response.Usage = sharedmodel.TokenUsage{
			PromptTokens: envelope.Usage.PromptTokens, CompletionTokens: envelope.Usage.CompletionTokens,
			TotalTokens: envelope.Usage.TotalTokens, Present: true,
		}
	}
	return response, nil
}

func normalizeResponses(raw []byte, maxContent int64) (sharedmodel.GenerationResponse, error) {
	var envelope responsesResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return sharedmodel.GenerationResponse{}, fmt.Errorf("llm response envelope is not valid JSON")
	}
	text := ""
	for _, item := range envelope.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" || part.Type == "text" {
				text += part.Text
			}
		}
	}
	finishReason := envelope.Status
	content, err := boundedContent(text, finishReason, maxContent)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	response := sharedmodel.GenerationResponse{
		Body:              content,
		FinishReason:      finishReason,
		ProviderRequestID: envelope.ID,
	}
	if envelope.Usage != nil {
		response.Usage = sharedmodel.TokenUsage{
			PromptTokens: envelope.Usage.InputTokens, CompletionTokens: envelope.Usage.OutputTokens,
			TotalTokens: envelope.Usage.TotalTokens, Present: true,
		}
	}
	return response, nil
}

func boundedContent(content, finishReason string, maxContent int64) ([]byte, error) {
	if finishReason == "length" {
		return nil, fmt.Errorf("llm response was truncated at the output token limit")
	}
	if content == "" {
		return nil, fmt.Errorf("llm response contained no content")
	}
	if int64(len(content)) > maxContent {
		return nil, fmt.Errorf("llm response content exceeds the %d-byte limit", maxContent)
	}
	if !utf8.ValidString(content) {
		return nil, fmt.Errorf("llm response content is not valid UTF-8")
	}
	return []byte(content), nil
}
