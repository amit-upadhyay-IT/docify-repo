package openai

import (
	"bytes"
	"encoding/json"
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
	capacity := len(request.Messages)
	if structured == sharedmodel.StructuredOutputPromptJSON {
		capacity++
	}
	messages := make([]chatMessage, 0, capacity)
	schemaInserted := false
	for _, message := range request.Messages {
		if structured == sharedmodel.StructuredOutputPromptJSON && !schemaInserted && message.Role != sharedmodel.RoleSystem {
			schemaMessage := sharedmodel.PromptJSONSchemaMessage(request.Schema)
			messages = append(messages, chatMessage{Role: string(schemaMessage.Role), Content: schemaMessage.Content})
			schemaInserted = true
		}
		messages = append(messages, chatMessage{Role: string(message.Role), Content: message.Content})
	}
	if structured == sharedmodel.StructuredOutputPromptJSON && !schemaInserted {
		schemaMessage := sharedmodel.PromptJSONSchemaMessage(request.Schema)
		messages = append(messages, chatMessage{Role: string(schemaMessage.Role), Content: schemaMessage.Content})
	}

	if request.Settings.APIMode == sharedmodel.APIModeResponses {
		payload := responsesRequest{
			Model:           request.Settings.Model,
			Temperature:     request.Settings.Temperature,
			MaxOutputTokens: request.Settings.MaxOutputTokens,
			Input:           messages,
			Text:            responsesTextFormat(structured, request),
		}
		return marshalProviderBody(payload)
	}

	payload := chatRequest{
		Model:          request.Settings.Model,
		Temperature:    request.Settings.Temperature,
		MaxTokens:      request.Settings.MaxOutputTokens,
		Messages:       messages,
		ResponseFormat: chatResponseFormat(structured, request),
	}
	return marshalProviderBody(payload)
}

func marshalProviderBody(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
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
	ID                string                     `json:"id"`
	Output            []responsesItem            `json:"output"`
	Status            string                     `json:"status"`
	IncompleteDetails *responsesIncompleteDetail `json:"incomplete_details"`
	Usage             *responsesUsage            `json:"usage"`
}

type responsesIncompleteDetail struct {
	Reason string `json:"reason"`
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
		return sharedmodel.GenerationResponse{}, &sharedmodel.CompletionError{Category: sharedmodel.CompletionFailureInvalidEnvelope}
	}
	if len(envelope.Choices) == 0 {
		return sharedmodel.GenerationResponse{}, &sharedmodel.CompletionError{
			Category: sharedmodel.CompletionFailureEmpty, ProviderRequestID: safeProviderRequestIDValue(envelope.ID),
		}
	}
	choice := envelope.Choices[0]
	finishReason := safeCompletionReason(choice.FinishReason)
	if choice.FinishReason != "stop" {
		category := sharedmodel.CompletionFailureIncomplete
		if choice.FinishReason == "length" {
			category = sharedmodel.CompletionFailureTruncated
		}
		return sharedmodel.GenerationResponse{}, &sharedmodel.CompletionError{
			Category: category, FinishReason: finishReason, ProviderRequestID: safeProviderRequestIDValue(envelope.ID),
		}
	}
	content, err := boundedContent(choice.Message.Content, finishReason, safeProviderRequestIDValue(envelope.ID), maxContent)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	response := sharedmodel.GenerationResponse{
		Body:              content,
		FinishReason:      finishReason,
		ProviderRequestID: safeProviderRequestIDValue(envelope.ID),
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
		return sharedmodel.GenerationResponse{}, &sharedmodel.CompletionError{Category: sharedmodel.CompletionFailureInvalidEnvelope}
	}
	if envelope.Status != "completed" {
		category := sharedmodel.CompletionFailureIncomplete
		finishReason := safeCompletionReason(envelope.Status)
		if envelope.IncompleteDetails != nil {
			finishReason = safeCompletionReason(envelope.IncompleteDetails.Reason)
			if envelope.Status == "incomplete" && envelope.IncompleteDetails.Reason == "max_output_tokens" {
				category = sharedmodel.CompletionFailureTruncated
			}
		}
		return sharedmodel.GenerationResponse{}, &sharedmodel.CompletionError{
			Category: category, FinishReason: finishReason, ProviderRequestID: safeProviderRequestIDValue(envelope.ID),
		}
	}
	text := ""
	for _, item := range envelope.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" || part.Type == "text" {
				text += part.Text
			}
		}
	}
	finishReason := safeCompletionReason(envelope.Status)
	content, err := boundedContent(text, finishReason, safeProviderRequestIDValue(envelope.ID), maxContent)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	response := sharedmodel.GenerationResponse{
		Body:              content,
		FinishReason:      finishReason,
		ProviderRequestID: safeProviderRequestIDValue(envelope.ID),
	}
	if envelope.Usage != nil {
		response.Usage = sharedmodel.TokenUsage{
			PromptTokens: envelope.Usage.InputTokens, CompletionTokens: envelope.Usage.OutputTokens,
			TotalTokens: envelope.Usage.TotalTokens, Present: true,
		}
	}
	return response, nil
}

func boundedContent(content, finishReason, providerRequestID string, maxContent int64) ([]byte, error) {
	if content == "" {
		return nil, &sharedmodel.CompletionError{
			Category: sharedmodel.CompletionFailureEmpty, FinishReason: finishReason, ProviderRequestID: providerRequestID,
		}
	}
	if int64(len(content)) > maxContent {
		return nil, &sharedmodel.CompletionError{
			Category: sharedmodel.CompletionFailureResponseTooLarge, FinishReason: finishReason,
			ProviderRequestID: providerRequestID, ResponseLimitBytes: maxContent,
		}
	}
	if !utf8.ValidString(content) {
		return nil, &sharedmodel.CompletionError{
			Category: sharedmodel.CompletionFailureInvalidUTF8, FinishReason: finishReason, ProviderRequestID: providerRequestID,
		}
	}
	return []byte(content), nil
}
