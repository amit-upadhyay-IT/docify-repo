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

	sharedmodel "docify-repo/internal/model"
)

// Identifier is the versioned prompt bundle identity recorded in requests and state.
const Identifier = "codebase-summary/v1"

// FragmentIdentifier is the independent prompt identity for bounded fragment
// contracts. The legacy dossier bundle remains unchanged until strategy rollout.
const FragmentIdentifier = "codebase-summary/v2"

//go:embed codebase-summary/v1/system.txt
//go:embed codebase-summary/v1/component.txt
//go:embed codebase-summary/v1/synthesis.txt
//go:embed codebase-summary/v1/repair.txt
//go:embed codebase-summary/v1/schema.json
//go:embed codebase-summary/v2/*.txt
//go:embed codebase-summary/v2/*.json
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

var fragmentResourcePaths = []string{
	"codebase-summary/v2/architecture.schema.json",
	"codebase-summary/v2/architecture.txt",
	"codebase-summary/v2/data_models.schema.json",
	"codebase-summary/v2/data_models.txt",
	"codebase-summary/v2/dependencies.schema.json",
	"codebase-summary/v2/dependencies.txt",
	"codebase-summary/v2/diagrams.schema.json",
	"codebase-summary/v2/diagrams.txt",
	"codebase-summary/v2/interfaces.schema.json",
	"codebase-summary/v2/interfaces.txt",
	"codebase-summary/v2/overview_candidate.schema.json",
	"codebase-summary/v2/overview_candidate.txt",
	"codebase-summary/v2/overview.schema.json",
	"codebase-summary/v2/overview.txt",
	"codebase-summary/v2/repair.txt",
	"codebase-summary/v2/review_gaps.schema.json",
	"codebase-summary/v2/review_gaps.txt",
	"codebase-summary/v2/system.txt",
	"codebase-summary/v2/workflows.schema.json",
	"codebase-summary/v2/workflows.txt",
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

type fragmentContract struct {
	prompt     string
	schema     []byte
	schemaName string
}

// FragmentBundle is an immutable view of the bounded fragment prompt contracts.
type FragmentBundle struct {
	identifier  string
	system      string
	repair      string
	contracts   map[sharedmodel.FragmentKind]fragmentContract
	overview    fragmentContract
	contentHash string
}

// CodebaseSummaryV1 returns the embedded codebase-summary/v1 prompt bundle.
func CodebaseSummaryV1() Bundle {
	return loaded
}

// CodebaseSummaryV2 returns the bounded fragment prompt bundle without changing the
// active legacy dossier bundle.
func CodebaseSummaryV2() FragmentBundle { return fragmentLoaded }

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

func (b FragmentBundle) Identifier() string  { return b.identifier }
func (b FragmentBundle) System() string      { return b.system }
func (b FragmentBundle) Repair() string      { return b.repair }
func (b FragmentBundle) ContentHash() string { return b.contentHash }

func (b FragmentBundle) OverviewPrompt() string { return b.overview.prompt }

func (b FragmentBundle) OverviewSchema() []byte {
	return append([]byte(nil), b.overview.schema...)
}

func (b FragmentBundle) OverviewSchemaName() string { return b.overview.schemaName }

// FragmentPrompt returns the section-specific prompt for kind.
func (b FragmentBundle) FragmentPrompt(kind sharedmodel.FragmentKind) (string, bool) {
	contract, ok := b.contracts[kind]
	return contract.prompt, ok
}

// FragmentSchema returns an isolated copy of the section-specific schema.
func (b FragmentBundle) FragmentSchema(kind sharedmodel.FragmentKind) ([]byte, bool) {
	contract, ok := b.contracts[kind]
	if !ok {
		return nil, false
	}
	result := append([]byte(nil), contract.schema...)
	return result, true
}

// FragmentSchemaName returns the stable provider schema name for kind.
func (b FragmentBundle) FragmentSchemaName(kind sharedmodel.FragmentKind) (string, bool) {
	contract, ok := b.contracts[kind]
	return contract.schemaName, ok
}

// loaded is built once at initialization from the compile-time embedded files.
var loaded = mustLoad()
var fragmentLoaded = mustLoadFragments()

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

func mustLoadFragments() FragmentBundle {
	read := func(path string) []byte {
		data, err := resources.ReadFile(path)
		if err != nil {
			panic(fmt.Sprintf("fragment prompt bundle missing embedded resource %q: %v", path, err))
		}
		return data
	}
	digest := contentHash(FragmentIdentifier, fragmentResourcePaths, read)
	contracts := make(map[sharedmodel.FragmentKind]fragmentContract, len(sharedmodel.FragmentKinds()))
	for _, kind := range sharedmodel.FragmentKinds() {
		name := string(kind)
		contracts[kind] = fragmentContract{
			prompt:     string(read("codebase-summary/v2/" + name + ".txt")),
			schema:     read("codebase-summary/v2/" + name + ".schema.json"),
			schemaName: "component_fragment_" + name,
		}
	}
	return FragmentBundle{
		identifier: FragmentIdentifier,
		system:     string(read("codebase-summary/v2/system.txt")),
		repair:     string(read("codebase-summary/v2/repair.txt")),
		contracts:  contracts,
		overview: fragmentContract{
			prompt: string(read("codebase-summary/v2/overview.txt")), schema: read("codebase-summary/v2/overview.schema.json"),
			schemaName: "component_overview_reducer",
		},
		contentHash: digest,
	}
}

func contentHash(identifier string, paths []string, read func(string) []byte) string {
	digest := sha256.New()
	writeField(digest, identifier)
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for _, path := range sorted {
		writeField(digest, path)
		writeField(digest, string(read(path)))
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil))
}

func writeField(destination interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = destination.Write([]byte(value))
}
