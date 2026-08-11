package usecase

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

func TestFragmentSchemasMirrorLocalLimits(t *testing.T) {
	tests := []struct {
		kind sharedmodel.FragmentKind
		path []string
		want int
	}{
		{sharedmodel.FragmentOverviewCandidate, []string{"properties", "title", "maxLength"}, fragmentMaxTitle},
		{sharedmodel.FragmentOverviewCandidate, []string{"properties", "purpose", "maxLength"}, fragmentMaxLongText},
		{sharedmodel.FragmentOverviewCandidate, []string{"properties", "source_paths", "maxItems"}, fragmentMaxSourcePaths},
		{sharedmodel.FragmentOverviewCandidate, []string{"properties", "source_paths", "items", "maxLength"}, fragmentMaxPath},
		{sharedmodel.FragmentArchitecture, []string{"properties", "omitted_count", "maximum"}, fragmentMaxOmittedCount},
		{sharedmodel.FragmentArchitecture, []string{"properties", "items", "maxItems"}, fragmentMaxArchitectureItems},
		{sharedmodel.FragmentArchitecture, []string{"properties", "items", "items", "properties", "description", "maxLength"}, fragmentMaxLongText},
		{sharedmodel.FragmentArchitecture, []string{"properties", "items", "items", "properties", "source_paths", "items", "maxLength"}, fragmentMaxPath},
		{sharedmodel.FragmentInterfaces, []string{"properties", "omitted_count", "maximum"}, fragmentMaxOmittedCount},
		{sharedmodel.FragmentInterfaces, []string{"properties", "items", "maxItems"}, fragmentMaxInterfaceItems},
		{sharedmodel.FragmentInterfaces, []string{"properties", "items", "items", "properties", "name", "maxLength"}, fragmentMaxName},
		{sharedmodel.FragmentInterfaces, []string{"properties", "items", "items", "properties", "source_paths", "maxItems"}, fragmentMaxSourcePaths},
		{sharedmodel.FragmentDataModels, []string{"properties", "omitted_count", "maximum"}, fragmentMaxOmittedCount},
		{sharedmodel.FragmentDataModels, []string{"properties", "items", "maxItems"}, fragmentMaxDataModelItems},
		{sharedmodel.FragmentDataModels, []string{"properties", "items", "items", "properties", "fields", "maxItems"}, fragmentMaxFields},
		{sharedmodel.FragmentDataModels, []string{"properties", "items", "items", "properties", "relationships", "maxItems"}, fragmentMaxRelationships},
		{sharedmodel.FragmentDataModels, []string{"properties", "items", "items", "properties", "fields", "items", "properties", "type", "maxLength"}, fragmentMaxType},
		{sharedmodel.FragmentDataModels, []string{"properties", "items", "items", "properties", "fields", "items", "properties", "description", "maxLength"}, fragmentMaxShortText},
		{sharedmodel.FragmentDataModels, []string{"properties", "items", "items", "properties", "source_paths", "maxItems"}, fragmentMaxSourcePaths},
		{sharedmodel.FragmentWorkflows, []string{"properties", "omitted_count", "maximum"}, fragmentMaxOmittedCount},
		{sharedmodel.FragmentWorkflows, []string{"properties", "items", "maxItems"}, fragmentMaxWorkflowItems},
		{sharedmodel.FragmentWorkflows, []string{"properties", "items", "items", "properties", "steps", "maxItems"}, fragmentMaxSteps},
		{sharedmodel.FragmentWorkflows, []string{"properties", "items", "items", "properties", "steps", "items", "properties", "action", "maxLength"}, fragmentMaxShortText},
		{sharedmodel.FragmentDependencies, []string{"properties", "omitted_count", "maximum"}, fragmentMaxOmittedCount},
		{sharedmodel.FragmentDependencies, []string{"properties", "items", "maxItems"}, fragmentMaxDependencyItems},
		{sharedmodel.FragmentDependencies, []string{"properties", "items", "items", "properties", "component_key", "maxLength"}, fragmentMaxPath},
		{sharedmodel.FragmentReviewGaps, []string{"properties", "omitted_count", "maximum"}, fragmentMaxOmittedCount},
		{sharedmodel.FragmentReviewGaps, []string{"properties", "items", "maxItems"}, fragmentMaxReviewGapItems},
		{sharedmodel.FragmentReviewGaps, []string{"properties", "items", "items", "properties", "recommendation", "maxLength"}, fragmentMaxShortText},
		{sharedmodel.FragmentDiagrams, []string{"properties", "omitted_count", "maximum"}, fragmentMaxOmittedCount},
		{sharedmodel.FragmentDiagrams, []string{"properties", "items", "maxItems"}, fragmentMaxDiagramItems},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "source_paths", "maxItems"}, fragmentMaxSourcePaths},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "source_paths", "items", "maxLength"}, fragmentMaxPath},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "flowchart", "properties", "nodes", "maxItems"}, fragmentMaxFlowchartNodes},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "flowchart", "properties", "edges", "maxItems"}, fragmentMaxFlowchartEdges},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "sequence", "properties", "participants", "maxItems"}, fragmentMaxSequenceParties},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "sequence", "properties", "messages", "maxItems"}, fragmentMaxSequenceMessages},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "class_diagram", "properties", "classes", "maxItems"}, fragmentMaxClassNodes},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "class_diagram", "properties", "classes", "items", "properties", "members", "maxItems"}, fragmentMaxClassMembers},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "class_diagram", "properties", "relationships", "maxItems"}, fragmentMaxClassRelationship},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "class_diagram", "properties", "classes", "items", "properties", "key", "maxLength"}, fragmentMaxDiagramKey},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "class_diagram", "properties", "classes", "items", "properties", "label", "maxLength"}, fragmentMaxDiagramLabel},
		{sharedmodel.FragmentDiagrams, []string{"$defs", "class_diagram", "properties", "classes", "items", "properties", "members", "items", "maxLength"}, fragmentMaxDiagramMember},
	}
	bundle := prompt.CodebaseSummaryV2()
	for _, test := range tests {
		t.Run(string(test.kind)+"/"+strings.Join(test.path, "/"), func(t *testing.T) {
			schema, _ := bundle.FragmentSchema(test.kind)
			if got := schemaInteger(t, schema, test.path...); got != test.want {
				t.Fatalf("schema value = %d, local limit = %d", got, test.want)
			}
		})
	}
}

func TestWorstCaseFragmentsFitMinimumProviderProfile(t *testing.T) {
	profile, err := boundedFragmentProfile()
	if err != nil {
		t.Fatalf("boundedFragmentProfile() error = %v", err)
	}
	if profile.Version != fragmentProfileVersion || profile.MinimumOutputTokens != 8_192 || profile.MinimumResponseBytes != 8_192 {
		t.Fatalf("profile = %+v", profile)
	}
	for _, kind := range sharedmodel.FragmentKinds() {
		t.Run(string(kind), func(t *testing.T) {
			body, err := maximumCanonicalFragment(kind)
			if err != nil {
				t.Fatalf("maximumCanonicalFragment() error = %v", err)
			}
			if len(body) > profile.MinimumOutputTokens/fragmentOutputBytesPerToken {
				t.Fatalf("maximum canonical body = %d bytes, exceeds conservative %d-token budget", len(body), profile.MinimumOutputTokens)
			}
			if int64(len(body)) > profile.MinimumResponseBytes {
				t.Fatalf("maximum canonical body = %d bytes, exceeds %d-byte response profile", len(body), profile.MinimumResponseBytes)
			}
		})
	}
	if profile.MaximumRepairRequestBytes > 500_000 {
		t.Fatalf("worst repair request = %d bytes, exceeds default request limit", profile.MaximumRepairRequestBytes)
	}
}

func TestFragmentProfileRejectsInsufficientConfiguration(t *testing.T) {
	profile, err := boundedFragmentProfile()
	if err != nil {
		t.Fatalf("boundedFragmentProfile() error = %v", err)
	}
	if err := validateFragmentProfile(profile.MinimumOutputTokens, profile.MinimumResponseBytes, 500_000); err != nil {
		t.Fatalf("minimum supported profile rejected: %v", err)
	}
	if err := validateFragmentProfile(profile.MinimumOutputTokens-1, profile.MinimumResponseBytes, 500_000); err == nil || !strings.Contains(err.Error(), "output tokens") {
		t.Fatalf("low output-token error = %v", err)
	}
	if err := validateFragmentProfile(profile.MinimumOutputTokens, profile.MinimumResponseBytes-1, 500_000); err == nil || !strings.Contains(err.Error(), "response bytes") {
		t.Fatalf("low response-byte error = %v", err)
	}
	if err := validateFragmentProfile(profile.MinimumOutputTokens, profile.MinimumResponseBytes, profile.MaximumRepairRequestBytes-1); err == nil || !strings.Contains(err.Error(), "repair envelope") {
		t.Fatalf("low request-byte error = %v", err)
	}
}

func TestWorstCaseFragmentRepairEnvelopesFitDefaultRequestLimit(t *testing.T) {
	bundle := prompt.CodebaseSummaryV2()
	for _, kind := range sharedmodel.FragmentKinds() {
		t.Run(string(kind), func(t *testing.T) {
			worst, err := fragmentRepairWorstCaseBytes(bundle, kind)
			if err != nil {
				t.Fatalf("fragmentRepairWorstCaseBytes() error = %v", err)
			}
			if worst > 500_000 {
				t.Fatalf("worst repair envelope = %d bytes", worst)
			}

			component := sharedmodel.Component{Key: "services/api"}
			settings := sharedmodel.GenerationSettings{MaxOutputTokens: fragmentMinimumOutputTokens}
			original, err := buildFragmentRequest(bundle, settings, component, kind, nil, nil, nil, nil, nil, "full", 1, 1, 1, 1, 65_536, 500_000)
			if err != nil {
				t.Fatalf("buildFragmentRequest() error = %v", err)
			}
			validCandidate := append([]byte{'"'}, bytes.Repeat([]byte{'x'}, fragmentResponseBytes-2)...)
			validCandidate = append(validCandidate, '"')
			validRepair, err := buildFragmentRepairRequest(bundle, original, validCandidate, nil, 65_536, 500_000)
			if err != nil {
				t.Fatalf("build valid-candidate repair: %v", err)
			}
			if size := requestContentBytes(validRepair); size > 500_000 {
				t.Fatalf("valid candidate repair = %d bytes", size)
			}
		})
	}
}

func TestFragmentPrimaryRequestUsesExactRequestLimit(t *testing.T) {
	component := sharedmodel.Component{
		Key: "services/api",
		TriggeringFiles: []sharedmodel.SourceFile{
			planSource("services/api/a.go", sharedmodel.RoleProductionSource, true, strings.Repeat("\\", 1_000)),
		},
	}
	request, err := buildFragmentRequest(
		prompt.CodebaseSummaryV2(), sharedmodel.GenerationSettings{MaxOutputTokens: fragmentMinimumOutputTokens}, component, sharedmodel.FragmentArchitecture,
		component.TriggeringFiles, nil, nil, []string{"services/api"}, nil, "full", 1, 1, 1, 1, 65_536, 500_000,
	)
	if err != nil {
		t.Fatalf("buildFragmentRequest() error = %v", err)
	}
	size := requestContentBytes(request)
	if err := validateFragmentRequestSize(request, size); err != nil {
		t.Fatalf("exact request limit rejected: %v", err)
	}
	if err := validateFragmentRequestSize(request, size-1); err == nil {
		t.Fatal("request exceeding the exact byte limit was accepted")
	}
}

func TestFragmentPrimaryProvesItsActualRepairEnvelope(t *testing.T) {
	profile, err := boundedFragmentProfile()
	if err != nil {
		t.Fatalf("boundedFragmentProfile() error = %v", err)
	}
	component := sharedmodel.Component{Key: "services/api"}
	var settings sharedmodel.GenerationSettings
	found := false
	for length := 1_000; length <= 50_000; length += 1_000 {
		candidate := sharedmodel.GenerationSettings{Model: strings.Repeat("\x00", length), MaxOutputTokens: fragmentMinimumOutputTokens}
		request, buildErr := buildFragmentRequestUnchecked(prompt.CodebaseSummaryV2(), candidate, component, sharedmodel.FragmentArchitecture, nil, nil, nil, []string{"services/api"}, nil, "full", 1, 1, 1, 1)
		if buildErr != nil {
			t.Fatalf("build unchecked request: %v", buildErr)
		}
		repairBytes, repairErr := fragmentRepairWorstCaseForRequest(prompt.CodebaseSummaryV2(), request)
		if repairErr != nil {
			t.Fatalf("calculate actual repair: %v", repairErr)
		}
		if requestContentBytes(request) <= profile.MaximumRepairRequestBytes && repairBytes > profile.MaximumRepairRequestBytes {
			settings = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("could not construct a request whose primary fits but actual repair envelope does not")
	}
	_, err = buildFragmentRequest(
		prompt.CodebaseSummaryV2(), settings, component, sharedmodel.FragmentArchitecture,
		nil, nil, nil, []string{"services/api"}, nil, "full", 1, 1, 1, 1,
		65_536, profile.MaximumRepairRequestBytes,
	)
	if err == nil || !strings.Contains(err.Error(), "worst-case repair request") {
		t.Fatalf("error = %v, want pre-call actual repair-envelope rejection", err)
	}
}

func schemaInteger(t *testing.T, schema []byte, path ...string) int {
	t.Helper()
	var current any
	if err := json.Unmarshal(schema, &current); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("schema path %v does not reach an object at %q", path, segment)
		}
		current, ok = object[segment]
		if !ok {
			t.Fatalf("schema path %v is missing %q", path, segment)
		}
	}
	value, ok := current.(float64)
	if !ok {
		t.Fatalf("schema path %v value = %T, want number", path, current)
	}
	return int(value)
}
