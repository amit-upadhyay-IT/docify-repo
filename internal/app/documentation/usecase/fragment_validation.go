package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

const (
	issueMissingField     = "missing_field"
	issueResponseTooLarge = "response_too_large"
	fragmentResponseBytes = 8 << 10
)

// fragmentValidation is the trusted result of one section-specific response. Value
// is one of the fragment model types and is eligible for assembly only when valid.
type fragmentValidation struct {
	kind           sharedmodel.FragmentKind
	value          any
	issues         []sharedmodel.ValidationIssue
	evidenceUsed   []string
	complete       bool
	saturated      bool
	repairable     bool
	itemCount      int
	sealedBody     string
	sealedEvidence string
	sealedCatalog  string
	boundScope     fragmentScopeKey
	scopeBound     bool
}

type overviewValidation struct {
	value             sharedmodel.ComponentOverview
	issues            []sharedmodel.ValidationIssue
	sealedBody        string
	componentIdentity string
}

func (v overviewValidation) valid() bool { return len(v.issues) == 0 }

func validateOverviewReduction(component sharedmodel.Component, body []byte) overviewValidation {
	result := overviewValidation{componentIdentity: componentIdentity(component.Key, component.RootComponent)}
	if len(body) > fragmentResponseBytes {
		result.issues = []sharedmodel.ValidationIssue{{Code: issueResponseTooLarge, Message: "overview response exceeds the bounded response profile"}}
		return result
	}
	if issue := decodeStrict(body, &result.value); issue != nil {
		result.issues = []sharedmodel.ValidationIssue{*issue}
		return result
	}
	root, ok := rawObject(body)
	if !ok {
		result.issues = []sharedmodel.ValidationIssue{{Code: issueInvalidType, Message: "overview must be a JSON object"}}
		return result
	}
	requireFields(&result.issues, root, "", "title", "purpose")
	validator := newFragmentValidator(nil, nil)
	validator.prose("/title", result.value.Title, fragmentMaxTitle, true)
	validator.prose("/purpose", result.value.Purpose, fragmentMaxLongText, true)
	result.issues = append(result.issues, validator.issues...)
	if result.valid() {
		result.sealedBody = string(append([]byte(nil), body...))
	}
	return result
}

func (v overviewValidation) revalidateSealed(component sharedmodel.Component) (overviewValidation, error) {
	if v.sealedBody == "" || v.componentIdentity != componentIdentity(component.Key, component.RootComponent) {
		return overviewValidation{}, fragmentAssemblyError{code: "overview_validation_unsealed"}
	}
	trusted := validateOverviewReduction(component, []byte(v.sealedBody))
	if !trusted.valid() {
		return overviewValidation{}, fragmentAssemblyError{code: "overview_validation_seal_mismatch"}
	}
	return trusted, nil
}

func (v fragmentValidation) valid() bool { return len(v.issues) == 0 }

// validateFragment strictly decodes one bounded fragment and applies the same local
// evidence, enum, component-reference, prose, and diagram rules as a complete dossier.
func validateFragment(kind sharedmodel.FragmentKind, body []byte, allowedEvidence, catalog []string) (result fragmentValidation) {
	if len(body) > fragmentResponseBytes {
		return fragmentValidation{
			kind: kind,
			issues: []sharedmodel.ValidationIssue{{
				Code: issueResponseTooLarge, Message: "fragment response exceeds the bounded response profile",
			}},
		}
	}

	switch kind {
	case sharedmodel.FragmentOverviewCandidate:
		result = validateOverviewCandidate(body, allowedEvidence, catalog)
	case sharedmodel.FragmentArchitecture:
		var fragment sharedmodel.ArchitectureFragment
		result = validateListFragment(kind, body, allowedEvidence, catalog, &fragment, func(d *dossierValidator) {
			d.architectureItems("/items", fragment.Items, fragmentMaxArchitectureItems)
		})
	case sharedmodel.FragmentInterfaces:
		var fragment sharedmodel.InterfacesFragment
		result = validateListFragment(kind, body, allowedEvidence, catalog, &fragment, func(d *dossierValidator) {
			d.interfaceItems("/items", fragment.Items, fragmentMaxInterfaceItems)
		})
	case sharedmodel.FragmentDataModels:
		var fragment sharedmodel.DataModelsFragment
		result = validateListFragment(kind, body, allowedEvidence, catalog, &fragment, func(d *dossierValidator) {
			d.dataModelItems("/items", fragment.Items, fragmentMaxDataModelItems)
		})
	case sharedmodel.FragmentWorkflows:
		var fragment sharedmodel.WorkflowsFragment
		result = validateListFragment(kind, body, allowedEvidence, catalog, &fragment, func(d *dossierValidator) {
			d.workflowItems("/items", fragment.Items, fragmentMaxWorkflowItems)
		})
	case sharedmodel.FragmentDependencies:
		var fragment sharedmodel.DependenciesFragment
		result = validateListFragment(kind, body, allowedEvidence, catalog, &fragment, func(d *dossierValidator) {
			d.dependencyItems("/items", fragment.Items, fragmentMaxDependencyItems)
		})
	case sharedmodel.FragmentReviewGaps:
		var fragment sharedmodel.ReviewGapsFragment
		result = validateListFragment(kind, body, allowedEvidence, catalog, &fragment, func(d *dossierValidator) {
			d.reviewGapItems("/items", fragment.Items, fragmentMaxReviewGapItems)
		})
	case sharedmodel.FragmentDiagrams:
		var fragment sharedmodel.DiagramsFragment
		result = validateListFragment(kind, body, allowedEvidence, catalog, &fragment, func(d *dossierValidator) {
			d.diagramItems("/items", fragment.Items, fragmentMaxDiagramItems)
		})
	default:
		return fragmentValidation{kind: kind, issues: []sharedmodel.ValidationIssue{{Code: issueInvalidValue, Message: "fragment kind is not supported"}}}
	}
	if result.valid() {
		result.sealedBody = string(append([]byte(nil), body...))
		result.sealedEvidence = encodeSealedStrings(allowedEvidence)
		result.sealedCatalog = encodeSealedStrings(catalog)
	}
	return result
}

// validateScopedFragment binds a validated required map result to the exact request
// scope that produced it. Coverage cannot be satisfied by swapping responses between
// batches or chunks.
func validateScopedFragment(scope fragmentScope, body []byte, allowedEvidence, catalog []string) fragmentValidation {
	if encodeSealedStrings(scope.AllowedEvidence) != encodeSealedStrings(allowedEvidence) {
		return fragmentValidation{kind: scope.Kind, issues: []sharedmodel.ValidationIssue{{Code: issueInvalidValue, Message: "fragment evidence scope does not match the planned request"}}}
	}
	result := validateFragment(scope.Kind, body, allowedEvidence, catalog)
	if result.valid() {
		result.boundScope = scope.key()
		result.scopeBound = true
	}
	return result
}

func (v fragmentValidation) revalidateSealed() (fragmentValidation, error) {
	if v.sealedBody == "" || v.sealedEvidence == "" || v.sealedCatalog == "" {
		return fragmentValidation{}, fragmentAssemblyError{code: "fragment_validation_unsealed"}
	}
	allowedEvidence, err := decodeSealedStrings(v.sealedEvidence)
	if err != nil {
		return fragmentValidation{}, fragmentAssemblyError{code: "fragment_evidence_seal_invalid"}
	}
	catalog, err := decodeSealedStrings(v.sealedCatalog)
	if err != nil {
		return fragmentValidation{}, fragmentAssemblyError{code: "fragment_catalog_seal_invalid"}
	}
	trusted := validateFragment(v.kind, []byte(v.sealedBody), allowedEvidence, catalog)
	if !trusted.valid() {
		return fragmentValidation{}, fragmentAssemblyError{code: "fragment_validation_seal_mismatch"}
	}
	trusted.boundScope, trusted.scopeBound = v.boundScope, v.scopeBound
	return trusted, nil
}

func encodeSealedStrings(values []string) string {
	encoded, _ := json.Marshal(sortedStrings(values))
	return string(encoded)
}

func decodeSealedStrings(encoded string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil || values == nil {
		return nil, fmt.Errorf("decode sealed string list")
	}
	return values, nil
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write > 0 && result[write-1] == value {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func validateOverviewCandidate(body []byte, allowedEvidence, catalog []string) fragmentValidation {
	var candidate sharedmodel.OverviewCandidate
	if issue := decodeStrict(body, &candidate); issue != nil {
		return fragmentValidation{kind: sharedmodel.FragmentOverviewCandidate, issues: []sharedmodel.ValidationIssue{*issue}, repairable: true}
	}
	if issues := fragmentShapeIssues(sharedmodel.FragmentOverviewCandidate, body); len(issues) > 0 {
		return fragmentValidation{kind: sharedmodel.FragmentOverviewCandidate, value: candidate, issues: issues, repairable: true}
	}
	validator := newFragmentValidator(allowedEvidence, catalog)
	validator.prose("/title", candidate.Title, fragmentMaxTitle, true)
	validator.prose("/purpose", candidate.Purpose, fragmentMaxLongText, true)
	validator.evidencePaths("/source_paths", candidate.SourcePaths, fragmentMaxSourcePaths, true)
	return fragmentResult(sharedmodel.FragmentOverviewCandidate, candidate, validator, true, false, 1)
}

type listFragmentWire interface {
	sharedmodel.ArchitectureFragment | sharedmodel.InterfacesFragment | sharedmodel.DataModelsFragment |
		sharedmodel.WorkflowsFragment | sharedmodel.DependenciesFragment | sharedmodel.ReviewGapsFragment |
		sharedmodel.DiagramsFragment
}

func validateListFragment[T listFragmentWire](
	kind sharedmodel.FragmentKind,
	body []byte,
	allowedEvidence, catalog []string,
	fragment *T,
	validateItems func(*dossierValidator),
) fragmentValidation {
	if issue := decodeStrict(body, fragment); issue != nil {
		return fragmentValidation{kind: kind, issues: []sharedmodel.ValidationIssue{*issue}, repairable: true}
	}
	if issues := fragmentShapeIssues(kind, body); len(issues) > 0 {
		return fragmentValidation{kind: kind, value: *fragment, issues: issues, repairable: true}
	}

	complete, omittedCount, itemCount := listFragmentMetadata(any(*fragment))
	validator := newFragmentValidator(allowedEvidence, catalog)
	if omittedCount != nil && (*omittedCount < 0 || *omittedCount > fragmentMaxOmittedCount) {
		validator.add(issueInvalidValue, "/omitted_count", "omitted_count is outside the bounded range")
	}
	if complete && omittedCount != nil && *omittedCount > 0 {
		validator.add(issueInvalidValue, "/omitted_count", "omitted_count must be zero when complete is true")
	}
	validateItems(validator)
	limit := fragmentItemLimit(string(kind))
	return fragmentResult(kind, *fragment, validator, complete, !complete || itemCount == limit, itemCount)
}

func listFragmentMetadata(fragment any) (bool, *int, int) {
	switch value := fragment.(type) {
	case sharedmodel.ArchitectureFragment:
		return value.Complete, value.OmittedCount, len(value.Items)
	case sharedmodel.InterfacesFragment:
		return value.Complete, value.OmittedCount, len(value.Items)
	case sharedmodel.DataModelsFragment:
		return value.Complete, value.OmittedCount, len(value.Items)
	case sharedmodel.WorkflowsFragment:
		return value.Complete, value.OmittedCount, len(value.Items)
	case sharedmodel.DependenciesFragment:
		return value.Complete, value.OmittedCount, len(value.Items)
	case sharedmodel.ReviewGapsFragment:
		return value.Complete, value.OmittedCount, len(value.Items)
	case sharedmodel.DiagramsFragment:
		return value.Complete, value.OmittedCount, len(value.Items)
	default:
		return false, nil, 0
	}
}

func newFragmentValidator(allowedEvidence, catalog []string) *dossierValidator {
	return &dossierValidator{
		evidence: toSet(allowedEvidence),
		catalog:  toSet(catalog),
		used:     make(map[string]struct{}),
		limits:   fragmentLimits,
	}
}

func fragmentResult(kind sharedmodel.FragmentKind, value any, validator *dossierValidator, complete, saturated bool, itemCount int) fragmentValidation {
	used := make([]string, 0, len(validator.used))
	for path := range validator.used {
		used = append(used, path)
	}
	sort.Strings(used)
	return fragmentValidation{
		kind: kind, value: value, issues: validator.issues, evidenceUsed: used,
		complete: complete, saturated: saturated, repairable: true, itemCount: itemCount,
	}
}

func fragmentShapeIssues(kind sharedmodel.FragmentKind, body []byte) []sharedmodel.ValidationIssue {
	root, ok := rawObject(body)
	if !ok {
		return []sharedmodel.ValidationIssue{{Code: issueInvalidType, Message: "fragment must be a JSON object"}}
	}
	issues := make([]sharedmodel.ValidationIssue, 0)
	if kind == sharedmodel.FragmentOverviewCandidate {
		requireFields(&issues, root, "", "title", "purpose", "source_paths")
		requireArray(&issues, root, "/source_paths", "source_paths")
		return issues
	}

	requireFields(&issues, root, "", "complete", "items")
	requireNonNull(&issues, root, "/complete", "complete")
	if _, exists := root["omitted_count"]; exists {
		requireNonNull(&issues, root, "/omitted_count", "omitted_count")
	}
	items := requireArray(&issues, root, "/items", "items")
	for index, raw := range items {
		base := fmt.Sprintf("/items/%d", index)
		item, object := requireRawObject(&issues, raw, base)
		if !object {
			continue
		}
		switch kind {
		case sharedmodel.FragmentArchitecture:
			requireFields(&issues, item, base, "title", "description", "source_paths")
			requireArray(&issues, item, base+"/source_paths", "source_paths")
		case sharedmodel.FragmentInterfaces:
			requireFields(&issues, item, base, "name", "kind", "direction", "description", "source_paths")
			requireArray(&issues, item, base+"/source_paths", "source_paths")
		case sharedmodel.FragmentDataModels:
			requireFields(&issues, item, base, "name", "kind", "description", "fields", "relationships", "source_paths")
			fields := requireArray(&issues, item, base+"/fields", "fields")
			for nestedIndex, nested := range fields {
				nestedBase := fmt.Sprintf("%s/fields/%d", base, nestedIndex)
				field, valid := requireRawObject(&issues, nested, nestedBase)
				if valid {
					requireFields(&issues, field, nestedBase, "name", "type", "description")
				}
			}
			relationships := requireArray(&issues, item, base+"/relationships", "relationships")
			for nestedIndex, nested := range relationships {
				nestedBase := fmt.Sprintf("%s/relationships/%d", base, nestedIndex)
				relation, valid := requireRawObject(&issues, nested, nestedBase)
				if valid {
					requireFields(&issues, relation, nestedBase, "target", "kind", "description")
				}
			}
			requireArray(&issues, item, base+"/source_paths", "source_paths")
		case sharedmodel.FragmentWorkflows:
			requireFields(&issues, item, base, "name", "description", "steps", "source_paths")
			steps := requireArray(&issues, item, base+"/steps", "steps")
			for nestedIndex, nested := range steps {
				nestedBase := fmt.Sprintf("%s/steps/%d", base, nestedIndex)
				step, valid := requireRawObject(&issues, nested, nestedBase)
				if valid {
					requireFields(&issues, step, nestedBase, "actor", "action", "target")
				}
			}
			requireArray(&issues, item, base+"/source_paths", "source_paths")
		case sharedmodel.FragmentDependencies:
			requireFields(&issues, item, base, "name", "kind", "purpose", "component_key", "source_paths")
			requireArray(&issues, item, base+"/source_paths", "source_paths")
		case sharedmodel.FragmentReviewGaps:
			requireFields(&issues, item, base, "kind", "description", "recommendation", "source_paths")
			requireArray(&issues, item, base+"/source_paths", "source_paths")
		case sharedmodel.FragmentDiagrams:
			validateDiagramShape(&issues, item, base)
		}
	}
	return issues
}

func dossierShapeIssues(body []byte) []sharedmodel.ValidationIssue {
	root, ok := rawObject(body)
	if !ok {
		return []sharedmodel.ValidationIssue{{Code: issueInvalidType, Message: "dossier must be a JSON object"}}
	}
	issues := make([]sharedmodel.ValidationIssue, 0)
	requireFields(&issues, root, "", "title", "purpose", "source_paths", "architecture", "interfaces", "data_models", "workflows", "dependencies", "review_gaps", "diagrams")
	requireArray(&issues, root, "/source_paths", "source_paths")
	sections := []struct {
		field string
		kind  sharedmodel.FragmentKind
	}{
		{"architecture", sharedmodel.FragmentArchitecture},
		{"interfaces", sharedmodel.FragmentInterfaces},
		{"data_models", sharedmodel.FragmentDataModels},
		{"workflows", sharedmodel.FragmentWorkflows},
		{"dependencies", sharedmodel.FragmentDependencies},
		{"review_gaps", sharedmodel.FragmentReviewGaps},
		{"diagrams", sharedmodel.FragmentDiagrams},
	}
	for _, section := range sections {
		raw, exists := root[section.field]
		items := requireArray(&issues, root, "/"+section.field, section.field)
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		envelope, _ := json.Marshal(map[string]any{"complete": true, "items": items})
		for _, issue := range fragmentShapeIssues(section.kind, envelope) {
			if len(issues) >= maxValidationIssues {
				break
			}
			issue.Path = "/" + section.field + strings.TrimPrefix(issue.Path, "/items")
			issues = append(issues, issue)
		}
	}
	return issues
}

func validateDiagramShape(issues *[]sharedmodel.ValidationIssue, item map[string]json.RawMessage, base string) {
	requireFields(issues, item, base, "type", "title", "source_paths")
	requireArray(issues, item, base+"/source_paths", "source_paths")
	var kind sharedmodel.DiagramType
	_ = json.Unmarshal(item["type"], &kind)
	switch kind {
	case sharedmodel.DiagramFlowchart:
		requireFields(issues, item, base, "nodes", "edges")
		forbidRawFields(issues, item, base, "participants", "messages", "classes", "relationships")
		validateKeyLabelArray(issues, requireArray(issues, item, base+"/nodes", "nodes"), base+"/nodes", "key", "label")
		validateRequiredObjectArray(issues, requireArray(issues, item, base+"/edges", "edges"), base+"/edges", "from", "to", "label")
	case sharedmodel.DiagramSequence:
		requireFields(issues, item, base, "participants", "messages")
		forbidRawFields(issues, item, base, "nodes", "edges", "classes", "relationships")
		validateKeyLabelArray(issues, requireArray(issues, item, base+"/participants", "participants"), base+"/participants", "key", "label")
		messages := requireArray(issues, item, base+"/messages", "messages")
		validateRequiredObjectArray(issues, messages, base+"/messages", "from", "to", "label", "response")
		requireObjectArrayFieldsNonNull(issues, messages, base+"/messages", "response")
	case sharedmodel.DiagramClass:
		requireFields(issues, item, base, "classes", "relationships")
		forbidRawFields(issues, item, base, "nodes", "edges", "participants", "messages")
		classes := requireArray(issues, item, base+"/classes", "classes")
		for index, raw := range classes {
			classBase := fmt.Sprintf("%s/classes/%d", base, index)
			class, valid := requireRawObject(issues, raw, classBase)
			if valid {
				requireFields(issues, class, classBase, "key", "label", "members")
				requireArray(issues, class, classBase+"/members", "members")
			}
		}
		validateRequiredObjectArray(issues, requireArray(issues, item, base+"/relationships", "relationships"), base+"/relationships", "from", "to", "kind", "label")
	}
}

func rawObject(raw []byte) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func requireRawObject(issues *[]sharedmodel.ValidationIssue, raw json.RawMessage, path string) (map[string]json.RawMessage, bool) {
	object, ok := rawObject(raw)
	if !ok {
		addShapeIssue(issues, issueInvalidType, path, "value must be a JSON object")
	}
	return object, ok
}

func requireFields(issues *[]sharedmodel.ValidationIssue, object map[string]json.RawMessage, base string, fields ...string) {
	for _, field := range fields {
		if _, exists := object[field]; !exists {
			addShapeIssue(issues, issueMissingField, base+"/"+field, "required field is missing")
		}
	}
}

func requireNonNull(issues *[]sharedmodel.ValidationIssue, object map[string]json.RawMessage, path, field string) {
	raw, exists := object[field]
	if exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		addShapeIssue(issues, issueInvalidType, path, "required field must not be null")
	}
}

func requireArray(issues *[]sharedmodel.ValidationIssue, object map[string]json.RawMessage, path, field string) []json.RawMessage {
	raw, exists := object[field]
	if !exists {
		return nil
	}
	var items []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &items) != nil {
		addShapeIssue(issues, issueInvalidType, path, "required field must be a JSON array")
		return nil
	}
	return items
}

func forbidRawFields(issues *[]sharedmodel.ValidationIssue, object map[string]json.RawMessage, base string, fields ...string) {
	for _, field := range fields {
		if _, exists := object[field]; exists {
			addShapeIssue(issues, issueDiagramFieldUnset, base+"/"+field, "field is not allowed for this diagram type")
		}
	}
}

func validateKeyLabelArray(issues *[]sharedmodel.ValidationIssue, items []json.RawMessage, base string, fields ...string) {
	validateRequiredObjectArray(issues, items, base, fields...)
}

func validateRequiredObjectArray(issues *[]sharedmodel.ValidationIssue, items []json.RawMessage, base string, fields ...string) {
	for index, raw := range items {
		itemBase := fmt.Sprintf("%s/%d", base, index)
		item, valid := requireRawObject(issues, raw, itemBase)
		if valid {
			requireFields(issues, item, itemBase, fields...)
		}
	}
}

func requireObjectArrayFieldsNonNull(issues *[]sharedmodel.ValidationIssue, items []json.RawMessage, base string, fields ...string) {
	for index, raw := range items {
		item, valid := rawObject(raw)
		if !valid {
			continue
		}
		for _, field := range fields {
			requireNonNull(issues, item, fmt.Sprintf("%s/%d/%s", base, index, field), field)
		}
	}
}

func addShapeIssue(issues *[]sharedmodel.ValidationIssue, code, path, message string) {
	if len(*issues) >= maxValidationIssues {
		return
	}
	*issues = append(*issues, sharedmodel.ValidationIssue{Code: code, Path: path, Message: message})
}
