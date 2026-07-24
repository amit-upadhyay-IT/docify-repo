package usecase

import (
	"context"
	"fmt"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

const (
	unavailableExitCode = 1
	maximumStateBytes   = 8 << 20
)

type GitSourceRepository interface {
	RepositoryRoot(ctx context.Context) (string, error)
	ListWorktreeTracked(ctx context.Context) ([]sharedmodel.TrackedEntry, error)
	ListTree(ctx context.Context, tree string) ([]sharedmodel.TrackedEntry, error)
	ReadBlob(ctx context.Context, objectID string, limit int64) (sharedmodel.FileContent, error)
	RevisionExists(ctx context.Context, revision string) (bool, error)
	Changes(ctx context.Context, baseSHA, headSHA string) ([]sharedmodel.RawChange, error)
}

type WorktreeFileRepository interface {
	ReadTracked(ctx context.Context, root, path string, limit int64) (sharedmodel.FileContent, error)
}

type StateRepository interface {
	Load(ctx context.Context, root, path string) (sharedmodel.StateLoadResult, error)
	Decode(ctx context.Context, data []byte) (sharedmodel.StateLoadResult, error)
}

// Generator sends one bounded, schema-constrained request to the configured
// OpenAI-compatible model and returns a normalized response. It receives no tools and
// no credentials from the usecase; the adapter owns credential handling internally.
type Generator interface {
	Generate(ctx context.Context, request sharedmodel.GenerationRequest) (sharedmodel.GenerationResponse, error)
}

// OutputRepository installs generated documentation and state as one recoverable
// transaction and reports currently installed output. It performs side effects only;
// every ownership and content decision is made by the usecase before Install is called.
type OutputRepository interface {
	ExistingPaths(ctx context.Context, root, docsDir, statePath string) (sharedmodel.ExistingOutput, error)
	ReadInstalled(ctx context.Context, root string, paths []string) (map[string][]byte, error)
	Install(ctx context.Context, root string, transaction sharedmodel.OutputTransaction) error
	Recover(ctx context.Context, root string) error
	WriteReport(ctx context.Context, root, reportPath string, data []byte) error
}

type Usecase struct {
	gitSource    GitSourceRepository
	worktree     WorktreeFileRepository
	state        StateRepository
	generator    Generator
	output       OutputRepository
	gitPublisher GitPublisher
	pullRequests PullRequestPublisher
}

func New(gitSource GitSourceRepository, worktree WorktreeFileRepository, state StateRepository, generator Generator, output OutputRepository, options ...Option) *Usecase {
	usecase := &Usecase{gitSource: gitSource, worktree: worktree, state: state, generator: generator, output: output}
	for _, option := range options {
		option(usecase)
	}
	return usecase
}

// Option configures optional Usecase dependencies. The GitHub pull-request publisher is
// injected this way so worktree, plan, and check runs never require it.
type Option func(*Usecase)

// WithPublisher installs the GitHub pull-request publisher dependencies. Both are required
// for the github-pr publishing mode; either being nil makes that mode a configuration error.
func WithPublisher(gitPublisher GitPublisher, pullRequests PullRequestPublisher) Option {
	return func(usecase *Usecase) {
		usecase.gitPublisher = gitPublisher
		usecase.pullRequests = pullRequests
	}
}

func (u *Usecase) Plan(ctx context.Context, input documentationmodel.PlanInput) (documentationmodel.ResultSummary, error) {
	if u.gitSource == nil || u.worktree == nil || u.state == nil {
		return documentationmodel.ResultSummary{}, fmt.Errorf("plan repositories are not configured")
	}
	if err := validateSourcePolicy(input.SourcePolicy); err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	if err := validateComponentPolicy(input.ComponentPolicy); err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	if err := validateGenerationPolicy(input.GenerationPolicy); err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}

	snapshot, err := u.resolveSnapshot(ctx, input)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	scan, err := u.scan(ctx, snapshot.root, snapshot.entries, input.SourcePolicy, snapshot.reader)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	components, ownedFiles, err := discoverComponents(scan.Files, input.ComponentPolicy, input.SourcePolicy.DocsDir)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	applyDecisionOwners(scan.Decisions, ownedFiles)

	plan, err := buildGenerationPlan(input, components, ownedFiles, snapshot.state, snapshot.rawChanges, snapshot.fullFallback)
	if err != nil {
		return documentationmodel.ResultSummary{}, sourceError{err: err}
	}
	result := documentationmodel.ResultSummary{
		Command:      "plan",
		Status:       "plan_complete",
		TrackedPaths: len(scan.Decisions),
		Files:        scan.Decisions,
		Plan:         plan,
	}
	for _, decision := range scan.Decisions {
		if decision.IncludedAsContext {
			result.IncludedPaths++
		} else {
			result.ExcludedPaths++
		}
		if decision.TriggersRegeneration {
			result.TriggeringPaths++
		}
	}
	if err := u.writeRunReport(ctx, input, result, nil); err != nil {
		return documentationmodel.ResultSummary{}, err
	}
	return result, nil
}

type resolvedSnapshot struct {
	root         string
	entries      []sharedmodel.TrackedEntry
	state        sharedmodel.StateLoadResult
	rawChanges   []sharedmodel.RawChange
	reader       trackedContentReader
	fullFallback bool
}

func (u *Usecase) resolveSnapshot(ctx context.Context, input documentationmodel.PlanInput) (resolvedSnapshot, error) {
	root, err := u.gitSource.RepositoryRoot(ctx)
	if err != nil {
		return resolvedSnapshot{}, err
	}
	if input.BaseSHA == "" && input.HeadSHA == "" {
		entries, err := u.gitSource.ListWorktreeTracked(ctx)
		if err != nil {
			return resolvedSnapshot{}, err
		}
		state, err := u.state.Load(ctx, root, input.SourcePolicy.StatePath)
		if err != nil {
			return resolvedSnapshot{}, err
		}
		return resolvedSnapshot{
			root:    root,
			entries: entries,
			state:   state,
			reader: func(ctx context.Context, root string, entry sharedmodel.TrackedEntry, limit int64) (sharedmodel.FileContent, error) {
				return u.worktree.ReadTracked(ctx, root, entry.Path, limit)
			},
		}, nil
	}
	if input.BaseSHA == "" || input.HeadSHA == "" {
		return resolvedSnapshot{}, fmt.Errorf("base SHA and head SHA must be supplied together")
	}
	headExists, err := u.gitSource.RevisionExists(ctx, input.HeadSHA)
	if err != nil {
		return resolvedSnapshot{}, err
	}
	if !headExists {
		return resolvedSnapshot{}, fmt.Errorf("head revision %q is unavailable", input.HeadSHA)
	}
	baseExists, err := u.gitSource.RevisionExists(ctx, input.BaseSHA)
	if err != nil {
		return resolvedSnapshot{}, err
	}
	if !baseExists && !input.AllowFullFallback {
		return resolvedSnapshot{}, fmt.Errorf("base revision %q is unavailable; increase checkout depth or pass --allow-full-fallback", input.BaseSHA)
	}
	entries, err := u.gitSource.ListTree(ctx, input.HeadSHA)
	if err != nil {
		return resolvedSnapshot{}, err
	}
	state, err := u.loadTreeState(ctx, entries, input.SourcePolicy.StatePath)
	if err != nil {
		return resolvedSnapshot{}, err
	}
	var changes []sharedmodel.RawChange
	if baseExists {
		changes, err = u.gitSource.Changes(ctx, input.BaseSHA, input.HeadSHA)
		if err != nil {
			return resolvedSnapshot{}, err
		}
	}
	return resolvedSnapshot{
		root:         root,
		entries:      entries,
		state:        state,
		rawChanges:   changes,
		fullFallback: !baseExists,
		reader: func(ctx context.Context, _ string, entry sharedmodel.TrackedEntry, limit int64) (sharedmodel.FileContent, error) {
			content, err := u.gitSource.ReadBlob(ctx, entry.ObjectID, limit)
			content.Path = entry.Path
			content.Symlink = entry.Mode == "120000"
			return content, err
		},
	}, nil
}

func (u *Usecase) loadTreeState(ctx context.Context, entries []sharedmodel.TrackedEntry, statePath string) (sharedmodel.StateLoadResult, error) {
	for _, entry := range entries {
		if entry.Path != statePath {
			continue
		}
		if entry.Mode != "100644" && entry.Mode != "100755" {
			return sharedmodel.StateLoadResult{}, fmt.Errorf("state %q is not a regular file", statePath)
		}
		content, err := u.gitSource.ReadBlob(ctx, entry.ObjectID, maximumStateBytes)
		if err != nil {
			return sharedmodel.StateLoadResult{}, err
		}
		if content.Truncated {
			return sharedmodel.StateLoadResult{}, fmt.Errorf("state %q exceeds %d bytes", statePath, maximumStateBytes)
		}
		return u.state.Decode(ctx, content.Data)
	}
	return sharedmodel.StateLoadResult{Missing: true}, nil
}

func applyDecisionOwners(decisions []sharedmodel.SourceDecision, files []sharedmodel.SourceFile) {
	owners := make(map[string]string, len(files))
	for _, file := range files {
		owners[file.Path] = file.ComponentKey
	}
	for index := range decisions {
		decisions[index].ComponentKey = owners[decisions[index].Path]
	}
}

type unavailableError struct {
	command string
}

func (e unavailableError) Error() string {
	return fmt.Sprintf("%s is not implemented yet", e.command)
}

func (e unavailableError) ExitCode() int {
	return unavailableExitCode
}

type sourceError struct {
	err error
}

func (e sourceError) Error() string {
	return e.err.Error()
}

func (e sourceError) Unwrap() error {
	return e.err
}

func (e sourceError) ExitCode() int {
	return 4
}
