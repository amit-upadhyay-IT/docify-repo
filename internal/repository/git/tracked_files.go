package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sharedmodel "docify-repo/internal/model"
)

const maximumGitErrorBytes = 32 * 1024

type Options struct {
	WorkingDirectory string
	Timeout          time.Duration
	// PublishTimeout bounds network operations (fetch, ls-remote, push). It defaults to a
	// longer value than Timeout because those calls contact the remote.
	PublishTimeout time.Duration
	// AskpassProgram is the executable Git invokes as GIT_ASKPASS during network operations.
	// When empty (local remotes and tests) no credential callback is installed.
	AskpassProgram string
}

type Repository struct {
	workingDirectory string
	timeout          time.Duration
	publishTimeout   time.Duration
	askpassProgram   string
}

func New(options Options) *Repository {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	publishTimeout := options.PublishTimeout
	if publishTimeout <= 0 {
		publishTimeout = 120 * time.Second
	}
	return &Repository{
		workingDirectory: options.WorkingDirectory,
		timeout:          timeout,
		publishTimeout:   publishTimeout,
		askpassProgram:   options.AskpassProgram,
	}
}

func (r *Repository) RepositoryRoot(ctx context.Context) (string, error) {
	output, err := r.run(ctx, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("discover Git repository: %w", err)
	}
	root := strings.TrimSuffix(string(output), "\n")
	root = strings.TrimSuffix(root, "\r")
	if root == "" || strings.ContainsAny(root, "\r\n") || !utf8.ValidString(root) {
		return "", fmt.Errorf("discover Git repository: invalid repository root")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	return filepath.Clean(absoluteRoot), nil
}

func (r *Repository) ListWorktreeTracked(ctx context.Context) ([]sharedmodel.TrackedEntry, error) {
	output, err := r.run(ctx, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	entries, err := parseTrackedEntries(output)
	if err != nil {
		return nil, fmt.Errorf("parse tracked files: %w", err)
	}
	return entries, nil
}

func (r *Repository) run(ctx context.Context, arguments ...string) ([]byte, error) {
	return r.runWith(ctx, r.timeout, hardenedEnvironment(), arguments...)
}

// runWith executes a Git subprocess with an explicit timeout and environment. The base
// arguments disable the pager, replacement objects, optional locks, the filesystem monitor,
// hooks, commit signing, and external diff so scanning and publishing can never execute
// repository-controlled code. stderr is returned bounded and never includes the environment.
func (r *Repository) runWith(ctx context.Context, timeout time.Duration, environment []string, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	baseArguments := []string{
		"--no-pager",
		"--no-replace-objects",
		"--no-optional-locks",
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgSign=false",
		"-c", "diff.external=",
		"-C", r.workingDirectory,
	}
	command := exec.CommandContext(commandContext, "git", append(baseArguments, arguments...)...)
	command.Env = environment
	var stdout bytes.Buffer
	stderr := &limitedBuffer{limit: maximumGitErrorBytes}
	command.Stdout = &stdout
	command.Stderr = stderr

	if err := command.Run(); err != nil {
		if commandContext.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", arguments[0], commandContext.Err())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("git %s: %w", arguments[0], err)
		}
		return nil, fmt.Errorf("git %s: %w: %s", arguments[0], err, message)
	}
	return stdout.Bytes(), nil
}

func hardenedEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+8)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			environment = append(environment, value)
		}
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_EXTERNAL_DIFF=",
		"GIT_PAGER=cat",
	)
}

func parseTrackedEntries(data []byte) ([]sharedmodel.TrackedEntry, error) {
	if len(data) == 0 {
		return []sharedmodel.TrackedEntry{}, nil
	}
	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("tracked-file output is not NUL terminated")
	}

	records := bytes.Split(data[:len(data)-1], []byte{0})
	entries := make([]sharedmodel.TrackedEntry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		metadata, rawPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("tracked-file record has no path separator")
		}
		fields := bytes.Fields(metadata)
		if len(fields) != 3 {
			return nil, fmt.Errorf("tracked-file record has invalid metadata")
		}
		if string(fields[2]) != "0" {
			return nil, fmt.Errorf("tracked path has unresolved index stage: %s", safePathIdentifier(rawPath))
		}
		if !validGitMode(string(fields[0])) {
			return nil, fmt.Errorf("tracked path has invalid mode: %s", safePathIdentifier(rawPath))
		}
		if !validObjectID(string(fields[1])) {
			return nil, fmt.Errorf("tracked path has invalid object ID: %s", safePathIdentifier(rawPath))
		}
		normalizedPath, err := normalizeGitPath(rawPath)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalizedPath]; exists {
			return nil, fmt.Errorf("duplicate tracked path: %s", strconv.Quote(normalizedPath))
		}
		seen[normalizedPath] = struct{}{}
		entries = append(entries, sharedmodel.TrackedEntry{
			Path:     normalizedPath,
			Mode:     string(fields[0]),
			ObjectID: string(fields[1]),
		})
	}

	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Path < entries[right].Path
	})
	return entries, nil
}

func normalizeGitPath(rawPath []byte) (string, error) {
	if !utf8.Valid(rawPath) {
		return "", fmt.Errorf("non-UTF-8 Git path: %s", safePathIdentifier(rawPath))
	}
	value := string(rawPath)
	if value == "" || strings.IndexByte(value, 0) >= 0 || path.IsAbs(value) {
		return "", fmt.Errorf("unsafe Git path: %s", safePathIdentifier(rawPath))
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe Git path: %s", safePathIdentifier(rawPath))
	}
	return value, nil
}

func validGitMode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '7' {
			return false
		}
	}
	return true
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safePathIdentifier(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("bytes=%s hash=sha256:%x", hex.EncodeToString(value), digest)
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return originalLength, nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
