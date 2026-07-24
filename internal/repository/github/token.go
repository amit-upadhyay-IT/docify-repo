// Package github implements the GitHub pull-request publisher behind the usecase
// PullRequestPublisher interface. It performs lookup, creation, and update over Go's HTTP
// client and never invokes the GitHub CLI. The credential is attached only while
// constructing an outbound request and never appears in logs, errors, or returned values.
package github

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// CredentialEnvVar is the environment variable the default token source reads. It is
// distinct from the LLM credential so the two never mix in dependency injection or at
// runtime; the LLM request builder has no reference to this value.
const CredentialEnvVar = "DOCIFY_GITHUB_TOKEN"

// TokenSource supplies the GitHub credential on demand. It is resolved lazily so plan,
// check, worktree sync, and no-op runs never require a credential, and the value never
// enters state, subprocess arguments, or logs.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// EnvTokenSource reads the credential from an environment variable at call time.
type EnvTokenSource struct {
	variable string
}

// NewEnvTokenSource returns a token source reading the given environment variable,
// defaulting to CredentialEnvVar when variable is empty.
func NewEnvTokenSource(variable string) EnvTokenSource {
	if strings.TrimSpace(variable) == "" {
		variable = CredentialEnvVar
	}
	return EnvTokenSource{variable: variable}
}

// Token returns the trimmed credential. It never includes the value in its error.
func (s EnvTokenSource) Token(context.Context) (string, error) {
	value := strings.TrimSpace(os.Getenv(s.variable))
	if value == "" {
		return "", fmt.Errorf("github credential is not set in %s", s.variable)
	}
	return value, nil
}

// StaticTokenSource returns a fixed credential for tests and for callers that resolve the
// credential through another mechanism; production uses EnvTokenSource.
type StaticTokenSource struct {
	value string
}

// NewStaticTokenSource returns a token source that always yields value.
func NewStaticTokenSource(value string) StaticTokenSource {
	return StaticTokenSource{value: value}
}

func (s StaticTokenSource) Token(context.Context) (string, error) {
	if strings.TrimSpace(s.value) == "" {
		return "", fmt.Errorf("github credential is empty")
	}
	return s.value, nil
}
