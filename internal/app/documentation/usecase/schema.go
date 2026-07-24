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
