package model

// RenderedDocument is one locally rendered generated file. Data is never serialized in
// reports or state; only the deterministic content hash and ownership metadata are safe
// to persist.
type RenderedDocument struct {
	Path          string   `json:"path"`
	Data          []byte   `json:"-"`
	ContentHash   string   `json:"content_hash"`
	ComponentKeys []string `json:"component_keys,omitempty"`
	Deterministic bool     `json:"deterministic,omitempty"`
}

// OutputDiff classifies how a candidate output set differs from the installed tree.
type OutputDiff struct {
	Added     []string `json:"added"`
	Changed   []string `json:"changed"`
	Deleted   []string `json:"deleted"`
	Unchanged []string `json:"unchanged"`
}

// ExistingOutput reports the tool-relevant paths currently present on disk so the
// usecase can make ownership-recovery decisions before any write.
type ExistingOutput struct {
	// GeneratedPaths are repository-relative paths that currently exist under the
	// configured generated directory, sorted bytewise.
	GeneratedPaths []string
	// StateExists reports whether the configured state path currently exists.
	StateExists bool
}

// OutputTransaction is the exact set of writes and deletes the filesystem repository
// installs atomically. The usecase has already validated ownership before this is built.
type OutputTransaction struct {
	// Writes are the files to create or replace, including the state file.
	Writes []RenderedDocument
	// Deletes are repository-relative paths to remove.
	Deletes []string
	// Preconditions describe the installed files observed during ownership
	// validation. The repository verifies them immediately before mutation.
	Preconditions []OutputPrecondition
}

// OutputPrecondition is a content-hash compare-and-swap guard for one installed path.
type OutputPrecondition struct {
	Path        string
	MustExist   bool
	ContentHash string
}
