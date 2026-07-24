package model

// TrackedEntry is raw Git index metadata for one repository-relative path.
type TrackedEntry struct {
	Path     string `json:"path"`
	Mode     string `json:"mode"`
	ObjectID string `json:"object_id,omitempty"`
}

// FileContent is a bounded worktree read. Data is never serialized in reports.
type FileContent struct {
	Path      string `json:"path"`
	Data      []byte `json:"-"`
	Size      int64  `json:"size"`
	Symlink   bool   `json:"symlink"`
	Truncated bool   `json:"truncated"`
}

type SourceRole string

const (
	RoleProductionSource       SourceRole = "production_source"
	RoleContract               SourceRole = "contract"
	RoleDatabase               SourceRole = "database"
	RoleRuntimeConfiguration   SourceRole = "runtime_configuration"
	RoleDependencyManifest     SourceRole = "dependency_manifest"
	RoleTest                   SourceRole = "test"
	RoleGeneratedCode          SourceRole = "generated_code"
	RoleLockFile               SourceRole = "lock_file"
	RoleFixture                SourceRole = "fixture"
	RoleGeneratedDocumentation SourceRole = "generated_documentation"
	RoleProse                  SourceRole = "prose"
	RoleUnknownSource          SourceRole = "unknown_source"
	RoleSecret                 SourceRole = "secret"
	RoleBinary                 SourceRole = "binary"
	RoleOversized              SourceRole = "oversized"
	RoleSpecialGitEntry        SourceRole = "special_git_entry"
)

type SourceDecision struct {
	Path                 string     `json:"path"`
	Role                 SourceRole `json:"role"`
	ComponentKey         string     `json:"component_key,omitempty"`
	IncludedAsContext    bool       `json:"included_as_context"`
	TriggersRegeneration bool       `json:"triggers_regeneration"`
	Reason               string     `json:"reason"`
	Size                 int64      `json:"size,omitempty"`
}

// SourceFile is eligible deterministic source context. Data is never serialized.
type SourceFile struct {
	Path                 string     `json:"path"`
	Role                 SourceRole `json:"role"`
	ComponentKey         string     `json:"component_key"`
	RootComponent        bool       `json:"root_component,omitempty"`
	SourceHash           string     `json:"source_hash"`
	TriggersRegeneration bool       `json:"triggers_regeneration"`
	Data                 []byte     `json:"-"`
	Size                 int64      `json:"size"`
}
