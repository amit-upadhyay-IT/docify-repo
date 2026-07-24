package git

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

func (r *Repository) Changes(ctx context.Context, baseSHA, headSHA string) ([]sharedmodel.RawChange, error) {
	if !validRevision(baseSHA) || !validRevision(headSHA) {
		return nil, fmt.Errorf("invalid Git change range")
	}
	output, err := r.run(ctx, "diff", "--name-status", "-z", "--find-renames", "--no-ext-diff", "--no-textconv", baseSHA, headSHA, "--")
	if err != nil {
		return nil, fmt.Errorf("discover Git changes: %w", err)
	}
	changes, err := parseChanges(output)
	if err != nil {
		return nil, fmt.Errorf("parse Git changes: %w", err)
	}
	return changes, nil
}

func parseChanges(data []byte) ([]sharedmodel.RawChange, error) {
	if len(data) == 0 {
		return []sharedmodel.RawChange{}, nil
	}
	if data[len(data)-1] != 0 {
		return nil, fmt.Errorf("change output is not NUL terminated")
	}
	fields := bytes.Split(data[:len(data)-1], []byte{0})
	changes := make([]sharedmodel.RawChange, 0, len(fields)/2)
	for index := 0; index < len(fields); {
		status := string(fields[index])
		index++
		if status == "" {
			return nil, fmt.Errorf("change record has empty status")
		}
		switch status[0] {
		case 'A', 'M', 'D', 'T':
			if len(status) != 1 || index >= len(fields) {
				return nil, fmt.Errorf("change record has invalid status or path count")
			}
			repositoryPath, err := normalizeGitPath(fields[index])
			if err != nil {
				return nil, err
			}
			index++
			change := sharedmodel.RawChange{Status: sharedmodel.ChangeModified, NewPath: repositoryPath}
			switch status[0] {
			case 'A':
				change.Status = sharedmodel.ChangeAdded
			case 'D':
				change.Status = sharedmodel.ChangeDeleted
				change.OldPath = repositoryPath
				change.NewPath = ""
			}
			changes = append(changes, change)
		case 'R':
			if index+1 >= len(fields) {
				return nil, fmt.Errorf("rename record has invalid path count")
			}
			similarity, err := strconv.Atoi(strings.TrimPrefix(status, "R"))
			if err != nil || similarity < 0 || similarity > 100 {
				return nil, fmt.Errorf("rename record has invalid similarity")
			}
			oldPath, err := normalizeGitPath(fields[index])
			if err != nil {
				return nil, err
			}
			newPath, err := normalizeGitPath(fields[index+1])
			if err != nil {
				return nil, err
			}
			index += 2
			changes = append(changes, sharedmodel.RawChange{
				Status:     sharedmodel.ChangeRenamed,
				OldPath:    oldPath,
				NewPath:    newPath,
				Similarity: similarity,
			})
		default:
			return nil, fmt.Errorf("unsupported change status %q", status)
		}
	}
	return changes, nil
}
