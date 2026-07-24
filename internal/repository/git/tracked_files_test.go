package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseTrackedEntriesSortsAndPreservesPaths(t *testing.T) {
	objectID := strings.Repeat("a", 40)
	data := []byte("100755 " + objectID + " 0\tz/entry.go\x00" +
		"100644 " + objectID + " 0\ta/new\nline.py\x00")

	entries, err := parseTrackedEntries(data)
	if err != nil {
		t.Fatalf("parseTrackedEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Path != "a/new\nline.py" || entries[1].Path != "z/entry.go" {
		t.Errorf("paths = %q, %q, want bytewise sorted paths", entries[0].Path, entries[1].Path)
	}
}

func TestParseTrackedEntriesRejectsUnsafeData(t *testing.T) {
	objectID := strings.Repeat("a", 40)
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "not terminated", data: []byte("100644 " + objectID + " 0\ta.go"), want: "not NUL terminated"},
		{name: "traversal", data: []byte("100644 " + objectID + " 0\t../a.go\x00"), want: "unsafe Git path"},
		{name: "stage", data: []byte("100644 " + objectID + " 2\ta.go\x00"), want: "unresolved index stage"},
		{name: "non UTF-8", data: append([]byte("100644 "+objectID+" 0\tbad"), 0xff, 0), want: "non-UTF-8 Git path"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseTrackedEntries(test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseTrackedEntries() error = %v, want %q", err, test.want)
			}
			if test.name == "non UTF-8" && (!strings.Contains(err.Error(), "hash=sha256:") || !strings.Contains(err.Error(), "bytes=")) {
				t.Errorf("error = %q, want byte-safe identifier", err)
			}
		})
	}
}

func TestRepositoryListsTrackedFilesWithoutExecutingFSMonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	directory := t.TempDir()
	runGit(t, directory, "init", "--quiet")
	writeFile(t, filepath.Join(directory, "z.go"), "package z\n", 0o600)
	writeFile(t, filepath.Join(directory, "a.py"), "print('a')\n", 0o600)
	runGit(t, directory, "add", "--all")

	marker := filepath.Join(directory, "fsmonitor-ran")
	script := filepath.Join(directory, "fsmonitor.sh")
	writeFile(t, script, "#!/bin/sh\ntouch "+shellQuote(marker)+"\n", 0o700)
	runGit(t, directory, "config", "core.fsmonitor", script)

	repository := New(Options{WorkingDirectory: directory, Timeout: 10 * time.Second})
	root, err := repository.RepositoryRoot(context.Background())
	if err != nil {
		t.Fatalf("RepositoryRoot() error = %v", err)
	}
	expectedRoot, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if root != filepath.Clean(expectedRoot) {
		t.Errorf("root = %q, want %q", root, filepath.Clean(expectedRoot))
	}
	entries, err := repository.ListWorktreeTracked(context.Background())
	if err != nil {
		t.Fatalf("ListWorktreeTracked() error = %v", err)
	}
	if len(entries) != 2 || entries[0].Path != "a.py" || entries[1].Path != "z.go" {
		t.Fatalf("entries = %+v, want sorted tracked files", entries)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("fsmonitor marker error = %v, repository-controlled command executed", err)
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append(hardenedGitArgs(directory), arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", arguments[0], err, output)
	}
}

// hardenedGitArgs disables background Git maintenance in test repositories so no gc,
// commit-graph, or fsmonitor process lingers to race t.TempDir cleanup.
func hardenedGitArgs(directory string) []string {
	return []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.fsmonitor=false", "-C", directory}
}

// gitTempDir is a temporary directory for a Git repository whose cleanup tolerates the brief
// window in which a just-finished push leaves a receive-pack object quarantine behind. The
// built-in t.TempDir cleanup fails hard on that transient directory; this one retries.
func gitTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "docify-git-")
	if err != nil {
		t.Fatalf("create temporary git directory: %v", err)
	}
	t.Cleanup(func() {
		for attempt := 0; attempt < 40; attempt++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

// hardenRepo persists the maintenance-off configuration into a repository so operations run
// by the production code (which does not pass the -c flags) also spawn no background gc,
// including receive-side auto-gc on a bare push target.
func hardenRepo(t *testing.T, directory string) {
	t.Helper()
	for key, value := range map[string]string{
		"gc.auto":          "0",
		"maintenance.auto": "false",
		"core.fsmonitor":   "false",
		"receive.autogc":   "false",
	} {
		runGit(t, directory, "config", key, value)
	}
}

func writeFile(t *testing.T, filePath, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filePath, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
