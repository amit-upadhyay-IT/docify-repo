package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"docify-repo/internal/app/documentation/handler"
	documentationmodel "docify-repo/internal/app/documentation/model"
	"docify-repo/internal/app/documentation/usecase"
	"docify-repo/internal/config"
	sharedmodel "docify-repo/internal/model"
	filesystemrepository "docify-repo/internal/repository/filesystem"
	gitrepository "docify-repo/internal/repository/git"
	githubrepository "docify-repo/internal/repository/github"
	openairepository "docify-repo/internal/repository/openai"
)

type Transport struct {
	config config.Config
	stdout io.Writer
	stderr io.Writer
}

func New(cfg config.Config, stdout, stderr io.Writer) *Transport {
	return &Transport{config: cfg, stdout: stdout, stderr: stderr}
}

func (t *Transport) Run(ctx context.Context) error {
	command := t.command()
	command.SetArgs(os.Args[1:])
	return command.ExecuteContext(ctx)
}

// llmTimeout parses the configured LLM timeout, falling back to the adapter default
// (returned as zero) when the value is empty or invalid. Structural validation of the
// value already happens in the config loader.
func llmTimeout(value string) time.Duration {
	if value == "" {
		return 0
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return timeout
}

// HandleAskpass answers a Git credential prompt when this process was re-invoked as the
// GIT_ASKPASS callback during a publisher network operation. It returns (exitCode, true)
// when it handled the invocation and (0, false) for a normal command run. The token is read
// from the environment and written only to out for Git to consume; it is never logged.
func HandleAskpass(arguments []string, lookup func(string) (string, bool), out io.Writer) (int, bool) {
	if marker, ok := lookup(gitrepository.AskpassMarkerEnvVar); !ok || marker != "1" {
		return 0, false
	}
	prompt := ""
	if len(arguments) > 1 {
		prompt = strings.ToLower(arguments[1])
	}
	switch {
	case strings.Contains(prompt, "username"):
		fmt.Fprintln(out, "x-access-token")
	case strings.Contains(prompt, "password"):
		token, _ := lookup(githubrepository.CredentialEnvVar)
		fmt.Fprintln(out, token)
	default:
		return 1, true
	}
	return 0, true
}

// ExitCode maps typed application errors without exposing those types to cmd.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 1
}

func (t *Transport) command() *cobra.Command {
	// The Git askpass callback is this executable re-invoked; resolving it here lets the Git
	// publisher supply the credential through a scoped child-process environment. If the path
	// cannot be resolved the publisher simply installs no callback (local remotes still work).
	executable, _ := os.Executable()
	gitSource := gitrepository.New(gitrepository.Options{
		WorkingDirectory: t.config.WorkingDirectory,
		Timeout:          30 * time.Second,
		PublishTimeout:   120 * time.Second,
		AskpassProgram:   executable,
	})
	worktree := filesystemrepository.NewSourceRepository()
	state := filesystemrepository.NewStateRepository()
	generator := openairepository.New(openairepository.Options{
		BaseURL:         t.config.LLM.BaseURL,
		TokenSource:     openairepository.NewEnvTokenSource(openairepository.CredentialEnvVar),
		Timeout:         llmTimeout(t.config.LLM.Timeout),
		Retries:         t.config.LLM.Retries,
		MaxContentBytes: int64(t.config.Components.MaxRequestBytes),
	})
	output := filesystemrepository.NewOutputRepository()
	pullRequests := githubrepository.New(githubrepository.Options{
		TokenSource: githubrepository.NewEnvTokenSource(githubrepository.CredentialEnvVar),
	})
	documentationUsecase := usecase.New(gitSource, worktree, state, generator, output, usecase.WithPublisher(gitSource, pullRequests))
	documentationHandler := handler.New(documentationUsecase, t.stdout)

	root := &cobra.Command{
		Use:           "docify-repo",
		Short:         "Generate and synchronize repository documentation",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetOut(t.stdout)
	root.SetErr(t.stderr)
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		t.syncCommand(documentationHandler),
		t.checkCommand(documentationHandler),
		t.planCommand(documentationHandler),
	)

	return root
}

type commonFlags struct {
	output            string
	reportPath        string
	baseSHA           string
	headSHA           string
	full              bool
	allowFullFallback bool
}

func (t *Transport) bindCommonFlags(command *cobra.Command, flags *commonFlags) {
	command.Flags().StringVar(&flags.output, "output", string(documentationmodel.OutputModeHuman), "summary output format: human or json")
	command.Flags().StringVar(&flags.reportPath, "report", t.config.ReportPath, "write a structured run report to this path")
	command.Flags().StringVar(&flags.baseSHA, "base-sha", t.config.Runtime.BaseSHA, "base revision for incremental planning")
	command.Flags().StringVar(&flags.headSHA, "head-sha", t.config.Runtime.HeadSHA, "head revision for incremental planning")
	command.Flags().BoolVar(&flags.full, "full", false, "force full documentation generation")
	command.Flags().BoolVar(&flags.allowFullFallback, "allow-full-fallback", false, "allow full generation when the base revision is unavailable")
}

func (t *Transport) sourcePolicy() documentationmodel.SourcePolicy {
	overrides := make([]documentationmodel.RoleOverride, 0, len(t.config.Source.RoleOverrides))
	for _, override := range t.config.Source.RoleOverrides {
		overrides = append(overrides, documentationmodel.RoleOverride{
			Pattern: override.Pattern,
			Role:    sharedmodel.SourceRole(override.Role),
		})
	}
	return documentationmodel.SourcePolicy{
		DocsDir:      t.config.DocsDir,
		StatePath:    t.config.StatePath,
		ReportPath:   t.config.ReportPath,
		Include:      append([]string(nil), t.config.Source.Include...),
		Exclude:      append([]string(nil), t.config.Source.Exclude...),
		MaxFileBytes: t.config.Source.MaxFileBytes,
		Tests: documentationmodel.SourceBehavior{
			IncludeAsContext: t.config.Source.Tests.IncludeAsContext,
			TriggerOnChange:  t.config.Source.Tests.TriggerOnChange,
		},
		Generated: documentationmodel.SourceBehavior{
			IncludeAsContext: t.config.Source.Generated.IncludeAsContext,
			TriggerOnChange:  t.config.Source.Generated.TriggerOnChange,
		},
		Fixtures: documentationmodel.SourceBehavior{
			IncludeAsContext: t.config.Source.Fixtures.IncludeAsContext,
			TriggerOnChange:  t.config.Source.Fixtures.TriggerOnChange,
		},
		RoleOverrides: overrides,
	}
}

func (t *Transport) componentPolicy() documentationmodel.ComponentPolicy {
	return documentationmodel.ComponentPolicy{
		Strategy:           t.config.Components.Strategy,
		Roots:              append([]string(nil), t.config.Components.Roots...),
		MaxContextBytes:    t.config.Components.MaxContextBytes,
		MaxBatchBytes:      t.config.Components.MaxBatchBytes,
		MaxSupportingBytes: t.config.Components.MaxSupportingBytes,
		MaxManifestBytes:   t.config.Components.MaxManifestBytes,
		MaxDiffBytes:       t.config.Components.MaxDiffBytes,
		MaxRequestBytes:    t.config.Components.MaxRequestBytes,
	}
}

func (t *Transport) generationPolicy() documentationmodel.GenerationPolicy {
	return documentationmodel.GenerationPolicy{
		Profile:              t.config.Documentation.Profile,
		Audience:             t.config.Documentation.Audience,
		Mermaid:              t.config.Documentation.Mermaid,
		Provider:             t.config.LLM.Provider,
		APIMode:              t.config.LLM.APIMode,
		Model:                t.config.LLM.Model,
		Temperature:          t.config.LLM.Temperature,
		MaxOutputTokens:      t.config.LLM.MaxOutputTokens,
		StructuredOutputMode: t.config.LLM.StructuredOutputMode,
	}
}

func (t *Transport) syncCommand(documentationHandler *handler.Handler) *cobra.Command {
	var common commonFlags
	var publisher string
	var branch string

	command := &cobra.Command{
		Use:   "sync",
		Short: "Synchronize generated documentation",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return documentationHandler.Sync(command.Context(), documentationmodel.RawSyncOptions{
				WorkingDirectory:  t.config.WorkingDirectory,
				Output:            common.output,
				ReportPath:        common.reportPath,
				BaseSHA:           common.baseSHA,
				HeadSHA:           common.headSHA,
				Publisher:         publisher,
				Full:              common.full,
				AllowFullFallback: common.allowFullFallback,
				Concurrency:       t.config.LLM.Concurrency,
				SourcePolicy:      t.sourcePolicy(),
				ComponentPolicy:   t.componentPolicy(),
				GenerationPolicy:  t.generationPolicy(),
				GitHubRepository:  t.config.Runtime.GitHubRepository,
				BaseBranch:        t.config.Runtime.BaseBranch,
				Branch:            branch,
				LLMCredential:     t.config.Runtime.LLMCredentialPresent,
				GitHubCredential:  t.config.Runtime.GitHubCredentialPresent,
			})
		},
	}
	t.bindCommonFlags(command, &common)
	command.Flags().StringVar(&publisher, "publisher", t.config.Publishing.Provider, "publishing mode: worktree or github-pr")
	command.Flags().StringVar(&branch, "branch", t.config.Publishing.Branch, "documentation branch for github-pr (default derived from the base branch)")
	return command
}

func (t *Transport) checkCommand(documentationHandler *handler.Handler) *cobra.Command {
	var common commonFlags

	command := &cobra.Command{
		Use:   "check",
		Short: "Check whether generated documentation is current",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return documentationHandler.Check(command.Context(), documentationmodel.RawCheckOptions{
				WorkingDirectory:  t.config.WorkingDirectory,
				Output:            common.output,
				ReportPath:        common.reportPath,
				BaseSHA:           common.baseSHA,
				HeadSHA:           common.headSHA,
				Full:              common.full,
				AllowFullFallback: common.allowFullFallback,
				SourcePolicy:      t.sourcePolicy(),
				ComponentPolicy:   t.componentPolicy(),
				GenerationPolicy:  t.generationPolicy(),
			})
		},
	}
	t.bindCommonFlags(command, &common)
	return command
}

func (t *Transport) planCommand(documentationHandler *handler.Handler) *cobra.Command {
	var common commonFlags

	command := &cobra.Command{
		Use:   "plan",
		Short: "Plan documentation changes without generating output",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return documentationHandler.Plan(command.Context(), documentationmodel.RawPlanOptions{
				WorkingDirectory:  t.config.WorkingDirectory,
				Output:            common.output,
				ReportPath:        common.reportPath,
				BaseSHA:           common.baseSHA,
				HeadSHA:           common.headSHA,
				Full:              common.full,
				AllowFullFallback: common.allowFullFallback,
				SourcePolicy:      t.sourcePolicy(),
				ComponentPolicy:   t.componentPolicy(),
				GenerationPolicy:  t.generationPolicy(),
			})
		},
	}
	t.bindCommonFlags(command, &common)
	return command
}
