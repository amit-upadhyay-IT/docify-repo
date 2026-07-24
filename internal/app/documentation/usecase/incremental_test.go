package usecase

import (
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func installedState(paths ...string) (sharedmodel.State, sharedmodel.ExistingOutput, map[string][]byte) {
	state := sharedmodel.State{GeneratedContentHashes: map[string]string{}}
	content := map[string][]byte{}
	for _, path := range paths {
		body := []byte("content of " + path)
		state.GeneratedPaths = append(state.GeneratedPaths, path)
		state.GeneratedContentHashes[path] = contentHash(body)
		content[path] = body
	}
	existing := sharedmodel.ExistingOutput{GeneratedPaths: append([]string(nil), paths...)}
	return state, existing, content
}

func TestVerifyInstalledIntegrityAcceptsMatchingTree(t *testing.T) {
	state, existing, content := installedState("docs/generated/index.md", "docs/generated/architecture.md")
	if ok, reason := verifyInstalledIntegrity(state, existing, content); !ok {
		t.Fatalf("verifyInstalledIntegrity() = false (%s), want true", reason)
	}
}

func TestVerifyInstalledIntegrityRejectsTamperedDocument(t *testing.T) {
	state, existing, content := installedState("docs/generated/index.md")
	content["docs/generated/index.md"] = []byte("edited by hand")
	if ok, _ := verifyInstalledIntegrity(state, existing, content); ok {
		t.Fatal("verifyInstalledIntegrity() = true for tampered content, want false")
	}
}

func TestVerifyInstalledIntegrityRejectsMissingDocument(t *testing.T) {
	state, existing, content := installedState("docs/generated/index.md", "docs/generated/architecture.md")
	// The architecture document is gone from disk.
	existing.GeneratedPaths = []string{"docs/generated/index.md"}
	delete(content, "docs/generated/architecture.md")
	if ok, _ := verifyInstalledIntegrity(state, existing, content); ok {
		t.Fatal("verifyInstalledIntegrity() = true with a missing owned document, want false")
	}
}

func TestVerifyInstalledIntegrityRejectsUnownedFile(t *testing.T) {
	state, existing, content := installedState("docs/generated/index.md")
	existing.GeneratedPaths = append(existing.GeneratedPaths, "docs/generated/stray.md")
	content["docs/generated/stray.md"] = []byte("not ours")
	if ok, _ := verifyInstalledIntegrity(state, existing, content); ok {
		t.Fatal("verifyInstalledIntegrity() = true with an unowned file present, want false")
	}
}

func TestResolveOwnershipComputesDeletes(t *testing.T) {
	existing := sharedmodel.ExistingOutput{GeneratedPaths: []string{"docs/generated/a.md", "docs/generated/gone.md"}}
	prior := map[string]struct{}{"docs/generated/a.md": {}, "docs/generated/gone.md": {}}
	candidate := map[string]struct{}{"docs/generated/a.md": {}, "docs/generated/new.md": {}}
	decision, err := resolveOwnership(existing, prior, true, candidate, false)
	if err != nil {
		t.Fatalf("resolveOwnership() error = %v", err)
	}
	if len(decision.deletes) != 1 || decision.deletes[0] != "docs/generated/gone.md" {
		t.Fatalf("deletes = %v, want [docs/generated/gone.md]", decision.deletes)
	}
}

func TestResolveOwnershipRefusesUnprovenStateWithoutFull(t *testing.T) {
	existing := sharedmodel.ExistingOutput{GeneratedPaths: []string{"docs/generated/a.md"}}
	candidate := map[string]struct{}{"docs/generated/a.md": {}}
	if _, err := resolveOwnership(existing, map[string]struct{}{}, false, candidate, false); err == nil {
		t.Fatal("resolveOwnership() error = nil, want refusal when ownership is unproven")
	}
}

func TestResolveOwnershipFullRecoveryOverwritesCandidatePaths(t *testing.T) {
	existing := sharedmodel.ExistingOutput{GeneratedPaths: []string{"docs/generated/a.md"}}
	candidate := map[string]struct{}{"docs/generated/a.md": {}}
	decision, err := resolveOwnership(existing, map[string]struct{}{}, false, candidate, true)
	if err != nil {
		t.Fatalf("full recovery error = %v, want success overwriting a reproduced path", err)
	}
	if len(decision.deletes) != 0 {
		t.Fatalf("full recovery deletes = %v, want none", decision.deletes)
	}
}

func TestResolveOwnershipFullRecoveryStillRefusesForeignFile(t *testing.T) {
	existing := sharedmodel.ExistingOutput{GeneratedPaths: []string{"docs/generated/a.md", "docs/generated/foreign.md"}}
	candidate := map[string]struct{}{"docs/generated/a.md": {}}
	if _, err := resolveOwnership(existing, map[string]struct{}{}, false, candidate, true); err == nil {
		t.Fatal("full recovery error = nil, want refusal for a file the candidate does not reproduce")
	}
}

func TestClassifyDiffCategorizesDocuments(t *testing.T) {
	rendered := renderedOutput{docs: []renderedDoc{
		{path: "docs/generated/added.md", content: "new"},
		{path: "docs/generated/changed.md", content: "v2"},
		{path: "docs/generated/same.md", content: "stable"},
	}}
	prior := map[string]string{
		"docs/generated/changed.md": contentHash([]byte("v1")),
		"docs/generated/same.md":    contentHash([]byte("stable")),
	}
	diff := classifyDiff(rendered, prior, []string{"docs/generated/removed.md"})
	if len(diff.Added) != 1 || diff.Added[0] != "docs/generated/added.md" {
		t.Errorf("added = %v", diff.Added)
	}
	if len(diff.Changed) != 1 || diff.Changed[0] != "docs/generated/changed.md" {
		t.Errorf("changed = %v", diff.Changed)
	}
	if len(diff.Unchanged) != 1 || diff.Unchanged[0] != "docs/generated/same.md" {
		t.Errorf("unchanged = %v", diff.Unchanged)
	}
	if len(diff.Deleted) != 1 || diff.Deleted[0] != "docs/generated/removed.md" {
		t.Errorf("deleted = %v", diff.Deleted)
	}
}
