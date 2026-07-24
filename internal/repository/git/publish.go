package git

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

// documentationFetchRef and baseFetchRef are the local refs the remote documentation and
// base branches are fetched into. Fixed private refs keep preparation independent of any
// configured remote name.
const (
	documentationFetchRef = "refs/docify/documentation"
	baseFetchRef          = "refs/docify/base"
)

// AskpassMarkerEnvVar signals a process re-invocation that is acting as the Git askpass
// callback. The transport reads it before command dispatch and answers the credential
// prompt from the environment, so the token never reaches Git through argv or a URL.
const AskpassMarkerEnvVar = "DOCIFY_INTERNAL_ASKPASS"

var branchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// PrepareDocumentationBranch fetches the tool-owned documentation branch when it exists,
// checks it out, and merges the triggering base-branch commit into it so an open
// documentation PR retains its pending generated changes. When the branch does not exist it
// is created from the triggering commit. On return the worktree reflects the prepared tip.
func (r *Repository) PrepareDocumentationBranch(ctx context.Context, request sharedmodel.BranchPreparation) (sharedmodel.PreparedBranch, error) {
	if err := validateRemote(request.Remote); err != nil {
		return sharedmodel.PreparedBranch{}, err
	}
	if !validBranchName(request.DocumentationBranch) {
		return sharedmodel.PreparedBranch{}, fmt.Errorf("invalid documentation branch name")
	}
	if !validRevision(request.TriggeringCommit) {
		return sharedmodel.PreparedBranch{}, fmt.Errorf("invalid triggering commit")
	}
	if !validBranchName(request.BaseBranch) {
		return sharedmodel.PreparedBranch{}, fmt.Errorf("invalid base branch name")
	}
	identity := request.Identity
	if identity.Name == "" || identity.Email == "" {
		return sharedmodel.PreparedBranch{}, fmt.Errorf("commit identity is required")
	}

	// Fetch the latest base branch so pull-request worthiness can be judged against the same
	// tip GitHub will compare against.
	if _, err := r.runWith(ctx, r.publishTimeout, r.networkEnvironment(), "fetch", "--no-tags", "--no-write-fetch-head",
		request.Remote, "+refs/heads/"+request.BaseBranch+":"+baseFetchRef); err != nil {
		return sharedmodel.PreparedBranch{}, fmt.Errorf("fetch base branch: %w", err)
	}
	baseTip, err := r.revParse(ctx, baseFetchRef)
	if err != nil {
		return sharedmodel.PreparedBranch{}, err
	}

	existed, err := r.remoteBranchExists(ctx, request.Remote, request.DocumentationBranch)
	if err != nil {
		return sharedmodel.PreparedBranch{}, err
	}

	if !existed {
		if _, err := r.runWith(ctx, r.timeout, identityEnvironment(hardenedEnvironment(), identity),
			"checkout", "--quiet", "-B", request.DocumentationBranch, "--no-track", request.TriggeringCommit); err != nil {
			return sharedmodel.PreparedBranch{}, fmt.Errorf("create documentation branch: %w", err)
		}
		tip, err := r.revParse(ctx, "HEAD")
		if err != nil {
			return sharedmodel.PreparedBranch{}, err
		}
		ahead, err := r.aheadOf(ctx, baseTip, tip)
		if err != nil {
			return sharedmodel.PreparedBranch{}, err
		}
		return sharedmodel.PreparedBranch{Existed: false, Tip: tip, AheadOfBase: ahead}, nil
	}

	if _, err := r.runWith(ctx, r.publishTimeout, r.networkEnvironment(), "fetch", "--no-tags", "--no-write-fetch-head",
		request.Remote, "+refs/heads/"+request.DocumentationBranch+":"+documentationFetchRef); err != nil {
		return sharedmodel.PreparedBranch{}, fmt.Errorf("fetch documentation branch: %w", err)
	}
	remoteTip, err := r.revParse(ctx, documentationFetchRef)
	if err != nil {
		return sharedmodel.PreparedBranch{}, err
	}
	if _, err := r.runWith(ctx, r.timeout, identityEnvironment(hardenedEnvironment(), identity),
		"checkout", "--quiet", "-B", request.DocumentationBranch, "--no-track", documentationFetchRef); err != nil {
		return sharedmodel.PreparedBranch{}, fmt.Errorf("check out documentation branch: %w", err)
	}
	message := request.MergeMessage
	if message == "" {
		message = "docs: merge base branch into documentation branch"
	}
	if _, err := r.runWith(ctx, r.timeout, identityEnvironment(hardenedEnvironment(), identity),
		"merge", "--no-edit", "--no-gpg-sign", "-m", message, "--end-of-options", request.TriggeringCommit); err != nil {
		// Abort a conflicted merge so the worktree is left clean for the serialized retry.
		_, _ = r.runWith(ctx, r.timeout, hardenedEnvironment(), "merge", "--abort")
		return sharedmodel.PreparedBranch{}, fmt.Errorf("merge base branch into documentation branch: %w", err)
	}
	tip, err := r.revParse(ctx, "HEAD")
	if err != nil {
		return sharedmodel.PreparedBranch{}, err
	}
	ahead, err := r.aheadOf(ctx, baseTip, tip)
	if err != nil {
		return sharedmodel.PreparedBranch{}, err
	}
	return sharedmodel.PreparedBranch{Existed: true, Tip: tip, RemoteTip: remoteTip, AheadOfBase: ahead}, nil
}

// aheadOf reports whether tip carries any commit the base does not.
func (r *Repository) aheadOf(ctx context.Context, base, tip string) (bool, error) {
	output, err := r.runWith(ctx, r.timeout, hardenedEnvironment(), "rev-list", "--count", "--end-of-options", base+".."+tip)
	if err != nil {
		return false, fmt.Errorf("count documentation commits: %w", err)
	}
	return strings.TrimSpace(string(output)) != "0", nil
}

// CommitDocumentation records one commit containing exactly the given tool-owned paths on
// top of the prepared tip, which must be the checked-out branch tip. It stages those paths
// (forcing past any ignore rules, since the tool owns them), writes a tree, and records the
// commit with plumbing that bypasses hooks and signing, then advances the branch. HEAD, the
// index, and the worktree stay consistent. It reports Changed=false when the staged tree
// matches the parent tree.
func (r *Repository) CommitDocumentation(ctx context.Context, request sharedmodel.DocumentationCommit) (sharedmodel.CommitResult, error) {
	if !validBranchName(request.DocumentationBranch) {
		return sharedmodel.CommitResult{}, fmt.Errorf("invalid documentation branch name")
	}
	if !validRevision(request.ParentTip) {
		return sharedmodel.CommitResult{}, fmt.Errorf("invalid parent commit")
	}
	if len(request.Paths) == 0 {
		return sharedmodel.CommitResult{}, fmt.Errorf("no tool-owned paths to commit")
	}
	for _, candidate := range request.Paths {
		if err := validateRelativePath(candidate); err != nil {
			return sharedmodel.CommitResult{}, err
		}
	}
	identity := request.Identity
	if identity.Name == "" || identity.Email == "" {
		return sharedmodel.CommitResult{}, fmt.Errorf("commit identity is required")
	}
	environment := identityEnvironment(hardenedEnvironment(), identity)

	stageArguments := append([]string{"add", "--force", "--all", "--"}, request.Paths...)
	if _, err := r.runWith(ctx, r.timeout, environment, stageArguments...); err != nil {
		return sharedmodel.CommitResult{}, fmt.Errorf("stage documentation: %w", err)
	}
	treeOutput, err := r.runWith(ctx, r.timeout, environment, "write-tree")
	if err != nil {
		return sharedmodel.CommitResult{}, fmt.Errorf("write documentation tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeOutput))
	if !validObjectID(tree) {
		return sharedmodel.CommitResult{}, fmt.Errorf("write documentation tree: invalid tree id")
	}
	parentTree, err := r.revParse(ctx, request.ParentTip+"^{tree}")
	if err != nil {
		return sharedmodel.CommitResult{}, err
	}
	if tree == parentTree {
		return sharedmodel.CommitResult{Changed: false}, nil
	}

	message := request.Message
	if message == "" {
		message = "docs: synchronize generated documentation"
	}
	commitOutput, err := r.runWith(ctx, r.timeout, environment, "commit-tree", tree, "-p", request.ParentTip, "-m", message)
	if err != nil {
		return sharedmodel.CommitResult{}, fmt.Errorf("create documentation commit: %w", err)
	}
	commit := strings.TrimSpace(string(commitOutput))
	if !validObjectID(commit) {
		return sharedmodel.CommitResult{}, fmt.Errorf("create documentation commit: invalid commit id")
	}
	// Compare-and-swap the branch onto the new commit; the branch tip must still be the
	// parent we built on, otherwise another run advanced it and we refuse rather than race.
	// HEAD is a symref to this branch, so the worktree stays consistent with the new commit.
	if _, err := r.runWith(ctx, r.timeout, hardenedEnvironment(), "update-ref",
		"refs/heads/"+request.DocumentationBranch, commit, request.ParentTip); err != nil {
		return sharedmodel.CommitResult{}, fmt.Errorf("advance documentation branch: %w", err)
	}
	return sharedmodel.CommitResult{Commit: commit, Changed: true}, nil
}

// PushDocumentationBranch pushes the documentation branch as a fast-forward update only. A
// non-fast-forward rejection is returned wrapping sharedmodel.ErrNonFastForward; the
// publisher never force-pushes, so a serialized retry can rebuild on the newer tip.
func (r *Repository) PushDocumentationBranch(ctx context.Context, request sharedmodel.PushSpec) error {
	if err := validateRemote(request.Remote); err != nil {
		return err
	}
	if !validBranchName(request.DocumentationBranch) {
		return fmt.Errorf("invalid documentation branch name")
	}
	reference := "refs/heads/" + request.DocumentationBranch
	_, err := r.runWith(ctx, r.publishTimeout, r.networkEnvironment(),
		"push", "--no-verify", request.Remote, reference+":"+reference)
	if err != nil {
		if isNonFastForward(err) {
			return fmt.Errorf("push documentation branch: %w", sharedmodel.ErrNonFastForward)
		}
		return fmt.Errorf("push documentation branch: %w", err)
	}
	return nil
}

func (r *Repository) remoteBranchExists(ctx context.Context, remote, branch string) (bool, error) {
	output, err := r.runWith(ctx, r.publishTimeout, r.networkEnvironment(),
		"ls-remote", "--heads", "--", remote, "refs/heads/"+branch)
	if err != nil {
		return false, fmt.Errorf("list remote documentation branch: %w", err)
	}
	return strings.Contains(string(output), "refs/heads/"+branch), nil
}

func (r *Repository) revParse(ctx context.Context, revision string) (string, error) {
	output, err := r.runWith(ctx, r.timeout, hardenedEnvironment(), "rev-parse", "--verify", "--quiet", "--end-of-options", revision)
	if err != nil {
		return "", fmt.Errorf("resolve revision: %w", err)
	}
	sha := strings.TrimSpace(string(output))
	if !validObjectID(sha) {
		return "", fmt.Errorf("resolve revision: invalid object id")
	}
	return sha, nil
}

// networkEnvironment adds the non-interactive credential callback to the hardened
// environment. Git invokes the askpass program, which reads the token from its own
// environment; the token never appears in a command-line argument or a remote URL.
func (r *Repository) networkEnvironment() []string {
	environment := hardenedEnvironment()
	if r.askpassProgram != "" {
		environment = append(environment,
			"GIT_ASKPASS="+r.askpassProgram,
			AskpassMarkerEnvVar+"=1",
		)
	}
	return environment
}

func identityEnvironment(base []string, identity sharedmodel.CommitIdentity) []string {
	return append(base,
		"GIT_AUTHOR_NAME="+identity.Name,
		"GIT_AUTHOR_EMAIL="+identity.Email,
		"GIT_COMMITTER_NAME="+identity.Name,
		"GIT_COMMITTER_EMAIL="+identity.Email,
	)
}

func isNonFastForward(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"non-fast-forward", "fetch first", "! [rejected]", "updates were rejected", "cannot lock ref"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func validBranchName(value string) bool {
	if !branchNamePattern.MatchString(value) {
		return false
	}
	if strings.Contains(value, "..") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".lock") {
		return false
	}
	return true
}

// validateRemote rejects a remote that could be interpreted as an option. A remote is a
// filesystem path or URL supplied by trusted configuration, but it is still guarded so it
// can never smuggle an argument into a Git subprocess.
func validateRemote(remote string) error {
	if strings.TrimSpace(remote) == "" {
		return fmt.Errorf("remote is required")
	}
	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("invalid remote")
	}
	return nil
}

func validateRelativePath(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("tool-owned path is required")
	}
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.Contains(value, "..") {
		return fmt.Errorf("invalid tool-owned path")
	}
	return nil
}
