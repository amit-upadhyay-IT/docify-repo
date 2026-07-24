package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	sharedmodel "docify-repo/internal/model"
)

const (
	defaultBaseURL      = "https://api.github.com"
	defaultTimeout      = 30 * time.Second
	defaultRetries      = 2
	defaultRetryBase    = 500 * time.Millisecond
	maxRetryBackoff     = 20 * time.Second
	maxResponseEnvelope = 4 << 20 // 4 MiB response ceiling
	apiVersionHeader    = "2022-11-28"
	userAgent           = "docify-repo"
)

// Options configure the pull-request client. TokenSource is required for live use.
type Options struct {
	BaseURL        string
	TokenSource    TokenSource
	Timeout        time.Duration
	Retries        int
	RetryBaseDelay time.Duration
}

// Client is the GitHub REST pull-request publisher.
type Client struct {
	baseURL        string
	tokens         TokenSource
	client         *http.Client
	retries        int
	retryBaseDelay time.Duration
}

// New builds a client. The HTTP client rejects redirects so the credential can never be
// forwarded to another origin.
func New(options Options) *Client {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	retries := options.Retries
	if retries < 0 {
		retries = 0
	} else if options.Retries == 0 {
		retries = defaultRetries
	}
	retryBaseDelay := options.RetryBaseDelay
	if retryBaseDelay <= 0 {
		retryBaseDelay = defaultRetryBase
	}
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: baseURL,
		tokens:  options.TokenSource,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return fmt.Errorf("redirects are disabled")
			},
		},
		retries:        retries,
		retryBaseDelay: retryBaseDelay,
	}
}

// pullRequestPayload is the subset of the GitHub pull-request representation the publisher
// relies on. Extra fields in the response are ignored.
type pullRequestPayload struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// FindOpenPullRequest returns the first open pull request whose head is the given branch,
// regardless of its base, so the caller can detect and correct a wrong-base pull request.
func (c *Client) FindOpenPullRequest(ctx context.Context, query sharedmodel.PullRequestQuery) (sharedmodel.PullRequest, bool, error) {
	owner, repo, err := splitRepository(query.Repository)
	if err != nil {
		return sharedmodel.PullRequest{}, false, err
	}
	if !validBranch(query.Head) {
		return sharedmodel.PullRequest{}, false, fmt.Errorf("invalid head branch")
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=100&head=%s",
		c.baseURL, owner, repo, url.QueryEscape(owner+":"+query.Head))

	status, body, err := c.send(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return sharedmodel.PullRequest{}, false, err
	}
	if status != http.StatusOK {
		return sharedmodel.PullRequest{}, false, statusError("list pull requests", status)
	}
	var payloads []pullRequestPayload
	if err := json.Unmarshal(body, &payloads); err != nil {
		return sharedmodel.PullRequest{}, false, fmt.Errorf("decode pull-request list")
	}
	for _, payload := range payloads {
		if payload.Head.Ref == query.Head {
			return sharedmodel.PullRequest{
				Number: payload.Number,
				State:  payload.State,
				Base:   payload.Base.Ref,
				Head:   payload.Head.Ref,
			}, true, nil
		}
	}
	return sharedmodel.PullRequest{}, false, nil
}

// CreatePullRequest opens a pull request with the fixed head, base, title, and body.
func (c *Client) CreatePullRequest(ctx context.Context, content sharedmodel.PullRequestContent) (sharedmodel.PullRequest, error) {
	owner, repo, err := splitRepository(content.Repository)
	if err != nil {
		return sharedmodel.PullRequest{}, err
	}
	if !validBranch(content.Head) || !validBranch(content.Base) {
		return sharedmodel.PullRequest{}, fmt.Errorf("invalid pull-request branches")
	}
	requestBody, err := json.Marshal(map[string]string{
		"title": content.Title,
		"head":  content.Head,
		"base":  content.Base,
		"body":  content.Body,
	})
	if err != nil {
		return sharedmodel.PullRequest{}, fmt.Errorf("encode pull-request creation")
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls", c.baseURL, owner, repo)
	status, body, err := c.send(ctx, http.MethodPost, endpoint, requestBody)
	if err != nil {
		return sharedmodel.PullRequest{}, err
	}
	if status != http.StatusCreated {
		return sharedmodel.PullRequest{}, statusError("create pull request", status)
	}
	var payload pullRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return sharedmodel.PullRequest{}, fmt.Errorf("decode created pull request")
	}
	return sharedmodel.PullRequest{
		Number: payload.Number,
		State:  payload.State,
		Base:   payload.Base.Ref,
		Head:   payload.Head.Ref,
	}, nil
}

// UpdatePullRequest sets the title, body, and base of an existing pull request. Updating the
// base repairs a pull request that was opened against the wrong base branch.
func (c *Client) UpdatePullRequest(ctx context.Context, number int, content sharedmodel.PullRequestContent) error {
	owner, repo, err := splitRepository(content.Repository)
	if err != nil {
		return err
	}
	if number <= 0 {
		return fmt.Errorf("invalid pull-request number")
	}
	if !validBranch(content.Base) {
		return fmt.Errorf("invalid base branch")
	}
	requestBody, err := json.Marshal(map[string]string{
		"title": content.Title,
		"body":  content.Body,
		"base":  content.Base,
	})
	if err != nil {
		return fmt.Errorf("encode pull-request update")
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.baseURL, owner, repo, number)
	status, _, err := c.send(ctx, http.MethodPatch, endpoint, requestBody)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return statusError("update pull request", status)
	}
	return nil
}

// send performs one HTTP request with bounded retries for 429 and 5xx responses. It returns
// the final status and bounded body. The credential and request/response bodies never appear
// in returned errors.
func (c *Client) send(ctx context.Context, method, endpoint string, body []byte) (int, []byte, error) {
	if c.tokens == nil {
		return 0, nil, fmt.Errorf("github client has no credential source")
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return 0, nil, err
	}
	if err := requireHTTPS(endpoint); err != nil {
		return 0, nil, err
	}

	for attempt := 0; ; attempt++ {
		status, raw, retryable, attemptErr := c.attempt(ctx, method, endpoint, token, body)
		if attemptErr == nil && !retryable {
			return status, raw, nil
		}
		if attempt >= c.retries {
			if attemptErr != nil {
				return 0, nil, attemptErr
			}
			return status, raw, nil
		}
		if err := c.wait(ctx, attempt); err != nil {
			return 0, nil, err
		}
	}
}

func (c *Client) attempt(ctx context.Context, method, endpoint, token string, body []byte) (int, []byte, bool, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, false, fmt.Errorf("build github request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", apiVersionHeader)
	request.Header.Set("User-Agent", userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	// The credential is attached only here, on the outbound request.
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := c.client.Do(request)
	if err != nil {
		return 0, nil, true, fmt.Errorf("github transport failure")
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseEnvelope+1))
	if err != nil {
		return 0, nil, true, fmt.Errorf("read github response")
	}
	if int64(len(raw)) > maxResponseEnvelope {
		return 0, nil, false, fmt.Errorf("github response envelope exceeds %d bytes", maxResponseEnvelope)
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return response.StatusCode, raw, true, nil
	}
	return response.StatusCode, raw, false, nil
}

func (c *Client) wait(ctx context.Context, attempt int) error {
	delay := c.retryBaseDelay * time.Duration(attempt+1)
	if delay > maxRetryBackoff {
		delay = maxRetryBackoff
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

func splitRepository(repository string) (string, string, error) {
	repository = strings.TrimSpace(repository)
	owner, repo, ok := strings.Cut(repository, "/")
	if !ok || !validRepositorySegment(owner) || !validRepositorySegment(repo) || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("repository must be in owner/name form")
	}
	return owner, repo, nil
}

func validRepositorySegment(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '-', character == '_', character == '.':
		default:
			return false
		}
	}
	return true
}

func validBranch(value string) bool {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	return true
}

func requireHTTPS(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid github endpoint")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
		return fmt.Errorf("github endpoint must use https")
	}
	return nil
}

func isLoopback(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// statusError produces a safe error for a non-success status without the response body.
func statusError(action string, status int) error {
	return fmt.Errorf("%s returned HTTP %d", action, status)
}
