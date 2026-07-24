package usecase

import (
	"crypto/sha256"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

// requiredTopLevelDocuments are the knowledge-base documents that must exist in every
// candidate output regardless of component count.
var requiredTopLevelDocuments = []string{
	docIndex, docCodebaseInfo, docComponents, docArchitecture, docInterfaces,
	docDataModels, docWorkflows, docDependencies, docReviewNotes,
}

func contentHash(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest)
}

// buildState constructs deterministic state from the scan, discovered components, the
// rendered documents, and the change context used to compute component input hashes. It
// stores integrity metadata only: no source bodies, prose, diagrams, or credentials.
func buildState(
	input documentationmodel.PlanInput,
	components []sharedmodel.Component,
	files []sharedmodel.SourceFile,
	changes []sharedmodel.Change,
	catalog []string,
	configHashes sharedmodel.StateConfigHashes,
	configHash string,
	boundedSupporting, boundedManifests map[string][]sharedmodel.SourceFile,
	rendered renderedOutput,
) sharedmodel.State {
	stateFiles := make(map[string]sharedmodel.StateFile, len(files))
	for _, file := range files {
		stateFiles[file.Path] = sharedmodel.StateFile{
			SourceHash:           file.SourceHash,
			Role:                 file.Role,
			TriggersRegeneration: file.TriggersRegeneration,
			ComponentKey:         file.ComponentKey,
			RootComponent:        file.RootComponent,
		}
	}

	stateComponents := make(map[string]sharedmodel.StateComponent, len(components))
	for _, component := range components {
		identity := componentIdentity(component.Key, component.RootComponent)
		relevant := changesForComponent(changes, component.Key, component.RootComponent)
		hash := componentInputHash(component, boundedSupporting[identity], boundedManifests[identity], catalog, relevant, configHash)
		stateComponents[component.Key] = sharedmodel.StateComponent{
			InputHash:     hash,
			Document:      component.Document,
			RootComponent: component.RootComponent,
		}
	}

	generatedPaths := make([]string, 0, len(rendered.docs))
	generatedHashes := make(map[string]string, len(rendered.docs))
	for _, doc := range rendered.docs {
		generatedPaths = append(generatedPaths, doc.path)
		generatedHashes[doc.path] = contentHash([]byte(doc.content))
	}
	sort.Strings(generatedPaths)

	return sharedmodel.State{
		SchemaVersion:          stateSchemaVersion,
		GeneratorVersion:       generatorVersion,
		PlannerVersion:         plannerVersion,
		PromptVersion:          promptVersion,
		RenderVersion:          renderVersion,
		OutputSchemaVersion:    outputSchemaVersion,
		ConfigHash:             configHash,
		ConfigHashes:           configHashes,
		GeneratedPaths:         generatedPaths,
		GeneratedContentHashes: generatedHashes,
		Files:                  stateFiles,
		Components:             stateComponents,
	}
}

var intraDocLinkPattern = regexp.MustCompile(`\]\(([^)]+)\)`)

// validateCandidateOutput performs the complete pre-install validation of the rendered
// candidate: required files, paths under the generated root, ownership markers, resolving
// local links, a generated-output secret scan, and content-hash agreement with state.
func validateCandidateOutput(docsDir string, rendered renderedOutput, state sharedmodel.State) error {
	present := make(map[string]renderedDoc, len(rendered.docs))
	for _, doc := range rendered.docs {
		if _, duplicate := present[doc.path]; duplicate {
			return outputValidationError{fmt.Sprintf("duplicate generated path %q", doc.path)}
		}
		present[doc.path] = doc
	}

	for _, base := range requiredTopLevelDocuments {
		full := path.Join(docsDir, base)
		if _, ok := present[full]; !ok {
			return outputValidationError{fmt.Sprintf("required document %q is missing", full)}
		}
	}

	for _, doc := range rendered.docs {
		if !pathWithin(doc.path, docsDir) {
			return outputValidationError{fmt.Sprintf("generated path %q is not below the generated root %q", doc.path, docsDir)}
		}
		if err := validateDocumentSections(doc.content, doc.sections); err != nil {
			return outputValidationError{fmt.Sprintf("%s: %v", doc.path, err)}
		}
		if containsHighConfidenceSecret([]byte(doc.content)) {
			return outputValidationError{fmt.Sprintf("generated document %q contains high-confidence secret material", doc.path)}
		}
		if err := validateLocalLinks(doc, present); err != nil {
			return outputValidationError{fmt.Sprintf("%s: %v", doc.path, err)}
		}
		expected, ok := state.GeneratedContentHashes[doc.path]
		if !ok {
			return outputValidationError{fmt.Sprintf("state is missing a content hash for %q", doc.path)}
		}
		if actual := contentHash([]byte(doc.content)); actual != expected {
			return outputValidationError{fmt.Sprintf("content hash mismatch for %q", doc.path)}
		}
	}

	if len(state.GeneratedPaths) != len(rendered.docs) {
		return outputValidationError{fmt.Sprintf("state lists %d generated paths but %d were rendered", len(state.GeneratedPaths), len(rendered.docs))}
	}
	for _, generatedPath := range state.GeneratedPaths {
		if _, ok := present[generatedPath]; !ok {
			return outputValidationError{fmt.Sprintf("state lists generated path %q that was not rendered", generatedPath)}
		}
	}
	return nil
}

// validateLocalLinks verifies that every intra-generated Markdown link resolves to a
// document in the candidate set. Links that climb out of the generated tree (source
// evidence links) are validated separately by evidence allow-listing during dossier
// validation and are skipped here.
func validateLocalLinks(doc renderedDoc, present map[string]renderedDoc) error {
	directory := path.Dir(doc.path)
	for _, match := range intraDocLinkPattern.FindAllStringSubmatch(doc.content, -1) {
		target := match[1]
		if strings.HasPrefix(target, "../") || strings.Contains(target, "://") || strings.HasPrefix(target, "/") {
			continue
		}
		resolved := path.Join(directory, target)
		if _, ok := present[resolved]; !ok {
			return fmt.Errorf("local link %q does not resolve to a generated document", target)
		}
	}
	return nil
}

// ownershipDecision is the result of the conservative ownership-recovery check.
type ownershipDecision struct {
	deletes []string
}

// resolveOwnership enforces the conservative recovery rules before any write. It fails
// when generated output exists that prior state does not own, so a handwritten or
// externally created file can never be overwritten or deleted.
//
//   - Missing/incompatible prior state with existing generated files fails, unless the run
//     is an explicit full recovery (--full). Even then, an existing file the candidate does
//     not produce is never touched.
//   - With valid prior state, every currently present generated path and every candidate
//     write target must be owned by prior state, unless it is a brand-new expected path.
//   - Deletions are the prior-owned generated paths that the candidate no longer produces.
func resolveOwnership(existing sharedmodel.ExistingOutput, priorOwned map[string]struct{}, hasProvenState bool, candidatePaths map[string]struct{}, fullRecovery bool) (ownershipDecision, error) {
	if !hasProvenState && !fullRecovery && len(existing.GeneratedPaths) > 0 {
		return ownershipDecision{}, outputValidationError{"generated files exist but no valid prior state proves ownership; restore valid state or rerun with --full to rebuild"}
	}

	for _, existingPath := range existing.GeneratedPaths {
		_, owned := priorOwned[existingPath]
		_, willWrite := candidatePaths[existingPath]
		if owned {
			continue
		}
		// A full recovery may overwrite files at deterministic tool paths that the candidate
		// reproduces, but must still never delete a file it cannot prove it owns.
		if willWrite && fullRecovery {
			continue
		}
		if !willWrite {
			return ownershipDecision{}, outputValidationError{fmt.Sprintf("generated root contains unowned file %q; refusing to proceed", existingPath)}
		}
		return ownershipDecision{}, outputValidationError{fmt.Sprintf("candidate would overwrite unowned file %q; refusing to proceed", existingPath)}
	}

	deletes := make([]string, 0)
	for ownedPath := range priorOwned {
		if _, stillProduced := candidatePaths[ownedPath]; !stillProduced {
			deletes = append(deletes, ownedPath)
		}
	}
	sort.Strings(deletes)
	return ownershipDecision{deletes: deletes}, nil
}

// outputValidationError is a safe ownership/output validation failure. It exposes only
// repository-relative paths and stable wording, never source or model prose.
type outputValidationError struct {
	message string
}

func (e outputValidationError) Error() string { return e.message }

func (e outputValidationError) ExitCode() int { return 6 }
