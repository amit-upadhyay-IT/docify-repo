package usecase

import (
	"encoding/json"
	"strings"
	"testing"

	sharedmodel "docify-repo/internal/model"
)

func TestValidateFragmentAcceptsEveryContract(t *testing.T) {
	for _, kind := range sharedmodel.FragmentKinds() {
		t.Run(string(kind), func(t *testing.T) {
			result := validateFragment(kind, validFragmentJSON(t, kind), evidence(), catalog())
			if !result.valid() {
				t.Fatalf("issues = %+v, want valid fragment", result.issues)
			}
			if len(result.evidenceUsed) != 1 || result.evidenceUsed[0] != evidence()[0] {
				t.Fatalf("evidenceUsed = %v, want exact cited evidence", result.evidenceUsed)
			}
		})
	}
}

func TestValidateFragmentRejectsUnknownFieldsForEveryContract(t *testing.T) {
	for _, kind := range sharedmodel.FragmentKinds() {
		t.Run(string(kind), func(t *testing.T) {
			body := validFragmentJSON(t, kind)
			body = append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"attack":true}`)...)
			result := validateFragment(kind, body, evidence(), catalog())
			if !hasFragmentIssue(result, issueUnknownField) {
				t.Fatalf("issues = %+v, want unknown_field", result.issues)
			}
		})
	}
}

func TestValidateFragmentRejectsContractViolations(t *testing.T) {
	tests := []struct {
		name string
		kind sharedmodel.FragmentKind
		body string
		code string
	}{
		{"missing completeness", sharedmodel.FragmentArchitecture, `{"items":[]}`, issueMissingField},
		{"missing items", sharedmodel.FragmentArchitecture, `{"complete":true}`, issueMissingField},
		{"null completeness", sharedmodel.FragmentArchitecture, `{"complete":null,"items":[]}`, issueInvalidType},
		{"null items", sharedmodel.FragmentArchitecture, `{"complete":true,"items":null}`, issueInvalidType},
		{"invalid interface enum", sharedmodel.FragmentInterfaces, `{"complete":true,"items":[{"name":"API","kind":"teleport","direction":"inbound","description":"Entry point.","source_paths":["services/payments/service.go"]}]}`, issueInvalidEnum},
		{"unknown dependency component", sharedmodel.FragmentDependencies, `{"complete":true,"items":[{"name":"other","kind":"internal_component","purpose":"Calls another component.","component_key":"services/missing","source_paths":["services/payments/service.go"]}]}`, issueUnknownComponent},
		{"unknown evidence", sharedmodel.FragmentArchitecture, `{"complete":true,"items":[{"title":"Boundary","description":"Owns processing.","source_paths":["services/other/secret.go"]}]}`, issueUnknownEvidence},
		{"unsafe prose", sharedmodel.FragmentReviewGaps, `{"complete":true,"items":[{"kind":"uncertainty","description":"See [details](https://example.com).","recommendation":"Inspect evidence.","source_paths":[]}]}`, issueUnsafeProse},
		{"oversize nested list", sharedmodel.FragmentDataModels, `{"complete":true,"items":[{"name":"Record","kind":"entity","description":"Stores data.","fields":[{"name":"a","type":"string","description":"A."},{"name":"b","type":"string","description":"B."},{"name":"c","type":"string","description":"C."},{"name":"d","type":"string","description":"D."}],"relationships":[],"source_paths":["services/payments/service.go"]}]}`, issueTooManyItems},
		{"missing nested array", sharedmodel.FragmentDataModels, `{"complete":true,"items":[{"name":"Record","kind":"entity","description":"Stores data.","relationships":[],"source_paths":["services/payments/service.go"]}]}`, issueMissingField},
		{"missing nullable target", sharedmodel.FragmentWorkflows, `{"complete":true,"items":[{"name":"Flow","description":"Runs work.","steps":[{"actor":"Caller","action":"Starts work."}],"source_paths":["services/payments/service.go"]}]}`, issueMissingField},
		{"missing nullable component", sharedmodel.FragmentDependencies, `{"complete":true,"items":[{"name":"library","kind":"library","purpose":"Provides helpers.","source_paths":["services/payments/service.go"]}]}`, issueMissingField},
		{"empty cross-type diagram field", sharedmodel.FragmentDiagrams, `{"complete":true,"items":[{"type":"flowchart","title":"Flow","nodes":[],"edges":[],"messages":[],"source_paths":["services/payments/service.go"]}]}`, issueDiagramFieldUnset},
		{"missing sequence response", sharedmodel.FragmentDiagrams, `{"complete":true,"items":[{"type":"sequence","title":"Flow","participants":[{"key":"a","label":"Caller"},{"key":"b","label":"Service"}],"messages":[{"from":"a","to":"b","label":"Calls"}],"source_paths":["services/payments/service.go"]}]}`, issueMissingField},
		{"null sequence response", sharedmodel.FragmentDiagrams, `{"complete":true,"items":[{"type":"sequence","title":"Flow","participants":[{"key":"a","label":"Caller"},{"key":"b","label":"Service"}],"messages":[{"from":"a","to":"b","label":"Calls","response":null}],"source_paths":["services/payments/service.go"]}]}`, issueInvalidType},
		{"invalid omitted count", sharedmodel.FragmentWorkflows, `{"complete":true,"omitted_count":10001,"items":[]}`, issueInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validateFragment(test.kind, []byte(test.body), evidence(), catalog())
			if !hasFragmentIssue(result, test.code) {
				t.Fatalf("issues = %+v, want %s", result.issues, test.code)
			}
		})
	}
}

func TestValidateFragmentRejectsOversizeBodyBeforeDecoding(t *testing.T) {
	body := []byte(`{"complete":true,"items":[],"padding":"` + strings.Repeat("x", fragmentResponseBytes) + `"}`)
	result := validateFragment(sharedmodel.FragmentArchitecture, body, evidence(), catalog())
	if !hasFragmentIssue(result, issueResponseTooLarge) || result.repairable {
		t.Fatalf("result = %+v, want non-repairable response_too_large", result)
	}
}

func TestFragmentWireLimitBoundsSchemaValidEscapedRepresentation(t *testing.T) {
	escapedTitle := strings.Repeat(`\u0061`, fragmentMaxTitle)
	escapedDescription := strings.Repeat(`\u0061`, fragmentMaxLongText)
	escapedPath := strings.Repeat(`\u0061`, fragmentMaxPath)
	body := []byte(`{"complete":true,"items":[{"title":"` + escapedTitle + `","description":"` + escapedDescription + `","source_paths":["` + escapedPath + `","` + escapedPath + `"]}]}`)
	if len(body) <= fragmentResponseBytes {
		t.Fatalf("escaped fixture = %d bytes, want it above the wire limit", len(body))
	}
	result := validateFragment(sharedmodel.FragmentArchitecture, body, nil, nil)
	if !hasFragmentIssue(result, issueResponseTooLarge) || result.repairable {
		t.Fatalf("result = %+v, want bounded non-repairable rejection", result)
	}
}

func TestValidateFragmentRejectsFragmentTextCeiling(t *testing.T) {
	fragment := sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{{
		Title: "Boundary", Description: strings.Repeat("x", fragmentMaxLongText+1), SourcePaths: evidence(),
	}}}
	result := validateFragment(sharedmodel.FragmentArchitecture, marshalFragment(t, fragment), evidence(), catalog())
	if !hasFragmentIssue(result, issueStringTooLong) {
		t.Fatalf("issues = %+v, want string_too_long", result.issues)
	}
}

func TestFragmentSaturationRequiresSmallerScope(t *testing.T) {
	incomplete := validateFragment(sharedmodel.FragmentArchitecture, []byte(`{"complete":false,"items":[]}`), evidence(), catalog())
	if !incomplete.valid() || !incomplete.saturated {
		t.Fatalf("incomplete result = %+v, want valid saturation", incomplete)
	}

	atCap := sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{
		{Title: "Boundary one", Description: "Owns one responsibility.", SourcePaths: evidence()},
		{Title: "Boundary two", Description: "Owns another responsibility.", SourcePaths: evidence()},
	}}
	atCapResult := validateFragment(sharedmodel.FragmentArchitecture, marshalFragment(t, atCap), evidence(), catalog())
	if !atCapResult.valid() || !atCapResult.saturated || atCapResult.itemCount != fragmentMaxArchitectureItems {
		t.Fatalf("at-cap result = %+v, want valid saturation", atCapResult)
	}

	belowCap := validateFragment(sharedmodel.FragmentArchitecture, []byte(`{"complete":true,"items":[]}`), evidence(), catalog())
	if !belowCap.valid() || belowCap.saturated {
		t.Fatalf("below-cap result = %+v, want complete unsaturated fragment", belowCap)
	}

	contradictory := validateFragment(sharedmodel.FragmentArchitecture, []byte(`{"complete":true,"omitted_count":1,"items":[]}`), evidence(), catalog())
	if contradictory.valid() || !hasIssueCode(contradictory.issues, issueInvalidValue) {
		t.Fatalf("contradictory completeness result = %+v, want rejection", contradictory)
	}
}

func TestFragmentValidationIssuesNeverContainModelProse(t *testing.T) {
	secretField := "secret-model-field-ABC123"
	body := []byte(`{"complete":true,"items":[],"` + secretField + `":true}`)
	result := validateFragment(sharedmodel.FragmentArchitecture, body, evidence(), catalog())
	for _, issue := range result.issues {
		if strings.Contains(issue.Message, secretField) {
			t.Fatalf("validation issue leaked model prose: %+v", issue)
		}
	}
}

func TestOverviewReductionValidationIsStrictAndSealed(t *testing.T) {
	component := sharedmodel.Component{Key: "services/api"}
	valid := validateOverviewReduction(component, []byte(`{"title":"API","purpose":"Documents the API."}`))
	if !valid.valid() {
		t.Fatalf("valid overview issues = %+v", valid.issues)
	}
	trusted, err := valid.revalidateSealed(component)
	if err != nil || trusted.value.Title != "API" {
		t.Fatalf("revalidateSealed() = %+v, %v", trusted, err)
	}
	unknown := validateOverviewReduction(component, []byte(`{"title":"API","purpose":"Documents the API.","source_paths":[]}`))
	if unknown.valid() || !hasIssueCode(unknown.issues, issueUnknownField) {
		t.Fatalf("unknown-field overview issues = %+v", unknown.issues)
	}
	unsafe := validateOverviewReduction(component, []byte(`{"title":"API","purpose":"[unsafe](https://example.test)"}`))
	if unsafe.valid() {
		t.Fatal("unsafe overview reducer prose was accepted")
	}
}

func hasIssueCode(issues []sharedmodel.ValidationIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func validFragmentJSON(t *testing.T, kind sharedmodel.FragmentKind) []byte {
	t.Helper()
	path := evidence()
	switch kind {
	case sharedmodel.FragmentOverviewCandidate:
		return marshalFragment(t, sharedmodel.OverviewCandidate{Title: "Payments", Purpose: "Processes payment intents.", SourcePaths: path})
	case sharedmodel.FragmentArchitecture:
		return marshalFragment(t, sharedmodel.ArchitectureFragment{Complete: true, Items: []sharedmodel.ArchitectureItem{{Title: "Command boundary", Description: "Owns payment processing.", SourcePaths: path}}})
	case sharedmodel.FragmentInterfaces:
		return marshalFragment(t, sharedmodel.InterfacesFragment{Complete: true, Items: []sharedmodel.InterfaceItem{{Name: "CreateIntent", Kind: "function", Direction: "inbound", Description: "Creates an intent.", SourcePaths: path}}})
	case sharedmodel.FragmentDataModels:
		return marshalFragment(t, sharedmodel.DataModelsFragment{Complete: true, Items: []sharedmodel.DataModelItem{{Name: "Intent", Kind: "entity", Description: "Stores payment state.", Fields: []sharedmodel.DataField{{Name: "ID", Type: "string", Description: "Identifies the intent."}}, Relationships: []sharedmodel.DataRelationship{{Target: "Account", Kind: "references", Description: "Associates an account."}}, SourcePaths: path}}})
	case sharedmodel.FragmentWorkflows:
		return marshalFragment(t, sharedmodel.WorkflowsFragment{Complete: true, Items: []sharedmodel.WorkflowItem{{Name: "Create payment", Description: "Creates a payment intent.", Steps: []sharedmodel.WorkflowStep{{Actor: "Caller", Action: "Submits an intent.", Target: "Service"}}, SourcePaths: path}}})
	case sharedmodel.FragmentDependencies:
		return marshalFragment(t, sharedmodel.DependenciesFragment{Complete: true, Items: []sharedmodel.DependencyItem{{Name: "billing", Kind: "internal_component", Purpose: "Reads billing accounts.", ComponentKey: "services/billing", SourcePaths: path}}})
	case sharedmodel.FragmentReviewGaps:
		return marshalFragment(t, sharedmodel.ReviewGapsFragment{Complete: true, Items: []sharedmodel.ReviewGap{{Kind: "missing_context", Description: "Settlement behavior is not visible.", Recommendation: "Inspect the settlement owner.", SourcePaths: path}}})
	case sharedmodel.FragmentDiagrams:
		return marshalFragment(t, sharedmodel.DiagramsFragment{Complete: true, Items: []sharedmodel.Diagram{{Type: sharedmodel.DiagramFlowchart, Title: "Intent flow", SourcePaths: path, Nodes: []sharedmodel.FlowchartNode{{Key: "create", Label: "Create"}, {Key: "settle", Label: "Settle"}}, Edges: []sharedmodel.FlowchartEdge{{From: "create", To: "settle", Label: "continues"}}}}})
	default:
		t.Fatalf("unsupported fixture kind %q", kind)
		return nil
	}
}

func marshalFragment(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fragment: %v", err)
	}
	return body
}

func hasFragmentIssue(result fragmentValidation, code string) bool {
	for _, issue := range result.issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
