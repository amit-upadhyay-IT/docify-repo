package usecase

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sharedmodel "docify-repo/internal/model"
	"docify-repo/internal/prompt"
	openairepository "docify-repo/internal/repository/openai"
)

// TestLiveFragmentSchemaCompatibility is an opt-in provider qualification test.
// It uses the production request builders, prompts, schemas, and local validators
// for every map and reducer contract. Ordinary CI remains deterministic and offline.
//
//	DOCIFY_LIVE_LLM_BASE_URL   OpenAI-compatible endpoint
//	DOCIFY_LIVE_LLM_MODEL      exact provider model ID
//	DOCIFY_LLM_API_KEY         bearer credential
//	DOCIFY_LIVE_LLM_API_MODE   optional: chat_completions (default) or responses
//	DOCIFY_LIVE_LLM_STRUCTURED optional: auto (default), json_schema, or prompt_json
func TestLiveFragmentSchemaCompatibility(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("DOCIFY_LIVE_LLM_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("DOCIFY_LIVE_LLM_MODEL"))
	if baseURL == "" || model == "" || strings.TrimSpace(os.Getenv(openairepository.CredentialEnvVar)) == "" {
		t.Skipf("set DOCIFY_LIVE_LLM_BASE_URL, DOCIFY_LIVE_LLM_MODEL, and %s to run live fragment qualification", openairepository.CredentialEnvVar)
	}

	settings := sharedmodel.GenerationSettings{
		Model: model, Temperature: 0, MaxOutputTokens: fragmentMinimumOutputTokens,
		APIMode:              liveAPIMode(t, os.Getenv("DOCIFY_LIVE_LLM_API_MODE")),
		StructuredOutputMode: liveStructuredOutputMode(t, os.Getenv("DOCIFY_LIVE_LLM_STRUCTURED")),
	}
	generator := openairepository.New(openairepository.Options{
		BaseURL: baseURL, TokenSource: openairepository.NewEnvTokenSource(openairepository.CredentialEnvVar),
		Timeout: 60 * time.Second, Retries: 2, MaxContentBytes: fragmentMinimumResponseBytes,
	})
	bundle := prompt.CodebaseSummaryV2()
	component, source, dossier := liveQualificationFixture()
	catalog := []string{component.Key}
	const maxRequestBytes = int64(500_000)

	type qualificationCase struct {
		name     string
		request  sharedmodel.GenerationRequest
		validate func([]byte) error
	}
	cases := make([]qualificationCase, 0, len(requiredFragmentKinds())+3)
	mapKinds := append([]sharedmodel.FragmentKind{sharedmodel.FragmentOverviewCandidate}, requiredFragmentKinds()...)
	for _, kind := range mapKinds {
		kind := kind
		request, err := buildFragmentRequest(bundle, settings, component, kind, source, nil, nil, catalog, nil, "full", 1, 1, 1, 1,
			fragmentMinimumResponseBytes, maxRequestBytes)
		if err != nil {
			t.Fatalf("build production %s request: %v", kind, err)
		}
		scope := fragmentScope{
			ComponentKey: component.Key, Kind: kind, SourceBatchIndex: 1, SourceBatchCount: 1,
			SourceChunkIndex: 1, SourceChunkCount: 1, SplitPath: "0", AllowedEvidence: []string{source[0].Path},
		}
		cases = append(cases, qualificationCase{
			name: string(kind), request: request,
			validate: func(body []byte) error {
				validation := validateScopedFragment(scope, body, scope.AllowedEvidence, catalog)
				if !validation.valid() {
					return fmt.Errorf("local validation failed with codes %v", issueCodes(validation.issues))
				}
				trusted, err := validation.revalidateSealed()
				if err != nil {
					return err
				}
				if liveFragmentItemCount(trusted.value) == 0 {
					return fmt.Errorf("fixture facts produced an empty %s response", kind)
				}
				return nil
			},
		})
	}

	overviewRequest, err := buildOverviewReducerRequest(bundle, settings, component,
		[]sharedmodel.OverviewCandidate{{Title: dossier.Title, Purpose: dossier.Purpose, SourcePaths: dossier.SourcePaths}},
		dossier.SourcePaths, overviewSections(dossier), fragmentMinimumResponseBytes, maxRequestBytes)
	if err != nil {
		t.Fatalf("build production overview reducer request: %v", err)
	}
	cases = append(cases, qualificationCase{
		name: "overview_reducer", request: overviewRequest,
		validate: func(body []byte) error {
			validation := validateOverviewReduction(component, body)
			if !validation.valid() {
				return fmt.Errorf("local validation failed with codes %v", issueCodes(validation.issues))
			}
			return nil
		},
	})

	projection, diagramEvidence := diagramProjectionFromDossier(dossier)
	diagramRequest, err := buildDiagramReducerRequest(bundle, settings, component, projection, diagramEvidence,
		fragmentMinimumResponseBytes, maxRequestBytes)
	if err != nil {
		t.Fatalf("build production diagram reducer request: %v", err)
	}
	diagramScope := fragmentScope{
		ComponentKey: component.Key, Kind: sharedmodel.FragmentDiagrams, SourceBatchIndex: 1, SourceBatchCount: 1,
		SourceChunkIndex: 1, SourceChunkCount: 1, SplitPath: "diagram", AllowedEvidence: diagramEvidence,
	}
	cases = append(cases, qualificationCase{
		name: "diagram_reducer", request: diagramRequest,
		validate: func(body []byte) error {
			validation := validateScopedFragment(diagramScope, body, diagramEvidence, catalog)
			if !validation.valid() {
				return fmt.Errorf("local validation failed with codes %v", issueCodes(validation.issues))
			}
			trusted, err := validation.revalidateSealed()
			if err != nil {
				return err
			}
			if liveFragmentItemCount(trusted.value) == 0 {
				return fmt.Errorf("fixture facts produced an empty diagram response")
			}
			return nil
		},
	})

	for _, test := range cases {
		test := test
		if passed := t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			response, err := generator.Generate(ctx, test.request)
			if err != nil {
				t.Fatalf("live request failed: %v", err)
			}
			if settings.StructuredOutputMode != sharedmodel.StructuredOutputAuto && response.StructuredOutputUsed != settings.StructuredOutputMode {
				t.Fatalf("structured mode = %q, want %q", response.StructuredOutputUsed, settings.StructuredOutputMode)
			}
			if err := test.validate(response.Body); err != nil {
				t.Fatalf("live response failed production validation: %v", err)
			}
			t.Logf("contract=%s structured=%s finish=%s attempts=%d usage_present=%t",
				test.name, response.StructuredOutputUsed, response.FinishReason, response.TransportAttempts, response.Usage.Present)
		}); !passed {
			return
		}
	}
}

func liveQualificationFixture() (sharedmodel.Component, []sharedmodel.SourceFile, sharedmodel.ComponentDossier) {
	path := "qualification/handler.go"
	component := sharedmodel.Component{
		Key: "qualification", Document: "components/qualification/index.md",
		TriggeringFiles: []sharedmodel.SourceFile{{Path: path, Role: sharedmodel.RoleProductionSource, TriggersRegeneration: true}},
	}
	source := []sharedmodel.SourceFile{{
		Path: path, Role: sharedmodel.RoleProductionSource, ComponentKey: component.Key, TriggersRegeneration: true,
		Data: []byte("package qualification\n\nimport \"encoding/json\"\n\ntype Request struct { ID string }\n\ntype Handler interface { Handle(Request) ([]byte, error) }\n\n// ProcessRequest validates a request and encodes the response.\n// TODO: define timeout behavior.\nfunc ProcessRequest(handler Handler, request Request) ([]byte, error) { return json.Marshal(request) }\n"),
	}}
	dossier := sharedmodel.ComponentDossier{
		Title: "Qualification handler", Purpose: "Validates requests and encodes responses.", SourcePaths: []string{path},
		Architecture: []sharedmodel.ArchitectureItem{{Title: "Request boundary", Description: "Separates request handling from response encoding.", SourcePaths: []string{path}}},
		Interfaces:   []sharedmodel.InterfaceItem{{Name: "Handler", Kind: "interface", Direction: "internal", Description: "Handles one request.", SourcePaths: []string{path}}},
		DataModels:   []sharedmodel.DataModelItem{{Name: "Request", Kind: "request", Description: "Carries a request identifier.", Fields: []sharedmodel.DataField{{Name: "ID", Type: "string", Description: "Identifies the request."}}, Relationships: []sharedmodel.DataRelationship{}, SourcePaths: []string{path}}},
		Workflows:    []sharedmodel.WorkflowItem{{Name: "Process request", Description: "Validates and encodes one request.", Steps: []sharedmodel.WorkflowStep{{Actor: "Caller", Action: "invokes", Target: "ProcessRequest"}}, SourcePaths: []string{path}}},
		Dependencies: []sharedmodel.DependencyItem{{Name: "encoding/json", Kind: "library", Purpose: "Encodes response data.", SourcePaths: []string{path}}},
		ReviewGaps:   []sharedmodel.ReviewGap{{Kind: "missing_context", Description: "Timeout behavior is not defined.", Recommendation: "Define timeout behavior.", SourcePaths: []string{path}}},
		Diagrams:     []sharedmodel.Diagram{},
	}
	return component, source, dossier
}

func liveFragmentItemCount(value any) int {
	switch value := value.(type) {
	case sharedmodel.OverviewCandidate:
		if value.Title != "" && value.Purpose != "" && len(value.SourcePaths) > 0 {
			return 1
		}
	case sharedmodel.ArchitectureFragment:
		return len(value.Items)
	case sharedmodel.InterfacesFragment:
		return len(value.Items)
	case sharedmodel.DataModelsFragment:
		return len(value.Items)
	case sharedmodel.WorkflowsFragment:
		return len(value.Items)
	case sharedmodel.DependenciesFragment:
		return len(value.Items)
	case sharedmodel.ReviewGapsFragment:
		return len(value.Items)
	case sharedmodel.DiagramsFragment:
		return len(value.Items)
	}
	return 0
}

func liveAPIMode(t *testing.T, value string) sharedmodel.APIMode {
	t.Helper()
	switch strings.TrimSpace(value) {
	case "", "chat_completions":
		return sharedmodel.APIModeChatCompletions
	case "responses":
		return sharedmodel.APIModeResponses
	default:
		t.Fatalf("DOCIFY_LIVE_LLM_API_MODE = %q, want chat_completions or responses", value)
		return ""
	}
}

func liveStructuredOutputMode(t *testing.T, value string) sharedmodel.StructuredOutputMode {
	t.Helper()
	switch strings.TrimSpace(value) {
	case "", "auto":
		return sharedmodel.StructuredOutputAuto
	case "json_schema":
		return sharedmodel.StructuredOutputJSONSchema
	case "prompt_json":
		return sharedmodel.StructuredOutputPromptJSON
	default:
		t.Fatalf("DOCIFY_LIVE_LLM_STRUCTURED = %q, want auto, json_schema, or prompt_json", value)
		return ""
	}
}
