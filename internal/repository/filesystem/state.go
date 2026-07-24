package filesystem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

const maximumStateBytes = 8 << 20

type StateRepository struct{}

func NewStateRepository() *StateRepository {
	return &StateRepository{}
}

func (r *StateRepository) Load(ctx context.Context, rootPath, repositoryPath string) (sharedmodel.StateLoadResult, error) {
	if err := validateRepositoryPath(repositoryPath); err != nil {
		return sharedmodel.StateLoadResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return sharedmodel.StateLoadResult{}, err
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("open repository root: %w", err)
	}
	defer root.Close()
	parent, base, closeParent, err := openTrackedParent(root, repositoryPath)
	if errors.Is(err, os.ErrNotExist) {
		return sharedmodel.StateLoadResult{Missing: true}, nil
	}
	if err != nil {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("open state path: %w", err)
	}
	defer closeParent()

	information, err := parent.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return sharedmodel.StateLoadResult{Missing: true}, nil
	}
	if err != nil {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("inspect state %q: %w", repositoryPath, err)
	}
	if !information.Mode().IsRegular() {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("state %q is not a regular file", repositoryPath)
	}
	if information.Size() > maximumStateBytes {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("state %q exceeds %d bytes", repositoryPath, maximumStateBytes)
	}
	file, err := parent.Open(base)
	if err != nil {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("open state %q: %w", repositoryPath, err)
	}
	defer file.Close()
	openedInformation, err := file.Stat()
	if err != nil || !openedInformation.Mode().IsRegular() || !os.SameFile(information, openedInformation) {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("state %q changed during read", repositoryPath)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("read state %q: %w", repositoryPath, err)
	}
	if len(data) > maximumStateBytes {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("state %q exceeds %d bytes", repositoryPath, maximumStateBytes)
	}
	return r.Decode(ctx, data)
}

func (r *StateRepository) Decode(ctx context.Context, data []byte) (sharedmodel.StateLoadResult, error) {
	if err := ctx.Err(); err != nil {
		return sharedmodel.StateLoadResult{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state sharedmodel.State
	if err := decoder.Decode(&state); err != nil {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("decode state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return sharedmodel.StateLoadResult{}, fmt.Errorf("decode state: multiple JSON values are not supported")
		}
		return sharedmodel.StateLoadResult{}, fmt.Errorf("decode state: %w", err)
	}
	if err := validateState(state); err != nil {
		return sharedmodel.StateLoadResult{}, fmt.Errorf("decode state: %w", err)
	}
	return sharedmodel.StateLoadResult{State: state}, nil
}

func validateState(state sharedmodel.State) error {
	if state.SchemaVersion <= 0 {
		return fmt.Errorf("schema_version must be greater than zero")
	}
	if !sort.StringsAreSorted(state.GeneratedPaths) {
		return fmt.Errorf("generated_paths must be sorted")
	}
	for name, value := range map[string]string{
		"config_hash": state.ConfigHash,
		"config_hashes.paths": state.ConfigHashes.Paths,
		"config_hashes.source": state.ConfigHashes.Source,
		"config_hashes.components": state.ConfigHashes.Components,
		"config_hashes.context": state.ConfigHashes.Context,
		"config_hashes.generation": state.ConfigHashes.Generation,
	} {
		if value != "" && !validSHA256(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	for index, generatedPath := range state.GeneratedPaths {
		if err := validateRepositoryPath(generatedPath); err != nil {
			return fmt.Errorf("generated_paths[%d]: %w", index, err)
		}
		if index > 0 && generatedPath == state.GeneratedPaths[index-1] {
			return fmt.Errorf("generated_paths contains duplicate %q", generatedPath)
		}
	}
	for generatedPath, hash := range state.GeneratedContentHashes {
		if err := validateRepositoryPath(generatedPath); err != nil {
			return fmt.Errorf("generated_content_hashes: %w", err)
		}
		if !validSHA256(hash) {
			return fmt.Errorf("generated_content_hashes[%q] has invalid hash", generatedPath)
		}
	}
	for repositoryPath, file := range state.Files {
		if err := validateRepositoryPath(repositoryPath); err != nil {
			return fmt.Errorf("files: %w", err)
		}
		if !validSHA256(file.SourceHash) {
			return fmt.Errorf("files[%q].source_hash is invalid", repositoryPath)
		}
		if !validStateRole(file.Role) {
			return fmt.Errorf("files[%q].role is invalid", repositoryPath)
		}
		if file.ComponentKey != "@root" {
			if err := validateRepositoryPath(file.ComponentKey); err != nil {
				return fmt.Errorf("files[%q].component_key is invalid", repositoryPath)
			}
		}
	}
	for key, component := range state.Components {
		if key != "@root" {
			if err := validateRepositoryPath(key); err != nil {
				return fmt.Errorf("components key %q is invalid", key)
			}
		}
		if !validSHA256(component.InputHash) {
			return fmt.Errorf("components[%q].input_hash is invalid", key)
		}
		if err := validateRepositoryPath(component.Document); err != nil {
			return fmt.Errorf("components[%q].document is invalid", key)
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validStateRole(role sharedmodel.SourceRole) bool {
	switch role {
	case sharedmodel.RoleProductionSource, sharedmodel.RoleContract, sharedmodel.RoleDatabase,
		sharedmodel.RoleRuntimeConfiguration, sharedmodel.RoleDependencyManifest, sharedmodel.RoleTest,
		sharedmodel.RoleGeneratedCode, sharedmodel.RoleLockFile, sharedmodel.RoleFixture,
		sharedmodel.RoleGeneratedDocumentation, sharedmodel.RoleProse, sharedmodel.RoleUnknownSource,
		sharedmodel.RoleSecret, sharedmodel.RoleBinary, sharedmodel.RoleOversized, sharedmodel.RoleSpecialGitEntry:
		return true
	default:
		return false
	}
}
