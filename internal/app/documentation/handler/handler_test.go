package handler

import (
	"path/filepath"
	"testing"

	documentationmodel "docify-repo/internal/app/documentation/model"
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
