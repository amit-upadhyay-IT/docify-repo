package openai

import (
	"context"
	"encoding/json"
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
	if _, err := generator.Generate(context.Background(), sampleRequest(sharedmodel.APIModeChatCompletions, sharedmodel.StructuredOutputJSONSchema)); err == nil {
		t.Fatal("expected redirects to be disabled")
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
