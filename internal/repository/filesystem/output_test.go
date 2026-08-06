package filesystem

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func writeUnder(t *testing.T, root, relative, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", relative, err)
	}
}

func readUnder(t *testing.T, root, relative string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %q: %v", relative, err)
	}
	return string(data), true
}

func doc(path, content string) sharedmodel.RenderedDocument {
	return sharedmodel.RenderedDocument{Path: path, Data: []byte(content)}
}

func outputTestHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", digest)
}

func TestOutputInstallCommitsWritesAndDeletes(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/old.md", "stale")
	writeUnder(t, root, "docs/generated/keep.md", "v1")

	repository := NewOutputRepository()
	transaction := sharedmodel.OutputTransaction{
		Writes: []sharedmodel.RenderedDocument{
			doc("docs/generated/keep.md", "v2"),
			doc("docs/generated/new.md", "fresh"),
			doc(".docify/state.json", "{}"),
		},
		Deletes: []string{"docs/generated/old.md"},
	}
	if err := repository.Install(context.Background(), root, transaction); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	if content, ok := readUnder(t, root, "docs/generated/keep.md"); !ok || content != "v2" {
		t.Errorf("keep.md = %q ok=%t, want v2", content, ok)
	}
	if content, ok := readUnder(t, root, "docs/generated/new.md"); !ok || content != "fresh" {
		t.Errorf("new.md = %q ok=%t, want fresh", content, ok)
	}
	if _, ok := readUnder(t, root, "docs/generated/old.md"); ok {
		t.Error("old.md should have been deleted")
	}
	if content, ok := readUnder(t, root, ".docify/state.json"); !ok || content != "{}" {
		t.Errorf("state = %q ok=%t", content, ok)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(transactionDir))); !os.IsNotExist(err) {
		t.Errorf("transaction directory was not cleaned up: %v", err)
	}
}

func TestOutputInstallRollsBackOnFailure(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/keep.md", "original")
	// A regular file where a write target needs a directory forces the move phase to fail
	// after keep.md has already been backed up.
	writeUnder(t, root, "docs/generated/blocker", "not a directory")

	repository := NewOutputRepository()
	transaction := sharedmodel.OutputTransaction{
		Writes: []sharedmodel.RenderedDocument{
			doc("docs/generated/keep.md", "updated"),
			doc("docs/generated/blocker/index.md", "cannot install"),
		},
	}
	if err := repository.Install(context.Background(), root, transaction); err == nil {
		t.Fatal("expected Install to fail")
	}

	if content, ok := readUnder(t, root, "docs/generated/keep.md"); !ok || content != "original" {
		t.Errorf("keep.md = %q ok=%t, want the original restored", content, ok)
	}
	if content, ok := readUnder(t, root, "docs/generated/blocker"); !ok || content != "not a directory" {
		t.Errorf("blocker = %q ok=%t, want untouched", content, ok)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(transactionDir))); !os.IsNotExist(err) {
		t.Errorf("transaction directory was not cleaned up after rollback: %v", err)
	}
}

func TestOutputInstallRejectsChangedPrecondition(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/keep.md", "changed after validation")

	repository := NewOutputRepository()
	transaction := sharedmodel.OutputTransaction{
		Writes: []sharedmodel.RenderedDocument{doc("docs/generated/keep.md", "candidate")},
		Preconditions: []sharedmodel.OutputPrecondition{{
			Path: "docs/generated/keep.md", MustExist: true, ContentHash: outputTestHash("validated content"),
		}},
	}
	if err := repository.Install(context.Background(), root, transaction); err == nil {
		t.Fatal("Install() error = nil, want changed-precondition refusal")
	}
	if content, ok := readUnder(t, root, "docs/generated/keep.md"); !ok || content != "changed after validation" {
		t.Fatalf("keep.md = %q ok=%t, want concurrent content untouched", content, ok)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(transactionDir))); !os.IsNotExist(err) {
		t.Errorf("transaction directory was not cleaned up: %v", err)
	}
}

func TestOutputRecoverRollsForwardCommitted(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/live.md", "installed")
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{Writes: []string{"docs/generated/live.md"}, Deletes: nil}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	// A committed marker and a lingering backup copy simulate a crash after commit.
	writeUnder(t, filepath.Join(txDir, transactionBackup), "docs/generated/live.md", "backup-old")
	if err := os.WriteFile(filepath.Join(txDir, transactionCommit), []byte("committed\n"), 0o644); err != nil {
		t.Fatalf("seed commit marker: %v", err)
	}

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if content, ok := readUnder(t, root, "docs/generated/live.md"); !ok || content != "installed" {
		t.Errorf("live.md = %q ok=%t, want the committed content preserved", content, ok)
	}
	if _, err := os.Stat(txDir); !os.IsNotExist(err) {
		t.Errorf("recover did not clean up committed transaction: %v", err)
	}
}

func TestOutputRecoverPreservesBackupForManualRecovery(t *testing.T) {
	root := t.TempDir()
	// A crash after backup moved the original but before candidate installation.
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes:      []string{"docs/generated/page.md"},
		WriteHashes: map[string]string{"docs/generated/page.md": outputTestHash("candidate")},
		Preconditions: []sharedmodel.OutputPrecondition{{
			Path: "docs/generated/page.md", MustExist: true, ContentHash: outputTestHash("original"),
		}},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	writeUnder(t, filepath.Join(txDir, transactionBackup), "docs/generated/page.md", "original")

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err == nil {
		t.Fatal("Recover() error = nil, want conservative manual recovery")
	}
	if _, ok := readUnder(t, root, "docs/generated/page.md"); ok {
		t.Fatal("recovery installed untrusted backup content")
	}
	if content, ok := readUnder(t, filepath.Join(txDir, transactionBackup), "docs/generated/page.md"); !ok || content != "original" {
		t.Fatalf("backup = %q ok=%t, want preserved", content, ok)
	}
}

func TestOutputRecoverPreservesConcurrentConflict(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/page.md", "concurrent edit")
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes: []string{"docs/generated/page.md"},
		WriteHashes: map[string]string{
			"docs/generated/page.md": outputTestHash("candidate"),
		},
		Preconditions: []sharedmodel.OutputPrecondition{{
			Path: "docs/generated/page.md", MustExist: true, ContentHash: outputTestHash("original"),
		}},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	writeUnder(t, filepath.Join(txDir, transactionBackup), "docs/generated/page.md", "original")

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err == nil {
		t.Fatal("Recover() error = nil, want ownership conflict")
	}
	if content, ok := readUnder(t, root, "docs/generated/page.md"); !ok || content != "concurrent edit" {
		t.Fatalf("live page = %q ok=%t, want concurrent edit preserved", content, ok)
	}
	if content, ok := readUnder(t, filepath.Join(txDir, transactionBackup), "docs/generated/page.md"); !ok || content != "original" {
		t.Fatalf("backup page = %q ok=%t, want original preserved", content, ok)
	}
	if _, err := os.Stat(filepath.Join(txDir, transactionConflict)); err != nil {
		t.Fatalf("conflict marker missing: %v", err)
	}
}

func TestOutputRecoverRecognizesPreMutationOriginal(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/page.md", "original")
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes: []string{"docs/generated/page.md"},
		WriteHashes: map[string]string{
			"docs/generated/page.md": outputTestHash("candidate"),
		},
		Preconditions: []sharedmodel.OutputPrecondition{{
			Path: "docs/generated/page.md", MustExist: true, ContentHash: outputTestHash("original"),
		}},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if content, ok := readUnder(t, root, "docs/generated/page.md"); !ok || content != "original" {
		t.Fatalf("page = %q ok=%t, want untouched original", content, ok)
	}
	if _, err := os.Stat(txDir); !os.IsNotExist(err) {
		t.Fatalf("transaction directory remains: %v", err)
	}
}

func TestOutputRecoverPreservesUntouchedOriginalMatchingCandidate(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/page.md", "same bytes")
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes:      []string{"docs/generated/page.md"},
		WriteHashes: map[string]string{"docs/generated/page.md": outputTestHash("same bytes")},
		Preconditions: []sharedmodel.OutputPrecondition{{
			Path: "docs/generated/page.md", MustExist: true, ContentHash: outputTestHash("same bytes"),
		}},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if content, ok := readUnder(t, root, "docs/generated/page.md"); !ok || content != "same bytes" {
		t.Fatalf("page = %q ok=%t, want untouched original", content, ok)
	}
}

func TestOutputRecoverRejectsUnsafeJournalPaths(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir repository: %v", err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("preserve me"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes: []string{"../outside.txt"}, WriteHashes: map[string]string{"../outside.txt": outputTestHash("preserve me")},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err == nil {
		t.Fatal("Recover() error = nil, want unsafe journal rejection")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "preserve me" {
		t.Fatalf("outside file = %q, %v; want preserved", data, err)
	}
}

func TestOutputRecoverRejectsJournalTargetOutsideGeneratedOutput(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, ".git/config", "preserve git config")
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes: []string{".git/config"}, WriteHashes: map[string]string{".git/config": outputTestHash("preserve git config")},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err == nil {
		t.Fatal("Recover() error = nil, want generated-output boundary rejection")
	}
	if content, ok := readUnder(t, root, ".git/config"); !ok || content != "preserve git config" {
		t.Fatalf("git config = %q ok=%t, want preserved", content, ok)
	}
}

func TestOutputRecoverRejectsUnjournaledBackup(t *testing.T) {
	root := t.TempDir()
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes:        []string{"docs/generated/page.md"},
		WriteHashes:   map[string]string{"docs/generated/page.md": outputTestHash("candidate")},
		Preconditions: []sharedmodel.OutputPrecondition{{Path: "docs/generated/page.md"}},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	writeUnder(t, filepath.Join(txDir, transactionBackup), "docs/generated/other.md", "forged backup")

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err == nil {
		t.Fatal("Recover() error = nil, want unjournaled backup rejection")
	}
	if _, ok := readUnder(t, root, "docs/generated/other.md"); ok {
		t.Fatal("unjournaled backup was restored into generated output")
	}
	if content, ok := readUnder(t, filepath.Join(txDir, transactionBackup), "docs/generated/other.md"); !ok || content != "forged backup" {
		t.Fatalf("backup = %q ok=%t, want preserved for inspection", content, ok)
	}
}

func TestOutputRecoverRefusesHashOnlyJournalWithoutChangingLiveFile(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/page.md", "preserve me")
	txDir := filepath.Join(root, filepath.FromSlash(transactionDir))
	if err := writeJournal(txDir, journal{
		Writes:      []string{"docs/generated/page.md"},
		WriteHashes: map[string]string{"docs/generated/page.md": outputTestHash("preserve me")},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if err := NewOutputRepository().Recover(context.Background(), root, "docs/generated", ".docify/state.json"); err == nil {
		t.Fatal("Recover() error = nil, want ownership-precondition refusal")
	}
	if content, ok := readUnder(t, root, "docs/generated/page.md"); !ok || content != "preserve me" {
		t.Fatalf("live file = %q ok=%t, want preserved", content, ok)
	}
}

func TestOutputReadInstalledReturnsPresentFilesOnly(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/index.md", "index body")
	writeUnder(t, root, "docs/generated/components/x/index.md", "component body")

	content, err := NewOutputRepository().ReadInstalled(context.Background(), root, []string{
		"docs/generated/index.md",
		"docs/generated/components/x/index.md",
		"docs/generated/missing.md",
	})
	if err != nil {
		t.Fatalf("ReadInstalled() error = %v", err)
	}
	if len(content) != 2 {
		t.Fatalf("read %d files, want 2 (the missing path is skipped)", len(content))
	}
	if string(content["docs/generated/index.md"]) != "index body" {
		t.Errorf("index content = %q", content["docs/generated/index.md"])
	}
	if _, ok := content["docs/generated/missing.md"]; ok {
		t.Error("missing file must not appear in the result")
	}
}

func TestOutputReadInstalledRejectsUnsafePath(t *testing.T) {
	if _, err := NewOutputRepository().ReadInstalled(context.Background(), t.TempDir(), []string{"../escape.md"}); err == nil {
		t.Fatal("ReadInstalled() error = nil, want an unsafe-path rejection")
	}
}

func TestOutputWriteReportReplacesAtomically(t *testing.T) {
	root := t.TempDir()
	repository := NewOutputRepository()
	if err := repository.WriteReport(context.Background(), root, ".docify/report.json", []byte("{\"v\":1}\n")); err != nil {
		t.Fatalf("WriteReport() error = %v", err)
	}
	if content, ok := readUnder(t, root, ".docify/report.json"); !ok || content != "{\"v\":1}\n" {
		t.Fatalf("report = %q ok=%t", content, ok)
	}
	// A second write replaces the file and leaves no temporary artifact behind.
	if err := repository.WriteReport(context.Background(), root, ".docify/report.json", []byte("{\"v\":2}\n")); err != nil {
		t.Fatalf("second WriteReport() error = %v", err)
	}
	if content, _ := readUnder(t, root, ".docify/report.json"); content != "{\"v\":2}\n" {
		t.Errorf("report = %q, want the replacement", content)
	}
	if _, ok := readUnder(t, root, ".docify/report.json.tmp"); ok {
		t.Error("temporary report staging file was left behind")
	}
}

func TestOutputExistingPathsListsGeneratedTree(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "docs/generated/index.md", "a")
	writeUnder(t, root, "docs/generated/components/x/index.md", "b")
	writeUnder(t, root, ".docify/state.json", "{}")

	existing, err := NewOutputRepository().ExistingPaths(context.Background(), root, "docs/generated", ".docify/state.json")
	if err != nil {
		t.Fatalf("ExistingPaths() error = %v", err)
	}
	if len(existing.GeneratedPaths) != 2 {
		t.Fatalf("generated paths = %v, want 2", existing.GeneratedPaths)
	}
	if existing.GeneratedPaths[0] != "docs/generated/components/x/index.md" || existing.GeneratedPaths[1] != "docs/generated/index.md" {
		t.Errorf("generated paths not sorted: %v", existing.GeneratedPaths)
	}
	if !existing.StateExists {
		t.Error("StateExists = false, want true")
	}
}
