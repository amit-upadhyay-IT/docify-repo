package usecase

import (
	"strings"
	"testing"

	documentationmodel "docify-repo/internal/app/documentation/model"
	sharedmodel "docify-repo/internal/model"
)

func TestDocumentationBranchNameIsDeterministic(t *testing.T) {
	first, err := documentationBranchName("", "main")
	if err != nil {
		t.Fatalf("documentationBranchName() error = %v", err)
	}
	second, _ := documentationBranchName("", "main")
	if first != second {
		t.Fatalf("branch name is not deterministic: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, documentationBranchPrefix+"main-") {
		t.Errorf("branch name = %q, want the derived docify prefix", first)
	}
	// A different base branch yields a different branch.
	other, _ := documentationBranchName("", "release")
	if other == first {
		t.Error("distinct base branches produced the same documentation branch")
	}
}

func TestDocumentationBranchNameHonorsConfiguredOverride(t *testing.T) {
	name, err := documentationBranchName("docs/auto", "main")
	if err != nil {
		t.Fatalf("documentationBranchName() error = %v", err)
	}
	if name != "docs/auto" {
		t.Errorf("configured branch = %q, want it used verbatim", name)
	}
	if _, err := documentationBranchName("--exec", "main"); err == nil {
		t.Error("an option-like configured branch must be rejected")
	}
}

func TestPortableBranchNameSanitizesAndBounds(t *testing.T) {
	cases := map[string]string{
		"main":                   "main",
		"feature/Foo_Bar":        "feature-foo-bar",
		"///":                    "base",
		strings.Repeat("x", 100): strings.Repeat("x", portableNameMaxLength),
		"Release/2026.07":        "release-2026-07",
	}
	for input, want := range cases {
		if got := portableBranchName(input); got != want {
			t.Errorf("portableBranchName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidatePublishInputRequiresIdentityAndCredential(t *testing.T) {
	valid := func() error {
		return validatePublishInput("octo/repo", "main", "head", "base", true)
	}
	if err := valid(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	cases := []struct {
		name       string
		repository string
		base       string
		head       string
		baseSHA    string
		credential bool
	}{
		{"missing repo", "", "main", "head", "base", true},
		{"bad repo", "not-a-repo", "main", "head", "base", true},
		{"missing base branch", "octo/repo", "", "head", "base", true},
		{"missing head sha", "octo/repo", "main", "", "base", true},
		{"missing base sha", "octo/repo", "main", "head", "", true},
		{"missing credential", "octo/repo", "main", "head", "base", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePublishInput(test.repository, test.base, test.head, test.baseSHA, test.credential); err == nil {
				t.Error("expected a validation error")
			}
		})
	}
}

func TestRenderPullRequestBodyIsSafeAndDeterministic(t *testing.T) {
	summary := documentationmodel.ResultSummary{
		Plan: sharedmodel.GenerationPlan{
			Mode: "incremental", StateStatus: "compatible", GenerationStrategy: "auto",
			Calls: sharedmodel.CallEstimate{TypicalLogical: 1, MaximumLogical: 80, MaximumHTTPAttempts: 480},
			AffectedComponents: []sharedmodel.AffectedComponent{
				{Key: "services/api", Action: sharedmodel.ComponentRegenerate},
				{Key: "service`\n@team", Action: sharedmodel.ComponentCreate},
			},
		},
		Generation: &documentationmodel.GenerationOutcome{
			Diff:          sharedmodel.OutputDiff{Added: []string{"a"}, Changed: []string{"b", "c"}},
			FragmentCalls: 7, RepairCalls: 1, FragmentFallbacks: 1, FragmentSourceSplits: 2,
		},
	}
	body := renderPullRequestBody("0123456789abcdef", "fedcba9876543210", summary)
	if body != renderPullRequestBody("0123456789abcdef", "fedcba9876543210", summary) {
		t.Fatal("pull-request body is not deterministic")
	}
	for _, want := range []string{
		"012345678", "incremental", "Generation strategy: `auto`", "1 typical / 80 maximum logical / 480 maximum HTTP attempts",
		"services/api (regenerate)", "Fragment calls: 7", "Repairs: 1", "Fragment fallbacks: 1", "Source splits: 2",
		"Added: 1", "Changed: 2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	// The short SHA must not include the full SHA (bounded to the hash length).
	if strings.Contains(body, "0123456789abcdef") {
		t.Error("body leaked the full SHA instead of the short form")
	}
	if strings.Contains(body, "\n@team") || !strings.Contains(body, `\u{A}`) {
		t.Errorf("body did not neutralize component Markdown controls:\n%s", body)
	}
}

func TestPublishFailureReportRetainsCompletedValidation(t *testing.T) {
	summary := documentationmodel.ResultSummary{
		Command: "sync", Status: "publish_failed",
		Generation: &documentationmodel.GenerationOutcome{},
		Failure:    &documentationmodel.GenerationFailure{Category: "publish"},
	}
	report := buildRunReport(summary, nil)
	if !report.Validation.OutputValidated {
		t.Fatalf("validation = %+v, want completed generation preserved", report.Validation)
	}
}
