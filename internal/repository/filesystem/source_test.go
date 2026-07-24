package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadTrackedRegularFileAndLimit(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source.go"), []byte("package source\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	repository := NewSourceRepository()

	content, err := repository.ReadTracked(context.Background(), directory, "source.go", 7)
	if err != nil {
		t.Fatalf("ReadTracked() error = %v", err)
	}
	if string(content.Data) != "package" || !content.Truncated || content.Size != 15 {
		t.Errorf("content = %+v data=%q, want bounded read", content, content.Data)
	}
}

func TestReadTrackedReturnsSymlinkMetadataWithoutFollowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	directory := t.TempDir()
	externalDirectory := t.TempDir()
	external := filepath.Join(externalDirectory, "secret.txt")
	if err := os.WriteFile(external, []byte("must-not-be-read"), 0o600); err != nil {
		t.Fatalf("write external fixture: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(directory, "link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	content, err := NewSourceRepository().ReadTracked(context.Background(), directory, "link", 4096)
	if err != nil {
		t.Fatalf("ReadTracked() error = %v", err)
	}
	if !content.Symlink || string(content.Data) != external {
		t.Errorf("content = %+v data=%q, want symlink target metadata", content, content.Data)
	}
	if strings.Contains(string(content.Data), "must-not-be-read") {
		t.Error("symlink target content was read")
	}
}

func TestReadTrackedRejectsTraversal(t *testing.T) {
	_, err := NewSourceRepository().ReadTracked(context.Background(), t.TempDir(), "../outside", 10)
	if err == nil || !strings.Contains(err.Error(), "unsafe repository path") {
		t.Fatalf("ReadTracked() error = %v, want unsafe path error", err)
	}
}

func TestReadTrackedRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	directory := t.TempDir()
	targetDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDirectory, "source.go"), []byte("untracked content"), 0o600); err != nil {
		t.Fatalf("write target fixture: %v", err)
	}
	if err := os.Symlink(targetDirectory, filepath.Join(directory, "src")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	_, err := NewSourceRepository().ReadTracked(context.Background(), directory, "src/source.go", 1024)
	if err == nil || !strings.Contains(err.Error(), "symlink parent") {
		t.Fatalf("ReadTracked() error = %v, want symlink parent error", err)
	}
}

func TestReadTrackedHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSourceRepository().ReadTracked(ctx, t.TempDir(), "source.go", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadTracked() error = %v, want context cancellation", err)
	}
}
