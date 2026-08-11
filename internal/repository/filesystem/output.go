package filesystem

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	transactionDir      = ".docify/tx"
	transactionStaging  = "staging"
	transactionBackup   = "backup"
	transactionJournal  = "journal.json"
	transactionCommit   = "committed"
	transactionConflict = "conflict"
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
	Writes        []string                         `json:"writes"`
	WriteHashes   map[string]string                `json:"write_hashes,omitempty"`
	Deletes       []string                         `json:"deletes"`
	Preconditions []sharedmodel.OutputPrecondition `json:"preconditions,omitempty"`
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
	for _, precondition := range transaction.Preconditions {
		if err := validateRepositoryPath(precondition.Path); err != nil {
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
	writeHashes := make(map[string]string, len(transaction.Writes))
	for _, write := range transaction.Writes {
		writeHashes[write.Path] = outputContentHash(write.Data)
	}
	if err := writeJournal(txPath, journal{
		Writes: writePaths, WriteHashes: writeHashes, Deletes: transaction.Deletes,
		Preconditions: transaction.Preconditions,
	}); err != nil {
		_ = os.RemoveAll(txPath)
		return err
	}

	// Phase 3: back up every existing target, then move staged files into place.
	if err := verifyOutputPreconditions(rootPath, transaction.Preconditions); err != nil {
		_ = os.RemoveAll(txPath)
		return err
	}
	targets := append(append([]string(nil), writePaths...), transaction.Deletes...)
	targetSet := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		targetSet[target] = struct{}{}
	}
	if err := backupExisting(rootPath, backupPath, targets); err != nil {
		_ = rollbackRuntime(rootPath, txPath, nil, targetSet)
		return err
	}
	if err := verifyBackedUpPreconditions(backupPath, transaction.Preconditions, targetSet); err != nil {
		_ = rollbackRuntime(rootPath, txPath, nil, targetSet)
		return err
	}
	installed := make(map[string]string, len(transaction.Writes))
	for _, write := range transaction.Writes {
		staged := filepath.Join(stagingPath, filepath.FromSlash(write.Path))
		final := filepath.Join(rootPath, filepath.FromSlash(write.Path))
		if err := installStagedNoReplace(staged, final); err != nil {
			_ = rollbackRuntime(rootPath, txPath, installed, targetSet)
			return fmt.Errorf("install %q: %w", write.Path, err)
		}
		installed[write.Path] = writeHashes[write.Path]
	}
	if err := verifyUntouchedPreconditions(rootPath, transaction.Preconditions, targetSet); err != nil {
		_ = rollbackRuntime(rootPath, txPath, installed, targetSet)
		return err
	}
	if err := verifyInstalledWrites(rootPath, transaction.Writes); err != nil {
		_ = rollbackRuntime(rootPath, txPath, installed, targetSet)
		return err
	}
	if err := verifyBackedUpPreconditions(backupPath, transaction.Preconditions, targetSet); err != nil {
		_ = rollbackRuntime(rootPath, txPath, installed, targetSet)
		return err
	}

	// Phase 4: mark committed, then clean up. After the commit marker exists, recovery
	// rolls forward (cleanup only) instead of rolling back.
	if err := writeFileSynced(filepath.Join(txPath, transactionCommit), []byte("committed\n")); err != nil {
		_ = rollbackRuntime(rootPath, txPath, installed, targetSet)
		return fmt.Errorf("mark committed: %w", err)
	}
	pruneEmptyDirs(rootPath, transaction.Deletes)
	if err := os.RemoveAll(txPath); err != nil {
		return fmt.Errorf("clean up transaction: %w", err)
	}
	return nil
}

func verifyBackedUpPreconditions(backupPath string, preconditions []sharedmodel.OutputPrecondition, targets map[string]struct{}) error {
	for _, precondition := range preconditions {
		if _, targeted := targets[precondition.Path]; !targeted {
			continue
		}
		if err := verifyFilePrecondition(backupPath, precondition); err != nil {
			return err
		}
	}
	return nil
}

func verifyUntouchedPreconditions(rootPath string, preconditions []sharedmodel.OutputPrecondition, targets map[string]struct{}) error {
	for _, precondition := range preconditions {
		if _, targeted := targets[precondition.Path]; targeted {
			continue
		}
		if err := verifyFilePrecondition(rootPath, precondition); err != nil {
			return err
		}
	}
	return nil
}

func verifyInstalledWrites(rootPath string, writes []sharedmodel.RenderedDocument) error {
	for _, write := range writes {
		installed, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(write.Path)))
		if err != nil || !bytes.Equal(installed, write.Data) {
			return fmt.Errorf("output path %q changed during installation", write.Path)
		}
	}
	return nil
}

func verifyOutputPreconditions(rootPath string, preconditions []sharedmodel.OutputPrecondition) error {
	seen := make(map[string]struct{}, len(preconditions))
	for _, precondition := range preconditions {
		if _, duplicate := seen[precondition.Path]; duplicate {
			return fmt.Errorf("duplicate output precondition for %q", precondition.Path)
		}
		seen[precondition.Path] = struct{}{}
		if err := verifyFilePrecondition(rootPath, precondition); err != nil {
			return err
		}
	}
	return nil
}

func verifyFilePrecondition(rootPath string, precondition sharedmodel.OutputPrecondition) error {
	fullPath := filepath.Join(rootPath, filepath.FromSlash(precondition.Path))
	info, err := os.Lstat(fullPath)
	if !precondition.MustExist {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("verify output precondition %q: %w", precondition.Path, err)
		}
		return fmt.Errorf("output path %q changed after ownership validation", precondition.Path)
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("output path %q changed after ownership validation", precondition.Path)
	}
	if err != nil {
		return fmt.Errorf("verify output precondition %q: %w", precondition.Path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("output path %q changed after ownership validation", precondition.Path)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("verify output precondition %q: %w", precondition.Path, err)
	}
	digest := sha256.Sum256(data)
	if actual := fmt.Sprintf("sha256:%x", digest); actual != precondition.ContentHash {
		return fmt.Errorf("output path %q changed after ownership validation", precondition.Path)
	}
	return nil
}

// Recover completes or rolls back an interrupted transaction left by a previous run. It
// is safe to call when no transaction is pending.
func (r *OutputRepository) Recover(ctx context.Context, rootPath, docsDir, statePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRepositoryPath(docsDir); err != nil {
		return err
	}
	if err := validateRepositoryPath(statePath); err != nil {
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
	targets, err := validateRecoveryJournal(record, docsDir, statePath)
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(filepath.Join(txPath, transactionConflict)); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect transaction conflict marker: %w", statErr)
	}

	if _, statErr := os.Lstat(filepath.Join(txPath, transactionCommit)); statErr == nil {
		// The move phase completed; roll forward by cleaning up only.
		pruneEmptyDirs(rootPath, record.Deletes)
		return os.RemoveAll(txPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect commit marker: %w", statErr)
	}
	if len(record.Preconditions) > 0 {
		return recoverHashedTransaction(rootPath, txPath, record, targets)
	}
	if len(record.WriteHashes) == 0 {
		return recoverLegacyTransaction(rootPath, txPath, record, targets)
	}
	_ = writeFileSynced(filepath.Join(txPath, transactionConflict), []byte("conflict\n"))
	return fmt.Errorf("transaction journal lacks ownership preconditions; preserve %q and recover it manually", filepath.ToSlash(txPath))
}

// recoverLegacyTransaction rolls back the original writes/deletes-only journal format.
// A missing staged write proves that the old rename-based installer moved that candidate;
// writes still in staging were not installed and their live paths remain untouched.
func recoverLegacyTransaction(rootPath, txPath string, record journal, targets map[string]struct{}) error {
	stagingPath := filepath.Join(txPath, transactionStaging)
	stagingInfo, err := os.Lstat(stagingPath)
	if err != nil || !stagingInfo.IsDir() {
		_ = writeFileSynced(filepath.Join(txPath, transactionConflict), []byte("conflict\n"))
		return fmt.Errorf("legacy transaction has an invalid staging directory; preserve %q and recover it manually", filepath.ToSlash(txPath))
	}
	backupPath := filepath.Join(txPath, transactionBackup)
	backupInfo, err := os.Lstat(backupPath)
	if err != nil || !backupInfo.IsDir() {
		_ = writeFileSynced(filepath.Join(txPath, transactionConflict), []byte("conflict\n"))
		return fmt.Errorf("legacy transaction has an invalid backup directory; preserve %q and recover it manually", filepath.ToSlash(txPath))
	}

	conflict := false
	_ = filepath.WalkDir(backupPath, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			conflict = true
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(backupPath, current)
		if err != nil {
			conflict = true
			return nil
		}
		relative = filepath.ToSlash(relative)
		if _, planned := targets[relative]; !planned || entry.Type()&os.ModeType != 0 {
			conflict = true
		}
		return nil
	})

	installed := make(map[string]string)
	for _, path := range record.Writes {
		staged := filepath.Join(stagingPath, filepath.FromSlash(path))
		info, err := os.Lstat(staged)
		if err == nil {
			if !info.Mode().IsRegular() {
				conflict = true
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			conflict = true
			continue
		}
		live, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			conflict = true
			continue
		}
		installed[path] = outputContentHash(live)
	}
	if conflict {
		_ = writeFileSynced(filepath.Join(txPath, transactionConflict), []byte("conflict\n"))
		return fmt.Errorf("legacy transaction has an output ownership conflict; backups were preserved")
	}
	return rollbackRuntime(rootPath, txPath, installed, targets)
}

// recoverHashedTransaction can safely roll back a modern transaction regardless of
// whether the crash happened before backup, during backup, or after candidate install.
// Original preconditions distinguish untouched originals from installed candidates.
func recoverHashedTransaction(rootPath, txPath string, record journal, targets map[string]struct{}) error {
	fullyInstalled := true
	for _, path := range record.Writes {
		data, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(path)))
		if err != nil || outputContentHash(data) != record.WriteHashes[path] {
			fullyInstalled = false
			break
		}
	}
	if fullyInstalled {
		for _, path := range record.Deletes {
			if _, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
				fullyInstalled = false
				break
			}
		}
	}
	if fullyInstalled {
		pruneEmptyDirs(rootPath, record.Deletes)
		return os.RemoveAll(txPath)
	}

	preconditions := make(map[string]sharedmodel.OutputPrecondition, len(record.Preconditions))
	for _, precondition := range record.Preconditions {
		preconditions[precondition.Path] = precondition
	}
	conflict := false
	backedUp := make(map[string]struct{})
	backupPath := filepath.Join(txPath, transactionBackup)
	_ = filepath.WalkDir(backupPath, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if current != backupPath || !errors.Is(walkErr, os.ErrNotExist) {
				conflict = true
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(backupPath, current)
		if err != nil {
			conflict = true
			return nil
		}
		relative = filepath.ToSlash(relative)
		if _, planned := targets[relative]; !planned {
			conflict = true
			return nil
		}
		precondition, known := preconditions[relative]
		if !known || !precondition.MustExist || entry.Type()&os.ModeType != 0 {
			conflict = true
			return nil
		}
		backup, err := os.ReadFile(current)
		if err != nil || outputContentHash(backup) != precondition.ContentHash {
			conflict = true
			return nil
		}
		backedUp[relative] = struct{}{}
		return nil
	})

	installed := make(map[string]string)
	for _, path := range record.Writes {
		candidateHash, hasCandidateHash := record.WriteHashes[path]
		precondition, known := preconditions[path]
		if !hasCandidateHash || !known {
			conflict = true
			continue
		}
		fullPath := filepath.Join(rootPath, filepath.FromSlash(path))
		data, err := os.ReadFile(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			if precondition.MustExist {
				if _, exists := backedUp[path]; !exists {
					conflict = true
				}
			}
			continue
		}
		if err != nil {
			conflict = true
			continue
		}
		hash := outputContentHash(data)
		if precondition.MustExist && hash == precondition.ContentHash {
			continue
		}
		if hash == candidateHash {
			installed[path] = candidateHash
			if precondition.MustExist {
				if _, exists := backedUp[path]; !exists {
					conflict = true
				}
			}
			continue
		}
		conflict = true
	}
	for _, path := range record.Deletes {
		precondition, known := preconditions[path]
		if !known || !precondition.MustExist {
			conflict = true
			continue
		}
		data, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			if _, exists := backedUp[path]; !exists {
				conflict = true
			}
			continue
		}
		if err != nil || outputContentHash(data) != precondition.ContentHash {
			conflict = true
		}
	}
	if conflict {
		_ = writeFileSynced(filepath.Join(txPath, transactionConflict), []byte("conflict\n"))
		return fmt.Errorf("transaction has an output ownership conflict; backups were preserved")
	}
	return rollbackRuntime(rootPath, txPath, installed, targets)
}

func validateRecoveryJournal(record journal, docsDir, statePath string) (map[string]struct{}, error) {
	targets := make(map[string]struct{}, len(record.Writes)+len(record.Deletes))
	seenWrites := make(map[string]struct{}, len(record.Writes))
	for _, path := range record.Writes {
		if err := validateRecoveryPath(path, docsDir, statePath); err != nil {
			return nil, fmt.Errorf("transaction journal contains invalid write path: %w", err)
		}
		if _, duplicate := seenWrites[path]; duplicate {
			return nil, fmt.Errorf("transaction journal contains duplicate write path %q", path)
		}
		seenWrites[path] = struct{}{}
		targets[path] = struct{}{}
	}
	seenDeletes := make(map[string]struct{}, len(record.Deletes))
	for _, path := range record.Deletes {
		if err := validateRecoveryPath(path, docsDir, statePath); err != nil {
			return nil, fmt.Errorf("transaction journal contains invalid delete path: %w", err)
		}
		if _, duplicate := seenDeletes[path]; duplicate {
			return nil, fmt.Errorf("transaction journal contains duplicate delete path %q", path)
		}
		seenDeletes[path] = struct{}{}
		targets[path] = struct{}{}
	}
	for path := range record.WriteHashes {
		if err := validateRecoveryPath(path, docsDir, statePath); err != nil {
			return nil, fmt.Errorf("transaction journal contains invalid write hash path: %w", err)
		}
		if _, planned := seenWrites[path]; !planned {
			return nil, fmt.Errorf("transaction journal contains an unplanned write hash for %q", path)
		}
	}
	seenPreconditions := make(map[string]struct{}, len(record.Preconditions))
	for _, precondition := range record.Preconditions {
		if err := validateRecoveryPath(precondition.Path, docsDir, statePath); err != nil {
			return nil, fmt.Errorf("transaction journal contains invalid precondition path: %w", err)
		}
		if _, duplicate := seenPreconditions[precondition.Path]; duplicate {
			return nil, fmt.Errorf("transaction journal contains duplicate precondition path %q", precondition.Path)
		}
		seenPreconditions[precondition.Path] = struct{}{}
	}
	return targets, nil
}

func validateRecoveryPath(path, docsDir, statePath string) error {
	if err := validateRepositoryPath(path); err != nil {
		return err
	}
	if path == transactionDir || strings.HasPrefix(path, transactionDir+"/") {
		return fmt.Errorf("path %q targets the transaction directory", path)
	}
	if path != statePath && !strings.HasPrefix(path, docsDir+"/") {
		return fmt.Errorf("path %q is outside configured generated output", path)
	}
	return nil
}

// rollbackRuntime restores backups only when doing so cannot overwrite a path changed
// concurrently. On conflict it preserves both the visible path and backup for manual
// recovery, and writes a marker recording that the rollback needs to be retried.
func rollbackRuntime(rootPath, txPath string, installed map[string]string, targets map[string]struct{}) error {
	conflict := false
	quarantineRoot, err := os.MkdirTemp(txPath, "rollback-installed-")
	if err != nil {
		_ = writeFileSynced(filepath.Join(txPath, transactionConflict), []byte("conflict\n"))
		return fmt.Errorf("create rollback quarantine: %w", err)
	}
	for path, expectedHash := range installed {
		fullPath := filepath.Join(rootPath, filepath.FromSlash(path))
		quarantined := filepath.Join(quarantineRoot, filepath.FromSlash(path))
		if err := movePath(fullPath, quarantined); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			conflict = true
			continue
		}
		current, err := os.ReadFile(quarantined)
		if err == nil && outputContentHash(current) == expectedHash {
			continue
		}
		// The path changed before the atomic quarantine. Restore only if no
		// concurrent replacement has appeared; otherwise preserve both copies.
		if restoreErr := installStagedNoReplace(quarantined, fullPath); restoreErr != nil {
			conflict = true
		}
		conflict = true
	}

	backupPath := filepath.Join(txPath, transactionBackup)
	_ = filepath.WalkDir(backupPath, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if current != backupPath || !errors.Is(walkErr, os.ErrNotExist) {
				conflict = true
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(backupPath, current)
		if err != nil {
			conflict = true
			return nil
		}
		relative = filepath.ToSlash(relative)
		if _, planned := targets[relative]; !planned {
			conflict = true
			return nil
		}
		destination := filepath.Join(rootPath, filepath.FromSlash(relative))
		if _, err := os.Lstat(destination); err == nil {
			backup, backupErr := os.ReadFile(current)
			live, liveErr := os.ReadFile(destination)
			if backupErr == nil && liveErr == nil && bytes.Equal(live, backup) {
				return nil
			}
			conflict = true
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			conflict = true
			return nil
		}
		if err := installStagedNoReplace(current, destination); err != nil {
			conflict = true
		}
		return nil
	})
	if conflict {
		_ = writeFileSynced(filepath.Join(txPath, transactionConflict), []byte("conflict\n"))
		return fmt.Errorf("output changed during installation; backups were preserved")
	}
	return os.RemoveAll(txPath)
}

func outputContentHash(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest)
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
		if err := installStagedNoReplace(source, destination); err != nil {
			return fmt.Errorf("back up %q: %w", target, err)
		}
	}
	return nil
}

// rollback restores the pre-install state: it removes any files placed at write targets
// and moves every backed-up file back to its original location.
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

func installStagedNoReplace(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Link(source, destination); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		_ = os.Remove(destination)
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
