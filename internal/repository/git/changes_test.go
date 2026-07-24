package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sharedmodel "docify-repo/internal/model"
)

func TestParseChangesHandlesAddModifyDeleteAndRename(t *testing.T) {
	data := []byte("A\x00added.go\x00M\x00modified.go\x00D\x00deleted.go\x00R087\x00old.go\x00new.go\x00")
	changes, err := parseChanges(data)
	if err != nil {
		t.Fatalf("parseChanges() error = %v", err)
	}
	want := []sharedmodel.RawChange{
		{Status: sharedmodel.ChangeAdded, NewPath: "added.go"},
		{Status: sharedmodel.ChangeModified, NewPath: "modified.go"},
		{Status: sharedmodel.ChangeDeleted, OldPath: "deleted.go"},
		{Status: sharedmodel.ChangeRenamed, OldPath: "old.go", NewPath: "new.go", Similarity: 87},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("changes = %+v, want %+v", changes, want)
	}
}

func TestRepositoryReadsImmutableTreeAndChanges(t *testing.T) {
	directory := gitTempDir(t)
	runGit(t, directory, "init", "--quiet")
	runGit(t, directory, "config", "user.name", "Test User")
	runGit(t, directory, "config", "user.email", "test@example.com")
	hardenRepo(t, directory)
	writeFile(t, filepath.Join(directory, "old.go"), "package old\n", 0o600)
	writeFile(t, filepath.Join(directory, "deleted.go"), "package deleted\n", 0o600)
	runGit(t, directory, "add", "--all")
	runGit(t, directory, "commit", "--quiet", "-m", "base")
	base := strings.TrimSpace(runGitOutput(t, directory, "rev-parse", "HEAD"))

	if err := os.Rename(filepath.Join(directory, "old.go"), filepath.Join(directory, "new.go")); err != nil {
		t.Fatalf("rename source: %v", err)
	}
	if err := os.Remove(filepath.Join(directory, "deleted.go")); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	writeFile(t, filepath.Join(directory, "added.go"), "package added\n", 0o600)
	runGit(t, directory, "add", "--all")
	runGit(t, directory, "commit", "--quiet", "-m", "head")
	head := strings.TrimSpace(runGitOutput(t, directory, "rev-parse", "HEAD"))

	repository := New(Options{WorkingDirectory: directory, Timeout: 10 * time.Second})
	for _, revision := range []string{base, head} {
		exists, err := repository.RevisionExists(context.Background(), revision)
		if err != nil || !exists {
			t.Fatalf("RevisionExists(%q) = %t, %v", revision, exists, err)
		}
	}
	changes, err := repository.Changes(context.Background(), base, head)
	if err != nil {
		t.Fatalf("Changes() error = %v", err)
	}
	if len(changes) != 3 || changes[0].Status != sharedmodel.ChangeAdded || changes[1].Status != sharedmodel.ChangeDeleted || changes[2].Status != sharedmodel.ChangeRenamed {
		t.Fatalf("changes = %+v, want add/delete/rename", changes)
	}
	entries, err := repository.ListTree(context.Background(), head)
	if err != nil {
		t.Fatalf("ListTree() error = %v", err)
	}
	if len(entries) != 2 || entries[0].Path != "added.go" || entries[1].Path != "new.go" {
		t.Fatalf("entries = %+v, want immutable head tree", entries)
	}
	content, err := repository.ReadBlob(context.Background(), entries[0].ObjectID, 100)
	if err != nil || string(content.Data) != "package added\n" || content.Truncated {
		t.Errorf("ReadBlob() = %+v, %v", content, err)
	}
	truncated, err := repository.ReadBlob(context.Background(), entries[0].ObjectID, 2)
	if err != nil || !truncated.Truncated || truncated.Size != int64(len("package added\n")) {
		t.Errorf("ReadBlob() truncated = %+v, %v", truncated, err)
	}
}

func runGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append(hardenedGitArgs(directory), arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", arguments[0], err, output)
	}
	return string(output)
}
