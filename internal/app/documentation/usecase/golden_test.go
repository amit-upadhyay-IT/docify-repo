package usecase_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestSyncGoldenOutput locks the byte-for-byte rendered knowledge base for a fixed
// fixture. Run with DOCIFY_UPDATE_GOLDEN=1 to regenerate the golden files after an
// intentional rendering change.
func TestSyncGoldenOutput(t *testing.T) {
	application, _, dir := newSyncTestbed(t)
	if _, err := application.Sync(context.Background(), syncInput(dir)); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	produced := make(map[string]string)
	for path, content := range snapshotTree(t, filepath.Join(dir, "docs", "generated")) {
		produced[filepath.ToSlash(filepath.Join("docs/generated", path))] = content
	}
	produced[".docify/state.json"] = readFileString(t, filepath.Join(dir, ".docify", "state.json"))

	goldenRoot := filepath.Join("testdata", "golden")
	if os.Getenv("DOCIFY_UPDATE_GOLDEN") == "1" {
		if err := os.RemoveAll(goldenRoot); err != nil {
			t.Fatalf("clear golden: %v", err)
		}
		for relative, content := range produced {
			destination := filepath.Join(goldenRoot, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatalf("mkdir golden: %v", err)
			}
			if err := os.WriteFile(destination, []byte(content), 0o644); err != nil {
				t.Fatalf("write golden: %v", err)
			}
		}
		t.Logf("updated %d golden files", len(produced))
		return
	}

	want := make(map[string]string)
	err := filepath.Walk(goldenRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(goldenRoot, path)
		if err != nil {
			return err
		}
		want[filepath.ToSlash(relative)] = readFileString(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("read golden tree: %v (run with DOCIFY_UPDATE_GOLDEN=1 to create it)", err)
	}
	if len(want) == 0 {
		t.Fatal("no golden files found; run with DOCIFY_UPDATE_GOLDEN=1")
	}

	for _, relative := range sortedKeys(want) {
		if produced[relative] != want[relative] {
			t.Errorf("golden mismatch for %q\n--- want ---\n%s\n--- got ---\n%s", relative, want[relative], produced[relative])
		}
	}
	for _, relative := range sortedKeys(produced) {
		if _, ok := want[relative]; !ok {
			t.Errorf("produced unexpected file not in golden: %q", relative)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
