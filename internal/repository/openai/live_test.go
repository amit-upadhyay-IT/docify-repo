package openai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	sharedmodel "docify-repo/internal/model"
)

// TestLiveGeminiCompatibility is an opt-in smoke test against a real OpenAI-compatible
// endpoint (e.g. Gemini Flash / Flash-Lite). It runs only when the endpoint, model, and
// credential are supplied through environment variables, so ordinary CI stays
// deterministic and offline.
//
//	DOCIFY_LIVE_LLM_BASE_URL   base URL, e.g. https://generativelanguage.googleapis.com/v1beta/openai
//	DOCIFY_LIVE_LLM_MODEL      model id, e.g. gemini-1.5-flash
//	DOCIFY_LLM_API_KEY         bearer credential
//	DOCIFY_LIVE_LLM_API_MODE   optional: chat_completions (default) or responses
//	DOCIFY_LIVE_LLM_STRUCTURED optional: auto (default), json_schema, or prompt_json
func TestLiveGeminiCompatibility(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("DOCIFY_LIVE_LLM_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("DOCIFY_LIVE_LLM_MODEL"))
	if baseURL == "" || model == "" || os.Getenv(CredentialEnvVar) == "" {
		t.Skipf("set DOCIFY_LIVE_LLM_BASE_URL, DOCIFY_LIVE_LLM_MODEL, and %s to run the live compatibility test", CredentialEnvVar)
	}

	apiMode := sharedmodel.APIModeChatCompletions
	if os.Getenv("DOCIFY_LIVE_LLM_API_MODE") == "responses" {
		apiMode = sharedmodel.APIModeResponses
	}
	structured := sharedmodel.StructuredOutputAuto
	switch os.Getenv("DOCIFY_LIVE_LLM_STRUCTURED") {
	case "json_schema":
		structured = sharedmodel.StructuredOutputJSONSchema
	case "prompt_json":
		structured = sharedmodel.StructuredOutputPromptJSON
	}

	schema := []byte(`{
		"type":"object","additionalProperties":false,
		"required":["title","items"],
		"properties":{
			"title":{"type":"string"},
			"items":{"type":"array","maxItems":5,"items":{"type":"string"}}
		}
	}`)

	generator := New(Options{
		BaseURL:     baseURL,
		TokenSource: NewEnvTokenSource(CredentialEnvVar),
		Timeout:     30 * time.Second,
		Retries:     2,
	})

	request := sharedmodel.GenerationRequest{
		Kind:          sharedmodel.RequestComponent,
		ComponentKey:  "live/smoke",
		PromptVersion: "codebase-summary/v1",
		SchemaName:    "smoke",
		Schema:        schema,
		Settings: sharedmodel.GenerationSettings{
			Model: model, Temperature: 0, MaxOutputTokens: 512,
			APIMode: apiMode, StructuredOutputMode: structured,
		},
		Messages: []sharedmodel.Message{
			{Role: sharedmodel.RoleSystem, Content: "Return only a JSON object matching the supplied schema. No prose."},
			{Role: sharedmodel.RoleUser, Content: `Produce {"title":"ok","items":["a","b"]} exactly.`},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	response, err := generator.Generate(ctx, request)
	if err != nil {
		t.Fatalf("live Generate() error = %v", err)
	}
	if !json.Valid(response.Body) {
		t.Fatalf("live response body is not valid JSON: %q", response.Body)
	}
	var decoded struct {
		Title string   `json:"title"`
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatalf("decode live response: %v", err)
	}
	if decoded.Title == "" {
		t.Fatalf("live response missing title: %q", response.Body)
	}
	t.Logf("live response: structured=%s attempts=%d usage_present=%t", response.StructuredOutputUsed, response.TransportAttempts, response.Usage.Present)
}
