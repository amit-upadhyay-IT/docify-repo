package model

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
	Target string `json:"target,omitempty"`
}

type DependencyItem struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Purpose      string   `json:"purpose"`
	ComponentKey string   `json:"component_key,omitempty"`
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
	Label string `json:"label,omitempty"`
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
	Label string `json:"label,omitempty"`
}

// ValidationIssue is a stable, safe description of one contract violation. It carries
// no source content and is reused verbatim in a repair request.
type ValidationIssue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}
