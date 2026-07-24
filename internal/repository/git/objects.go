package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

func (r *Repository) RevisionExists(ctx context.Context, revision string) (bool, error) {
	if !validRevision(revision) {
		return false, fmt.Errorf("invalid Git revision")
	}
	_, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", revision+"^{commit}")
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("verify Git revision: %w", err)
}

func (r *Repository) ListTree(ctx context.Context, tree string) ([]sharedmodel.TrackedEntry, error) {
	if !validRevision(tree) {
		return nil, fmt.Errorf("invalid Git tree revision")
	}
	output, err := r.run(ctx, "ls-tree", "-r", "-z", "--full-tree", tree, "--")
	if err != nil {
		return nil, fmt.Errorf("list Git tree: %w", err)
	}
	entries, err := parseTreeEntries(output)
	if err != nil {
		return nil, fmt.Errorf("parse Git tree: %w", err)
	}
	return entries, nil
}

func (r *Repository) ReadBlob(ctx context.Context, objectID string, limit int64) (sharedmodel.FileContent, error) {
	if !validObjectID(objectID) {
		return sharedmodel.FileContent{}, fmt.Errorf("invalid Git object ID")
	}
	if limit <= 0 {
		return sharedmodel.FileContent{}, fmt.Errorf("blob byte limit must be greater than zero")
	}
	sizeOutput, err := r.run(ctx, "cat-file", "-s", objectID)
	if err != nil {
		return sharedmodel.FileContent{}, fmt.Errorf("inspect Git blob: %w", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOutput)), 10, 64)
	if err != nil || size < 0 {
		return sharedmodel.FileContent{}, fmt.Errorf("inspect Git blob: invalid size")
	}
	result := sharedmodel.FileContent{Size: size}
	if size > limit {
		result.Truncated = true
		return result, nil
	}
	data, err := r.run(ctx, "cat-file", "blob", objectID)
	if err != nil {
		return sharedmodel.FileContent{}, fmt.Errorf("read Git blob: %w", err)
	}
	if int64(len(data)) != size {
		return sharedmodel.FileContent{}, fmt.Errorf("read Git blob: size changed during read")
	}
	result.Data = data
	return result, nil
}

func parseTreeEntries(data []byte) ([]sharedmodel.TrackedEntry, error) {
	if len(data) == 0 {
		return []sharedmodel.TrackedEntry{}, nil
	}
	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("tree output is not NUL terminated")
	}
	records := bytes.Split(data[:len(data)-1], []byte{0})
	entries := make([]sharedmodel.TrackedEntry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		metadata, rawPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("tree record has no path separator")
		}
		fields := bytes.Fields(metadata)
		if len(fields) != 3 || !validGitMode(string(fields[0])) || !validObjectID(string(fields[2])) {
			return nil, fmt.Errorf("tree record has invalid metadata")
		}
		objectType := string(fields[1])
		if objectType != "blob" && !(objectType == "commit" && string(fields[0]) == "160000") {
			return nil, fmt.Errorf("tree record has unsupported object type")
		}
		normalizedPath, err := normalizeGitPath(rawPath)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalizedPath]; exists {
			return nil, fmt.Errorf("duplicate tree path: %s", strconv.Quote(normalizedPath))
		}
		seen[normalizedPath] = struct{}{}
		entries = append(entries, sharedmodel.TrackedEntry{
			Path:     normalizedPath,
			Mode:     string(fields[0]),
			ObjectID: string(fields[2]),
		})
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Path < entries[right].Path
	})
	return entries, nil
}

func validRevision(value string) bool {
	return validObjectID(value)
}
