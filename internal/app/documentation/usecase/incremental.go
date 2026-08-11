package usecase

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"sort"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

// installedInspection captures the currently installed generated output and whether
// decoded prior state independently proves ownership and integrity. Generation-version
// compatibility is evaluated by planning and does not invalidate ownership proof.
type installedInspection struct {
	existing    sharedmodel.ExistingOutput
	content     map[string][]byte
	stateOwned  bool
	stateHash   string
	provenState bool
	integrityOK bool
	reason      string
}

// inspectInstalled reads the installed generated tree and verifies it against prior state.
// It performs no model call; a mismatch is reported through integrityOK and reason so the
// caller can either refuse an incremental update (sync) or report staleness (check).
func (u *Usecase) inspectInstalled(ctx context.Context, input documentationmodel.PlanInput, state sharedmodel.StateLoadResult) (installedInspection, error) {
	existing, err := u.output.ExistingPaths(ctx, input.WorkingDirectory, input.SourcePolicy.DocsDir, input.SourcePolicy.StatePath)
	if err != nil {
		return installedInspection{}, outputValidationError{fmt.Sprintf("inspect installed output: %v", err)}
	}
	content, err := u.output.ReadInstalled(ctx, input.WorkingDirectory, existing.GeneratedPaths)
	if err != nil {
		return installedInspection{}, outputValidationError{fmt.Sprintf("read installed output: %v", err)}
	}

	inspection := installedInspection{existing: existing, content: content, stateOwned: !state.Missing && !state.Invalid}
	if inspection.stateOwned && !existing.StateExists {
		return installedInspection{}, outputValidationError{"the configured state file changed during ownership inspection"}
	}
	if existing.StateExists {
		stateContent, err := u.output.ReadInstalled(ctx, input.WorkingDirectory, []string{input.SourcePolicy.StatePath})
		if err != nil {
			return installedInspection{}, outputValidationError{fmt.Sprintf("read installed state: %v", err)}
		}
		data, ok := stateContent[input.SourcePolicy.StatePath]
		if !ok {
			return installedInspection{}, outputValidationError{"the configured state file changed during ownership inspection"}
		}
		inspection.stateHash = contentHash(data)
		if inspection.stateOwned {
			decoded, err := u.state.Decode(ctx, data)
			if err != nil || !reflect.DeepEqual(decoded, state) {
				return installedInspection{}, outputValidationError{"the configured state file changed during ownership inspection"}
			}
		}
	}
	if !inspection.stateOwned {
		if len(existing.GeneratedPaths) > 0 {
			inspection.reason = "no valid prior state proves ownership of the installed documentation"
		} else {
			inspection.reason = "no documentation has been generated yet"
		}
		return inspection, nil
	}
	inspection.integrityOK, inspection.reason = verifyInstalledIntegrity(state.State, existing, content)
	inspection.provenState = inspection.integrityOK
	return inspection, nil
}

// verifyInstalledIntegrity checks that the installed tree exactly matches what prior state
// records: every owned path present with a matching content hash, and no unowned file. It
// never inspects file bodies beyond hashing, so it exposes no source or prose.
func verifyInstalledIntegrity(state sharedmodel.State, existing sharedmodel.ExistingOutput, content map[string][]byte) (bool, string) {
	owned := make(map[string]struct{}, len(state.GeneratedPaths))
	for _, ownedPath := range state.GeneratedPaths {
		owned[ownedPath] = struct{}{}
	}
	installed := make(map[string]struct{}, len(existing.GeneratedPaths))
	for _, installedPath := range existing.GeneratedPaths {
		installed[installedPath] = struct{}{}
	}
	for _, installedPath := range existing.GeneratedPaths {
		if _, ok := owned[installedPath]; !ok {
			return false, "the generated directory contains a file not owned by prior state"
		}
	}
	for _, ownedPath := range state.GeneratedPaths {
		if _, ok := installed[ownedPath]; !ok {
			return false, "an owned generated document is missing"
		}
		data, ok := content[ownedPath]
		if !ok {
			return false, "an owned generated document is missing"
		}
		expected, ok := state.GeneratedContentHashes[ownedPath]
		if !ok || expected == "" {
			return false, "prior state has no content hash for an owned document"
		}
		if contentHash(data) != expected {
			return false, "an installed generated document was modified since it was generated"
		}
	}
	return true, ""
}

// freshComponentSet is the set of component identities the plan requires to be regenerated
// or created. Every current component outside this set reuses its installed bytes.
func freshComponentSet(plan sharedmodel.GenerationPlan) map[string]bool {
	fresh := make(map[string]bool)
	for _, affected := range plan.AffectedComponents {
		switch affected.Action {
		case sharedmodel.ComponentCreate, sharedmodel.ComponentRegenerate:
			fresh[componentIdentity(affected.Key, affected.RootComponent)] = true
		}
	}
	return fresh
}

// aggregateDocumentPaths returns the set of topic-document paths that carry per-component
// owned sections. Their unchanged sections are reused byte-for-byte; index.md and
// codebase_info.md are always rendered fresh from deterministic scanner metadata.
func aggregateDocumentPaths(docsDir string) map[string]struct{} {
	bases := []string{docComponents, docArchitecture, docInterfaces, docDataModels, docWorkflows, docDependencies, docReviewNotes}
	set := make(map[string]struct{}, len(bases))
	for _, base := range bases {
		set[path.Join(docsDir, base)] = struct{}{}
	}
	return set
}

// buildReuse turns verified installed content into the section and page maps the renderer
// uses to preserve unchanged components byte-for-byte. It is only called after integrity
// has been confirmed, so parsing owned sections cannot legitimately fail.
func buildReuse(docsDir string, content map[string][]byte) (map[sectionID]string, map[string]string, error) {
	aggregate := aggregateDocumentPaths(docsDir)
	reuseSections := make(map[sectionID]string)
	reusePages := make(map[string]string)
	for installedPath, data := range content {
		if _, ok := aggregate[installedPath]; ok {
			sections, err := parseOwnedSections(string(data))
			if err != nil {
				return nil, nil, fmt.Errorf("parse installed document %q: %w", installedPath, err)
			}
			for _, section := range sections {
				reuseSections[sectionID{Topic: section.Topic, Key: section.Key}] = section.Content
			}
			continue
		}
		reusePages[installedPath] = string(data)
	}
	return reuseSections, reusePages, nil
}

// classifyDiff compares candidate documents against the installed content hashes recorded
// in prior state. Deleted paths are supplied by the ownership decision.
func classifyDiff(rendered renderedOutput, priorHashes map[string]string, deletes []string) sharedmodel.OutputDiff {
	diff := sharedmodel.OutputDiff{Deleted: append([]string(nil), deletes...)}
	for _, doc := range rendered.docs {
		hash := contentHash([]byte(doc.content))
		previous, existed := priorHashes[doc.path]
		switch {
		case !existed:
			diff.Added = append(diff.Added, doc.path)
		case previous != hash:
			diff.Changed = append(diff.Changed, doc.path)
		default:
			diff.Unchanged = append(diff.Unchanged, doc.path)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Changed)
	sort.Strings(diff.Deleted)
	sort.Strings(diff.Unchanged)
	return diff
}
