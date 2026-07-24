package usecase

import (
	"fmt"
	"path"
	"sort"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

// renderVersion changes whenever any local rendering rule, template, marker format, or
// Mermaid emission changes. It participates in state compatibility so a renderer change
// forces regeneration.
const renderVersion = "v1"

// Topic identifiers used in ownership markers. They are stable and versioned by
// renderVersion.
const (
	topicComponents   = "components"
	topicArchitecture = "architecture"
	topicInterfaces   = "interfaces"
	topicDataModels   = "data_models"
	topicWorkflows    = "workflows"
	topicDependencies = "dependencies"
	topicReviewGaps   = "review_gaps"
	topicScanner      = "scanner"

	scannerSectionKey = "@scanner"
)

// Generated document base names under the configured generated directory.
const (
	docIndex        = "index.md"
	docCodebaseInfo = "codebase_info.md"
	docArchitecture = "architecture.md"
	docComponents   = "components.md"
	docInterfaces   = "interfaces.md"
	docDataModels   = "data_models.md"
	docWorkflows    = "workflows.md"
	docDependencies = "dependencies.md"
	docReviewNotes  = "review_notes.md"
)

// renderInput is the deterministic, already-validated input to local rendering. It
// contains no credentials and no unvalidated model output.
type renderInput struct {
	docsDir        string
	audience       string
	mermaidEnabled bool
	components     []sharedmodel.Component
	dossiers       map[string]sharedmodel.ComponentDossier
	decisions      []sharedmodel.SourceDecision
	files          []sharedmodel.SourceFile
	trackedPaths   int

	// Incremental reuse. When freshComponents is nil every component is rendered fresh
	// from its dossier (full generation). Otherwise only identities present in
	// freshComponents are rendered from dossiers; every other component reuses the exact
	// installed bytes so unrelated sections stay byte-for-byte identical. reuseSections
	// holds the inner bytes of each installed owned section, and reusePages holds the full
	// content of each installed component page.
	freshComponents map[string]bool
	reuseSections   map[sectionID]string
	reusePages      map[string]string
}

// isFresh reports whether a component must be rendered from a freshly generated dossier.
// In full generation (freshComponents == nil) every component is fresh.
func (input renderInput) isFresh(component sharedmodel.Component) bool {
	if input.freshComponents == nil {
		return true
	}
	return input.freshComponents[componentIdentity(component.Key, component.RootComponent)]
}

// ownedSectionContent returns the inner content bytes for one component's owned section in
// a topic document. A fresh component is rendered from its dossier; any other component
// reuses the exact installed bytes so its section is preserved byte-for-byte. A missing
// reusable section is a caller-visible error, which the incremental orchestration only
// reaches after verifying installed output integrity.
func (input renderInput) ownedSectionContent(topic string, component sharedmodel.Component, renderFresh func() string) (string, error) {
	if input.isFresh(component) {
		return renderFresh(), nil
	}
	content, ok := input.reuseSections[sectionID{Topic: topic, Key: component.Key}]
	if !ok {
		return "", fmt.Errorf("incremental render: installed document is missing section topic %q key %q", topic, component.Key)
	}
	return content, nil
}

// renderedDoc is one rendered generated document plus the ordered sections it owns and
// whether it is fully deterministic (no model prose).
type renderedDoc struct {
	path          string
	content       string
	sections      []sectionID
	componentKeys []string
	deterministic bool
}

// renderedOutput is the complete rendered document set.
type renderedOutput struct {
	docs []renderedDoc
}

// renderDocumentation renders every knowledge-base document from validated dossiers and
// deterministic scanner metadata. Output is byte-deterministic: local code owns all
// headings, ordering, links, markers, and escaping.
func renderDocumentation(input renderInput) (renderedOutput, error) {
	ordered := sortComponentsForDisplay(input.components)
	for _, component := range ordered {
		if !input.isFresh(component) {
			continue
		}
		if _, ok := input.dossiers[componentIdentity(component.Key, component.RootComponent)]; !ok {
			return renderedOutput{}, fmt.Errorf("render: component %q has no validated dossier", component.Key)
		}
	}
	hierarchy := hierarchyResult{}
	if input.mermaidEnabled {
		hierarchy = renderHierarchy(ordered)
	}

	components, err := renderComponentsOverview(input, ordered)
	if err != nil {
		return renderedOutput{}, err
	}
	reviewNotes, err := renderReviewNotes(input, ordered, hierarchy)
	if err != nil {
		return renderedOutput{}, err
	}
	docs := []renderedDoc{
		renderIndex(input, ordered),
		renderCodebaseInfo(input, ordered, hierarchy),
		components,
	}
	topics := []struct {
		topic, file, title string
		render             sectionRenderer
	}{
		{topicArchitecture, docArchitecture, "Architecture", renderArchitectureSection},
		{topicInterfaces, docInterfaces, "Interfaces", renderInterfacesSection},
		{topicDataModels, docDataModels, "Data Models", renderDataModelsSection},
		{topicWorkflows, docWorkflows, "Workflows", renderWorkflowsSection},
		{topicDependencies, docDependencies, "Dependencies", renderDependenciesSection},
	}
	for _, entry := range topics {
		doc, err := renderTopic(input, ordered, entry.topic, entry.file, entry.title, entry.render)
		if err != nil {
			return renderedOutput{}, err
		}
		docs = append(docs, doc)
	}
	docs = append(docs, reviewNotes)
	for _, component := range ordered {
		page, err := renderComponentPage(input, component)
		if err != nil {
			return renderedOutput{}, err
		}
		docs = append(docs, page)
	}
	return renderedOutput{docs: docs}, nil
}

// sortComponentsForDisplay orders components with root components first, then non-root
// components by key. This is the canonical order for every generated document.
func sortComponentsForDisplay(components []sharedmodel.Component) []sharedmodel.Component {
	ordered := append([]sharedmodel.Component(nil), components...)
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].RootComponent != ordered[right].RootComponent {
			return ordered[left].RootComponent
		}
		return ordered[left].Key < ordered[right].Key
	})
	return ordered
}

func (input renderInput) dossierFor(component sharedmodel.Component) sharedmodel.ComponentDossier {
	return input.dossiers[componentIdentity(component.Key, component.RootComponent)]
}

func relativeWithinDocs(docsDir, target string) string {
	return strings.TrimPrefix(target, docsDir+"/")
}

// ---- index.md ----------------------------------------------------------------

func renderIndex(input renderInput, components []sharedmodel.Component) renderedDoc {
	docPath := path.Join(input.docsDir, docIndex)
	var out mdBuilder
	out.line("# Repository Documentation")
	out.blank()
	out.line("This knowledge base is generated by docify-repo. It is the primary entry point for")
	out.line("maintainers, onboarding engineers, API consumers, and AI coding assistants.")
	out.blank()
	out.line("## Documents")
	out.blank()
	for _, entry := range indexDocumentCatalog {
		out.line(fmt.Sprintf("- [%s](%s) — %s", entry.title, entry.file, entry.description))
	}
	out.blank()
	out.line("## Components")
	out.blank()
	if len(components) == 0 {
		out.line("_No documentable components were discovered._")
	}
	for _, component := range components {
		link := relativeWithinDocs(input.docsDir, component.Document)
		out.line(fmt.Sprintf("- [`%s`](%s)", component.Key, link))
	}
	out.blank()
	out.line("## Navigation")
	out.blank()
	out.line("Start with the topic documents for a cross-component view, or open a component page")
	out.line("for its complete dossier. Each component owns clearly marked sections in the topic")
	out.line("documents; generated content is managed by docify-repo and should not be edited by hand.")
	return renderedDoc{path: docPath, content: out.finish(), deterministic: true}
}

type indexEntry struct {
	title       string
	file        string
	description string
}

// indexDocumentCatalog is fixed profile text describing each topic document. It does not
// depend on model output or semantic state.
var indexDocumentCatalog = []indexEntry{
	{"Codebase Info", docCodebaseInfo, "Scanner metadata, detected languages, manifests, and the component hierarchy."},
	{"Components", docComponents, "Catalog of components with purpose and responsibilities."},
	{"Architecture", docArchitecture, "Architectural responsibilities and boundaries per component."},
	{"Interfaces", docInterfaces, "APIs, events, commands, and integration points per component."},
	{"Data Models", docDataModels, "Entities, schemas, fields, relationships, and class diagrams."},
	{"Workflows", docWorkflows, "Runtime and development flows with flow and sequence diagrams."},
	{"Dependencies", docDependencies, "External and internal dependencies per component."},
	{"Review Notes", docReviewNotes, "Deterministic scanner notes and model-reported review gaps."},
}

// ---- codebase_info.md --------------------------------------------------------

func renderCodebaseInfo(input renderInput, components []sharedmodel.Component, hierarchy hierarchyResult) renderedDoc {
	docPath := path.Join(input.docsDir, docCodebaseInfo)
	included, triggering, excluded := decisionCounts(input.decisions)

	var out mdBuilder
	out.line("# Codebase Info")
	out.blank()
	out.line("Deterministic metadata collected by the scanner. This document contains no model output.")
	out.blank()
	out.line("## Summary")
	out.blank()
	out.line("| Metric | Value |")
	out.line("| --- | --- |")
	out.line(fmt.Sprintf("| Tracked files | %d |", input.trackedPaths))
	out.line(fmt.Sprintf("| Included as context | %d |", included))
	out.line(fmt.Sprintf("| Triggering regeneration | %d |", triggering))
	out.line(fmt.Sprintf("| Excluded | %d |", excluded))
	out.line(fmt.Sprintf("| Components | %d |", len(components)))
	out.blank()

	out.line("## Languages")
	out.blank()
	languages := languageCounts(input.files)
	if len(languages) == 0 {
		out.line("_No source languages detected._")
	} else {
		out.line("| Language | Files |")
		out.line("| --- | --- |")
		for _, entry := range languages {
			out.line(fmt.Sprintf("| %s | %d |", escapeCell(entry.name), entry.count))
		}
	}
	out.blank()

	out.line("## Dependency Manifests")
	out.blank()
	manifests := manifestPaths(input.files)
	if len(manifests) == 0 {
		out.line("_No dependency manifests detected._")
	} else {
		for _, manifest := range manifests {
			out.line("- " + sourceLink(docPath, manifest))
		}
	}
	out.blank()

	out.line("## Component Hierarchy")
	out.blank()
	if !input.mermaidEnabled {
		out.line("_Diagram rendering is disabled._")
	} else {
		out.raw(hierarchy.Mermaid)
		if hierarchy.Omitted > 0 {
			out.blank()
			out.line(fmt.Sprintf("_%d additional component(s) omitted from the diagram; see review notes._", hierarchy.Omitted))
		}
	}
	return renderedDoc{path: docPath, content: out.finish(), deterministic: true}
}

func decisionCounts(decisions []sharedmodel.SourceDecision) (included, triggering, excluded int) {
	for _, decision := range decisions {
		if decision.IncludedAsContext {
			included++
		} else {
			excluded++
		}
		if decision.TriggersRegeneration {
			triggering++
		}
	}
	return included, triggering, excluded
}

type languageCount struct {
	name  string
	count int
}

func languageCounts(files []sharedmodel.SourceFile) []languageCount {
	counts := make(map[string]int)
	for _, file := range files {
		if file.Role != sharedmodel.RoleProductionSource && file.Role != sharedmodel.RoleUnknownSource {
			continue
		}
		counts[languageForPath(file.Path)]++
	}
	result := make([]languageCount, 0, len(counts))
	for name, count := range counts {
		result = append(result, languageCount{name: name, count: count})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].count != result[right].count {
			return result[left].count > result[right].count
		}
		return result[left].name < result[right].name
	})
	return result
}

// languageExtensions maps a file extension to a display language. It is deterministic,
// versioned data; unknown extensions render as "Other".
var languageExtensions = map[string]string{
	".go": "Go", ".py": "Python", ".js": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".java": "Java", ".kt": "Kotlin",
	".rb": "Ruby", ".rs": "Rust", ".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++",
	".cs": "C#", ".php": "PHP", ".swift": "Swift", ".scala": "Scala", ".sh": "Shell",
	".sql": "SQL", ".proto": "Protocol Buffers",
}

func languageForPath(repositoryPath string) string {
	extension := strings.ToLower(path.Ext(repositoryPath))
	if language, ok := languageExtensions[extension]; ok {
		return language
	}
	return "Other"
}

func manifestPaths(files []sharedmodel.SourceFile) []string {
	paths := make([]string, 0)
	for _, file := range files {
		if file.Role == sharedmodel.RoleDependencyManifest {
			paths = append(paths, file.Path)
		}
	}
	sort.Strings(paths)
	return paths
}

// ---- components.md -----------------------------------------------------------

func renderComponentsOverview(input renderInput, components []sharedmodel.Component) (renderedDoc, error) {
	docPath := path.Join(input.docsDir, docComponents)
	var out mdBuilder
	out.line("# Components")
	out.blank()
	out.line("Every discovered component, its purpose, and its primary responsibilities.")
	out.blank()

	sections := make([]sectionID, 0, len(components))
	keys := make([]string, 0, len(components))
	for _, component := range components {
		content, err := input.ownedSectionContent(topicComponents, component, func() string {
			dossier := input.dossierFor(component)
			link := relativeWithinDocs(input.docsDir, component.Document)
			var section mdBuilder
			section.line(fmt.Sprintf("## [`%s`](%s)", component.Key, link))
			section.blank()
			section.line(fmt.Sprintf("**%s**", escapeInline(dossier.Title)))
			section.blank()
			section.line(escapeInline(dossier.Purpose))
			section.blank()
			section.line("**Responsibilities:**")
			section.blank()
			if len(dossier.Architecture) == 0 {
				section.line("_None documented._")
			} else {
				for _, item := range dossier.Architecture {
					section.line("- " + escapeInline(item.Title))
				}
			}
			return section.finish()
		})
		if err != nil {
			return renderedDoc{}, err
		}
		out.raw(renderOwnedSection(topicComponents, component.Key, content))
		out.blank()
		sections = append(sections, sectionID{Topic: topicComponents, Key: component.Key})
		keys = append(keys, component.Key)
	}
	return renderedDoc{path: docPath, content: out.finish(), sections: sections, componentKeys: keys}, nil
}

// ---- generic topic documents -------------------------------------------------

type sectionRenderer func(input renderInput, docPath string, dossier sharedmodel.ComponentDossier) string

func renderTopic(input renderInput, components []sharedmodel.Component, topic, file, title string, render sectionRenderer) (renderedDoc, error) {
	docPath := path.Join(input.docsDir, file)
	var out mdBuilder
	out.line("# " + title)
	out.blank()
	out.line(fmt.Sprintf("%s owned by each component. Sections are managed by docify-repo.", title))
	out.blank()

	sections := make([]sectionID, 0, len(components))
	keys := make([]string, 0, len(components))
	for _, component := range components {
		content, err := input.ownedSectionContent(topic, component, func() string {
			dossier := input.dossierFor(component)
			var section mdBuilder
			section.line(fmt.Sprintf("## `%s`", component.Key))
			section.blank()
			section.raw(render(input, docPath, dossier))
			return section.finish()
		})
		if err != nil {
			return renderedDoc{}, err
		}
		out.raw(renderOwnedSection(topic, component.Key, content))
		out.blank()
		sections = append(sections, sectionID{Topic: topic, Key: component.Key})
		keys = append(keys, component.Key)
	}
	return renderedDoc{path: docPath, content: out.finish(), sections: sections, componentKeys: keys}, nil
}

func renderArchitectureSection(_ renderInput, docPath string, dossier sharedmodel.ComponentDossier) string {
	if len(dossier.Architecture) == 0 {
		return "_No architecture documented for this component._\n"
	}
	var out mdBuilder
	for _, item := range dossier.Architecture {
		out.line("### " + escapeInline(item.Title))
		out.blank()
		out.line(escapeInline(item.Description))
		out.blank()
		out.line("Source: " + sourcePathsLine(docPath, item.SourcePaths))
		out.blank()
	}
	return out.finish()
}

func renderInterfacesSection(_ renderInput, docPath string, dossier sharedmodel.ComponentDossier) string {
	if len(dossier.Interfaces) == 0 {
		return "_No interfaces documented for this component._\n"
	}
	var out mdBuilder
	out.line("| Name | Kind | Direction | Description | Sources |")
	out.line("| --- | --- | --- | --- | --- |")
	for _, item := range dossier.Interfaces {
		out.line(fmt.Sprintf("| %s | %s | %s | %s | %s |",
			escapeCell(item.Name), escapeCell(item.Kind), escapeCell(item.Direction),
			escapeCell(item.Description), cellSources(docPath, item.SourcePaths)))
	}
	return out.finish()
}

func renderDataModelsSection(input renderInput, docPath string, dossier sharedmodel.ComponentDossier) string {
	var out mdBuilder
	if len(dossier.DataModels) == 0 {
		out.line("_No data models documented for this component._")
	}
	for _, item := range dossier.DataModels {
		out.line(fmt.Sprintf("### %s (%s)", escapeInline(item.Name), escapeInline(item.Kind)))
		out.blank()
		out.line(escapeInline(item.Description))
		out.blank()
		if len(item.Fields) > 0 {
			out.line("| Field | Type | Description |")
			out.line("| --- | --- | --- |")
			for _, field := range item.Fields {
				out.line(fmt.Sprintf("| %s | %s | %s |", escapeCell(field.Name), escapeCell(field.Type), escapeCell(field.Description)))
			}
			out.blank()
		}
		if len(item.Relationships) > 0 {
			out.line("| Relationship | Kind | Description |")
			out.line("| --- | --- | --- |")
			for _, relation := range item.Relationships {
				out.line(fmt.Sprintf("| %s | %s | %s |", escapeCell(relation.Target), escapeCell(relation.Kind), escapeCell(relation.Description)))
			}
			out.blank()
		}
		out.line("Source: " + sourcePathsLine(docPath, item.SourcePaths))
		out.blank()
	}
	renderComponentDiagrams(&out, input, dossier, sharedmodel.DiagramClass)
	return out.finish()
}

func renderWorkflowsSection(input renderInput, docPath string, dossier sharedmodel.ComponentDossier) string {
	var out mdBuilder
	if len(dossier.Workflows) == 0 {
		out.line("_No workflows documented for this component._")
	}
	for _, item := range dossier.Workflows {
		out.line("### " + escapeInline(item.Name))
		out.blank()
		out.line(escapeInline(item.Description))
		out.blank()
		for index, step := range item.Steps {
			line := fmt.Sprintf("%d. **%s** — %s", index+1, escapeInline(step.Actor), escapeInline(step.Action))
			if strings.TrimSpace(step.Target) != "" {
				line += " → " + escapeInline(step.Target)
			}
			out.line(line)
		}
		out.blank()
		out.line("Source: " + sourcePathsLine(docPath, item.SourcePaths))
		out.blank()
	}
	renderComponentDiagrams(&out, input, dossier, sharedmodel.DiagramFlowchart, sharedmodel.DiagramSequence)
	return out.finish()
}

func renderDependenciesSection(_ renderInput, docPath string, dossier sharedmodel.ComponentDossier) string {
	if len(dossier.Dependencies) == 0 {
		return "_No dependencies documented for this component._\n"
	}
	var out mdBuilder
	out.line("| Name | Kind | Purpose | Component | Sources |")
	out.line("| --- | --- | --- | --- | --- |")
	for _, item := range dossier.Dependencies {
		component := ""
		if item.ComponentKey != "" {
			component = "`" + escapeCell(item.ComponentKey) + "`"
		}
		out.line(fmt.Sprintf("| %s | %s | %s | %s | %s |",
			escapeCell(item.Name), escapeCell(item.Kind), escapeCell(item.Purpose), component, cellSources(docPath, item.SourcePaths)))
	}
	return out.finish()
}

// renderComponentDiagrams appends the component's diagrams of the given types, in dossier
// order, when Mermaid is enabled.
func renderComponentDiagrams(out *mdBuilder, input renderInput, dossier sharedmodel.ComponentDossier, types ...sharedmodel.DiagramType) {
	if !input.mermaidEnabled {
		return
	}
	allowed := make(map[sharedmodel.DiagramType]struct{}, len(types))
	for _, kind := range types {
		allowed[kind] = struct{}{}
	}
	for _, diagram := range dossier.Diagrams {
		if _, ok := allowed[diagram.Type]; !ok {
			continue
		}
		out.blank()
		out.line("**" + escapeInline(diagram.Title) + "**")
		out.blank()
		out.raw(renderDiagram(diagram))
		out.blank()
	}
}

// cellSources renders evidence links joined for a table cell (comma separated, no pipes).
func cellSources(docPath string, paths []string) string {
	links := make([]string, 0, len(paths))
	for _, evidence := range paths {
		links = append(links, sourceLink(docPath, evidence))
	}
	return strings.Join(links, "<br>")
}

// ---- review_notes.md ---------------------------------------------------------

func renderReviewNotes(input renderInput, components []sharedmodel.Component, hierarchy hierarchyResult) (renderedDoc, error) {
	docPath := path.Join(input.docsDir, docReviewNotes)
	var out mdBuilder
	out.line("# Review Notes")
	out.blank()
	out.line("Deterministic scanner notes followed by model-reported review gaps per component.")
	out.blank()

	sections := make([]sectionID, 0, len(components)+1)
	keys := make([]string, 0, len(components))

	// The scanner section is deterministic scanner metadata, so it is always rendered
	// fresh regardless of which components were regenerated.
	var scanner mdBuilder
	scanner.line("## Scanner Notes")
	scanner.blank()
	excludedForReview := excludedReviewPaths(input.decisions)
	if hierarchy.Omitted == 0 && len(excludedForReview) == 0 {
		scanner.line("_No scanner review notes._")
	}
	if hierarchy.Omitted > 0 {
		scanner.line(fmt.Sprintf("- %d component(s) were omitted from the codebase hierarchy diagram because of the node limit.", hierarchy.Omitted))
	}
	for _, entry := range excludedForReview {
		scanner.line(fmt.Sprintf("- Excluded `%s` (%s).", entry.path, entry.reason))
	}
	out.raw(renderOwnedSection(topicScanner, scannerSectionKey, scanner.finish()))
	out.blank()
	sections = append(sections, sectionID{Topic: topicScanner, Key: scannerSectionKey})

	for _, component := range components {
		content, err := input.ownedSectionContent(topicReviewGaps, component, func() string {
			dossier := input.dossierFor(component)
			var section mdBuilder
			section.line(fmt.Sprintf("## `%s`", component.Key))
			section.blank()
			if len(dossier.ReviewGaps) == 0 {
				section.line("_No review gaps reported._")
			}
			for _, gap := range dossier.ReviewGaps {
				section.line(fmt.Sprintf("- **%s** — %s", escapeInline(gap.Kind), escapeInline(gap.Description)))
				if strings.TrimSpace(gap.Recommendation) != "" {
					section.line("  - Recommendation: " + escapeInline(gap.Recommendation))
				}
				if len(gap.SourcePaths) > 0 {
					section.line("  - Source: " + sourcePathsLine(docPath, gap.SourcePaths))
				}
			}
			return section.finish()
		})
		if err != nil {
			return renderedDoc{}, err
		}
		out.raw(renderOwnedSection(topicReviewGaps, component.Key, content))
		out.blank()
		sections = append(sections, sectionID{Topic: topicReviewGaps, Key: component.Key})
		keys = append(keys, component.Key)
	}
	return renderedDoc{path: docPath, content: out.finish(), sections: sections, componentKeys: keys}, nil
}

type excludedReview struct {
	path   string
	reason string
}

// excludedReviewPaths reports excluded inputs worth surfacing (oversized, binary, or
// secret-value denied) so a reviewer can see what analysis could not read. Ordinary
// include-misses and tool-owned output are not review-worthy.
func excludedReviewPaths(decisions []sharedmodel.SourceDecision) []excludedReview {
	result := make([]excludedReview, 0)
	for _, decision := range decisions {
		if decision.IncludedAsContext {
			continue
		}
		switch decision.Reason {
		case "file_size_limit", "binary_content":
			result = append(result, excludedReview{path: decision.Path, reason: decision.Reason})
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].path < result[right].path })
	return result
}

// ---- component page ----------------------------------------------------------

func renderComponentPage(input renderInput, component sharedmodel.Component) (renderedDoc, error) {
	docPath := component.Document
	if !input.isFresh(component) {
		content, ok := input.reusePages[docPath]
		if !ok {
			return renderedDoc{}, fmt.Errorf("incremental render: installed component page %q is missing", docPath)
		}
		return renderedDoc{path: docPath, content: content, componentKeys: []string{component.Key}}, nil
	}
	dossier := input.dossierFor(component)
	var out mdBuilder
	out.line("# " + escapeInline(dossier.Title))
	out.blank()
	out.line(fmt.Sprintf("Component `%s`.", component.Key))
	out.blank()
	out.line("## Purpose")
	out.blank()
	out.line(escapeInline(dossier.Purpose))
	out.blank()

	if len(dossier.SourcePaths) > 0 {
		out.line("## Source Paths")
		out.blank()
		for _, evidence := range dossier.SourcePaths {
			out.line("- " + sourceLink(docPath, evidence))
		}
		out.blank()
	}

	out.line("## Architecture")
	out.blank()
	out.raw(renderArchitectureSection(input, docPath, dossier))
	out.blank()

	out.line("## Interfaces")
	out.blank()
	out.raw(renderInterfacesSection(input, docPath, dossier))
	out.blank()

	out.line("## Data Models")
	out.blank()
	out.raw(renderDataModelsSection(input, docPath, dossier))
	out.blank()

	out.line("## Workflows")
	out.blank()
	out.raw(renderWorkflowsSection(input, docPath, dossier))
	out.blank()

	out.line("## Dependencies")
	out.blank()
	out.raw(renderDependenciesSection(input, docPath, dossier))
	out.blank()

	out.line("## Review Gaps")
	out.blank()
	if len(dossier.ReviewGaps) == 0 {
		out.line("_No review gaps reported._")
	}
	for _, gap := range dossier.ReviewGaps {
		out.line(fmt.Sprintf("- **%s** — %s", escapeInline(gap.Kind), escapeInline(gap.Description)))
		if strings.TrimSpace(gap.Recommendation) != "" {
			out.line("  - Recommendation: " + escapeInline(gap.Recommendation))
		}
	}
	return renderedDoc{path: docPath, content: out.finish(), componentKeys: []string{component.Key}}, nil
}
