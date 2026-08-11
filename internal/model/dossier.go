package model

import "encoding/json"

// ComponentDossier is the structured analysis result for one component. It is decoded
// strictly from model output and validated locally before rendering. It is never
// stored in state and never used as planning scope.
type ComponentDossier struct {
	Title        string             `json:"title"`
	Purpose      string             `json:"purpose"`
	SourcePaths  []string           `json:"source_paths"`
	Architecture []ArchitectureItem `json:"architecture"`
	Interfaces   []InterfaceItem    `json:"interfaces"`
	DataModels   []DataModelItem    `json:"data_models"`
	Workflows    []WorkflowItem     `json:"workflows"`
	Dependencies []DependencyItem   `json:"dependencies"`
	ReviewGaps   []ReviewGap        `json:"review_gaps"`
	Diagrams     []Diagram          `json:"diagrams"`
}

// OverviewCandidate is the bounded map result used to derive a component title and
// purpose without generating the complete dossier.
type OverviewCandidate struct {
	Title       string   `json:"title"`
	Purpose     string   `json:"purpose"`
	SourcePaths []string `json:"source_paths"`
}

// ComponentOverview is the bounded reducer result. Evidence remains owned by the
// validated input fragments and is assembled locally rather than selected by the model.
type ComponentOverview struct {
	Title   string `json:"title"`
	Purpose string `json:"purpose"`
}

// ListFragment is the common bounded envelope for section-specific model output.
// Complete is the model's coverage claim; local validation also treats a response at
// its item cap as saturated regardless of that claim.
type ListFragment[T any] struct {
	Complete     bool `json:"complete"`
	OmittedCount *int `json:"omitted_count,omitempty"`
	Items        []T  `json:"items"`
}

type ArchitectureFragment = ListFragment[ArchitectureItem]
type InterfacesFragment = ListFragment[InterfaceItem]
type DataModelsFragment = ListFragment[DataModelItem]
type WorkflowsFragment = ListFragment[WorkflowItem]
type DependenciesFragment = ListFragment[DependencyItem]
type ReviewGapsFragment = ListFragment[ReviewGap]
type DiagramsFragment = ListFragment[Diagram]

type ArchitectureItem struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	SourcePaths []string `json:"source_paths"`
}

type InterfaceItem struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Direction   string   `json:"direction"`
	Description string   `json:"description"`
	SourcePaths []string `json:"source_paths"`
}

type DataModelItem struct {
	Name          string             `json:"name"`
	Kind          string             `json:"kind"`
	Description   string             `json:"description"`
	Fields        []DataField        `json:"fields"`
	Relationships []DataRelationship `json:"relationships"`
	SourcePaths   []string           `json:"source_paths"`
}

type DataField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type DataRelationship struct {
	Target      string `json:"target"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type WorkflowItem struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []WorkflowStep `json:"steps"`
	SourcePaths []string       `json:"source_paths"`
}

type WorkflowStep struct {
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
}

type DependencyItem struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Purpose      string   `json:"purpose"`
	ComponentKey string   `json:"component_key"`
	SourcePaths  []string `json:"source_paths"`
}

type ReviewGap struct {
	Kind           string   `json:"kind"`
	Description    string   `json:"description"`
	Recommendation string   `json:"recommendation"`
	SourcePaths    []string `json:"source_paths"`
}

// DiagramType is the typed diagram discriminator. Only these three unions are
// supported; the local renderer emits Mermaid, never the model.
type DiagramType string

const (
	DiagramFlowchart DiagramType = "flowchart"
	DiagramSequence  DiagramType = "sequence"
	DiagramClass     DiagramType = "class"
)

// Diagram is a flat union across the three supported diagram types. Local validation
// enforces that only the fields belonging to Type are populated.
type Diagram struct {
	Type          DiagramType           `json:"type"`
	Title         string                `json:"title"`
	SourcePaths   []string              `json:"source_paths"`
	Nodes         []FlowchartNode       `json:"nodes,omitempty"`
	Edges         []FlowchartEdge       `json:"edges,omitempty"`
	Participants  []SequenceParticipant `json:"participants,omitempty"`
	Messages      []SequenceMessage     `json:"messages,omitempty"`
	Classes       []ClassNode           `json:"classes,omitempty"`
	Relationships []ClassRelationship   `json:"relationships,omitempty"`
}

type FlowchartNode struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type FlowchartEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

type SequenceParticipant struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type SequenceMessage struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Label    string `json:"label"`
	Response bool   `json:"response"`
}

type ClassNode struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Members []string `json:"members"`
}

type ClassRelationship struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// MarshalJSON preserves the schema's discriminated diagram union. Active arrays are
// emitted even when empty, while inactive arrays are omitted unless they were
// explicitly populated so final validation can still detect contamination.
func (d Diagram) MarshalJSON() ([]byte, error) {
	type diagramWire struct {
		Type          DiagramType            `json:"type"`
		Title         string                 `json:"title"`
		Nodes         *[]FlowchartNode       `json:"nodes,omitempty"`
		Edges         *[]FlowchartEdge       `json:"edges,omitempty"`
		Participants  *[]SequenceParticipant `json:"participants,omitempty"`
		Messages      *[]SequenceMessage     `json:"messages,omitempty"`
		Classes       *[]ClassNode           `json:"classes,omitempty"`
		Relationships *[]ClassRelationship   `json:"relationships,omitempty"`
		SourcePaths   []string               `json:"source_paths"`
	}

	nodes, edges := d.Nodes, d.Edges
	participants, messages := d.Participants, d.Messages
	classes, relationships := d.Classes, d.Relationships
	if d.Type == DiagramFlowchart {
		if nodes == nil {
			nodes = []FlowchartNode{}
		}
		if edges == nil {
			edges = []FlowchartEdge{}
		}
	}
	if d.Type == DiagramSequence {
		if participants == nil {
			participants = []SequenceParticipant{}
		}
		if messages == nil {
			messages = []SequenceMessage{}
		}
	}
	if d.Type == DiagramClass {
		if classes == nil {
			classes = []ClassNode{}
		}
		if relationships == nil {
			relationships = []ClassRelationship{}
		}
	}

	wire := diagramWire{Type: d.Type, Title: d.Title, SourcePaths: d.SourcePaths}
	if d.Type == DiagramFlowchart || d.Nodes != nil {
		wire.Nodes = &nodes
	}
	if d.Type == DiagramFlowchart || d.Edges != nil {
		wire.Edges = &edges
	}
	if d.Type == DiagramSequence || d.Participants != nil {
		wire.Participants = &participants
	}
	if d.Type == DiagramSequence || d.Messages != nil {
		wire.Messages = &messages
	}
	if d.Type == DiagramClass || d.Classes != nil {
		wire.Classes = &classes
	}
	if d.Type == DiagramClass || d.Relationships != nil {
		wire.Relationships = &relationships
	}
	return json.Marshal(wire)
}

// ValidationIssue is a stable, safe description of one contract violation. It carries
// no source content and is reused verbatim in a repair request.
type ValidationIssue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}
