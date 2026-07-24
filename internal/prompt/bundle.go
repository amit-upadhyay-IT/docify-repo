// Package prompt owns the go:embed directive for the versioned prompt subtree and
// exposes immutable prompt bytes plus their content hash. It lives here, rather than
// in the usecase package, because go:embed cannot reference files in a parent or
// sibling directory of the importing package.
package prompt

import (
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"fmt"
	"sort"
)

// Identifier is the versioned prompt bundle identity recorded in requests and state.
const Identifier = "codebase-summary/v1"

//go:embed codebase-summary/v1/system.txt
//go:embed codebase-summary/v1/component.txt
//go:embed codebase-summary/v1/synthesis.txt
//go:embed codebase-summary/v1/repair.txt
//go:embed codebase-summary/v1/schema.json
var resources embed.FS

// resourcePaths is the fixed, sorted set of embedded resources. The content hash is
// computed over these in order so the same bundle always yields the same hash.
var resourcePaths = []string{
	"codebase-summary/v1/component.txt",
	"codebase-summary/v1/repair.txt",
	"codebase-summary/v1/schema.json",
	"codebase-summary/v1/synthesis.txt",
	"codebase-summary/v1/system.txt",
}

// Bundle is an immutable view of one versioned prompt bundle. Text fields are strings
// so callers cannot mutate embedded bytes; Schema is returned as a fresh copy.
type Bundle struct {
	identifier  string
	system      string
	component   string
	synthesis   string
	repair      string
	schema      []byte
	contentHash string
}

// CodebaseSummaryV1 returns the embedded codebase-summary/v1 prompt bundle.
func CodebaseSummaryV1() Bundle {
	return loaded
}

// Identifier returns the versioned bundle identity.
func (b Bundle) Identifier() string { return b.identifier }

// System returns the stable system prompt.
func (b Bundle) System() string { return b.system }

// Component returns the normal or single-batch component analysis prompt.
func (b Bundle) Component() string { return b.component }

// Synthesis returns the batch-synthesis prompt.
func (b Bundle) Synthesis() string { return b.synthesis }

// Repair returns the single-repair prompt.
func (b Bundle) Repair() string { return b.repair }

// Schema returns a fresh copy of the strict response schema bytes.
func (b Bundle) Schema() []byte {
	out := make([]byte, len(b.schema))
	copy(out, b.schema)
	return out
}

// ContentHash returns a deterministic hash over every resource in the bundle. Any
// change to a prompt resource changes this hash, which invalidates affected component
// input hashes.
func (b Bundle) ContentHash() string { return b.contentHash }

// loaded is built once at initialization from the compile-time embedded files.
var loaded = mustLoad()

func mustLoad() Bundle {
	read := func(path string) []byte {
		data, err := resources.ReadFile(path)
		if err != nil {
			panic(fmt.Sprintf("prompt bundle missing embedded resource %q: %v", path, err))
		}
		return data
	}

	digest := sha256.New()
	writeField(digest, Identifier)
	sorted := append([]string(nil), resourcePaths...)
	sort.Strings(sorted)
	for _, path := range sorted {
		writeField(digest, path)
		writeField(digest, string(read(path)))
	}

	return Bundle{
		identifier:  Identifier,
		system:      string(read("codebase-summary/v1/system.txt")),
		component:   string(read("codebase-summary/v1/component.txt")),
		synthesis:   string(read("codebase-summary/v1/synthesis.txt")),
		repair:      string(read("codebase-summary/v1/repair.txt")),
		schema:      read("codebase-summary/v1/schema.json"),
		contentHash: fmt.Sprintf("sha256:%x", digest.Sum(nil)),
	}
}

func writeField(destination interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write([]byte(value))
}
