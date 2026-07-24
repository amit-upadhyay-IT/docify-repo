package usecase

import (
	"reflect"
	"sort"
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func TestDiscoverComponentsAppliesRulesAndAttachesSupportingFiles(t *testing.T) {
	files := []sharedmodel.SourceFile{
		planSource("README.md", sharedmodel.RoleProse, false, "readme"),
		planSource("apps/api/main.go", sharedmodel.RoleProductionSource, true, "api"),
		planSource("apps/api/main_test.go", sharedmodel.RoleTest, false, "test"),
		planSource("apps/worker/main.go", sharedmodel.RoleProductionSource, true, "worker"),
		planSource("custom/nested/main.py", sharedmodel.RoleProductionSource, true, "custom"),
		planSource("custom/nested/pyproject.toml", sharedmodel.RoleDependencyManifest, true, "manifest"),
		planSource("misc/tool.rb", sharedmodel.RoleProductionSource, true, "tool"),
		planSource("orphan/only_test.go", sharedmodel.RoleTest, false, "orphan"),
		planSource("root.go", sharedmodel.RoleProductionSource, true, "root"),
	}
	policy := testPlanInput().ComponentPolicy
	policy.Roots = []string{"custom", "custom/nested"}

	components, owned, err := discoverComponents(files, policy, "docs/generated")
	if err != nil {
		t.Fatalf("discoverComponents() error = %v", err)
	}
	gotKeys := make([]string, 0, len(components))
	for _, component := range components {
		gotKeys = append(gotKeys, component.Key)
	}
	if want := []string{"@root", "apps/api", "apps/worker", "custom/nested", "misc"}; !reflect.DeepEqual(gotKeys, want) {
		t.Fatalf("component keys = %v, want %v", gotKeys, want)
	}
	if ownerByPath(owned, "custom/nested/main.py") != "custom/nested" {
		t.Error("longest explicit root did not win")
	}
	api := componentByDocument(t, components, "docs/generated/components/apps/api/index.md")
	if len(api.SupportingFiles) != 1 || api.SupportingFiles[0].Path != "apps/api/main_test.go" {
		t.Errorf("API supporting files = %+v, want same-component test", api.SupportingFiles)
	}
	for _, component := range components {
		for _, supporting := range component.SupportingFiles {
			if supporting.Path == "orphan/only_test.go" {
				t.Fatal("supporting-only path created or attached to a component")
			}
		}
	}
}

func TestDiscoverComponentsUsesDeepestManifestRoot(t *testing.T) {
	files := []sharedmodel.SourceFile{
		planSource("platform/go.mod", sharedmodel.RoleDependencyManifest, true, "outer"),
		planSource("platform/service/package.json", sharedmodel.RoleDependencyManifest, true, "inner"),
		planSource("platform/service/src/main.ts", sharedmodel.RoleProductionSource, true, "source"),
	}
	components, owned, err := discoverComponents(files, testPlanInput().ComponentPolicy, "docs/generated")
	if err != nil {
		t.Fatalf("discoverComponents() error = %v", err)
	}
	if got := ownerByPath(owned, "platform/service/src/main.ts"); got != "platform/service" {
		t.Fatalf("owner = %q, want deepest manifest root", got)
	}
	if len(components) != 2 {
		t.Fatalf("len(components) = %d, want outer and inner manifest components", len(components))
	}
}

func TestComponentArtifactEncodingRoundTrips(t *testing.T) {
	tests := map[string]string{
		"services/payments": "services/payments",
		"src/Catalog":       "src/%43atalog",
		"con":               "%63on",
		"con.txt":           "%63on.txt",
		"name.":             "name%2E",
		"@root":             "%40root",
		"percent%name":      "percent%25name",
		"café":              "caf%C3%A9",
	}
	keys := make([]string, 0, len(tests))
	for key := range tests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		encoded := encodeComponentPath(key)
		if encoded != tests[key] {
			t.Errorf("encodeComponentPath(%q) = %q, want %q", key, encoded, tests[key])
			continue
		}
		decoded, err := decodeComponentPath(encoded)
		if err != nil {
			t.Errorf("decodeComponentPath(%q) error = %v", encoded, err)
		} else if decoded != key {
			t.Errorf("decodeComponentPath(%q) = %q, want %q", encoded, decoded, key)
		}
	}
	if decoded, err := decodeComponentPath("@root"); err != nil || decoded != "@root" {
		t.Errorf("reserved root decode = %q, %v", decoded, err)
	}
	if _, err := decodeComponentPath("%61pp"); err == nil {
		t.Fatal("decodeComponentPath() accepted non-canonical encoding")
	}
}

func TestRealAtRootDirectoryDoesNotCollideWithReservedRootArtifact(t *testing.T) {
	files := []sharedmodel.SourceFile{
		planSource("main.go", sharedmodel.RoleProductionSource, true, "root"),
		planSource("@root/main.go", sharedmodel.RoleProductionSource, true, "directory"),
	}
	components, _, err := discoverComponents(files, testPlanInput().ComponentPolicy, "docs/generated")
	if err != nil {
		t.Fatalf("discoverComponents() error = %v", err)
	}
	documents := []string{components[0].Document, components[1].Document}
	sort.Strings(documents)
	want := []string{
		"docs/generated/components/%40root/index.md",
		"docs/generated/components/@root/index.md",
	}
	if !reflect.DeepEqual(documents, want) {
		t.Errorf("documents = %v, want %v", documents, want)
	}
}

func planSource(repositoryPath string, role sharedmodel.SourceRole, triggers bool, content string) sharedmodel.SourceFile {
	return sharedmodel.SourceFile{
		Path: repositoryPath, Role: role, SourceHash: hashText(content), TriggersRegeneration: triggers,
		Data: []byte(content), Size: int64(len(content)),
	}
}

func ownerByPath(files []sharedmodel.SourceFile, repositoryPath string) string {
	for _, file := range files {
		if file.Path == repositoryPath {
			return file.ComponentKey
		}
	}
	return ""
}

func componentByDocument(t *testing.T, components []sharedmodel.Component, document string) sharedmodel.Component {
	t.Helper()
	for _, component := range components {
		if component.Document == document {
			return component
		}
	}
	t.Fatalf("component document %q not found", document)
	return sharedmodel.Component{}
}
