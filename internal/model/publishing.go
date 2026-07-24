package model

import "errors"

// ErrNonFastForward reports that a documentation-branch push was rejected because it was
// not a fast-forward. The publisher never force-pushes; a serialized retry rebuilds on the
// newer remote tip. It is defined in the shared model so the usecase can recognize it
// without importing a repository package.
var ErrNonFastForward = errors.New("documentation branch push was not a fast-forward")

// Publishing control types cross the boundary between the documentation usecase and the
// Git and GitHub publishing repositories. They carry only control metadata: branch and
// repository identifiers, a fixed commit identity, and pull-request text rendered locally
// from the run report. No source, prompts, model responses, or credentials appear here.
// The credential is held inside the publishing repositories, never passed through the
// usecase.

// CommitIdentity is the fixed author and committer used for every tool commit. It is
// control metadata computed by fixed code, never by the model.
type CommitIdentity struct {
	Name  string
	Email string
}

// BranchPreparation requests preparation of the tool-owned documentation branch. The
// triggering commit is the base-branch commit that started the run; it is merged into an
// existing documentation branch or used to create a new one.
type BranchPreparation struct {
	Remote              string
	BaseBranch          string
	DocumentationBranch string
	TriggeringCommit    string
	MergeMessage        string
	Identity            CommitIdentity
}

// PreparedBranch reports the state of the documentation branch after preparation. On
// return the worktree reflects the prepared branch tip. AheadOfBase reports whether the
// prepared tip carries commits the base branch does not, which is what makes a pull request
// worth opening or keeping.
type PreparedBranch struct {
	Existed     bool
	Tip         string
	RemoteTip   string
	AheadOfBase bool
}

// DocumentationCommit requests one tool commit built from a temporary index seeded by the
// prepared branch tip. Exactly the listed tool-owned paths are staged from the worktree;
// every other path is carried forward unchanged from the parent tree.
type DocumentationCommit struct {
	DocumentationBranch string
	ParentTip           string
	Paths               []string
	Message             string
	Identity            CommitIdentity
}

// CommitResult reports the outcome of a documentation commit. Changed is false when the
// staged tree is identical to the parent tree, in which case no commit is recorded.
type CommitResult struct {
	Commit  string
	Changed bool
}

// PushSpec requests a fast-forward-only push of the documentation branch. A non-fast-forward
// rejection is surfaced as an error; the publisher never force-pushes.
type PushSpec struct {
	Remote              string
	DocumentationBranch string
	Commit              string
}

// PullRequestQuery locates an open pull request by head branch within a repository.
type PullRequestQuery struct {
	Repository string
	Head       string
}

// PullRequest identifies an existing pull request and its current base.
type PullRequest struct {
	Number int
	State  string
	Base   string
	Head   string
}

// PullRequestContent is the fixed, locally rendered pull-request identity and text. The
// title and body are produced by fixed code from the source range and run report.
type PullRequestContent struct {
	Repository string
	Head       string
	Base       string
	Title      string
	Body       string
}
