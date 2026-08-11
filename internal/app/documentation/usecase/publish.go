package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

// GitPublisher performs the mutating Git operations of the GitHub pull-request publisher.
// Implementations run every subprocess non-interactively with hooks, commit signing,
// external diff, and text conversion disabled, and supply the credential only through a
// scoped child-process environment — never a command-line argument or a remote URL.
type GitPublisher interface {
	PrepareDocumentationBranch(ctx context.Context, request sharedmodel.BranchPreparation) (sharedmodel.PreparedBranch, error)
	CommitDocumentation(ctx context.Context, request sharedmodel.DocumentationCommit) (sharedmodel.CommitResult, error)
	PushDocumentationBranch(ctx context.Context, request sharedmodel.PushSpec) error
}

// PullRequestPublisher performs GitHub REST pull-request lookup, creation, and update over
// an HTTP client. It never invokes the GitHub CLI.
type PullRequestPublisher interface {
	FindOpenPullRequest(ctx context.Context, query sharedmodel.PullRequestQuery) (sharedmodel.PullRequest, bool, error)
	CreatePullRequest(ctx context.Context, content sharedmodel.PullRequestContent) (sharedmodel.PullRequest, error)
	UpdatePullRequest(ctx context.Context, number int, content sharedmodel.PullRequestContent) error
}

const (
	publishRemote             = "origin"
	publishCommitName         = "docify-repo"
	publishCommitEmail        = "docify-repo@users.noreply.github.com"
	publishCommitMessage      = "docs: synchronize generated documentation"
	publishMergeMessage       = "docs: merge base branch into documentation branch"
	publishPullRequestTitle   = "docs: synchronize generated documentation"
	documentationBranchPrefix = "docify/generated-docs-"
	portableNameMaxLength     = 40
	branchHashLength          = 12
)

// publishGitHub runs the deterministic GitHub pull-request lifecycle: validate identity,
// prepare the tool-owned documentation branch, generate on the prepared branch, commit and
// fast-forward push only when a generated file changed, and open or update one deterministic
// pull request. Generation never happens for an unchanged range, and a push-success/PR-failure
// retry reconciles the pull request without regenerating.
func (u *Usecase) publishGitHub(ctx context.Context, input documentationmodel.SyncInput) (documentationmodel.ResultSummary, error) {
	if u.gitPublisher == nil || u.pullRequests == nil {
		return documentationmodel.ResultSummary{}, fmt.Errorf("sync --publisher github-pr requires a configured publisher")
	}
	repository := strings.TrimSpace(input.GitHubRepository)
	baseBranch := strings.TrimSpace(input.BaseBranch)
	headSHA := strings.TrimSpace(input.HeadSHA)
	if err := validatePublishInput(repository, baseBranch, headSHA, strings.TrimSpace(input.BaseSHA), input.GitHubCredentialPresent); err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	branch, err := documentationBranchName(input.Branch, baseBranch)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	corePlanInput := planInputFromSync(input)
	corePlanInput.BaseSHA = ""
	corePlanInput.HeadSHA = ""

	identity := sharedmodel.CommitIdentity{Name: publishCommitName, Email: publishCommitEmail}
	prepared, err := u.gitPublisher.PrepareDocumentationBranch(ctx, sharedmodel.BranchPreparation{
		Remote:              publishRemote,
		BaseBranch:          baseBranch,
		DocumentationBranch: branch,
		TriggeringCommit:    headSHA,
		MergeMessage:        publishMergeMessage,
		Identity:            identity,
	})
	if err != nil {
		failure := publishError{fmt.Sprintf("prepare documentation branch: %v", err)}
		summary := documentationmodel.ResultSummary{
			Command: "sync", Files: []sharedmodel.SourceDecision{},
			Plan: sharedmodel.GenerationPlan{
				GenerationStrategy: input.GenerationPolicy.GenerationStrategy,
				Components:         []sharedmodel.ComponentSummary{}, Changes: []sharedmodel.Change{},
				AffectedComponents: []sharedmodel.AffectedComponent{}, DeletedDocuments: []string{},
			},
		}
		return u.finishPublishFailure(ctx, corePlanInput, summary, nil, failure), failure
	}

	// Generate against the prepared branch now checked out in the worktree. Clearing the
	// event range makes the core read source and pending state from that branch, so an open
	// pull request's generated sections are retained and only genuinely changed components
	// regenerate.
	generation, err := u.prepareGeneration(ctx, "sync", corePlanInput)
	if err != nil {
		return documentationmodel.ResultSummary{}, err
	}

	if generation.plan.Noop {
		generation.summary.Status = "noop"
		// Reconcile a pull request that is missing, closed, or on the wrong base even when
		// nothing regenerated, as long as the branch already proposes documentation changes.
		if prepared.AheadOfBase {
			content := u.pullRequestContent(repository, branch, baseBranch, input, generation.summary)
			if err := u.reconcilePullRequest(ctx, content); err != nil {
				return u.finishPublishFailure(ctx, corePlanInput, generation.summary, nil, err), err
			}
		}
		if err := u.writeRunReport(ctx, corePlanInput, generation.summary, nil); err != nil {
			return documentationmodel.ResultSummary{}, err
		}
		return generation.summary, nil
	}

	candidate, inspection, err := u.generateCandidate(ctx, generation)
	if err != nil {
		return u.finishGenerationFailure(ctx, corePlanInput, generation.summary, candidate, &inspection, err), err
	}
	inspection, err = u.revalidateInstallationOwnership(ctx, generation, inspection)
	if err != nil {
		return u.finishGenerationFailure(ctx, corePlanInput, generation.summary, candidate, &inspection, err), err
	}
	if err := u.installCandidate(ctx, corePlanInput, candidate, inspection); err != nil {
		failure := outputValidationError{fmt.Sprintf("install generated output: %v", err)}
		return u.finishGenerationFailure(ctx, corePlanInput, generation.summary, candidate, &inspection, failure), failure
	}
	generation.summary.Generation = candidate.outcome()
	markFragmentFallbacks(&generation.summary.Plan, candidate.fallbackComponents)

	commit, err := u.gitPublisher.CommitDocumentation(ctx, sharedmodel.DocumentationCommit{
		DocumentationBranch: branch,
		ParentTip:           prepared.Tip,
		Paths:               []string{corePlanInput.SourcePolicy.DocsDir, corePlanInput.SourcePolicy.StatePath},
		Message:             publishCommitMessage,
		Identity:            identity,
	})
	if err != nil {
		failure := publishError{fmt.Sprintf("commit documentation: %v", err)}
		return u.finishPublishFailure(ctx, corePlanInput, generation.summary, &inspection, failure), failure
	}
	if commit.Changed {
		if err := u.gitPublisher.PushDocumentationBranch(ctx, sharedmodel.PushSpec{
			Remote:              publishRemote,
			DocumentationBranch: branch,
			Commit:              commit.Commit,
		}); err != nil {
			if errors.Is(err, sharedmodel.ErrNonFastForward) {
				failure := publishError{"documentation branch was updated concurrently; the push was not a fast-forward. Rerun to rebuild on the newer tip."}
				return u.finishPublishFailure(ctx, corePlanInput, generation.summary, &inspection, failure), failure
			}
			failure := publishError{fmt.Sprintf("push documentation branch: %v", err)}
			return u.finishPublishFailure(ctx, corePlanInput, generation.summary, &inspection, failure), failure
		}
	}

	generation.summary.Status = "synced"
	if commit.Changed || prepared.AheadOfBase {
		content := u.pullRequestContent(repository, branch, baseBranch, input, generation.summary)
		if err := u.reconcilePullRequest(ctx, content); err != nil {
			return u.finishPublishFailure(ctx, corePlanInput, generation.summary, &inspection, err), err
		}
	}
	if err := u.writeRunReport(ctx, corePlanInput, generation.summary, &inspection); err != nil {
		return documentationmodel.ResultSummary{}, err
	}
	return generation.summary, nil
}

func (u *Usecase) finishPublishFailure(
	ctx context.Context,
	input documentationmodel.PlanInput,
	summary documentationmodel.ResultSummary,
	inspection *installedInspection,
	_ error,
) documentationmodel.ResultSummary {
	summary.Status = "publish_failed"
	summary.Failure = &documentationmodel.GenerationFailure{Category: "publish"}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = u.writeRunReport(reportCtx, input, summary, inspection)
	return summary
}

// reconcilePullRequest opens the deterministic pull request or updates the existing open one.
// Updating repairs a wrong base and refreshes the title and body; finding none creates it.
func (u *Usecase) reconcilePullRequest(ctx context.Context, content sharedmodel.PullRequestContent) error {
	existing, found, err := u.pullRequests.FindOpenPullRequest(ctx, sharedmodel.PullRequestQuery{
		Repository: content.Repository,
		Head:       content.Head,
	})
	if err != nil {
		return publishError{fmt.Sprintf("look up pull request: %v", err)}
	}
	if found {
		if err := u.pullRequests.UpdatePullRequest(ctx, existing.Number, content); err != nil {
			return publishError{fmt.Sprintf("update pull request: %v", err)}
		}
		return nil
	}
	if _, err := u.pullRequests.CreatePullRequest(ctx, content); err != nil {
		return publishError{fmt.Sprintf("create pull request: %v", err)}
	}
	return nil
}

// pullRequestContent builds the fixed pull-request identity and locally rendered body. No
// model text participates; the body carries only the source range and safe run-report metadata.
func (u *Usecase) pullRequestContent(repository, branch, baseBranch string, input documentationmodel.SyncInput, summary documentationmodel.ResultSummary) sharedmodel.PullRequestContent {
	return sharedmodel.PullRequestContent{
		Repository: repository,
		Head:       branch,
		Base:       baseBranch,
		Title:      publishPullRequestTitle,
		Body:       renderPullRequestBody(input.BaseSHA, input.HeadSHA, summary),
	}
}

// renderPullRequestBody renders a deterministic, source-free pull-request body from the
// commit range and plan-derived summary. It lists affected components and document counts
// only — never source text, prompts, or model prose.
func renderPullRequestBody(baseSHA, headSHA string, summary documentationmodel.ResultSummary) string {
	var builder strings.Builder
	builder.WriteString("Automated documentation synchronization by docify-repo.\n\n")
	builder.WriteString(fmt.Sprintf("- Source range: %s..%s\n", inlineCodePath(shortSHA(baseSHA)), inlineCodePath(shortSHA(headSHA))))
	builder.WriteString(fmt.Sprintf("- Plan mode: %s\n", inlineCodePath(summary.Plan.Mode)))
	builder.WriteString(fmt.Sprintf("- Generation strategy: %s\n", inlineCodePath(summary.Plan.GenerationStrategy)))
	builder.WriteString(fmt.Sprintf("- Planned LLM calls: %d typical / %d maximum logical / %d maximum HTTP attempts\n",
		summary.Plan.Calls.TypicalLogical, summary.Plan.Calls.MaximumLogical, summary.Plan.Calls.MaximumHTTPAttempts))
	if summary.Plan.StateStatus != "" {
		builder.WriteString(fmt.Sprintf("- State: %s\n", inlineCodePath(summary.Plan.StateStatus)))
	}

	affected := make([]string, 0, len(summary.Plan.AffectedComponents))
	for _, component := range summary.Plan.AffectedComponents {
		affected = append(affected, fmt.Sprintf("%s (%s)", component.Key, component.Action))
	}
	sort.Strings(affected)
	if len(affected) > 0 {
		builder.WriteString("\n**Affected components**\n")
		for _, entry := range affected {
			builder.WriteString(fmt.Sprintf("- %s\n", inlineCodePath(entry)))
		}
	}

	if outcome := summary.Generation; outcome != nil {
		builder.WriteString("\n**LLM generation**\n")
		builder.WriteString(fmt.Sprintf("- Fragment calls: %d\n", outcome.FragmentCalls))
		builder.WriteString(fmt.Sprintf("- Repairs: %d\n", outcome.RepairCalls))
		builder.WriteString(fmt.Sprintf("- Fragment fallbacks: %d\n", outcome.FragmentFallbacks))
		builder.WriteString(fmt.Sprintf("- Source splits: %d\n", outcome.FragmentSourceSplits))
		builder.WriteString("\n**Documents**\n")
		builder.WriteString(fmt.Sprintf("- Added: %d\n", len(outcome.Diff.Added)))
		builder.WriteString(fmt.Sprintf("- Changed: %d\n", len(outcome.Diff.Changed)))
		builder.WriteString(fmt.Sprintf("- Deleted: %d\n", len(outcome.Diff.Deleted)))
	}
	builder.WriteString("\nThis pull request is generated and maintained automatically; its branch is tool-owned.\n")
	return builder.String()
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= branchHashLength {
		return sha
	}
	return sha[:branchHashLength]
}

// documentationBranchName returns the configured branch verbatim when set, otherwise the
// deterministic default docify/generated-docs-<portable-base-name>-<base-name-hash>. The
// name is computed only by this fixed code, never by the model.
func documentationBranchName(configured, baseBranch string) (string, error) {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		if !isSafeBranchName(trimmed) {
			return "", fmt.Errorf("configured documentation branch %q is not a valid branch name", trimmed)
		}
		return trimmed, nil
	}
	if strings.TrimSpace(baseBranch) == "" {
		return "", fmt.Errorf("base branch is required to derive the documentation branch name")
	}
	digest := sha256.Sum256([]byte(baseBranch))
	name := documentationBranchPrefix + portableBranchName(baseBranch) + "-" + hex.EncodeToString(digest[:])[:branchHashLength]
	return name, nil
}

// portableBranchName reduces an arbitrary base branch name to lowercase ASCII alphanumerics
// separated by single hyphens, bounded in length. The hash suffix preserves uniqueness, so
// this only needs to be readable and portable across ref-name rules.
func portableBranchName(baseBranch string) string {
	lowered := strings.ToLower(baseBranch)
	var builder strings.Builder
	previousHyphen := false
	for _, character := range lowered {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
			previousHyphen = false
		default:
			if !previousHyphen {
				builder.WriteByte('-')
				previousHyphen = true
			}
		}
	}
	portable := strings.Trim(builder.String(), "-")
	if portable == "" {
		portable = "base"
	}
	if len(portable) > portableNameMaxLength {
		portable = strings.Trim(portable[:portableNameMaxLength], "-")
	}
	return portable
}

func validatePublishInput(repository, baseBranch, headSHA, baseSHA string, credentialPresent bool) error {
	if repository == "" {
		return fmt.Errorf("github-pr publishing requires a repository (DOCIFY_GITHUB_REPOSITORY)")
	}
	if owner, name, ok := strings.Cut(repository, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("github repository must be in owner/name form")
	}
	if baseBranch == "" {
		return fmt.Errorf("github-pr publishing requires a base branch (DOCIFY_BASE_BRANCH)")
	}
	if headSHA == "" || baseSHA == "" {
		return fmt.Errorf("github-pr publishing requires both base and head SHAs (DOCIFY_BASE_SHA and DOCIFY_HEAD_SHA)")
	}
	if !credentialPresent {
		return fmt.Errorf("github-pr publishing requires a token (DOCIFY_GITHUB_TOKEN)")
	}
	return nil
}

// isSafeBranchName guards a configured branch name so it can never smuggle a Git argument or
// break ref-name rules.
func isSafeBranchName(value string) bool {
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	if strings.Contains(value, "..") || strings.HasSuffix(value, ".lock") {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '-', character == '_', character == '.', character == '/':
		default:
			return false
		}
	}
	return value != ""
}

// publishError is a safe publishing failure. It exposes only stable wording and
// repository-relative identifiers, never source, prompts, model prose, or credentials.
type publishError struct {
	message string
}

func (e publishError) Error() string { return e.message }

func (e publishError) ExitCode() int { return 7 }
