package openai

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// CredentialEnvVar is the environment variable the default token source reads. It is
// intentionally distinct from the GitHub token variable so the two credentials stay
// separate in dependency injection and at runtime.
const CredentialEnvVar = "DOCIFY_LLM_API_KEY"

// TokenSource supplies a bearer credential on demand. It is resolved lazily so that
// plan, check, and no-op runs never require a credential, and the value never enters
// prompts, state, subprocess arguments, or logs.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// EnvTokenSource reads the bearer credential from an environment variable at call time.
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
		return "", fmt.Errorf("llm credential is not set in %s", s.variable)
	}
	return value, nil
}

// StaticTokenSource returns a fixed credential. It exists for tests and for callers
// that resolve the credential through another mechanism; production uses EnvTokenSource.
type StaticTokenSource struct {
	value string
}

// NewStaticTokenSource returns a token source that always yields value.
func NewStaticTokenSource(value string) StaticTokenSource {
	return StaticTokenSource{value: value}
}

func (s StaticTokenSource) Token(context.Context) (string, error) {
	if strings.TrimSpace(s.value) == "" {
		return "", fmt.Errorf("llm credential is empty")
	}
	return s.value, nil
}
