package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func TestStateRepositoryDecodesStrictValidState(t *testing.T) {
	repository := NewStateRepository()
	state := validTestState()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := repository.Decode(context.Background(), data)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if result.Missing || result.State.PlannerVersion != "v1" {
		t.Errorf("result = %+v, want decoded state", result)
	}
}

func TestStateRepositoryRejectsUnknownAndUnsortedState(t *testing.T) {
	repository := NewStateRepository()
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "unknown", data: `{"schema_version":1,"unknown":true}`, want: "unknown field"},
		{name: "unsorted", data: string(mustJSON(t, func(state *sharedmodel.State) {
			state.GeneratedPaths = []string{"z.md", "a.md"}
		})), want: "must be sorted"},
		{name: "trailing", data: string(append(mustJSON(t, nil), []byte(` {}`)...)), want: "multiple JSON values"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := repository.Decode(context.Background(), []byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Decode() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStateRepositoryLoadsMissingAndRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	repository := NewStateRepository()
	result, err := repository.Load(context.Background(), directory, ".docify/state.json")
	if err != nil || !result.Missing {
		t.Fatalf("Load() = %+v, %v, want missing", result, err)
	}
	if err := os.Mkdir(filepath.Join(directory, ".docify"), 0o700); err != nil {
		t.Fatalf("mkdir state directory: %v", err)
	}
	target := filepath.Join(directory, "state-target.json")
	if err := os.WriteFile(target, mustJSON(t, nil), 0o600); err != nil {
		t.Fatalf("write state target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(directory, ".docify", "state.json")); err != nil {
		t.Fatalf("create state symlink: %v", err)
	}
	if _, err := repository.Load(context.Background(), directory, ".docify/state.json"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Load() symlink error = %v, want regular-file rejection", err)
	}
}

func validTestState() sharedmodel.State {
	hash := "sha256:" + strings.Repeat("a", 64)
	return sharedmodel.State{
		SchemaVersion: 1, GeneratorVersion: "0.1.0", PlannerVersion: "v1", PromptVersion: "v1",
		ConfigHash: hash, GeneratedPaths: []string{"docs/generated/index.md"},
		GeneratedContentHashes: map[string]string{"docs/generated/index.md": hash},
		Files: map[string]sharedmodel.StateFile{
			"src/main.go": {SourceHash: hash, Role: sharedmodel.RoleProductionSource, TriggersRegeneration: true, ComponentKey: "src/main"},
		},
		Components: map[string]sharedmodel.StateComponent{
			"src/main": {InputHash: hash, Document: "docs/generated/components/src/main/index.md"},
		},
	}
}

func mustJSON(t *testing.T, mutate func(*sharedmodel.State)) []byte {
	t.Helper()
	state := validTestState()
	if mutate != nil {
		mutate(&state)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}
