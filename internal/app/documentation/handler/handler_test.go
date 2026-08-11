package handler

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

func TestNormalizeCommon(t *testing.T) {
	inputDirectory := t.TempDir()
	got, err := normalizeCommon(inputDirectory, " JSON ", "", " base ", " head ")
	if err != nil {
		t.Fatalf("normalizeCommon() error = %v", err)
	}

	wantDirectory, err := filepath.Abs(inputDirectory)
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	if got.workingDirectory != wantDirectory {
		t.Errorf("workingDirectory = %q, want %q", got.workingDirectory, wantDirectory)
	}
	if got.output != documentationmodel.OutputModeJSON {
		t.Errorf("output = %q, want %q", got.output, documentationmodel.OutputModeJSON)
	}
	if got.reportPath != "" {
		t.Errorf("reportPath = %q, want empty", got.reportPath)
	}
	if got.baseSHA != "base" || got.headSHA != "head" {
		t.Errorf("range = %q..%q, want base..head", got.baseSHA, got.headSHA)
	}
}

func TestWriteResultHumanIncludesPhase5PlanningAndFallbackMetadata(t *testing.T) {
	var output bytes.Buffer
	handler := &Handler{output: &output}
	result := documentationmodel.ResultSummary{
		Command: "sync", Status: "synced",
		Plan: sharedmodel.GenerationPlan{
			Mode: "full", GenerationStrategy: "auto",
			Calls: sharedmodel.CallEstimate{
				Primary: 1, TypicalLogical: 1, MaximumLogical: 80, MaximumHTTPAttempts: 480,
				MaximumTruncationFallbackCalls: 79, MaximumSourceSplitCalls: 79, FallbackRequestBytes: 1234,
			},
			AffectedComponents: []sharedmodel.AffectedComponent{{
				Key: "services/api", Action: sharedmodel.ComponentCreate, GenerationStrategy: "dossier",
				FragmentFallbackPlan: true, FragmentFallback: true,
			}},
		},
		Generation: &documentationmodel.GenerationOutcome{FragmentCalls: 7, FragmentFallbacks: 1, FragmentSourceSplits: 2},
	}
	if err := handler.writeResult(result, documentationmodel.OutputModeHuman); err != nil {
		t.Fatalf("writeResult() error = %v", err)
	}
	for _, expected := range []string{
		"generation_strategy=auto", "maximum_http_attempts=480", "maximum_truncation_fallback_calls=79",
		"fallback_request_bytes=1234", "fragment_fallbacks=1", "fragment_fallback_plan=true", "fragment_fallback=true",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("human output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestNormalizeCommonRejectsIncompleteRange(t *testing.T) {
	_, err := normalizeCommon(t.TempDir(), "human", "", "base", "")
	if err == nil {
		t.Fatal("normalizeCommon() error = nil, want incomplete range error")
	}
}

func TestNormalizeCommonRejectsUnknownOutput(t *testing.T) {
	_, err := normalizeCommon(t.TempDir(), "yaml", "", "", "")
	if err == nil {
		t.Fatal("normalizeCommon() error = nil, want output error")
	}
}
