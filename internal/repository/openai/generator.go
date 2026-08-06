// Package openai implements an OpenAI-compatible chat-completions and responses
// adapter behind the usecase Generator interface. It translates endpoint-neutral
// generation requests into HTTP requests, attaches the bearer credential only while
// constructing the outbound request, and normalizes responses and usage. It never
// logs or returns request bodies, response bodies, or authorization headers.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sharedmodel "docify-repo/internal/model"
)

const (
	defaultTimeout         = 60 * time.Second
	defaultRetryBaseDelay  = 500 * time.Millisecond
	maxBackoff             = 20 * time.Second
	maxRetryAfter          = 20 * time.Second
	defaultMaxContent      = 128 << 10 // 128 KiB of model content
	envelopeHardLimit      = 8 << 20   // 8 MiB provider-envelope memory ceiling
	providerErrorLimit     = 16 << 10  // bounded metadata used only for fallback classification
	providerRequestIDLimit = 256
)

// Options configure the adapter. BaseURL and TokenSource are required for live use.
type Options struct {
	BaseURL                 string
	TokenSource             TokenSource
	Timeout                 time.Duration
	Retries                 int
	RetryBaseDelay          time.Duration
	MaxContentBytes         int64
	AllowSameOriginRedirect bool
}

// Generator is the OpenAI-compatible adapter.
type Generator struct {
	baseURL         string
	tokens          TokenSource
	client          *http.Client
	retries         int
	retryBaseDelay  time.Duration
	maxContentBytes int64
}

// New builds an adapter. The HTTP client honors only the configured endpoint and the
// explicit redirect policy; redirects are disabled unless same-origin is allowed, and
// authorization is never forwarded to a different origin.
func New(options Options) *Generator {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	retryBaseDelay := options.RetryBaseDelay
	if retryBaseDelay < 0 {
		retryBaseDelay = defaultRetryBaseDelay
	}
	maxContent := options.MaxContentBytes
	if maxContent <= 0 {
		maxContent = defaultMaxContent
	}
	allowRedirect := options.AllowSameOriginRedirect
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if !allowRedirect {
				return fmt.Errorf("redirects are disabled")
			}
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			origin := via[0].URL
			if request.URL.Scheme != origin.Scheme || request.URL.Host != origin.Host {
				return fmt.Errorf("cross-origin redirect is not allowed")
			}
			// Same origin: Go preserves headers. Cross origin is rejected above, so the
			// Authorization header can never reach a different origin.
			return nil
		},
	}
	return &Generator{
		baseURL:         strings.TrimRight(options.BaseURL, "/"),
		tokens:          options.TokenSource,
		client:          client,
		retries:         options.Retries,
		retryBaseDelay:  retryBaseDelay,
		maxContentBytes: maxContent,
	}
}

// Generate sends one request and returns a normalized response. In auto mode it tries
// the provider JSON-schema format first and falls back once to prompt JSON when the
// provider rejects the schema feature.
func (g *Generator) Generate(ctx context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error) {
	if g.tokens == nil {
		return sharedmodel.GenerationResponse{}, fmt.Errorf("generator has no credential source")
	}
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}
	endpoint, err := g.endpoint(request.Settings.APIMode)
	if err != nil {
		return sharedmodel.GenerationResponse{}, err
	}

	modes := modeSequence(request.Settings.StructuredOutputMode)
	totalAttempts := 0
	var lastErr error
	for index, structured := range modes {
		body, err := buildBody(request, structured)
		if err != nil {
			return sharedmodel.GenerationResponse{}, err
		}
		status, raw, attempts, headerRequestID, err := g.send(ctx, endpoint, token, body)
		totalAttempts += attempts
		if err != nil {
			var transportErr *sharedmodel.TransportError
			if errors.As(err, &transportErr) {
				failure := *transportErr
				failure.RequestKind = request.Kind
				failure.ComponentKey = request.ComponentKey
				failure.BatchIndex = request.BatchIndex
				failure.BatchCount = request.BatchCount
				failure.FragmentKind = request.FragmentKind
				failure.SourceBatchIndex = request.SourceBatchIndex
				failure.SourceBatchCount = request.SourceBatchCount
				failure.SourceChunkIndex = request.SourceChunkIndex
				failure.SourceChunkCount = request.SourceChunkCount
				failure.SourceSplitPath = request.SourceSplitPath
				failure.StructuredOutputUsed = structured
				failure.TransportAttempts = totalAttempts
				return sharedmodel.GenerationResponse{}, &failure
			}
			return sharedmodel.GenerationResponse{}, err
		}
		if status == http.StatusOK {
			response, err := normalizeResponse(request.Settings.APIMode, raw, g.maxContentBytes)
			if err != nil {
				var completionErr *sharedmodel.CompletionError
				if errors.As(err, &completionErr) {
					failure := *completionErr
					failure.RequestKind = request.Kind
					failure.ComponentKey = request.ComponentKey
					failure.BatchIndex = request.BatchIndex
					failure.BatchCount = request.BatchCount
					failure.FragmentKind = request.FragmentKind
					failure.SourceBatchIndex = request.SourceBatchIndex
					failure.SourceBatchCount = request.SourceBatchCount
					failure.SourceChunkIndex = request.SourceChunkIndex
					failure.SourceChunkCount = request.SourceChunkCount
					failure.SourceSplitPath = request.SourceSplitPath
					failure.StructuredOutputUsed = structured
					failure.TransportAttempts = totalAttempts
					if failure.ProviderRequestID == "" {
						failure.ProviderRequestID = headerRequestID
					}
					return sharedmodel.GenerationResponse{}, &failure
				}
				return sharedmodel.GenerationResponse{}, err
			}
			if response.ProviderRequestID == "" {
				response.ProviderRequestID = headerRequestID
			}
			response.StructuredOutputUsed = structured
			response.TransportAttempts = totalAttempts
			return response, nil
		}

		lastErr = statusError(request, status)
		if index+1 < len(modes) && unsupportedStructuredOutput(status, raw) {
			continue
		}
		return sharedmodel.GenerationResponse{}, lastErr
	}
	return sharedmodel.GenerationResponse{}, lastErr
}

// endpoint resolves the request URL without dropping a configured base path.
func (g *Generator) endpoint(mode sharedmodel.APIMode) (string, error) {
	suffix := "/chat/completions"
	if mode == sharedmodel.APIModeResponses {
		suffix = "/responses"
	}
	if g.baseURL == "" {
		return "", fmt.Errorf("llm base URL is not configured")
	}
	parsed, err := url.Parse(g.baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid llm base URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("llm base URL must be http or https")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + suffix
	return parsed.String(), nil
}

// send performs the HTTP request with bounded retries for network failures, HTTP 429,
// and retryable 5xx responses. It returns the final status, the bounded raw body, and
// the number of transport attempts made.
func (g *Generator) send(ctx context.Context, endpoint, token string, body []byte) (int, []byte, int, string, error) {
	attempts := 0
	for attempt := 0; attempt <= g.retries; attempt++ {
		attempts++
		status, raw, retryAfter, requestID, err := g.attempt(ctx, endpoint, token, body)
		if err == nil && status != http.StatusTooManyRequests && status < 500 {
			return status, raw, attempts, requestID, nil
		}
		if attempt == g.retries {
			if err != nil {
				return 0, nil, attempts, "", transportError(err)
			}
			return status, raw, attempts, requestID, nil
		}
		if err == nil && !retryableStatus(status) {
			return status, raw, attempts, requestID, nil
		}
		if waitErr := g.wait(ctx, attempt, retryAfter); waitErr != nil {
			return 0, nil, attempts, "", transportError(waitErr)
		}
	}
	return 0, nil, attempts, "", fmt.Errorf("llm request exhausted retries")
}

func (g *Generator) attempt(ctx context.Context, endpoint, token string, body []byte) (int, []byte, time.Duration, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, 0, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	// The bearer credential is attached only here, on the outbound request.
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := g.client.Do(request)
	if err != nil {
		return 0, nil, 0, "", err
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, envelopeHardLimit+1))
	if err != nil {
		return 0, nil, 0, "", err
	}
	if int64(len(raw)) > envelopeHardLimit {
		return 0, nil, 0, "", fmt.Errorf("llm response envelope exceeds %d bytes", envelopeHardLimit)
	}
	retryAfter := parseRetryAfter(response.Header.Get("Retry-After"))
	return response.StatusCode, raw, retryAfter, providerRequestID(response.Header), nil
}

func (g *Generator) wait(ctx context.Context, attempt int, retryAfter time.Duration) error {
	delay := g.retryBaseDelay * time.Duration(attempt+1)
	if delay > maxBackoff {
		delay = maxBackoff
	}
	if retryAfter > 0 {
		if retryAfter > maxRetryAfter {
			retryAfter = maxRetryAfter
		}
		delay = retryAfter
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func modeSequence(mode sharedmodel.StructuredOutputMode) []sharedmodel.StructuredOutputMode {
	switch mode {
	case sharedmodel.StructuredOutputJSONSchema:
		return []sharedmodel.StructuredOutputMode{sharedmodel.StructuredOutputJSONSchema}
	case sharedmodel.StructuredOutputPromptJSON:
		return []sharedmodel.StructuredOutputMode{sharedmodel.StructuredOutputPromptJSON}
	default:
		return []sharedmodel.StructuredOutputMode{sharedmodel.StructuredOutputJSONSchema, sharedmodel.StructuredOutputPromptJSON}
	}
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func unsupportedStructuredOutput(status int, raw []byte) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusNotImplemented:
	default:
		return false
	}
	if len(raw) == 0 || len(raw) > providerErrorLimit {
		return false
	}

	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Error) == 0 {
		return false
	}
	metadata := ""
	var message string
	if err := json.Unmarshal(envelope.Error, &message); err == nil {
		return unsupportedStructuredMessage(message)
	} else {
		var detail struct {
			Type    string `json:"type"`
			Param   string `json:"param"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(envelope.Error, &detail); err != nil {
			return false
		}
		parameter := strings.ToLower(strings.TrimSpace(detail.Param))
		code := strings.ToLower(strings.TrimSpace(detail.Code))
		parameterTargeted := parameter == "response_format" || parameter == "response_format.type" ||
			parameter == "response_format.json_schema" || parameter == "text.format" || parameter == "text.format.type"
		codeUnsupported := code == "unsupported_parameter" || code == "unsupported_value" ||
			code == "not_supported" || code == "unsupported_feature"
		if parameterTargeted && codeUnsupported {
			return true
		}
		metadata = detail.Message
	}
	return unsupportedStructuredMessage(metadata)
}

func unsupportedStructuredMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	patterns := []string{
		"json_schema is not supported", "json schema is not supported",
		"json_schema unsupported", "json schema unsupported",
		"does not support json_schema", "does not support json schema",
		"does not support response_format", "does not support response format",
		"response_format is not supported", "response format is not supported",
		"structured output is not supported", "structured outputs are not supported",
		"unknown parameter: response_format", "unknown parameter 'response_format'",
		"unrecognized parameter: response_format", "unrecognized parameter 'response_format'",
		"text.format is not supported",
	}
	for _, pattern := range patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func providerRequestID(header http.Header) string {
	for _, name := range []string{"X-Request-Id", "Request-Id", "OpenAI-Request-Id"} {
		if value := safeProviderRequestIDValue(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func safeProviderRequestIDValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > providerRequestIDLimit {
		return ""
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') &&
			character != '-' && character != '_' && character != '.' && character != ':' {
			return ""
		}
	}
	return value
}

func safeCompletionReason(value string) string {
	switch value {
	case "stop", "length", "content_filter", "tool_calls", "function_call",
		"completed", "incomplete", "failed", "cancelled", "max_output_tokens":
		return value
	case "":
		return "missing"
	default:
		return "unknown"
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

// statusError produces a safe error for a non-retryable, non-success status. It never
// includes the response body.
func statusError(request sharedmodel.GenerationRequest, status int) error {
	return fmt.Errorf("llm %s request for %q returned HTTP %d", request.Kind, request.ComponentKey, status)
}

// transportError wraps a network error without exposing the credential or body. Only
// the error's own text is preserved, which contains no request content.
func transportError(err error) error {
	return &sharedmodel.TransportError{Cause: err}
}
