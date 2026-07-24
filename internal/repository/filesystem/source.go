package filesystem

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

type SourceRepository struct{}

func NewSourceRepository() *SourceRepository {
	return &SourceRepository{}
}

func (r *SourceRepository) ReadTracked(ctx context.Context, rootPath, repositoryPath string, limit int64) (sharedmodel.FileContent, error) {
	if err := validateRepositoryPath(repositoryPath); err != nil {
		return sharedmodel.FileContent{}, err
	}
	if limit <= 0 {
		return sharedmodel.FileContent{}, fmt.Errorf("read %q: byte limit must be greater than zero", repositoryPath)
	}
	if err := ctx.Err(); err != nil {
		return sharedmodel.FileContent{}, err
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return sharedmodel.FileContent{}, fmt.Errorf("open repository root: %w", err)
	}
	defer root.Close()
	parent, base, closeParent, err := openTrackedParent(root, repositoryPath)
	if err != nil {
		return sharedmodel.FileContent{}, err
	}
	defer closeParent()

	information, err := parent.Lstat(base)
	if err != nil {
		return sharedmodel.FileContent{}, fmt.Errorf("inspect tracked path %q: %w", repositoryPath, err)
	}
	if information.Mode()&os.ModeSymlink != 0 {
		target, err := parent.Readlink(base)
		if err != nil {
			return sharedmodel.FileContent{}, fmt.Errorf("read tracked symlink %q: %w", repositoryPath, err)
		}
		data := []byte(target)
		result := sharedmodel.FileContent{Path: repositoryPath, Size: int64(len(data)), Symlink: true}
		if int64(len(data)) > limit {
			result.Data = append([]byte(nil), data[:limit]...)
			result.Truncated = true
		} else {
			result.Data = append([]byte(nil), data...)
		}
		return result, nil
	}
	if !information.Mode().IsRegular() {
		return sharedmodel.FileContent{}, fmt.Errorf("tracked path %q is not a regular file or symlink", repositoryPath)
	}

	file, err := parent.Open(base)
	if err != nil {
		return sharedmodel.FileContent{}, fmt.Errorf("open tracked file %q: %w", repositoryPath, err)
	}
	defer file.Close()
	openedInformation, err := file.Stat()
	if err != nil {
		return sharedmodel.FileContent{}, fmt.Errorf("inspect open tracked file %q: %w", repositoryPath, err)
	}
	if !openedInformation.Mode().IsRegular() || !os.SameFile(information, openedInformation) {
		return sharedmodel.FileContent{}, fmt.Errorf("tracked file %q changed during read", repositoryPath)
	}

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return sharedmodel.FileContent{}, fmt.Errorf("read tracked file %q: %w", repositoryPath, err)
	}
	result := sharedmodel.FileContent{Path: repositoryPath, Size: openedInformation.Size()}
	if int64(len(data)) > limit {
		result.Data = data[:limit]
		result.Truncated = true
	} else {
		result.Data = data
	}
	return result, nil
}

func openTrackedParent(root *os.Root, repositoryPath string) (*os.Root, string, func(), error) {
	segments := strings.Split(repositoryPath, "/")
	parent := root
	parentOwned := false
	closeParent := func() {
		if parentOwned {
			_ = parent.Close()
		}
	}

	for _, segment := range segments[:len(segments)-1] {
		information, err := parent.Lstat(segment)
		if err != nil {
			closeParent()
			return nil, "", func() {}, fmt.Errorf("inspect tracked path parent %q: %w", segment, err)
		}
		if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
			closeParent()
			return nil, "", func() {}, fmt.Errorf("tracked path %q has a non-directory or symlink parent", repositoryPath)
		}

		next, err := parent.OpenRoot(segment)
		if err != nil {
			closeParent()
			return nil, "", func() {}, fmt.Errorf("open tracked path parent %q: %w", segment, err)
		}
		openedInformation, err := next.Lstat(".")
		if err != nil || !os.SameFile(information, openedInformation) {
			_ = next.Close()
			closeParent()
			return nil, "", func() {}, fmt.Errorf("tracked path %q parent changed during read", repositoryPath)
		}
		if parentOwned {
			_ = parent.Close()
		}
		parent = next
		parentOwned = true
	}

	return parent, segments[len(segments)-1], closeParent, nil
}

func validateRepositoryPath(repositoryPath string) error {
	if repositoryPath == "" || path.IsAbs(repositoryPath) || strings.IndexByte(repositoryPath, 0) >= 0 {
		return fmt.Errorf("unsafe repository path %q", repositoryPath)
	}
	cleaned := path.Clean(repositoryPath)
	if cleaned != repositoryPath || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("unsafe repository path %q", repositoryPath)
	}
	return nil
}
