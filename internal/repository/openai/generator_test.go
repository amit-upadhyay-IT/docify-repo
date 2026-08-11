package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sharedmodel "docify-repo/internal/model"
)

func sampleRequest(mode sharedmodel.APIMode, structured sharedmodel.StructuredOutputMode) sharedmodel.GenerationRequest {
	return sharedmodel.GenerationRequest{
		Kind:          sharedmodel.RequestComponent,
		ComponentKey:  "services/api",
		PromptVersion: "codebase-summary/v1",
		SchemaName:    "component_dossier",
		Schema:        []byte(`{"type":"object"}`),
		Settings: sharedmodel.GenerationSettings{
			Model: "gemini-1.5-flash", Temperature: 0, MaxOutputTokens: 1024,
			APIMode: mode, StructuredOutputMode: structured,
		},
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: "system instructions"},
			{Role: sharedmodel.RoleUser, Content: "analyze this payload"},
		},
	}
}

func chatSuccessBody(t *testing.T, content string) string {
	t.Helper()
	body, err := json.Marshal(chatResponse{
		ID:      "req-123",
		Choices: []chatChoice{{FinishReason: "stop", Message: chatMessage{Role: "assistant", Content: content}}},
		Usage:   &usageBlock{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	})
	if err != nil {
		t.Fatalf("marshal chat body: %v", err)
	}
	return string(body)
}

func fastGenerator(server *httptest.Server, options Options) *Generator {
	options.BaseURL = server.URL
	if options.TokenSource == nil {
		options.TokenSource = NewStaticTokenSource("test-token")
	}
	options.RetryBaseDelay = time.Millisecond
	return New(options)
}

func TestGenerateChatCompletionSuccess(t *testing.T) {
	var gotAuth, gotPath, gotFormatType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		var payload map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &payload)
		if format, ok := payload["response_format"].(map[string]any); ok {
			gotFormatType, _ = format["type"].(string)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatSuccessBody(t, `{"title":"API"}`))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	response, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputAuto))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if string(response.Body) != `{"title":"API"}` {
		t.Fatalf("body = %q", response.Body)
	}
	if !response.Usage.Present || response.Usage.TotalTokens != 30 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if response.ProviderRequestID != "req-123" {
		t.Fatalf("request id = %q", response.ProviderRequestID)
	}
	if response.StructuredOutputUsed != sharedmodel.StructuredOutputJSONSchema {
		t.Fatalf("structured output = %q, want json_schema", response.StructuredOutputUsed)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotFormatType != "json_schema" {
		t.Fatalf("response_format type = %q, want json_schema first", gotFormatType)
	}
}

func TestGeneratePreservesBasePath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, chatSuccessBody(t, `{"title":"API"}`))
	}))
	defer server.Close()

	generator := New(Options{
		BaseURL:     server.URL + "/v1/openai",
		TokenSource: NewStaticTokenSource("test-token"),
	})
	if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if gotPath != "/v1/openai/chat/completions" {
		t.Fatalf("path = %q, want configured base path preserved", gotPath)
	}
}

func TestGenerateAutoFallsBackToPromptJSON(t *testing.T) {
	var formats []string
	var fallbackMessages []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &payload)
		format, _ := payload["response_format"].(map[string]any)
		formatType, _ := format["type"].(string)
		formats = append(formats, formatType)
		if formatType == "json_schema" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"json_schema is not supported"}`)
			return
		}
		fallbackMessages, _ = payload["messages"].([]map[string]any)
		if fallbackMessages == nil {
			if messages, ok := payload["messages"].([]any); ok {
				for _, message := range messages {
					decoded, _ := message.(map[string]any)
					fallbackMessages = append(fallbackMessages, decoded)
				}
			}
		}
		_, _ = io.WriteString(w, chatSuccessBody(t, `{"title":"API"}`))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	response, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputAuto))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.StructuredOutputUsed != sharedmodel.StructuredOutputPromptJSON {
		t.Fatalf("structured output = %q, want prompt_json after fallback", response.StructuredOutputUsed)
	}
	if len(formats) != 2 || formats[0] != "json_schema" || formats[1] != "json_object" {
		t.Fatalf("format sequence = %v, want json_schema then json_object", formats)
	}
	if response.TransportAttempts < 2 {
		t.Fatalf("transport attempts = %d, want the fallback counted", response.TransportAttempts)
	}
	wantSchemaMessage := sharedmodel.PromptJSONSchemaMessage(sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputAuto).Schema).Content
	if len(fallbackMessages) != 3 || fallbackMessages[1]["role"] != "system" || fallbackMessages[1]["content"] != wantSchemaMessage {
		t.Fatalf("fallback messages = %#v, want exact trusted schema message", fallbackMessages)
	}
}

func TestGeneratePromptJSONIncludesExactSchemaForResponses(t *testing.T) {
	request := sampleRequest(sharedmodel.APIModeResponses, sharedmodel.StructuredOutputPromptJSON)
	var input []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &payload)
		input, _ = payload["input"].([]any)
		_, _ = io.WriteString(w, `{"id":"resp-1","status":"completed","output":[{"content":[{"type":"output_text","text":"{\"title\":\"API\"}"}]}]}`)
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	if _, err := generator.Generate(context.Background(), request); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(input) != 3 {
		t.Fatalf("input messages = %d, want original two plus schema", len(input))
	}
	schemaMessage, _ := input[1].(map[string]any)
	if got, want := schemaMessage["content"], sharedmodel.PromptJSONSchemaMessage(request.Schema).Content; got != want {
		t.Fatalf("schema message = %q, want %q", got, want)
	}
}

func TestBuildBodyDisablesHTMLExpansion(t *testing.T) {
	request := sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputPromptJSON)
	request.Messages[1].Content = "<>&"
	body, err := buildBody(request, sharedmodel.StructuredOutputPromptJSON)
	if err != nil {
		t.Fatalf("buildBody() error = %v", err)
	}
	if strings.Contains(string(body), `\u003c`) || strings.Contains(string(body), `\u003e`) || strings.Contains(string(body), `\u0026`) {
		t.Fatalf("provider body applies HTML expansion: %s", body)
	}
}

func TestGenerateDoesNotFallbackForGenericClientError(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"message":"max_tokens is invalid","param":"max_tokens","code":"invalid_value"}}`)
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputAuto)); err == nil {
		t.Fatal("expected provider status error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("HTTP calls = %d, want no structured-output fallback", got)
	}
}

func TestGenerateCountsRetriesAcrossStructuredFallback(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		var payload map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &payload)
		format, _ := payload["response_format"].(map[string]any)
		if format["type"] == "json_schema" {
			if call == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"json_schema is not supported","param":"response_format","code":"unsupported_value"}}`)
			return
		}
		_, _ = io.WriteString(w, chatSuccessBody(t, `{"title":"API"}`))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{Retries: 1})
	response, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputAuto))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.TransportAttempts != 3 || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("attempt metadata=%d HTTP calls=%d, want exactly 3", response.TransportAttempts, calls)
	}
}

func TestGenerateCountsAllAttemptsOnTruncatedFallback(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		var payload map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &payload)
		format, _ := payload["response_format"].(map[string]any)
		if format["type"] == "json_schema" {
			if call == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"json_schema is not supported"}`)
			return
		}
		_, _ = io.WriteString(w, chatBodyWithFinish(t, "partial", "length"))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{Retries: 1})
	_, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputAuto))
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) {
		t.Fatalf("error = %v, want CompletionError", err)
	}
	if completionErr.TransportAttempts != 3 || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("attempt metadata=%d HTTP calls=%d, want exactly 3", completionErr.TransportAttempts, calls)
	}
	if completionErr.StructuredOutputUsed != sharedmodel.StructuredOutputPromptJSON {
		t.Fatalf("structured mode = %q, want prompt_json", completionErr.StructuredOutputUsed)
	}
}

func TestGenerateRetriesOnRateLimit(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow down"}`)
			return
		}
		_, _ = io.WriteString(w, chatSuccessBody(t, `{"title":"API"}`))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{Retries: 2})
	response, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d, want one retry", calls)
	}
	if response.TransportAttempts != 2 {
		t.Fatalf("transport attempts = %d, want 2", response.TransportAttempts)
	}
}

func TestGenerateRetriesOnServerError(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, chatSuccessBody(t, `{"title":"API"}`))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{Retries: 3})
	if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d, want one retry then success", calls)
	}
}

func TestGenerateRejectsMalformedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not json at all")
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)); err == nil {
		t.Fatal("expected error for malformed envelope")
	}
}

func TestGenerateRejectsTruncatedAndEmptyContent(t *testing.T) {
	cases := map[string]string{
		"truncated": chatBodyWithFinish(t, "partial", "length"),
		"empty":     chatBodyWithFinish(t, "", "stop"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			generator := fastGenerator(server, Options{})
			if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)); err == nil {
				t.Fatalf("expected error for %s content", name)
			}
		})
	}
}

func TestGenerateReturnsTypedChatTruncationWithoutPartialContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "header-request-id")
		_, _ = io.WriteString(w, chatBodyWithFinish(t, "partial-secret-content", "length"))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	request := sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)
	request.Kind = sharedmodel.RequestFragment
	request.FragmentKind = sharedmodel.FragmentArchitecture
	request.SourceBatchIndex, request.SourceBatchCount = 2, 3
	request.SourceChunkIndex, request.SourceChunkCount = 1, 2
	request.SourceSplitPath = "b2/c1/0"
	_, err := generator.Generate(context.Background(), request)
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) {
		t.Fatalf("error = %v, want CompletionError", err)
	}
	if completionErr.Category != sharedmodel.CompletionFailureTruncated || completionErr.FinishReason != "length" {
		t.Fatalf("completion error = %+v", completionErr)
	}
	if completionErr.ProviderRequestID != "req-1" || completionErr.TransportAttempts != 1 {
		t.Fatalf("completion metadata = %+v", completionErr)
	}
	if completionErr.StructuredOutputUsed != sharedmodel.StructuredOutputJSONSchema || completionErr.ComponentKey != "services/api" {
		t.Fatalf("request metadata = %+v", completionErr)
	}
	if completionErr.FragmentKind != sharedmodel.FragmentArchitecture || completionErr.SourceBatchIndex != 2 ||
		completionErr.SourceBatchCount != 3 || completionErr.SourceChunkIndex != 1 || completionErr.SourceChunkCount != 2 ||
		completionErr.SourceSplitPath != "b2/c1/0" {
		t.Fatalf("fragment metadata = %+v", completionErr)
	}
	if strings.Contains(completionErr.Error(), "partial-secret-content") {
		t.Fatalf("typed error leaked partial content: %v", completionErr)
	}
}

func TestGenerateReturnsTypedResponsesTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"resp-truncated","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"content":[{"type":"output_text","text":"partial-secret-content"}]}]}`)
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	_, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeResponses, sharedmodel.StructuredOutputJSONSchema))
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) {
		t.Fatalf("error = %v, want CompletionError", err)
	}
	if completionErr.Category != sharedmodel.CompletionFailureTruncated || completionErr.FinishReason != "max_output_tokens" {
		t.Fatalf("completion error = %+v", completionErr)
	}
	if completionErr.ProviderRequestID != "resp-truncated" || strings.Contains(completionErr.Error(), "partial-secret-content") {
		t.Fatalf("unsafe or missing completion metadata: %+v", completionErr)
	}
}

func TestGenerateDoesNotMisclassifyFailedResponsesAsTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"resp-failed","status":"failed","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`)
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	_, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeResponses, sharedmodel.StructuredOutputJSONSchema))
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) {
		t.Fatalf("error = %v, want CompletionError", err)
	}
	if completionErr.Category != sharedmodel.CompletionFailureIncomplete || completionErr.FinishReason != "max_output_tokens" {
		t.Fatalf("completion error = %+v, want non-truncation incomplete failure", completionErr)
	}
}

func TestGenerateRejectsOtherIncompleteStatuses(t *testing.T) {
	tests := map[string]struct {
		mode sharedmodel.APIMode
		body string
	}{
		"chat content filter": {
			mode: sharedmodel.APIModeChatCompletions,
			body: chatBodyWithFinish(t, `{"title":"partial"}`, "content_filter"),
		},
		"chat unknown finish": {
			mode: sharedmodel.APIModeChatCompletions,
			body: chatBodyWithFinish(t, `{"title":"partial"}`, "provider-prose-reason"),
		},
		"responses missing status": {
			mode: sharedmodel.APIModeResponses,
			body: `{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"{\"title\":\"partial\"}"}]}]}`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()

			generator := fastGenerator(server, Options{})
			_, err := generator.Generate(context.Background(), sampleRequest(test.mode, sharedmodel.StructuredOutputJSONSchema))
			var completionErr *sharedmodel.CompletionError
			if !errors.As(err, &completionErr) || completionErr.Category != sharedmodel.CompletionFailureIncomplete {
				t.Fatalf("error = %v, want incomplete CompletionError", err)
			}
			if strings.Contains(completionErr.FinishReason, "provider-prose") {
				t.Fatalf("unsafe finish reason = %q", completionErr.FinishReason)
			}
		})
	}
}

func TestGenerateDropsUnsafeEnvelopeRequestID(t *testing.T) {
	unsafeID := strings.Repeat("provider prose ", 30)
	body, err := json.Marshal(chatResponse{
		ID: unsafeID, Choices: []chatChoice{{FinishReason: "length", Message: chatMessage{Content: "partial"}}},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	_, err = generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema))
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) || completionErr.ProviderRequestID != "" {
		t.Fatalf("completion error = %+v, want unsafe request ID omitted", completionErr)
	}
}

func TestGeneratePreservesAttemptsWhenRetryWaitIsCancelled(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	generator := New(Options{
		BaseURL: server.URL, TokenSource: NewStaticTokenSource("test-token"), Retries: 2, RetryBaseDelay: time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := generator.Generate(ctx, sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema))
	var transportErr *sharedmodel.TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("error = %v, want TransportError", err)
	}
	if transportErr.TransportAttempts != 1 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("attempt metadata=%d HTTP calls=%d, want exactly 1", transportErr.TransportAttempts, calls)
	}
}

func TestGenerateUsesSafeHeaderRequestIDWhenEnvelopeHasNone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "header-request-id")
		_, _ = io.WriteString(w, chatBodyWithFinishNoID(t, "partial", "length"))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{})
	_, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema))
	var completionErr *sharedmodel.CompletionError
	if !errors.As(err, &completionErr) || completionErr.ProviderRequestID != "header-request-id" {
		t.Fatalf("completion error = %+v, want safe header request ID", completionErr)
	}
}

func TestGenerateRejectsOverLimitContent(t *testing.T) {
	big := strings.Repeat("x", 2048)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, chatSuccessBody(t, big))
	}))
	defer server.Close()

	generator := fastGenerator(server, Options{MaxContentBytes: 1024})
	if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)); err == nil {
		t.Fatal("expected over-limit content to be rejected")
	}
}

func TestGenerateDisablesRedirectsAndDoesNotLeakAuthorization(t *testing.T) {
	var foreignAuth atomic.Value
	foreignAuth.Store("")
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth.Store(r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, chatSuccessBody(t, `{"title":"API"}`))
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/chat/completions", http.StatusFound)
	}))
	defer origin.Close()

	generator := New(Options{BaseURL: origin.URL, TokenSource: NewStaticTokenSource("secret-token"), Retries: 0, RetryBaseDelay: time.Millisecond})
	_, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema))
	if err == nil {
		t.Fatal("expected redirects to be disabled")
	}
	var transportErr *sharedmodel.TransportError
	if !errors.As(err, &transportErr) || transportErr.TransportAttempts != 1 {
		t.Fatalf("error = %v, want typed one-attempt transport failure", err)
	}
	if got := foreignAuth.Load().(string); got != "" {
		t.Fatalf("foreign origin received authorization %q, want none", got)
	}
}

func TestGenerateErrorsRedactCredentialAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"secret-diagnostic-body"}`)
	}))
	defer server.Close()

	generator := New(Options{BaseURL: server.URL, TokenSource: NewStaticTokenSource("super-secret-token")})
	_, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema))
	if err == nil {
		t.Fatal("expected error for HTTP 400")
	}
	if strings.Contains(err.Error(), "super-secret-token") || strings.Contains(err.Error(), "secret-diagnostic-body") {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
}

func TestGenerateMissingCredentialFailsBeforeRequest(t *testing.T) {
	var called int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
	}))
	defer server.Close()

	generator := New(Options{BaseURL: server.URL, TokenSource: NewEnvTokenSource("DOCIFY_LLM_API_KEY_ABSENT_FOR_TEST")})
	if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)); err == nil {
		t.Fatal("expected missing-credential error")
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("no HTTP request should be made without a credential")
	}
}

func chatBodyWithFinish(t *testing.T, content, finish string) string {
	t.Helper()
	body, err := json.Marshal(chatResponse{
		ID:      "req-1",
		Choices: []chatChoice{{FinishReason: finish, Message: chatMessage{Role: "assistant", Content: content}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

func chatBodyWithFinishNoID(t *testing.T, content, finish string) string {
	t.Helper()
	body, err := json.Marshal(chatResponse{
		Choices: []chatChoice{{FinishReason: finish, Message: chatMessage{Role: "assistant", Content: content}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}
