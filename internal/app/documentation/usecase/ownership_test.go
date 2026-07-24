package usecase

import (
	"strings"
	"testing"
)

func TestRenderOwnedSectionRoundTrips(t *testing.T) {
	body := renderOwnedSection(topicArchitecture, "services/api", "line one\nline two")
	sections, err := parseOwnedSections(body)
	if err != nil {
		t.Fatalf("parseOwnedSections() error = %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(sections))
	}
	if sections[0].Topic != topicArchitecture || sections[0].Key != "services/api" {
		t.Fatalf("section identity = %+v", sections[0])
	}
	if sections[0].Content != "line one\nline two" {
		t.Fatalf("section content = %q", sections[0].Content)
	}
}

func TestParseDocumentSegmentsReassemblesInput(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("intro line\n")
	builder.WriteString(renderOwnedSection("t", "a", "alpha"))
	builder.WriteString("between\n")
	builder.WriteString(renderOwnedSection("t", "b", "beta"))
	original := builder.String()

	segments, err := parseDocumentSegments(original)
	if err != nil {
		t.Fatalf("parseDocumentSegments() error = %v", err)
	}
	var reassembled strings.Builder
	for _, segment := range segments {
		if segment.owned {
			reassembled.WriteString(renderOwnedSection(segment.section.Topic, segment.section.Key, segment.section.Content))
		} else {
			reassembled.WriteString(segment.literal)
		}
	}
	if reassembled.String() != original {
		t.Fatalf("reassembled != original:\n%q\n%q", reassembled.String(), original)
	}
}

func TestParseOwnedSectionsRejectsMalformedMarkers(t *testing.T) {
	tests := map[string]string{
		"unterminated":   beginOnly("t", "a"),
		"stray end":      endOnly("t", "a"),
		"duplicate":      renderOwnedSection("t", "a", "x") + renderOwnedSection("t", "a", "y"),
		"nested begin":   beginOnly("t", "a") + beginOnly("t", "b") + endOnly("t", "b") + endOnly("t", "a"),
		"mismatched end": beginOnly("t", "a") + endOnly("t", "b"),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOwnedSections(body); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestValidateDocumentSectionsChecksOrderAndCount(t *testing.T) {
	body := renderOwnedSection("t", "a", "x") + renderOwnedSection("t", "b", "y")
	if err := validateDocumentSections(body, []sectionID{{Topic: "t", Key: "a"}, {Topic: "t", Key: "b"}}); err != nil {
		t.Fatalf("expected matching sections to validate, got %v", err)
	}
	if err := validateDocumentSections(body, []sectionID{{Topic: "t", Key: "b"}, {Topic: "t", Key: "a"}}); err == nil {
		t.Fatal("expected an out-of-order failure")
	}
	if err := validateDocumentSections(body, []sectionID{{Topic: "t", Key: "a"}}); err == nil {
		t.Fatal("expected a count mismatch failure")
	}
	if err := validateDocumentSections("no markers here\n", nil); err != nil {
		t.Fatalf("expected a marker-free document to validate, got %v", err)
	}
	if err := validateDocumentSections(body, nil); err == nil {
		t.Fatal("expected unexpected markers to fail against an empty expectation")
	}
}

func beginOnly(topic, key string) string { return sectionBeginMarker(topic, key) + "\n" }
func endOnly(topic, key string) string   { return sectionEndMarker(topic, key) + "\n" }
