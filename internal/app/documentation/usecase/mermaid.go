package usecase

import (
	"fmt"
	"sort"
	"strings"

	sharedmodel "docify-repo/internal/model"
)

// mermaidVersion changes whenever the local Mermaid emission rules change, which is a
// rendering change and must bump renderVersion.
const mermaidVersion = "v1"

// hierarchyMaxNodes bounds the deterministic codebase hierarchy flowchart. Beyond this
// the renderer collapses the remaining components and records the omitted count as a
// review note. It is a versioned rendering constant.
const hierarchyMaxNodes = 60

// mermaidEntityEscaper replaces the characters that would break Mermaid syntax with
// their numeric or named HTML entities. It is a single-pass replacer, so the entities it
// emits (which themselves contain '#' and ';') are never re-scanned. Labels reaching
// this function are already validated plain text (no control characters, no line breaks,
// no HTML), so this only neutralizes the small set of Mermaid metacharacters.
var mermaidEntityEscaper = strings.NewReplacer(
	"#", "#35;",
	"\"", "#quot;",
	";", "#59;",
	"<", "#lt;",
	">", "#gt;",
)

func mermaidEscape(value string) string {
	return mermaidEntityEscaper.Replace(value)
}

// renderDiagram renders one validated typed diagram to a fenced Mermaid block. The
// diagram has already passed local validation (unique keys, resolvable references, safe
// labels, count limits), so this function only assigns position-based safe IDs and emits
// supported syntax. Raw Mermaid is never accepted from the model.
func renderDiagram(diagram sharedmodel.Diagram) string {
	var body string
	switch diagram.Type {
	case sharedmodel.DiagramFlowchart:
		body = renderFlowchart(diagram)
	case sharedmodel.DiagramSequence:
		body = renderSequence(diagram)
	case sharedmodel.DiagramClass:
		body = renderClassDiagram(diagram)
	default:
		return ""
	}
	var builder strings.Builder
	builder.WriteString("```mermaid\n")
	builder.WriteString(body)
	builder.WriteString("```\n")
	return builder.String()
}

func renderFlowchart(diagram sharedmodel.Diagram) string {
	ids := make(map[string]string, len(diagram.Nodes))
	var builder strings.Builder
	builder.WriteString("flowchart TD\n")
	for index, node := range diagram.Nodes {
		id := fmt.Sprintf("n%d", index)
		ids[node.Key] = id
		fmt.Fprintf(&builder, "    %s[\"%s\"]\n", id, mermaidEscape(node.Label))
	}
	for _, edge := range diagram.Edges {
		from, to := ids[edge.From], ids[edge.To]
		if strings.TrimSpace(edge.Label) == "" {
			fmt.Fprintf(&builder, "    %s --> %s\n", from, to)
			continue
		}
		fmt.Fprintf(&builder, "    %s -->|\"%s\"| %s\n", from, mermaidEscape(edge.Label), to)
	}
	return builder.String()
}

func renderSequence(diagram sharedmodel.Diagram) string {
	ids := make(map[string]string, len(diagram.Participants))
	var builder strings.Builder
	builder.WriteString("sequenceDiagram\n")
	for index, participant := range diagram.Participants {
		id := fmt.Sprintf("p%d", index)
		ids[participant.Key] = id
		fmt.Fprintf(&builder, "    participant %s as %s\n", id, mermaidEscape(participant.Label))
	}
	for _, message := range diagram.Messages {
		arrow := "->>"
		if message.Response {
			arrow = "-->>"
		}
		fmt.Fprintf(&builder, "    %s%s%s: %s\n", ids[message.From], arrow, ids[message.To], mermaidEscape(message.Label))
	}
	return builder.String()
}

// classRelationConnectors maps validated relationship kinds to Mermaid class-diagram
// connectors. The set matches classRelationKinds in schema.go.
var classRelationConnectors = map[string]string{
	"association": "-->",
	"aggregation": "o--",
	"composition": "*--",
	"inheritance": "--|>",
	"realization": "..|>",
	"dependency":  "..>",
}

func renderClassDiagram(diagram sharedmodel.Diagram) string {
	ids := make(map[string]string, len(diagram.Classes))
	var builder strings.Builder
	builder.WriteString("classDiagram\n")
	for index, class := range diagram.Classes {
		id := fmt.Sprintf("c%d", index)
		ids[class.Key] = id
		fmt.Fprintf(&builder, "    class %s[\"%s\"]\n", id, mermaidEscape(class.Label))
		for _, member := range class.Members {
			fmt.Fprintf(&builder, "    %s : %s\n", id, mermaidEscape(member))
		}
	}
	for _, relation := range diagram.Relationships {
		connector := classRelationConnectors[relation.Kind]
		from, to := ids[relation.From], ids[relation.To]
		if strings.TrimSpace(relation.Label) == "" {
			fmt.Fprintf(&builder, "    %s %s %s\n", from, connector, to)
			continue
		}
		fmt.Fprintf(&builder, "    %s %s %s : %s\n", from, connector, to, mermaidEscape(relation.Label))
	}
	return builder.String()
}

// hierarchyResult is the deterministic codebase hierarchy flowchart plus the number of
// components collapsed out of it because of the node bound.
type hierarchyResult struct {
	Mermaid string
	Omitted int
}

// renderHierarchy builds the bounded codebase hierarchy flowchart from component
// metadata only. It requires no dossier field and no LLM call: each component becomes a
// node under its nearest ancestor component, or the repository root. When the component
// count exceeds the node bound, the deepest-sorted remainder is collapsed and the count
// is returned so the scanner can record it as a review note.
func renderHierarchy(components []sharedmodel.Component) hierarchyResult {
	keys := make([]string, 0, len(components))
	for _, component := range components {
		if component.RootComponent || component.Key == rootComponentKey {
			continue
		}
		keys = append(keys, component.Key)
	}
	sort.Strings(keys)

	omitted := 0
	limit := hierarchyMaxNodes - 1 // reserve one node for the repository root
	if limit < 0 {
		limit = 0
	}
	if len(keys) > limit {
		omitted = len(keys) - limit
		keys = keys[:limit]
	}
	included := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		included[key] = struct{}{}
	}

	ids := map[string]string{rootComponentKey: "r"}
	for index, key := range keys {
		ids[key] = fmt.Sprintf("k%d", index)
	}

	var builder strings.Builder
	builder.WriteString("```mermaid\n")
	builder.WriteString("flowchart TD\n")
	builder.WriteString("    r[\"repository root\"]\n")
	for _, key := range keys {
		fmt.Fprintf(&builder, "    %s[\"%s\"]\n", ids[key], mermaidEscape(key))
	}
	for _, key := range keys {
		parent := nearestAncestorComponent(key, included)
		fmt.Fprintf(&builder, "    %s --> %s\n", ids[parent], ids[key])
	}
	builder.WriteString("```\n")
	return hierarchyResult{Mermaid: builder.String(), Omitted: omitted}
}

// nearestAncestorComponent returns the longest proper path-prefix of key that is itself
// an included component, or the repository root when none exists.
func nearestAncestorComponent(key string, included map[string]struct{}) string {
	directory := key
	for {
		slash := strings.LastIndexByte(directory, '/')
		if slash < 0 {
			return rootComponentKey
		}
		directory = directory[:slash]
		if _, ok := included[directory]; ok {
			return directory
		}
	}
}
