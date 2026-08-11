package usecase

// Schema limits mirror internal/prompt/codebase-summary/v1/schema.json. They are the
// hard ceilings the local validator enforces; configuration may impose lower limits
// but never higher. Changing any of these is a schema change and must bump
// outputSchemaVersion.
const (
	schemaMaxItemsPerSection = 100
	schemaMaxDiagrams        = 10
	schemaMaxSourcePaths     = 200
	schemaMaxFields          = 200
	schemaMaxRelationships   = 100
	schemaMaxSteps           = 200

	schemaMaxTitle     = 200
	schemaMaxName      = 200
	schemaMaxType      = 200
	schemaMaxShortText = 2000
	schemaMaxLongText  = 4000
	schemaMaxPath      = 512

	schemaMaxDiagramKey    = 64
	schemaMaxDiagramLabel  = 200
	schemaMaxDiagramMember = 200

	schemaMaxFlowchartNodes    = 60
	schemaMaxFlowchartEdges    = 120
	schemaMaxSequenceParties   = 40
	schemaMaxSequenceMessages  = 120
	schemaMaxClassNodes        = 40
	schemaMaxClassMembers      = 60
	schemaMaxClassRelationship = 120
)

// Assembled limits are named independently from both fragment response limits and
// the complete wire schema. Merge policy may become stricter without changing either
// response contract.
const (
	assembledMaxArchitectureItems = schemaMaxItemsPerSection
	assembledMaxInterfaceItems    = schemaMaxItemsPerSection
	assembledMaxDataModelItems    = schemaMaxItemsPerSection
	assembledMaxWorkflowItems     = schemaMaxItemsPerSection
	assembledMaxDependencyItems   = schemaMaxItemsPerSection
	assembledMaxReviewGapItems    = schemaMaxItemsPerSection
	assembledMaxDiagrams          = schemaMaxDiagrams
	assembledMaxSourcePaths       = schemaMaxSourcePaths
	assembledMaxItemSourcePaths   = schemaMaxSourcePaths
)

// Fragment limits bound one model response independently from the larger assembled
// dossier contract. They are mirrored by codebase-summary/v2 fragment schemas.
const (
	fragmentMaxArchitectureItems = 2
	fragmentMaxInterfaceItems    = 2
	fragmentMaxDataModelItems    = 1
	fragmentMaxWorkflowItems     = 1
	fragmentMaxDependencyItems   = 2
	fragmentMaxReviewGapItems    = 2
	fragmentMaxDiagramItems      = 1
	fragmentMaxOmittedCount      = 10_000

	fragmentMaxSourcePaths   = 2
	fragmentMaxFields        = 3
	fragmentMaxRelationships = 2
	fragmentMaxSteps         = 5

	fragmentMaxTitle     = 100
	fragmentMaxName      = 100
	fragmentMaxType      = 100
	fragmentMaxShortText = 200
	fragmentMaxLongText  = 400
	fragmentMaxPath      = 512

	fragmentMaxDiagramKey    = 32
	fragmentMaxDiagramLabel  = 80
	fragmentMaxDiagramMember = 80

	fragmentMaxFlowchartNodes    = 6
	fragmentMaxFlowchartEdges    = 8
	fragmentMaxSequenceParties   = 5
	fragmentMaxSequenceMessages  = 8
	fragmentMaxClassNodes        = 4
	fragmentMaxClassMembers      = 3
	fragmentMaxClassRelationship = 5
)

type validationLimits struct {
	sourcePaths, fields, relationships, steps        int
	title, name, typeName, shortText, longText, path int
	diagramKey, diagramLabel, diagramMember          int
	flowchartNodes, flowchartEdges                   int
	sequenceParties, sequenceMessages                int
	classNodes, classMembers, classRelationships     int
}

var (
	dossierLimits = validationLimits{
		sourcePaths: schemaMaxSourcePaths, fields: schemaMaxFields, relationships: schemaMaxRelationships, steps: schemaMaxSteps,
		title: schemaMaxTitle, name: schemaMaxName, typeName: schemaMaxType, shortText: schemaMaxShortText, longText: schemaMaxLongText, path: schemaMaxPath,
		diagramKey: schemaMaxDiagramKey, diagramLabel: schemaMaxDiagramLabel, diagramMember: schemaMaxDiagramMember,
		flowchartNodes: schemaMaxFlowchartNodes, flowchartEdges: schemaMaxFlowchartEdges,
		sequenceParties: schemaMaxSequenceParties, sequenceMessages: schemaMaxSequenceMessages,
		classNodes: schemaMaxClassNodes, classMembers: schemaMaxClassMembers, classRelationships: schemaMaxClassRelationship,
	}
	fragmentLimits = validationLimits{
		sourcePaths: fragmentMaxSourcePaths, fields: fragmentMaxFields, relationships: fragmentMaxRelationships, steps: fragmentMaxSteps,
		title: fragmentMaxTitle, name: fragmentMaxName, typeName: fragmentMaxType, shortText: fragmentMaxShortText, longText: fragmentMaxLongText, path: fragmentMaxPath,
		diagramKey: fragmentMaxDiagramKey, diagramLabel: fragmentMaxDiagramLabel, diagramMember: fragmentMaxDiagramMember,
		flowchartNodes: fragmentMaxFlowchartNodes, flowchartEdges: fragmentMaxFlowchartEdges,
		sequenceParties: fragmentMaxSequenceParties, sequenceMessages: fragmentMaxSequenceMessages,
		classNodes: fragmentMaxClassNodes, classMembers: fragmentMaxClassMembers, classRelationships: fragmentMaxClassRelationship,
	}
)

func fragmentItemLimit(kind string) int {
	switch kind {
	case "architecture":
		return fragmentMaxArchitectureItems
	case "interfaces":
		return fragmentMaxInterfaceItems
	case "data_models":
		return fragmentMaxDataModelItems
	case "workflows":
		return fragmentMaxWorkflowItems
	case "dependencies":
		return fragmentMaxDependencyItems
	case "review_gaps":
		return fragmentMaxReviewGapItems
	case "diagrams":
		return fragmentMaxDiagramItems
	default:
		return 0
	}
}

// enumSet is a fixed set of allowed enum values with stable membership checks.
type enumSet map[string]struct{}

func newEnumSet(values ...string) enumSet {
	set := make(enumSet, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func (s enumSet) has(value string) bool {
	_, ok := s[value]
	return ok
}

var (
	interfaceKinds        = newEnumSet("http_api", "rpc", "function", "method", "interface", "event", "command", "schema", "configuration", "other")
	interfaceDirections   = newEnumSet("inbound", "outbound", "internal")
	dataModelKinds        = newEnumSet("entity", "value_object", "request", "response", "event", "schema", "table", "configuration", "other")
	dataRelationshipKinds = newEnumSet("contains", "references", "inherits", "implements", "reads", "writes", "other")
	dependencyKinds       = newEnumSet("internal_component", "external_service", "library", "tool", "protocol", "other")
	reviewGapKinds        = newEnumSet("uncertainty", "missing_context", "inconsistency", "unsupported_construct", "excluded_input", "other")
	classRelationKinds    = newEnumSet("association", "aggregation", "composition", "inheritance", "realization", "dependency")
)
