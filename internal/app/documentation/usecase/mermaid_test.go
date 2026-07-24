package usecase

import (
	"strings"
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func TestRenderFlowchartAssignsPositionalIDs(t *testing.T) {
	diagram := sharedmodel.Diagram{
		Type:  sharedmodel.DiagramFlowchart,
		Title: "Flow",
		Nodes: []sharedmodel.FlowchartNode{{Key: "start", Label: "Start"}, {Key: "done", Label: "Done"}},
		Edges: []sharedmodel.FlowchartEdge{{From: "start", To: "done", Label: "then"}},
	}
	got := renderDiagram(diagram)
	for _, want := range []string{"```mermaid\n", "flowchart TD\n", `n0["Start"]`, `n1["Done"]`, `n0 -->|"then"| n1`} {
		if !strings.Contains(got, want) {
			t.Errorf("flowchart missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "start") || strings.Contains(got, "done") {
		t.Errorf("flowchart leaked model-supplied keys:\n%s", got)
	}
}

func TestRenderSequenceUsesResponseArrows(t *testing.T) {
	diagram := sharedmodel.Diagram{
		Type:         sharedmodel.DiagramSequence,
		Title:        "Seq",
		Participants: []sharedmodel.SequenceParticipant{{Key: "c", Label: "Client"}, {Key: "s", Label: "Server"}},
		Messages: []sharedmodel.SequenceMessage{
			{From: "c", To: "s", Label: "request"},
			{From: "s", To: "c", Label: "reply", Response: true},
		},
	}
	got := renderDiagram(diagram)
	for _, want := range []string{"sequenceDiagram\n", "participant p0 as Client", "p0->>p1: request", "p1-->>p0: reply"} {
		if !strings.Contains(got, want) {
			t.Errorf("sequence missing %q\n%s", want, got)
		}
	}
}

func TestRenderClassDiagramMapsRelationConnectors(t *testing.T) {
	diagram := sharedmodel.Diagram{
		Type:  sharedmodel.DiagramClass,
		Title: "Classes",
		Classes: []sharedmodel.ClassNode{
			{Key: "a", Label: "Base", Members: []string{"+id int"}},
			{Key: "b", Label: "Derived"},
		},
		Relationships: []sharedmodel.ClassRelationship{{From: "b", To: "a", Kind: "inheritance"}},
	}
	got := renderDiagram(diagram)
	for _, want := range []string{"classDiagram\n", `class c0["Base"]`, "c0 : +id int", "c1 --|> c0"} {
		if !strings.Contains(got, want) {
			t.Errorf("class diagram missing %q\n%s", want, got)
		}
	}
}

func TestMermaidEscapeNeutralizesMetacharacters(t *testing.T) {
	got := mermaidEscape(`say "hi"; a<b>c #1`)
	want := `say #quot;hi#quot;#59; a#lt;b#gt;c #35;1`
	if got != want {
		t.Fatalf("mermaidEscape() = %q, want %q", got, want)
	}
	// The dangerous bare characters must never survive; entities legitimately reuse
	// '#' and ';', so those are not checked here.
	for _, forbidden := range []string{`"`, "<", ">"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("escape left bare %q in %q", forbidden, got)
		}
	}
}

func TestRenderHierarchyBoundsNodesAndReportsOmitted(t *testing.T) {
	components := []sharedmodel.Component{
		{Key: rootComponentKey, RootComponent: true},
		{Key: "services/api"},
		{Key: "services/api/internal"},
	}
	result := renderHierarchy(components)
	if result.Omitted != 0 {
		t.Fatalf("omitted = %d, want 0", result.Omitted)
	}
	// services/api/internal must attach to services/api, not the repository root.
	if !strings.Contains(result.Mermaid, "k0 --> k1") {
		t.Errorf("hierarchy did not nest under the ancestor component:\n%s", result.Mermaid)
	}
	if !strings.Contains(result.Mermaid, `r["repository root"]`) {
		t.Errorf("hierarchy missing repository root node:\n%s", result.Mermaid)
	}
}
