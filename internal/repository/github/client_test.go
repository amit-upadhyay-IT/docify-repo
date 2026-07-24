package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func newTestClient(handler http.HandlerFunc) (*Client, *httptest.Server) {
	server := httptest.NewServer(handler)
	client := New(Options{BaseURL: server.URL, TokenSource: NewStaticTokenSource("test-token")})
	return client, server
}

func TestFindOpenPullRequestReturnsMatchingHead(t *testing.T) {
	var authorization, query string
	client, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		query = r.URL.RawQuery
		if r.Method != http.MethodGet || r.URL.Path != "/repos/octo/repo/pulls" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `[{"number":7,"state":"open","base":{"ref":"main"},"head":{"ref":"docify/docs"}}]`)
	})
	defer server.Close()

	found, ok, err := client.FindOpenPullRequest(context.Background(), sharedmodel.PullRequestQuery{Repository: "octo/repo", Head: "docify/docs"})
	if err != nil {
		t.Fatalf("FindOpenPullRequest() error = %v", err)
	}
	if !ok || found.Number != 7 || found.Base != "main" || found.Head != "docify/docs" {
		t.Fatalf("found = %+v ok=%t, want #7 base main", found, ok)
	}
	if authorization != "Bearer test-token" {
		t.Errorf("authorization = %q, want a bearer credential", authorization)
	}
	if !strings.Contains(query, "state=open") || !strings.Contains(query, "head=octo") {
		t.Errorf("query = %q, want an open head filter scoped to the owner", query)
	}
}

func TestFindOpenPullRequestReportsWrongBase(t *testing.T) {
	client, server := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"number":9,"state":"open","base":{"ref":"develop"},"head":{"ref":"docify/docs"}}]`)
	})
	defer server.Close()

	found, ok, err := client.FindOpenPullRequest(context.Background(), sharedmodel.PullRequestQuery{Repository: "octo/repo", Head: "docify/docs"})
	if err != nil || !ok {
		t.Fatalf("FindOpenPullRequest() = %+v ok=%t err=%v", found, ok, err)
	}
	if found.Base != "develop" {
		t.Errorf("found.Base = %q, want the actual (wrong) base so the caller can repair it", found.Base)
	}
}

func TestFindOpenPullRequestReturnsNotFound(t *testing.T) {
	client, server := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	})
	defer server.Close()

	_, ok, err := client.FindOpenPullRequest(context.Background(), sharedmodel.PullRequestQuery{Repository: "octo/repo", Head: "docify/docs"})
	if err != nil {
		t.Fatalf("FindOpenPullRequest() error = %v", err)
	}
	if ok {
		t.Error("ok = true, want no open pull request found")
	}
}

func TestCreatePullRequestSendsFixedContent(t *testing.T) {
	var body map[string]string
	client, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/octo/repo/pulls" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number":12,"state":"open","base":{"ref":"main"},"head":{"ref":"docify/docs"}}`)
	})
	defer server.Close()

	created, err := client.CreatePullRequest(context.Background(), sharedmodel.PullRequestContent{
		Repository: "octo/repo", Head: "docify/docs", Base: "main",
		Title: "docs: synchronize generated documentation", Body: "body",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest() error = %v", err)
	}
	if created.Number != 12 {
		t.Errorf("created number = %d, want 12", created.Number)
	}
	if body["head"] != "docify/docs" || body["base"] != "main" || body["title"] == "" {
		t.Errorf("create payload = %+v, want fixed head/base/title", body)
	}
}

func TestUpdatePullRequestRepairsBase(t *testing.T) {
	var body map[string]string
	client, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/octo/repo/pulls/7" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"number":7,"state":"open","base":{"ref":"main"},"head":{"ref":"docify/docs"}}`)
	})
	defer server.Close()

	if err := client.UpdatePullRequest(context.Background(), 7, sharedmodel.PullRequestContent{
		Repository: "octo/repo", Head: "docify/docs", Base: "main", Title: "t", Body: "b",
	}); err != nil {
		t.Fatalf("UpdatePullRequest() error = %v", err)
	}
	if body["base"] != "main" {
		t.Errorf("update payload base = %q, want main so a wrong base is corrected", body["base"])
	}
}

func TestCreatePullRequestSurfacesStatusError(t *testing.T) {
	client, server := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"No commits between main and docify/docs"}`)
	})
	defer server.Close()

	_, err := client.CreatePullRequest(context.Background(), sharedmodel.PullRequestContent{
		Repository: "octo/repo", Head: "docify/docs", Base: "main", Title: "t", Body: "b",
	})
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("CreatePullRequest() error = %v, want an HTTP 422 status error", err)
	}
	// The response body (which could echo attacker-influenced branch text) must not leak.
	if strings.Contains(err.Error(), "No commits between") {
		t.Errorf("error leaks the response body: %v", err)
	}
}

func TestRepositoryMustBeOwnerName(t *testing.T) {
	client := New(Options{TokenSource: NewStaticTokenSource("t")})
	if _, _, err := client.FindOpenPullRequest(context.Background(), sharedmodel.PullRequestQuery{Repository: "not-a-repo", Head: "b"}); err == nil {
		t.Error("FindOpenPullRequest accepted a malformed repository")
	}
}

func TestNonLoopbackHTTPEndpointRejected(t *testing.T) {
	client := New(Options{BaseURL: "http://api.example.com", TokenSource: NewStaticTokenSource("t")})
	if _, _, err := client.FindOpenPullRequest(context.Background(), sharedmodel.PullRequestQuery{Repository: "octo/repo", Head: "b"}); err == nil {
		t.Error("plain-HTTP GitHub endpoint on a public host must be rejected")
	}
}
