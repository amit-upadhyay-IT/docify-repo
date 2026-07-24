package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sharedmodel "docify-repo/internal/model"
)

var testIdentity = sharedmodel.CommitIdentity{Name: "docify-repo", Email: "docify-repo@users.noreply.github.com"}

// newPublishFixture builds a bare origin plus a working clone on branch main with one source
// commit already pushed, and returns a repository bound to the clone plus the head SHA.
func newPublishFixture(t *testing.T) (repository *Repository, origin, clone, head string) {
	t.Helper()
	origin = gitTempDir(t)
	runGit(t, origin, "init", "--bare", "--quiet")
	hardenRepo(t, origin)
	clone = gitTempDir(t)
	runGit(t, clone, "init", "--quiet")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "config", "user.email", "test@example.com")
	runGit(t, clone, "config", "commit.gpgsign", "false")
	hardenRepo(t, clone)
	writeFile(t, filepath.Join(clone, "main.go"), "package main\n", 0o600)
	runGit(t, clone, "add", "--all")
	runGit(t, clone, "commit", "--quiet", "-m", "initial")
	runGit(t, clone, "branch", "-M", "main")
	runGit(t, clone, "remote", "add", "origin", origin)
	runGit(t, clone, "push", "--quiet", "origin", "main")
	head = strings.TrimSpace(runGitOutput(t, clone, "rev-parse", "HEAD"))
	repository = New(Options{WorkingDirectory: clone, Timeout: 20 * time.Second, PublishTimeout: 30 * time.Second})
	return repository, origin, clone, head
}

func writeNested(t *testing.T, root, relative, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, full, content, 0o600)
}

const docsBranch = "docify/generated-docs-main-0123456789ab"

func TestPrepareCreatesDocumentationBranchFromTriggeringCommit(t *testing.T) {
	repository, origin, clone, head := newPublishFixture(t)

	prepared, err := repository.PrepareDocumentationBranch(context.Background(), sharedmodel.BranchPreparation{
		Remote: origin, BaseBranch: "main", DocumentationBranch: docsBranch, TriggeringCommit: head, Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("PrepareDocumentationBranch() error = %v", err)
	}
	if prepared.Existed {
		t.Error("prepared.Existed = true, want false for a new branch")
	}
	if prepared.Tip != head {
		t.Errorf("prepared.Tip = %q, want the triggering commit %q", prepared.Tip, head)
	}
	if prepared.AheadOfBase {
		t.Error("a freshly created branch at the base tip must not be ahead of base")
	}
	if current := strings.TrimSpace(runGitOutput(t, clone, "rev-parse", "--abbrev-ref", "HEAD")); current != docsBranch {
		t.Errorf("worktree is on %q, want the documentation branch", current)
	}
}

func TestCommitDocumentationBuildsTemporaryIndexCommitAndPushes(t *testing.T) {
	repository, origin, clone, head := newPublishFixture(t)
	ctx := context.Background()
	prepared, err := repository.PrepareDocumentationBranch(ctx, sharedmodel.BranchPreparation{
		Remote: origin, BaseBranch: "main", DocumentationBranch: docsBranch, TriggeringCommit: head, Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("prepare error = %v", err)
	}

	writeNested(t, clone, "docs/generated/index.md", "# Docs\n")
	writeNested(t, clone, ".docify/state.json", "{}\n")

	commit, err := repository.CommitDocumentation(ctx, sharedmodel.DocumentationCommit{
		DocumentationBranch: docsBranch, ParentTip: prepared.Tip,
		Paths: []string{"docs/generated", ".docify/state.json"}, Message: "docs: synchronize generated documentation", Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("CommitDocumentation() error = %v", err)
	}
	if !commit.Changed || !validObjectID(commit.Commit) {
		t.Fatalf("commit = %+v, want a real change", commit)
	}

	// The commit tree carries the generated docs and the untouched source, and the branch ref
	// advanced to the new commit.
	tree := runGitOutput(t, clone, "ls-tree", "-r", "--name-only", docsBranch)
	for _, want := range []string{"main.go", "docs/generated/index.md", ".docify/state.json"} {
		if !strings.Contains(tree, want) {
			t.Errorf("commit tree missing %q; tree=\n%s", want, tree)
		}
	}
	if branchTip := strings.TrimSpace(runGitOutput(t, clone, "rev-parse", "refs/heads/"+docsBranch)); branchTip != commit.Commit {
		t.Errorf("branch tip = %q, want the new commit %q", branchTip, commit.Commit)
	}
	// The parent of the documentation commit is the prepared tip.
	if parent := strings.TrimSpace(runGitOutput(t, clone, "rev-parse", commit.Commit+"^")); parent != prepared.Tip {
		t.Errorf("commit parent = %q, want prepared tip %q", parent, prepared.Tip)
	}

	if err := repository.PushDocumentationBranch(ctx, sharedmodel.PushSpec{Remote: origin, DocumentationBranch: docsBranch, Commit: commit.Commit}); err != nil {
		t.Fatalf("PushDocumentationBranch() error = %v", err)
	}
	if originTip := strings.TrimSpace(runGitOutput(t, origin, "rev-parse", "refs/heads/"+docsBranch)); originTip != commit.Commit {
		t.Errorf("origin branch tip = %q, want %q", originTip, commit.Commit)
	}
}

func TestCommitDocumentationReportsNoChangeWhenTreeIsIdentical(t *testing.T) {
	repository, origin, clone, head := newPublishFixture(t)
	ctx := context.Background()
	prepared, err := repository.PrepareDocumentationBranch(ctx, sharedmodel.BranchPreparation{
		Remote: origin, BaseBranch: "main", DocumentationBranch: docsBranch, TriggeringCommit: head, Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("prepare error = %v", err)
	}
	writeNested(t, clone, "docs/generated/index.md", "# Docs\n")
	first, err := repository.CommitDocumentation(ctx, sharedmodel.DocumentationCommit{
		DocumentationBranch: docsBranch, ParentTip: prepared.Tip,
		Paths: []string{"docs/generated"}, Message: "docs: sync", Identity: testIdentity,
	})
	if err != nil || !first.Changed {
		t.Fatalf("first commit = %+v, err = %v", first, err)
	}

	// Committing again with the identical worktree produces the same tree, so nothing changes.
	second, err := repository.CommitDocumentation(ctx, sharedmodel.DocumentationCommit{
		DocumentationBranch: docsBranch, ParentTip: first.Commit,
		Paths: []string{"docs/generated"}, Message: "docs: sync", Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("second CommitDocumentation() error = %v", err)
	}
	if second.Changed {
		t.Error("commit.Changed = true, want false when nothing changed")
	}
}

func TestPrepareMergesExistingBranchRetainingDocumentation(t *testing.T) {
	repository, origin, clone, head := newPublishFixture(t)
	ctx := context.Background()

	// Create the documentation branch with one generated file and push it.
	prepared, err := repository.PrepareDocumentationBranch(ctx, sharedmodel.BranchPreparation{
		Remote: origin, BaseBranch: "main", DocumentationBranch: docsBranch, TriggeringCommit: head, Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("first prepare error = %v", err)
	}
	writeNested(t, clone, "docs/generated/index.md", "# Docs v1\n")
	commit, err := repository.CommitDocumentation(ctx, sharedmodel.DocumentationCommit{
		DocumentationBranch: docsBranch, ParentTip: prepared.Tip,
		Paths: []string{"docs/generated"}, Message: "docs: sync", Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("commit error = %v", err)
	}
	if err := repository.PushDocumentationBranch(ctx, sharedmodel.PushSpec{Remote: origin, DocumentationBranch: docsBranch, Commit: commit.Commit}); err != nil {
		t.Fatalf("push error = %v", err)
	}

	// A new base-branch commit lands while the documentation PR is still open.
	runGit(t, clone, "checkout", "--quiet", "main")
	writeNested(t, clone, "services/api.go", "package services\n")
	runGit(t, clone, "add", "--all")
	runGit(t, clone, "commit", "--quiet", "-m", "add service")
	runGit(t, clone, "push", "--quiet", "origin", "main")
	head2 := strings.TrimSpace(runGitOutput(t, clone, "rev-parse", "HEAD"))

	prepared2, err := repository.PrepareDocumentationBranch(ctx, sharedmodel.BranchPreparation{
		Remote: origin, BaseBranch: "main", DocumentationBranch: docsBranch, TriggeringCommit: head2, Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("second prepare error = %v", err)
	}
	if !prepared2.Existed {
		t.Error("prepared2.Existed = false, want true for the retained branch")
	}
	if !prepared2.AheadOfBase {
		t.Error("the documentation branch carries commits the base does not; AheadOfBase should be true")
	}
	// The merged worktree retains the pending generated documentation and gains the new source.
	if got := readFileTrimmed(t, filepath.Join(clone, filepath.FromSlash("docs/generated/index.md"))); got != "# Docs v1" {
		t.Errorf("pending documentation was not retained after merge: %q", got)
	}
	if _, err := os.Stat(filepath.Join(clone, filepath.FromSlash("services/api.go"))); err != nil {
		t.Errorf("merged worktree is missing the new base-branch source: %v", err)
	}
}

func TestPushRejectsNonFastForwardWithoutForce(t *testing.T) {
	repository, origin, clone, head := newPublishFixture(t)
	ctx := context.Background()
	prepared, err := repository.PrepareDocumentationBranch(ctx, sharedmodel.BranchPreparation{
		Remote: origin, BaseBranch: "main", DocumentationBranch: docsBranch, TriggeringCommit: head, Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("prepare error = %v", err)
	}
	writeNested(t, clone, "docs/generated/index.md", "# Docs v1\n")
	commit, err := repository.CommitDocumentation(ctx, sharedmodel.DocumentationCommit{
		DocumentationBranch: docsBranch, ParentTip: prepared.Tip, Paths: []string{"docs/generated"}, Message: "docs: sync", Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("commit error = %v", err)
	}
	if err := repository.PushDocumentationBranch(ctx, sharedmodel.PushSpec{Remote: origin, DocumentationBranch: docsBranch, Commit: commit.Commit}); err != nil {
		t.Fatalf("initial push error = %v", err)
	}

	// A competing runner advances the remote documentation branch.
	competitor := gitTempDir(t)
	runGit(t, competitor, "clone", "--quiet", origin, ".")
	runGit(t, competitor, "config", "user.name", "Other")
	runGit(t, competitor, "config", "user.email", "other@example.com")
	hardenRepo(t, competitor)
	runGit(t, competitor, "checkout", "--quiet", docsBranch)
	writeNested(t, competitor, "docs/generated/other.md", "# competing\n")
	runGit(t, competitor, "add", "--all")
	runGit(t, competitor, "commit", "--quiet", "-m", "competing docs")
	runGit(t, competitor, "push", "--quiet", "origin", docsBranch)
	competingTip := strings.TrimSpace(runGitOutput(t, competitor, "rev-parse", "HEAD"))

	// Our local branch builds another commit on the old tip and tries to push.
	writeNested(t, clone, "docs/generated/index.md", "# Docs v2\n")
	commit2, err := repository.CommitDocumentation(ctx, sharedmodel.DocumentationCommit{
		DocumentationBranch: docsBranch, ParentTip: commit.Commit, Paths: []string{"docs/generated"}, Message: "docs: sync", Identity: testIdentity,
	})
	if err != nil {
		t.Fatalf("second commit error = %v", err)
	}
	err = repository.PushDocumentationBranch(ctx, sharedmodel.PushSpec{Remote: origin, DocumentationBranch: docsBranch, Commit: commit2.Commit})
	if !errors.Is(err, sharedmodel.ErrNonFastForward) {
		t.Fatalf("push error = %v, want ErrNonFastForward", err)
	}
	if originTip := strings.TrimSpace(runGitOutput(t, origin, "rev-parse", "refs/heads/"+docsBranch)); originTip != competingTip {
		t.Errorf("origin tip = %q, want the competing commit %q left intact (no force push)", originTip, competingTip)
	}
}

func TestNetworkEnvironmentInstallsAskpassCallback(t *testing.T) {
	repository := New(Options{WorkingDirectory: t.TempDir(), AskpassProgram: "/usr/local/bin/docify-repo"})
	environment := repository.networkEnvironment()
	var sawAskpass, sawMarker bool
	for _, entry := range environment {
		if entry == "GIT_ASKPASS=/usr/local/bin/docify-repo" {
			sawAskpass = true
		}
		if entry == AskpassMarkerEnvVar+"=1" {
			sawMarker = true
		}
	}
	if !sawAskpass || !sawMarker {
		t.Errorf("network environment missing askpass callback: askpass=%t marker=%t", sawAskpass, sawMarker)
	}

	// With no askpass program configured (local remotes, tests), no callback is installed.
	plain := New(Options{WorkingDirectory: t.TempDir()}).networkEnvironment()
	for _, entry := range plain {
		if strings.HasPrefix(entry, "GIT_ASKPASS=") {
			t.Errorf("unexpected askpass callback installed: %q", entry)
		}
	}
}

func TestPublishInputValidationRejectsUnsafeValues(t *testing.T) {
	repository := New(Options{WorkingDirectory: t.TempDir()})
	ctx := context.Background()
	if _, err := repository.PrepareDocumentationBranch(ctx, sharedmodel.BranchPreparation{
		Remote: "-oops", BaseBranch: "main", DocumentationBranch: docsBranch, TriggeringCommit: strings.Repeat("a", 40), Identity: testIdentity,
	}); err == nil {
		t.Error("prepare accepted an option-like remote")
	}
	if err := repository.PushDocumentationBranch(ctx, sharedmodel.PushSpec{Remote: "origin", DocumentationBranch: "bad branch name"}); err == nil {
		t.Error("push accepted an invalid branch name")
	}
}

func readFileTrimmed(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}
