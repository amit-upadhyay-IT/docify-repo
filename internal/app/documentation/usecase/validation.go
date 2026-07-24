package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	sharedmodel "docify-repo/internal/model"
)

// Stable validation issue codes. They are safe to send to the model in a repair
// request and never carry source content.
const (
	issueInvalidJSON       = "invalid_json"
	issueUnknownField      = "unknown_field"
	issueInvalidType       = "invalid_type"
	issueTrailingData      = "trailing_data"
	issueEmptyValue        = "empty_value"
	issueStringTooLong     = "string_too_long"
	issueTooManyItems      = "too_many_items"
	issueInvalidEnum       = "invalid_enum"
	issueUnsafeProse       = "unsafe_prose"
	issueUnknownEvidence   = "unknown_evidence_path"
	issueUnknownComponent  = "unknown_component_key"
	issueMissingComponent  = "missing_component_key"
	issueInvalidDiagram    = "invalid_diagram_type"
	issueDiagramFieldUnset = "diagram_field_not_allowed"
	issueDiagramReference  = "invalid_diagram_reference"
	issueDuplicateKey      = "duplicate_key"
)

// maxValidationIssues bounds the issues embedded in a repair request so it stays
// within the response limit.
const maxValidationIssues = 50

// dossierValidation is the outcome of validating one raw response body.
type dossierValidation struct {
	dossier      sharedmodel.ComponentDossier
	issues       []sharedmodel.ValidationIssue
	evidenceUsed []string
}

// valid reports whether the response passed with no issues.
func (v dossierValidation) valid() bool { return len(v.issues) == 0 }

// validateDossier strictly decodes and validates one response body. allowedEvidence is
// the exact set of paths the response may cite; catalog is the set of component keys it
// may reference. The adapter has already rejected empty, non-UTF-8, truncated, and
// over-limit bodies, so every issue here is repair-eligible.
func validateDossier(body []byte, allowedEvidence, catalog []string) dossierValidation {
	validator := &dossierValidator{
		evidence: toSet(allowedEvidence),
		catalog:  toSet(catalog),
		used:     make(map[string]struct{}),
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var dossier sharedmodel.ComponentDossier
	if err := decoder.Decode(&dossier); err != nil {
		return dossierValidation{issues: []sharedmodel.ValidationIssue{decodeIssue(err)}}
	}
	if decoder.More() {
		return dossierValidation{issues: []sharedmodel.ValidationIssue{{Code: issueTrailingData, Path: "", Message: "response contains data after the JSON object"}}}
	}

	validator.validate(dossier)

	used := make([]string, 0, len(validator.used))
	for path := range validator.used {
		used = append(used, path)
	}
	sort.Strings(used)
	return dossierValidation{dossier: dossier, issues: validator.issues, evidenceUsed: used}
}

type dossierValidator struct {
	evidence map[string]struct{}
	catalog  map[string]struct{}
	used     map[string]struct{}
	issues   []sharedmodel.ValidationIssue
	stopped  bool
}

func (d *dossierValidator) add(code, path, message string) {
	if d.stopped {
		return
	}
	if len(d.issues) >= maxValidationIssues {
		d.stopped = true
		return
	}
	d.issues = append(d.issues, sharedmodel.ValidationIssue{Code: code, Path: path, Message: message})
}

func (d *dossierValidator) validate(dossier sharedmodel.ComponentDossier) {
	d.prose("/title", dossier.Title, schemaMaxTitle, true)
	d.prose("/purpose", dossier.Purpose, schemaMaxLongText, true)
	d.evidencePaths("/source_paths", dossier.SourcePaths, schemaMaxSourcePaths, false)

	d.maxItems("/architecture", len(dossier.Architecture), schemaMaxItemsPerSection)
	for index, item := range dossier.Architecture {
		base := fmt.Sprintf("/architecture/%d", index)
		d.prose(base+"/title", item.Title, schemaMaxTitle, true)
		d.prose(base+"/description", item.Description, schemaMaxLongText, true)
		d.evidencePaths(base+"/source_paths", item.SourcePaths, schemaMaxSourcePaths, true)
	}

	d.maxItems("/interfaces", len(dossier.Interfaces), schemaMaxItemsPerSection)
	for index, item := range dossier.Interfaces {
		base := fmt.Sprintf("/interfaces/%d", index)
		d.prose(base+"/name", item.Name, schemaMaxName, true)
		d.enum(base+"/kind", item.Kind, interfaceKinds)
		d.enum(base+"/direction", item.Direction, interfaceDirections)
		d.prose(base+"/description", item.Description, schemaMaxLongText, true)
		d.evidencePaths(base+"/source_paths", item.SourcePaths, schemaMaxSourcePaths, true)
	}

	d.maxItems("/data_models", len(dossier.DataModels), schemaMaxItemsPerSection)
	for index, item := range dossier.DataModels {
		base := fmt.Sprintf("/data_models/%d", index)
		d.prose(base+"/name", item.Name, schemaMaxName, true)
		d.enum(base+"/kind", item.Kind, dataModelKinds)
		d.prose(base+"/description", item.Description, schemaMaxLongText, true)
		d.maxItems(base+"/fields", len(item.Fields), schemaMaxFields)
		for fieldIndex, field := range item.Fields {
			fieldBase := fmt.Sprintf("%s/fields/%d", base, fieldIndex)
			d.prose(fieldBase+"/name", field.Name, schemaMaxName, true)
			d.prose(fieldBase+"/type", field.Type, schemaMaxType, true)
			d.prose(fieldBase+"/description", field.Description, schemaMaxShortText, true)
		}
		d.maxItems(base+"/relationships", len(item.Relationships), schemaMaxRelationships)
		for relationIndex, relation := range item.Relationships {
			relationBase := fmt.Sprintf("%s/relationships/%d", base, relationIndex)
			d.prose(relationBase+"/target", relation.Target, schemaMaxName, true)
			d.enum(relationBase+"/kind", relation.Kind, dataRelationshipKinds)
			d.prose(relationBase+"/description", relation.Description, schemaMaxShortText, true)
		}
		d.evidencePaths(base+"/source_paths", item.SourcePaths, schemaMaxSourcePaths, true)
	}

	d.maxItems("/workflows", len(dossier.Workflows), schemaMaxItemsPerSection)
	for index, item := range dossier.Workflows {
		base := fmt.Sprintf("/workflows/%d", index)
		d.prose(base+"/name", item.Name, schemaMaxName, true)
		d.prose(base+"/description", item.Description, schemaMaxLongText, true)
		d.maxItems(base+"/steps", len(item.Steps), schemaMaxSteps)
		for stepIndex, step := range item.Steps {
			stepBase := fmt.Sprintf("%s/steps/%d", base, stepIndex)
			d.prose(stepBase+"/actor", step.Actor, schemaMaxName, true)
			d.prose(stepBase+"/action", step.Action, schemaMaxShortText, true)
			d.prose(stepBase+"/target", step.Target, schemaMaxName, false)
		}
		d.evidencePaths(base+"/source_paths", item.SourcePaths, schemaMaxSourcePaths, true)
	}

	d.maxItems("/dependencies", len(dossier.Dependencies), schemaMaxItemsPerSection)
	for index, item := range dossier.Dependencies {
		base := fmt.Sprintf("/dependencies/%d", index)
		d.prose(base+"/name", item.Name, schemaMaxName, true)
		d.enum(base+"/kind", item.Kind, dependencyKinds)
		d.prose(base+"/purpose", item.Purpose, schemaMaxShortText, true)
		d.dependencyComponent(base+"/component_key", item.Kind, item.ComponentKey)
		d.evidencePaths(base+"/source_paths", item.SourcePaths, schemaMaxSourcePaths, true)
	}

	d.maxItems("/review_gaps", len(dossier.ReviewGaps), schemaMaxItemsPerSection)
	for index, item := range dossier.ReviewGaps {
		base := fmt.Sprintf("/review_gaps/%d", index)
		d.enum(base+"/kind", item.Kind, reviewGapKinds)
		d.prose(base+"/description", item.Description, schemaMaxLongText, true)
		d.prose(base+"/recommendation", item.Recommendation, schemaMaxShortText, true)
		// A review gap may cite no evidence when it reports missing or excluded input.
		d.evidencePaths(base+"/source_paths", item.SourcePaths, schemaMaxSourcePaths, false)
	}

	d.maxItems("/diagrams", len(dossier.Diagrams), schemaMaxDiagrams)
	for index, diagram := range dossier.Diagrams {
		d.diagram(fmt.Sprintf("/diagrams/%d", index), diagram)
	}
}

func (d *dossierValidator) prose(path, value string, maxBytes int, required bool) {
	if strings.TrimSpace(value) == "" {
		if required {
			d.add(issueEmptyValue, path, "value is required and must not be empty")
		}
		return
	}
	if len(value) > maxBytes {
		d.add(issueStringTooLong, path, fmt.Sprintf("value exceeds the %d-byte limit", maxBytes))
	}
	if reason := unsafeProseReason(value); reason != "" {
		d.add(issueUnsafeProse, path, "prose must be plain text: "+reason+" is not allowed")
	}
}

func (d *dossierValidator) enum(path, value string, set enumSet) {
	if value == "" {
		d.add(issueEmptyValue, path, "value is required")
		return
	}
	if !set.has(value) {
		d.add(issueInvalidEnum, path, "value is not an allowed enum member")
	}
}

func (d *dossierValidator) maxItems(path string, count, limit int) {
	if count > limit {
		d.add(issueTooManyItems, path, fmt.Sprintf("array has %d items, exceeding the limit of %d", count, limit))
	}
}

func (d *dossierValidator) evidencePaths(path string, paths []string, limit int, requireOne bool) {
	if requireOne && len(paths) == 0 {
		d.add(issueEmptyValue, path, "at least one evidence path is required")
		return
	}
	if len(paths) > limit {
		d.add(issueTooManyItems, path, fmt.Sprintf("array has %d paths, exceeding the limit of %d", len(paths), limit))
	}
	seen := make(map[string]struct{}, len(paths))
	for index, candidate := range paths {
		itemPath := fmt.Sprintf("%s/%d", path, index)
		if strings.TrimSpace(candidate) == "" {
			d.add(issueEmptyValue, itemPath, "evidence path must not be empty")
			continue
		}
		if len(candidate) > schemaMaxPath {
			d.add(issueStringTooLong, itemPath, fmt.Sprintf("evidence path exceeds the %d-byte limit", schemaMaxPath))
		}
		if _, duplicate := seen[candidate]; duplicate {
			d.add(issueDuplicateKey, itemPath, "duplicate evidence path")
		}
		seen[candidate] = struct{}{}
		if _, ok := d.evidence[candidate]; !ok {
			d.add(issueUnknownEvidence, itemPath, "path is not in the allowed evidence set")
			continue
		}
		d.used[candidate] = struct{}{}
	}
}

func (d *dossierValidator) dependencyComponent(path, kind, componentKey string) {
	trimmed := strings.TrimSpace(componentKey)
	if kind == "internal_component" && trimmed == "" {
		d.add(issueMissingComponent, path, "internal_component dependency requires a component_key")
		return
	}
	if trimmed == "" {
		return
	}
	if len(trimmed) > schemaMaxPath {
		d.add(issueStringTooLong, path, fmt.Sprintf("component_key exceeds the %d-byte limit", schemaMaxPath))
	}
	if _, ok := d.catalog[trimmed]; !ok {
		d.add(issueUnknownComponent, path, "component_key is not in the component catalog")
	}
}

func (d *dossierValidator) diagram(path string, diagram sharedmodel.Diagram) {
	d.prose(path+"/title", diagram.Title, schemaMaxTitle, true)
	d.evidencePaths(path+"/source_paths", diagram.SourcePaths, schemaMaxSourcePaths, true)

	switch diagram.Type {
	case sharedmodel.DiagramFlowchart:
		d.forbidDiagramFields(path, diagram, sharedmodel.DiagramFlowchart)
		d.flowchart(path, diagram)
	case sharedmodel.DiagramSequence:
		d.forbidDiagramFields(path, diagram, sharedmodel.DiagramSequence)
		d.sequence(path, diagram)
	case sharedmodel.DiagramClass:
		d.forbidDiagramFields(path, diagram, sharedmodel.DiagramClass)
		d.classDiagram(path, diagram)
	default:
		d.add(issueInvalidDiagram, path+"/type", "diagram type must be flowchart, sequence, or class")
	}
}

// forbidDiagramFields rejects sub-fields that do not belong to the declared diagram
// type, so a flowchart cannot smuggle sequence messages or class members.
func (d *dossierValidator) forbidDiagramFields(path string, diagram sharedmodel.Diagram, kind sharedmodel.DiagramType) {
	forbid := func(name string, present bool) {
		if present {
			d.add(issueDiagramFieldUnset, path+"/"+name, "field is not allowed for this diagram type")
		}
	}
	forbid("nodes", kind != sharedmodel.DiagramFlowchart && len(diagram.Nodes) > 0)
	forbid("edges", kind != sharedmodel.DiagramFlowchart && len(diagram.Edges) > 0)
	forbid("participants", kind != sharedmodel.DiagramSequence && len(diagram.Participants) > 0)
	forbid("messages", kind != sharedmodel.DiagramSequence && len(diagram.Messages) > 0)
	forbid("classes", kind != sharedmodel.DiagramClass && len(diagram.Classes) > 0)
	forbid("relationships", kind != sharedmodel.DiagramClass && len(diagram.Relationships) > 0)
}

func (d *dossierValidator) flowchart(path string, diagram sharedmodel.Diagram) {
	d.maxItems(path+"/nodes", len(diagram.Nodes), schemaMaxFlowchartNodes)
	d.maxItems(path+"/edges", len(diagram.Edges), schemaMaxFlowchartEdges)
	keys := make(map[string]struct{}, len(diagram.Nodes))
	for index, node := range diagram.Nodes {
		nodePath := fmt.Sprintf("%s/nodes/%d", path, index)
		d.diagramKey(nodePath+"/key", node.Key, keys)
		d.label(nodePath+"/label", node.Label, true)
	}
	for index, edge := range diagram.Edges {
		edgePath := fmt.Sprintf("%s/edges/%d", path, index)
		d.diagramReference(edgePath+"/from", edge.From, keys)
		d.diagramReference(edgePath+"/to", edge.To, keys)
		d.label(edgePath+"/label", edge.Label, false)
	}
}

func (d *dossierValidator) sequence(path string, diagram sharedmodel.Diagram) {
	d.maxItems(path+"/participants", len(diagram.Participants), schemaMaxSequenceParties)
	d.maxItems(path+"/messages", len(diagram.Messages), schemaMaxSequenceMessages)
	keys := make(map[string]struct{}, len(diagram.Participants))
	for index, participant := range diagram.Participants {
		participantPath := fmt.Sprintf("%s/participants/%d", path, index)
		d.diagramKey(participantPath+"/key", participant.Key, keys)
		d.label(participantPath+"/label", participant.Label, true)
	}
	for index, message := range diagram.Messages {
		messagePath := fmt.Sprintf("%s/messages/%d", path, index)
		d.diagramReference(messagePath+"/from", message.From, keys)
		d.diagramReference(messagePath+"/to", message.To, keys)
		d.label(messagePath+"/label", message.Label, true)
	}
}

func (d *dossierValidator) classDiagram(path string, diagram sharedmodel.Diagram) {
	d.maxItems(path+"/classes", len(diagram.Classes), schemaMaxClassNodes)
	d.maxItems(path+"/relationships", len(diagram.Relationships), schemaMaxClassRelationship)
	keys := make(map[string]struct{}, len(diagram.Classes))
	for index, class := range diagram.Classes {
		classPath := fmt.Sprintf("%s/classes/%d", path, index)
		d.diagramKey(classPath+"/key", class.Key, keys)
		d.label(classPath+"/label", class.Label, true)
		d.maxItems(classPath+"/members", len(class.Members), schemaMaxClassMembers)
		for memberIndex, member := range class.Members {
			d.label(fmt.Sprintf("%s/members/%d", classPath, memberIndex), member, true)
		}
	}
	for index, relation := range diagram.Relationships {
		relationPath := fmt.Sprintf("%s/relationships/%d", path, index)
		d.diagramReference(relationPath+"/from", relation.From, keys)
		d.diagramReference(relationPath+"/to", relation.To, keys)
		d.enum(relationPath+"/kind", relation.Kind, classRelationKinds)
		d.label(relationPath+"/label", relation.Label, false)
	}
}

func (d *dossierValidator) diagramKey(path, key string, keys map[string]struct{}) {
	if strings.TrimSpace(key) == "" {
		d.add(issueEmptyValue, path, "diagram key is required")
		return
	}
	if len(key) > schemaMaxDiagramKey {
		d.add(issueStringTooLong, path, fmt.Sprintf("diagram key exceeds the %d-byte limit", schemaMaxDiagramKey))
	}
	if _, duplicate := keys[key]; duplicate {
		d.add(issueDuplicateKey, path, "duplicate diagram key")
		return
	}
	keys[key] = struct{}{}
}

func (d *dossierValidator) diagramReference(path, key string, keys map[string]struct{}) {
	if strings.TrimSpace(key) == "" {
		d.add(issueEmptyValue, path, "diagram reference is required")
		return
	}
	if _, ok := keys[key]; !ok {
		d.add(issueDiagramReference, path, "reference does not match any defined key in this diagram")
	}
}

func (d *dossierValidator) label(path, value string, required bool) {
	if strings.TrimSpace(value) == "" {
		if required {
			d.add(issueEmptyValue, path, "label is required")
		}
		return
	}
	if len(value) > schemaMaxDiagramLabel {
		d.add(issueStringTooLong, path, fmt.Sprintf("label exceeds the %d-byte limit", schemaMaxDiagramLabel))
	}
	if reason := unsafeLabelReason(value); reason != "" {
		d.add(issueUnsafeProse, path, "label must be plain text: "+reason+" is not allowed")
	}
}

func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func decodeIssue(err error) sharedmodel.ValidationIssue {
	message := err.Error()
	switch {
	case strings.Contains(message, "unknown field"):
		return sharedmodel.ValidationIssue{Code: issueUnknownField, Path: "", Message: message}
	case strings.Contains(message, "cannot unmarshal"):
		return sharedmodel.ValidationIssue{Code: issueInvalidType, Path: "", Message: message}
	default:
		return sharedmodel.ValidationIssue{Code: issueInvalidJSON, Path: "", Message: "response is not valid JSON for the schema"}
	}
}

var (
	proseURIScheme  = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://|\b(?:javascript|data|vbscript|mailto|tel|file):)`)
	proseMarkdown   = regexp.MustCompile(`\]\(`)
	proseHTML       = regexp.MustCompile(`(?i)</?[a-z!][^>]*>`)
	proseListItem   = regexp.MustCompile(`^(?:[-*+]\s+|\d+[.)]\s+)`)
	proseMermaid    = regexp.MustCompile(`(?i)^(?:graph|flowchart)\s+(?:tb|td|bt|rl|lr|dt)\b|^(?:sequencediagram|classdiagram|erdiagram|statediagram(?:-v2)?|gantt|pie|journey|gitgraph|mindmap|timeline)\b`)
	proseASCIIArrow = regexp.MustCompile(`-->|<--|==>|<==|[-=_]{4,}`)
	proseMachineDir = regexp.MustCompile(`(?i)(?:^|[\s"'(<])(?:[a-z]:\\|/(?:users|home|root|private|var|tmp|etc|opt|usr|mnt|dev|proc|sys)(?:/|$|\s))`)
)

// unsafeProseReason returns a short reason a prose value is not plain text, or the
// empty string when it is safe. Local code owns all Markdown, links, headings, and
// diagram syntax, so model prose may contain none of it.
func unsafeProseReason(value string) string {
	if reason := unsafeRunes(value); reason != "" {
		return reason
	}
	if proseURIScheme.MatchString(value) {
		return "a URI scheme"
	}
	if proseMarkdown.MatchString(value) {
		return "a Markdown link"
	}
	if proseHTML.MatchString(value) {
		return "HTML"
	}
	if strings.Contains(value, "```") || strings.Contains(value, "~~~") {
		return "a code fence"
	}
	if proseASCIIArrow.MatchString(value) {
		return "an ASCII diagram"
	}
	if proseMachineDir.MatchString(value) {
		return "a machine path"
	}
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			return "a heading"
		}
		if proseListItem.MatchString(trimmed) {
			return "a list"
		}
		if proseMermaid.MatchString(trimmed) {
			return "diagram syntax"
		}
	}
	return ""
}

// unsafeLabelReason is the diagram-label variant: labels are rendered on a single line,
// so newlines are additionally rejected.
func unsafeLabelReason(value string) string {
	if strings.ContainsAny(value, "\n\r") {
		return "a line break"
	}
	return unsafeProseReason(value)
}

func unsafeRunes(value string) string {
	for _, r := range value {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return "a control character"
		}
		if unicode.Is(unicode.Cf, r) {
			return "a formatting character"
		}
		if r >= 0x2500 && r <= 0x257F {
			return "box-drawing characters"
		}
	}
	return ""
}
