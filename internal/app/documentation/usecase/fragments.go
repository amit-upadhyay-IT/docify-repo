package usecase

import (
	"bytes"
	"fmt"

	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
)

const (
	fragmentProfileVersion          = "v1"
	fragmentMinimumOutputTokens     = 8_192
	fragmentMinimumResponseBytes    = fragmentResponseBytes
	fragmentOutputBytesPerToken     = 1
	fragmentRepairIssueCodeBytes    = 32
	fragmentRepairIssuePathBytes    = 128
	fragmentRepairIssueMessageBytes = 160
)

type fragmentProfile struct {
	Version                   string
	MinimumOutputTokens       int
	MinimumResponseBytes      int64
	MaximumCanonicalBytes     int64
	MaximumRepairRequestBytes int64
}

// boundedFragmentProfile calculates the versioned provider requirements from the
// actual prompt, schema, model, and repair envelopes. One canonical output byte per
// token is intentionally conservative and independent of request-token estimation.
func boundedFragmentProfile() (fragmentProfile, error) {
	bundle := prompt.CodebaseSummaryV2()
	profile := fragmentProfile{
		Version: fragmentProfileVersion, MinimumOutputTokens: fragmentMinimumOutputTokens,
		MinimumResponseBytes: fragmentMinimumResponseBytes,
	}
	for _, kind := range sharedmodel.FragmentKinds() {
		body, err := maximumCanonicalFragment(kind)
		if err != nil {
			return fragmentProfile{}, err
		}
		if int64(len(body)) > profile.MaximumCanonicalBytes {
			profile.MaximumCanonicalBytes = int64(len(body))
		}
		repairBytes, err := fragmentRepairWorstCaseBytes(bundle, kind)
		if err != nil {
			return fragmentProfile{}, err
		}
		if repairBytes > profile.MaximumRepairRequestBytes {
			profile.MaximumRepairRequestBytes = repairBytes
		}
	}
	if profile.MaximumCanonicalBytes*fragmentOutputBytesPerToken > int64(profile.MinimumOutputTokens) {
		return fragmentProfile{}, fmt.Errorf("fragment profile requires %d canonical output bytes but supports %d output tokens", profile.MaximumCanonicalBytes, profile.MinimumOutputTokens)
	}
	if profile.MaximumCanonicalBytes > profile.MinimumResponseBytes {
		return fragmentProfile{}, fmt.Errorf("fragment profile requires %d response bytes but supports %d", profile.MaximumCanonicalBytes, profile.MinimumResponseBytes)
	}
	return profile, nil
}

func validateFragmentProfile(maxOutputTokens int, maxResponseBytes, maxRequestBytes int64) error {
	profile, err := boundedFragmentProfile()
	if err != nil {
		return err
	}
	if maxOutputTokens < profile.MinimumOutputTokens {
		return fmt.Errorf("fragment profile %s requires at least %d output tokens", profile.Version, profile.MinimumOutputTokens)
	}
	if maxResponseBytes < profile.MinimumResponseBytes {
		return fmt.Errorf("fragment profile %s requires at least %d response bytes", profile.Version, profile.MinimumResponseBytes)
	}
	if maxRequestBytes < profile.MaximumRepairRequestBytes {
		return fmt.Errorf("fragment profile %s requires at least %d request bytes for the worst-case repair envelope", profile.Version, profile.MaximumRepairRequestBytes)
	}
	return nil
}

func validateFragmentRequestSize(request sharedmodel.GenerationRequest, maxRequestBytes int64) error {
	if size := requestContentBytes(request); size > maxRequestBytes {
		return fmt.Errorf("component %q %s fragment request is %d bytes, exceeding the %d-byte request limit", request.ComponentKey, request.FragmentKind, size, maxRequestBytes)
	}
	return nil
}

func fragmentRepairWorstCaseBytes(bundle prompt.FragmentBundle, kind sharedmodel.FragmentKind) (int64, error) {
	component := sharedmodel.Component{Key: repeated(fragmentMaxPath)}
	original, err := buildFragmentRequestUnchecked(bundle, sharedmodel.GenerationSettings{MaxOutputTokens: fragmentMinimumOutputTokens}, component, kind, nil, nil, nil, nil, nil, "full", 1, 1, 1, 1)
	if err != nil {
		return 0, err
	}
	return fragmentRepairWorstCaseForRequest(bundle, original)
}

func fragmentRepairWorstCaseForRequest(bundle prompt.FragmentBundle, original sharedmodel.GenerationRequest) (int64, error) {
	issues := make([]sharedmodel.ValidationIssue, maxValidationIssues)
	for index := range issues {
		issues[index] = sharedmodel.ValidationIssue{
			Code: maximumJSONExpansionString(fragmentRepairIssueCodeBytes), Path: maximumJSONExpansionString(fragmentRepairIssuePathBytes),
			Message: maximumJSONExpansionString(fragmentRepairIssueMessageBytes),
		}
	}
	// NUL is valid UTF-8 but invalid raw JSON and expands to six bytes per byte when
	// safely embedded as a JSON string, making this the conservative repair case.
	repair, err := buildFragmentRepairRequestUnchecked(bundle, original, bytes.Repeat([]byte{0}, fragmentResponseBytes), issues)
	if err != nil {
		return 0, err
	}
	return requestContentBytes(repair), nil
}

func maximumCanonicalFragment(kind sharedmodel.FragmentKind) ([]byte, error) {
	switch kind {
	case sharedmodel.FragmentOverviewCandidate:
		return marshalRequestJSON(sharedmodel.OverviewCandidate{
			Title: repeated(fragmentMaxTitle), Purpose: repeated(fragmentMaxLongText), SourcePaths: maximumPaths(),
		})
	case sharedmodel.FragmentArchitecture:
		items := make([]sharedmodel.ArchitectureItem, fragmentMaxArchitectureItems)
		for index := range items {
			items[index] = sharedmodel.ArchitectureItem{Title: repeated(fragmentMaxTitle), Description: repeated(fragmentMaxLongText), SourcePaths: maximumPaths()}
		}
		return marshalRequestJSON(sharedmodel.ArchitectureFragment{Complete: false, OmittedCount: maximumOmittedCount(), Items: items})
	case sharedmodel.FragmentInterfaces:
		items := make([]sharedmodel.InterfaceItem, fragmentMaxInterfaceItems)
		for index := range items {
			items[index] = sharedmodel.InterfaceItem{Name: repeated(fragmentMaxName), Kind: "configuration", Direction: "internal", Description: repeated(fragmentMaxLongText), SourcePaths: maximumPaths()}
		}
		return marshalRequestJSON(sharedmodel.InterfacesFragment{Complete: false, OmittedCount: maximumOmittedCount(), Items: items})
	case sharedmodel.FragmentDataModels:
		fields := make([]sharedmodel.DataField, fragmentMaxFields)
		for index := range fields {
			fields[index] = sharedmodel.DataField{Name: repeated(fragmentMaxName), Type: repeated(fragmentMaxType), Description: repeated(fragmentMaxShortText)}
		}
		relationships := make([]sharedmodel.DataRelationship, fragmentMaxRelationships)
		for index := range relationships {
			relationships[index] = sharedmodel.DataRelationship{Target: repeated(fragmentMaxName), Kind: "implements", Description: repeated(fragmentMaxShortText)}
		}
		items := []sharedmodel.DataModelItem{{Name: repeated(fragmentMaxName), Kind: "configuration", Description: repeated(fragmentMaxLongText), Fields: fields, Relationships: relationships, SourcePaths: maximumPaths()}}
		return marshalRequestJSON(sharedmodel.DataModelsFragment{Complete: false, OmittedCount: maximumOmittedCount(), Items: items})
	case sharedmodel.FragmentWorkflows:
		steps := make([]sharedmodel.WorkflowStep, fragmentMaxSteps)
		for index := range steps {
			steps[index] = sharedmodel.WorkflowStep{Actor: repeated(fragmentMaxName), Action: repeated(fragmentMaxShortText), Target: repeated(fragmentMaxName)}
		}
		items := []sharedmodel.WorkflowItem{{Name: repeated(fragmentMaxName), Description: repeated(fragmentMaxLongText), Steps: steps, SourcePaths: maximumPaths()}}
		return marshalRequestJSON(sharedmodel.WorkflowsFragment{Complete: false, OmittedCount: maximumOmittedCount(), Items: items})
	case sharedmodel.FragmentDependencies:
		items := make([]sharedmodel.DependencyItem, fragmentMaxDependencyItems)
		for index := range items {
			items[index] = sharedmodel.DependencyItem{Name: repeated(fragmentMaxName), Kind: "internal_component", Purpose: repeated(fragmentMaxShortText), ComponentKey: repeated(fragmentMaxPath), SourcePaths: maximumPaths()}
		}
		return marshalRequestJSON(sharedmodel.DependenciesFragment{Complete: false, OmittedCount: maximumOmittedCount(), Items: items})
	case sharedmodel.FragmentReviewGaps:
		items := make([]sharedmodel.ReviewGap, fragmentMaxReviewGapItems)
		for index := range items {
			items[index] = sharedmodel.ReviewGap{Kind: "unsupported_construct", Description: repeated(fragmentMaxLongText), Recommendation: repeated(fragmentMaxShortText), SourcePaths: maximumPaths()}
		}
		return marshalRequestJSON(sharedmodel.ReviewGapsFragment{Complete: false, OmittedCount: maximumOmittedCount(), Items: items})
	case sharedmodel.FragmentDiagrams:
		return maximumDiagramFragment()
	default:
		return nil, fmt.Errorf("unsupported fragment kind %q", kind)
	}
}

func maximumDiagramFragment() ([]byte, error) {
	candidates := []sharedmodel.Diagram{maximumFlowchart(), maximumSequence(), maximumClassDiagram()}
	var largest []byte
	for _, diagram := range candidates {
		body, err := marshalRequestJSON(sharedmodel.DiagramsFragment{Complete: false, OmittedCount: maximumOmittedCount(), Items: []sharedmodel.Diagram{diagram}})
		if err != nil {
			return nil, err
		}
		if len(body) > len(largest) {
			largest = body
		}
	}
	return largest, nil
}

func maximumFlowchart() sharedmodel.Diagram {
	nodes := make([]sharedmodel.FlowchartNode, fragmentMaxFlowchartNodes)
	for index := range nodes {
		nodes[index] = sharedmodel.FlowchartNode{Key: repeated(fragmentMaxDiagramKey), Label: repeated(fragmentMaxDiagramLabel)}
	}
	edges := make([]sharedmodel.FlowchartEdge, fragmentMaxFlowchartEdges)
	for index := range edges {
		edges[index] = sharedmodel.FlowchartEdge{From: repeated(fragmentMaxDiagramKey), To: repeated(fragmentMaxDiagramKey), Label: repeated(fragmentMaxDiagramLabel)}
	}
	return sharedmodel.Diagram{Type: sharedmodel.DiagramFlowchart, Title: repeated(fragmentMaxTitle), SourcePaths: maximumPaths(), Nodes: nodes, Edges: edges}
}

func maximumSequence() sharedmodel.Diagram {
	participants := make([]sharedmodel.SequenceParticipant, fragmentMaxSequenceParties)
	for index := range participants {
		participants[index] = sharedmodel.SequenceParticipant{Key: repeated(fragmentMaxDiagramKey), Label: repeated(fragmentMaxDiagramLabel)}
	}
	messages := make([]sharedmodel.SequenceMessage, fragmentMaxSequenceMessages)
	for index := range messages {
		messages[index] = sharedmodel.SequenceMessage{From: repeated(fragmentMaxDiagramKey), To: repeated(fragmentMaxDiagramKey), Label: repeated(fragmentMaxDiagramLabel), Response: false}
	}
	return sharedmodel.Diagram{Type: sharedmodel.DiagramSequence, Title: repeated(fragmentMaxTitle), SourcePaths: maximumPaths(), Participants: participants, Messages: messages}
}

func maximumClassDiagram() sharedmodel.Diagram {
	classes := make([]sharedmodel.ClassNode, fragmentMaxClassNodes)
	for index := range classes {
		members := make([]string, fragmentMaxClassMembers)
		for member := range members {
			members[member] = repeated(fragmentMaxDiagramMember)
		}
		classes[index] = sharedmodel.ClassNode{Key: repeated(fragmentMaxDiagramKey), Label: repeated(fragmentMaxDiagramLabel), Members: members}
	}
	relationships := make([]sharedmodel.ClassRelationship, fragmentMaxClassRelationship)
	for index := range relationships {
		relationships[index] = sharedmodel.ClassRelationship{From: repeated(fragmentMaxDiagramKey), To: repeated(fragmentMaxDiagramKey), Kind: "composition", Label: repeated(fragmentMaxDiagramLabel)}
	}
	return sharedmodel.Diagram{Type: sharedmodel.DiagramClass, Title: repeated(fragmentMaxTitle), SourcePaths: maximumPaths(), Classes: classes, Relationships: relationships}
}

func maximumPaths() []string {
	paths := make([]string, fragmentMaxSourcePaths)
	for index := range paths {
		paths[index] = repeated(fragmentMaxPath)
	}
	return paths
}

func maximumOmittedCount() *int {
	value := fragmentMaxOmittedCount
	return &value
}

func repeated(length int) string { return string(bytes.Repeat([]byte{'\\'}, length)) }

func maximumJSONExpansionString(length int) string {
	return string(bytes.Repeat([]byte{0}, length))
}
