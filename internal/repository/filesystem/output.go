package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

// Transaction working paths live under the tool-owned .docify directory so they share a
// filesystem with the repository and stay out of the scanned source tree.
const (
	transactionDir     = ".docify/tx"
	transactionStaging = "staging"
	transactionBackup  = "backup"
	transactionJournal = "journal.json"
	transactionCommit  = "committed"
)

// OutputRepository installs generated documentation and state as one recoverable
// filesystem transaction. It performs side effects only; every ownership and content
// decision is made by the usecase before Install is called.
type OutputRepository struct{}

func NewOutputRepository() *OutputRepository {
	return &OutputRepository{}
}

// journal records the target paths of a transaction. It never contains source or model
// content, only repository-relative paths and the two path lists needed for recovery.
type journal struct {
	Writes  []string `json:"writes"`
	Deletes []string `json:"deletes"`
}

// ReadInstalled reads the content of the named installed generated documents. A path that
// does not exist is skipped rather than reported as an error so the usecase can detect a
// missing owned document by its absence from the returned map. It reads only files, never
// following directories, and validates every path stays repository-relative.
func (r *OutputRepository) ReadInstalled(ctx context.Context, rootPath string, paths []string) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(paths))
	for _, relative := range paths {
		if err := validateRepositoryPath(relative); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(relative)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read installed %q: %w", relative, err)
		}
		result[relative] = data
	}
	return result, nil
}

// WriteReport writes the structured run report to reportPath as a single atomic
// replacement (staged in the same directory, then renamed). The report is a run artifact,
// not part of the recoverable documentation transaction, so it is installed independently.
func (r *OutputRepository) WriteReport(ctx context.Context, rootPath, reportPath string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRepositoryPath(reportPath); err != nil {
		return err
	}
	final := filepath.Join(rootPath, filepath.FromSlash(reportPath))
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	temporary := final + ".tmp"
	if err := writeFileSynced(temporary, data); err != nil {
		return fmt.Errorf("stage report: %w", err)
	}
	if err := os.Rename(temporary, final); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("install report: %w", err)
	}
	return nil
}

// ExistingPaths reports the generated files currently present under docsDir and whether
// the state file currently exists, so the usecase can make ownership-recovery decisions.
func (r *OutputRepository) ExistingPaths(ctx context.Context, rootPath, docsDir, statePath string) (sharedmodel.ExistingOutput, error) {
	if err := ctx.Err(); err != nil {
		return sharedmodel.ExistingOutput{}, err
	}
	if err := validateRepositoryPath(docsDir); err != nil {
		return sharedmodel.ExistingOutput{}, err
	}
	if err := validateRepositoryPath(statePath); err != nil {
		return sharedmodel.ExistingOutput{}, err
	}

	generatedRoot := filepath.Join(rootPath, filepath.FromSlash(docsDir))
	paths := make([]string, 0)
	err := filepath.WalkDir(generatedRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(rootPath, current)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return sharedmodel.ExistingOutput{}, fmt.Errorf("scan generated output: %w", err)
	}
	sort.Strings(paths)

	stateExists := false
	if _, statErr := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(statePath))); statErr == nil {
		stateExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return sharedmodel.ExistingOutput{}, fmt.Errorf("inspect state path: %w", statErr)
	}
	return sharedmodel.ExistingOutput{GeneratedPaths: paths, StateExists: stateExists}, nil
}

// Install writes every file in the transaction and removes every deletion as one
// recoverable unit. On any command-visible failure it rolls back to the pre-install
// state from on-disk backups.
func (r *OutputRepository) Install(ctx context.Context, rootPath string, transaction sharedmodel.OutputTransaction) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writePaths := make([]string, 0, len(transaction.Writes))
	for _, write := range transaction.Writes {
		if err := validateRepositoryPath(write.Path); err != nil {
			return err
		}
		writePaths = append(writePaths, write.Path)
	}
	for _, deletePath := range transaction.Deletes {
		if err := validateRepositoryPath(deletePath); err != nil {
			return err
		}
	}

	txPath := filepath.Join(rootPath, filepath.FromSlash(transactionDir))
	if err := os.RemoveAll(txPath); err != nil {
		return fmt.Errorf("clear transaction directory: %w", err)
	}
	stagingPath := filepath.Join(txPath, transactionStaging)
	backupPath := filepath.Join(txPath, transactionBackup)
	if err := os.MkdirAll(stagingPath, 0o755); err != nil {
		return fmt.Errorf("create staging: %w", err)
	}
	if err := os.MkdirAll(backupPath, 0o755); err != nil {
		return fmt.Errorf("create backup: %w", err)
	}

	// Phase 1: stage all writes on the same filesystem.
	for _, write := range transaction.Writes {
		staged := filepath.Join(stagingPath, filepath.FromSlash(write.Path))
		if err := writeFileSynced(staged, write.Data); err != nil {
			_ = os.RemoveAll(txPath)
			return fmt.Errorf("stage %q: %w", write.Path, err)
		}
	}

	// Phase 2: record the journal so an interrupted transaction can be recovered.
	if err := writeJournal(txPath, journal{Writes: writePaths, Deletes: transaction.Deletes}); err != nil {
		_ = os.RemoveAll(txPath)
		return err
	}

	// Phase 3: back up every existing target, then move staged files into place.
	targets := append(append([]string(nil), writePaths...), transaction.Deletes...)
	if err := backupExisting(rootPath, backupPath, targets); err != nil {
		rollback(rootPath, txPath, writePaths)
		return err
	}
	for _, write := range transaction.Writes {
		staged := filepath.Join(stagingPath, filepath.FromSlash(write.Path))
		final := filepath.Join(rootPath, filepath.FromSlash(write.Path))
		if err := movePath(staged, final); err != nil {
			rollback(rootPath, txPath, writePaths)
			return fmt.Errorf("install %q: %w", write.Path, err)
		}
	}

	// Phase 4: mark committed, then clean up. After the commit marker exists, recovery
	// rolls forward (cleanup only) instead of rolling back.
	if err := writeFileSynced(filepath.Join(txPath, transactionCommit), []byte("committed\n")); err != nil {
		rollback(rootPath, txPath, writePaths)
		return fmt.Errorf("mark committed: %w", err)
	}
	pruneEmptyDirs(rootPath, transaction.Deletes)
	if err := os.RemoveAll(txPath); err != nil {
		return fmt.Errorf("clean up transaction: %w", err)
	}
	return nil
}

// Recover completes or rolls back an interrupted transaction left by a previous run. It
// is safe to call when no transaction is pending.
func (r *OutputRepository) Recover(ctx context.Context, rootPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	txPath := filepath.Join(rootPath, filepath.FromSlash(transactionDir))
	journalPath := filepath.Join(txPath, transactionJournal)
	data, err := os.ReadFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read transaction journal: %w", err)
	}
	var record journal
	if err := json.Unmarshal(data, &record); err != nil {
		return fmt.Errorf("decode transaction journal: %w", err)
	}

	if _, statErr := os.Lstat(filepath.Join(txPath, transactionCommit)); statErr == nil {
		// The move phase completed; roll forward by cleaning up only.
		pruneEmptyDirs(rootPath, record.Deletes)
		return os.RemoveAll(txPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect commit marker: %w", statErr)
	}
	rollback(rootPath, txPath, record.Writes)
	return nil
}

func backupExisting(rootPath, backupPath string, targets []string) error {
	for _, target := range targets {
		source := filepath.Join(rootPath, filepath.FromSlash(target))
		if _, err := os.Lstat(source); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect existing %q: %w", target, err)
		}
		destination := filepath.Join(backupPath, filepath.FromSlash(target))
		if err := movePath(source, destination); err != nil {
			return fmt.Errorf("back up %q: %w", target, err)
		}
	}
	return nil
}

// rollback restores the pre-install state: it removes any files placed at write targets
// and moves every backed-up file back to its original location.
func rollback(rootPath, txPath string, writePaths []string) {
	for _, write := range writePaths {
		_ = os.Remove(filepath.Join(rootPath, filepath.FromSlash(write)))
	}
	backupPath := filepath.Join(txPath, transactionBackup)
	_ = filepath.WalkDir(backupPath, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(backupPath, current)
		if err != nil {
			return nil
		}
		_ = movePath(current, filepath.Join(rootPath, relative))
		return nil
	})
	_ = os.RemoveAll(txPath)
}

func writeJournal(txPath string, record journal) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode transaction journal: %w", err)
	}
	return writeFileSynced(filepath.Join(txPath, transactionJournal), data)
}

func writeFileSynced(destination string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func movePath(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	return nil
}

// pruneEmptyDirs removes now-empty ancestor directories of deleted files, stopping at the
// first non-empty directory. It is best effort and never removes the repository root.
func pruneEmptyDirs(rootPath string, deletes []string) {
	for _, deletePath := range deletes {
		directory := filepath.Dir(filepath.Join(rootPath, filepath.FromSlash(deletePath)))
		for {
			relative, err := filepath.Rel(rootPath, directory)
			if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
				break
			}
			if err := os.Remove(directory); err != nil {
				break
			}
			directory = filepath.Dir(directory)
		}
	}
}
