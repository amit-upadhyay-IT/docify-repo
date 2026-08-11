package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	documentationmodel "docify-repo/internal/app/documentation/model"
	"docify-repo/internal/app/documentation/usecase"
	sharedmodel "docify-repo/internal/model"
	filesystemrepository "docify-repo/internal/repository/filesystem"
	gitrepository "docify-repo/internal/repository/git"
)

// fakePullRequests is an in-memory PullRequestPublisher recording lookups, creations, and
// updates so publishing behavior can be asserted without contacting GitHub.
type fakePullRequests struct {
	byHead    map[string]sharedmodel.PullRequest
	next      int
	createErr error
	finds     int
	creates   int
	updates   int
}

func newFakePullRequests() *fakePullRequests {
	return &fakePullRequests{byHead: map[string]sharedmodel.PullRequest{}}
}

func (f *fakePullRequests) FindOpenPullRequest(_ context.Context, query sharedmodel.PullRequestQuery) (sharedmodel.PullRequest, bool, error) {
	f.finds++
	pr, ok := f.byHead[query.Head]
	if !ok || pr.State != "open" {
		return sharedmodel.PullRequest{}, false, nil
	}
	return pr, true, nil
}

func (f *fakePullRequests) CreatePullRequest(_ context.Context, content sharedmodel.PullRequestContent) (sharedmodel.PullRequest, error) {
	f.creates++
	if f.createErr != nil {
		return sharedmodel.PullRequest{}, f.createErr
	}
	f.next++
	pr := sharedmodel.PullRequest{Number: f.next, State: "open", Base: content.Base, Head: content.Head}
	f.byHead[content.Head] = pr
	return pr, nil
}

func (f *fakePullRequests) UpdatePullRequest(_ context.Context, number int, content sharedmodel.PullRequestContent) error {
	f.updates++
	if pr, ok := f.byHead[content.Head]; ok {
		pr.Base = content.Base
		f.byHead[content.Head] = pr
	}
	return nil
}

// fakeGitPublisher lets a test drive the publisher through generation to the push step with a
// scripted outcome, without a real remote.
type fakeGitPublisher struct {
	tip           string
	commitChanged bool
	prepareErr    error
	pushErr       error
	pushes        int
}

func (f *fakeGitPublisher) PrepareDocumentationBranch(_ context.Context, _ sharedmodel.BranchPreparation) (sharedmodel.PreparedBranch, error) {
	if f.prepareErr != nil {
		return sharedmodel.PreparedBranch{}, f.prepareErr
	}
	return sharedmodel.PreparedBranch{Existed: false, Tip: f.tip}, nil
}

func (f *fakeGitPublisher) CommitDocumentation(_ context.Context, _ sharedmodel.DocumentationCommit) (sharedmodel.CommitResult, error) {
	return sharedmodel.CommitResult{Commit: f.tip, Changed: f.commitChanged}, nil
}

func (f *fakeGitPublisher) PushDocumentationBranch(_ context.Context, _ sharedmodel.PushSpec) error {
	f.pushes++
	return f.pushErr
}

// publishFixture holds the persistent state shared across simulated CI events: the bare
// origin, a driver clone used to advance the base branch, the call-counting generator, and
// the in-memory pull-request store.
type publishFixture struct {
	origin    string
	driver    string
	generator *scriptedGenerator
	prs       *fakePullRequests
}

func newPublishFixture(t *testing.T) *publishFixture {
	t.Helper()
	origin := gitTempDir(t)
	runGit(t, origin, "init", "--bare", "--quiet")
	hardenRepo(t, origin)
	driver := gitTempDir(t)
	runGit(t, driver, "init", "--quiet")
	runGit(t, driver, "config", "user.email", "test@example.com")
	runGit(t, driver, "config", "user.name", "Test")
	hardenRepo(t, driver)
	writeFile(t, driver, "go.mod", "module example.test/app\n\ngo 1.26\n")
	writeFile(t, driver, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, driver, "services/api/service.go", "package api\n\nfunc Handle() {}\n")
	writeFile(t, driver, "services/api/service_test.go", "package api\n")
	runGit(t, driver, "add", "--all")
	runGit(t, driver, "commit", "--quiet", "-m", "initial")
	runGit(t, driver, "branch", "-M", "main")
	runGit(t, driver, "remote", "add", "origin", origin)
	runGit(t, driver, "push", "--quiet", "-u", "origin", "main")
	runGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	return &publishFixture{origin: origin, driver: driver, generator: &scriptedGenerator{}, prs: newFakePullRequests()}
}

func (fixture *publishFixture) head(t *testing.T) string {
	t.Helper()
	return gitOutput(t, fixture.driver, "rev-parse", "HEAD")
}

// advanceMain commits a base-branch change and pushes it, returning the new head SHA.
func (fixture *publishFixture) advanceMain(t *testing.T, relative, content string) string {
	t.Helper()
	runGit(t, fixture.driver, "checkout", "--quiet", "main")
	writeFile(t, fixture.driver, relative, content)
	runGit(t, fixture.driver, "add", "--all")
	runGit(t, fixture.driver, "commit", "--quiet", "-m", "change")
	runGit(t, fixture.driver, "push", "--quiet", "origin", "main")
	return fixture.head(t)
}

// run performs one simulated CI event: a fresh checkout of the triggering commit (like
// actions/checkout) plus a publisher run against the shared origin and pull-request store.
func (fixture *publishFixture) run(t *testing.T, head, base string) (documentationmodel.ResultSummary, error) {
	t.Helper()
	clone := gitTempDir(t)
	runGit(t, clone, "clone", "--quiet", fixture.origin, ".")
	runGit(t, clone, "config", "user.email", "test@example.com")
	runGit(t, clone, "config", "user.name", "Test")
	hardenRepo(t, clone)
	runGit(t, clone, "checkout", "--quiet", head)
	gitRepository := gitrepository.New(gitrepository.Options{WorkingDirectory: clone})
	application := usecase.New(
		gitRepository,
		filesystemrepository.NewSourceRepository(),
		filesystemrepository.NewStateRepository(),
		fixture.generator,
		filesystemrepository.NewOutputRepository(),
		usecase.WithPublisher(gitRepository, fixture.prs),
	)
	return application.Sync(context.Background(), fixture.publishInput(clone, head, base))
}

func (fixture *publishFixture) publishInput(dir, head, base string) documentationmodel.SyncInput {
	input := syncInput(dir)
	input.Publisher = "github-pr"
	input.GitHubRepository = "octo/repo"
	input.BaseBranch = "main"
	input.HeadSHA = head
	input.BaseSHA = base
	input.GitHubCredentialPresent = true
	return input
}

func (fixture *publishFixture) documentationBranch(t *testing.T) string {
	t.Helper()
	refs := gitOutput(t, fixture.origin, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	for _, name := range strings.Fields(refs) {
		if strings.HasPrefix(name, "docify/generated-docs-main-") {
			return name
		}
	}
	t.Fatalf("no documentation branch was created; refs=%q", refs)
	return ""
}

func TestPublishBootstrapCreatesBranchAndPullRequest(t *testing.T) {
	fixture := newPublishFixture(t)
	head := fixture.head(t)

	result, err := fixture.run(t, head, head)
	if err != nil {
		t.Fatalf("bootstrap publish error = %v", err)
	}
	if result.Status != "synced" {
		t.Fatalf("status = %q, want synced", result.Status)
	}
	if fixture.generator.calls.Load() == 0 {
		t.Error("bootstrap made no model call")
	}
	branch := fixture.documentationBranch(t)
	// The branch exists on the origin and carries the generated documentation.
	tree := gitOutput(t, fixture.origin, "ls-tree", "-r", "--name-only", branch)
	if !strings.Contains(tree, "docs/generated/index.md") || !strings.Contains(tree, ".docify/state.json") {
		t.Errorf("documentation branch is missing generated output; tree=\n%s", tree)
	}
	if fixture.prs.creates != 1 {
		t.Errorf("pull-request creates = %d, want 1", fixture.prs.creates)
	}
	if pr := fixture.prs.byHead[branch]; pr.Base != "main" {
		t.Errorf("pull request base = %q, want main", pr.Base)
	}
}

func TestPublishReusesBranchAndPullRequestAcrossEvents(t *testing.T) {
	fixture := newPublishFixture(t)
	head0 := fixture.head(t)
	if _, err := fixture.run(t, head0, head0); err != nil {
		t.Fatalf("first publish error = %v", err)
	}
	afterFirst := fixture.generator.calls.Load()
	branch := fixture.documentationBranch(t)
	firstTip := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/"+branch)

	// A second base-branch event changes only services/api.
	fixture.generator.variant = "2"
	head1 := fixture.advanceMain(t, "services/api/service.go", "package api\n\nfunc Handle() { /* v2 */ }\n")
	result, err := fixture.run(t, head1, head0)
	if err != nil {
		t.Fatalf("second publish error = %v", err)
	}
	if result.Status != "synced" {
		t.Fatalf("second status = %q, want synced", result.Status)
	}
	if delta := fixture.generator.calls.Load() - afterFirst; delta != 1 {
		t.Fatalf("second event regenerated %d components, want exactly 1", delta)
	}
	// One branch, one pull request: created once, updated on the second event.
	if fixture.prs.creates != 1 || fixture.prs.updates == 0 {
		t.Errorf("pull-request lifecycle = creates %d updates %d, want one create then updates", fixture.prs.creates, fixture.prs.updates)
	}
	if second := fixture.documentationBranch(t); second != branch {
		t.Errorf("documentation branch changed from %q to %q", branch, second)
	}
	if secondTip := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/"+branch); secondTip == firstTip {
		t.Error("second event did not advance the documentation branch")
	}
}

func TestPublishNoopMakesNoCommitButReconcilesPullRequest(t *testing.T) {
	fixture := newPublishFixture(t)
	head := fixture.head(t)
	if _, err := fixture.run(t, head, head); err != nil {
		t.Fatalf("first publish error = %v", err)
	}
	afterFirst := fixture.generator.calls.Load()
	branch := fixture.documentationBranch(t)
	tipAfterFirst := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/"+branch)

	// Re-run the same event: nothing changed, so no generation, commit, or push occurs.
	result, err := fixture.run(t, head, head)
	if err != nil {
		t.Fatalf("no-op publish error = %v", err)
	}
	if result.Status != "noop" {
		t.Fatalf("status = %q, want noop", result.Status)
	}
	if delta := fixture.generator.calls.Load() - afterFirst; delta != 0 {
		t.Fatalf("no-op event made %d model calls, want 0", delta)
	}
	if tip := gitOutput(t, fixture.origin, "rev-parse", "refs/heads/"+branch); tip != tipAfterFirst {
		t.Errorf("no-op event advanced the documentation branch from %q to %q", tipAfterFirst, tip)
	}
	// The existing pull request is still reconciled (found and refreshed), never duplicated.
	if fixture.prs.creates != 1 {
		t.Errorf("pull-request creates = %d, want 1", fixture.prs.creates)
	}
	if fixture.prs.updates == 0 {
		t.Error("no-op event did not reconcile the existing pull request")
	}
}

func TestPublishRetainsPendingChangesAcrossNewerBaseEvent(t *testing.T) {
	fixture := newPublishFixture(t)
	head0 := fixture.head(t)
	if _, err := fixture.run(t, head0, head0); err != nil {
		t.Fatalf("first publish error = %v", err)
	}
	branch := fixture.documentationBranch(t)
	rootPage := "docs/generated/components/@root/index.md"
	apiPage := "docs/generated/components/services/api/index.md"
	rootBefore := gitOutput(t, fixture.origin, "show", "refs/heads/"+branch+":"+rootPage)
	apiBefore := gitOutput(t, fixture.origin, "show", "refs/heads/"+branch+":"+apiPage)

	// A newer base event touches only services/api while the documentation PR is still open.
	fixture.generator.variant = "2"
	head1 := fixture.advanceMain(t, "services/api/service.go", "package api\n\nfunc Handle() { /* v2 */ }\n")
	if _, err := fixture.run(t, head1, head0); err != nil {
		t.Fatalf("second publish error = %v", err)
	}

	rootAfter := gitOutput(t, fixture.origin, "show", "refs/heads/"+branch+":"+rootPage)
	apiAfter := gitOutput(t, fixture.origin, "show", "refs/heads/"+branch+":"+apiPage)
	if rootAfter != rootBefore {
		t.Error("merging a newer base event altered the unrelated @root component page")
	}
	if apiAfter == apiBefore {
		t.Error("the changed services/api component page was not regenerated")
	}
}

func TestPublishRetryAfterPullRequestFailureDoesNotRegenerate(t *testing.T) {
	fixture := newPublishFixture(t)
	head := fixture.head(t)

	// First event: the push succeeds but creating the pull request fails.
	fixture.prs.createErr = errors.New("pull-request API unavailable")
	failed, err := fixture.run(t, head, head)
	if err == nil {
		t.Fatal("expected the pull-request failure to surface")
	}
	var coder exitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 7 {
		t.Fatalf("error = %v, want a publishing failure with exit code 7", err)
	}
	if failed.Status != "publish_failed" || failed.Failure == nil || failed.Failure.Category != "publish" || failed.Generation == nil {
		t.Fatalf("failed publish summary = %+v, want safe partial metadata", failed)
	}
	branch := fixture.documentationBranch(t) // the branch was pushed before the PR step failed
	_ = branch
	afterFirst := fixture.generator.calls.Load()

	// Retry: the branch already carries the generated docs, so nothing regenerates, but the
	// missing pull request is created.
	fixture.prs.createErr = nil
	result, err := fixture.run(t, head, head)
	if err != nil {
		t.Fatalf("retry publish error = %v", err)
	}
	if result.Status != "noop" {
		t.Fatalf("retry status = %q, want noop", result.Status)
	}
	if delta := fixture.generator.calls.Load() - afterFirst; delta != 0 {
		t.Fatalf("retry regenerated %d components, want 0", delta)
	}
	if fixture.prs.byHead[fixture.documentationBranch(t)].Number == 0 {
		t.Error("retry did not create the missing pull request")
	}
}

func TestPublishNonFastForwardFailsWithoutForce(t *testing.T) {
	dir := gitTempDir(t)
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	hardenRepo(t, dir)
	writeFile(t, dir, "go.mod", "module example.test/app\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	runGit(t, dir, "add", "--all")
	runGit(t, dir, "commit", "--quiet", "-m", "initial")
	head := gitOutput(t, dir, "rev-parse", "HEAD")

	generator := &scriptedGenerator{}
	prs := newFakePullRequests()
	gitRepository := gitrepository.New(gitrepository.Options{WorkingDirectory: dir})
	rejectingGit := &fakeGitPublisher{tip: head, commitChanged: true, pushErr: sharedmodel.ErrNonFastForward}
	application := usecase.New(
		gitRepository,
		filesystemrepository.NewSourceRepository(),
		filesystemrepository.NewStateRepository(),
		generator,
		filesystemrepository.NewOutputRepository(),
		usecase.WithPublisher(rejectingGit, prs),
	)
	input := documentationmodel.SyncInput{
		WorkingDirectory: dir, Output: documentationmodel.OutputModeHuman, Publisher: "github-pr",
		GitHubRepository: "octo/repo", BaseBranch: "main", HeadSHA: head, BaseSHA: head, GitHubCredentialPresent: true,
		SourcePolicy:     syncInput(dir).SourcePolicy,
		ComponentPolicy:  syncInput(dir).ComponentPolicy,
		GenerationPolicy: syncInput(dir).GenerationPolicy,
	}

	result, err := application.Sync(context.Background(), input)
	if err == nil {
		t.Fatal("expected a non-fast-forward push to fail the run")
	}
	var coder exitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 7 {
		t.Fatalf("error = %v, want a publishing failure with exit code 7", err)
	}
	if result.Status != "publish_failed" || result.Failure == nil || result.Failure.Category != "publish" {
		t.Fatalf("failed push summary = %+v, want safe partial metadata", result)
	}
	if !strings.Contains(err.Error(), "fast-forward") {
		t.Errorf("error = %v, want a non-fast-forward explanation", err)
	}
	if rejectingGit.pushes != 1 {
		t.Errorf("push attempts = %d, want exactly 1 (no forced retry)", rejectingGit.pushes)
	}
	if prs.creates != 0 {
		t.Error("a rejected push must not create a pull request")
	}
}

func TestPublishPreparationFailureReturnsSafePartialSummary(t *testing.T) {
	dir := gitTempDir(t)
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	hardenRepo(t, dir)
	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "--all")
	runGit(t, dir, "commit", "--quiet", "-m", "initial")
	head := gitOutput(t, dir, "rev-parse", "HEAD")
	generator := &scriptedGenerator{}
	application := usecase.New(
		gitrepository.New(gitrepository.Options{WorkingDirectory: dir}), filesystemrepository.NewSourceRepository(),
		filesystemrepository.NewStateRepository(), generator, filesystemrepository.NewOutputRepository(),
		usecase.WithPublisher(&fakeGitPublisher{prepareErr: errors.New("prepare failed")}, newFakePullRequests()),
	)
	input := syncInput(dir)
	input.Publisher = "github-pr"
	input.GitHubRepository = "octo/repo"
	input.BaseBranch = "main"
	input.HeadSHA = head
	input.BaseSHA = head
	input.GitHubCredentialPresent = true

	result, err := application.Sync(context.Background(), input)
	if err == nil {
		t.Fatal("expected preparation failure")
	}
	if result.Command != "sync" || result.Status != "publish_failed" || result.Failure == nil || result.Failure.Category != "publish" {
		t.Fatalf("result = %+v, want safe publish failure", result)
	}
	if generator.calls.Load() != 0 {
		t.Fatalf("preparation failure made %d model calls", generator.calls.Load())
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal failed summary: %v", marshalErr)
	}
	for _, expected := range []string{`"files":[]`, `"components":[]`, `"changes":[]`, `"affected_components":[]`, `"deleted_documents":[]`} {
		if !strings.Contains(string(data), expected) {
			t.Errorf("failed summary JSON missing stable array %s: %s", expected, data)
		}
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append(hardenedGitArgs(directory), arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", arguments[0], err, output)
	}
	return strings.TrimSpace(string(output))
}
