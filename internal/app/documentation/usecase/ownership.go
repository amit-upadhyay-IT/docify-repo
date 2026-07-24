package usecase

import (
	"fmt"
	"regexp"
	"strings"
)

// Ownership markers delimit the sections of an aggregate topic document that belong to
// one component. They are HTML comments so they are invisible in rendered Markdown, and
// their exact byte form is owned by the local renderer. Model prose can never emit them:
// the dossier validator rejects HTML in every prose field.
//
// An owned section is written as:
//
//	<!-- docify:begin topic="architecture" key="services/payments" -->
//	...rendered content...
//	<!-- docify:end topic="architecture" key="services/payments" -->
//
// Incremental rendering (a later phase) parses these to replace only selected sections
// while copying unaffected bytes verbatim. Phase 4 emits and validates them so that the
// installed output is already incremental-ready.
const (
	markerBeginPrefix = "<!-- docify:begin "
	markerEndPrefix   = "<!-- docify:end "
	markerSuffix      = " -->"
)

// ownedSection is one parsed marker-delimited region of a document.
type ownedSection struct {
	Topic   string
	Key     string
	Content string
	// begin and end are byte offsets of the marker lines within the document, used by
	// incremental replacement.
	beginLine int
	endLine   int
}

func sectionBeginMarker(topic, key string) string {
	return fmt.Sprintf("%stopic=%q key=%q%s", markerBeginPrefix, topic, key, markerSuffix)
}

func sectionEndMarker(topic, key string) string {
	return fmt.Sprintf("%stopic=%q key=%q%s", markerEndPrefix, topic, key, markerSuffix)
}

// renderOwnedSection wraps already-rendered content in begin/end markers. The content is
// emitted verbatim between marker lines; a trailing newline is normalized so sections
// concatenate deterministically.
func renderOwnedSection(topic, key, content string) string {
	var builder strings.Builder
	builder.WriteString(sectionBeginMarker(topic, key))
	builder.WriteByte('\n')
	trimmed := strings.Trim(content, "\n")
	if trimmed != "" {
		builder.WriteString(trimmed)
		builder.WriteByte('\n')
	}
	builder.WriteString(sectionEndMarker(topic, key))
	builder.WriteByte('\n')
	return builder.String()
}

var markerLinePattern = regexp.MustCompile(`^<!-- docify:(begin|end) topic="([^"]*)" key="([^"]*)" -->$`)

// parseOwnedSections extracts every marker-delimited section from a document in order.
// It fails on unbalanced, mismatched, nested, or duplicate markers so ownership can
// never be guessed. Content outside any marker is ignored by this parser; callers that
// need the surrounding bytes use parseDocumentSegments.
func parseOwnedSections(body string) ([]ownedSection, error) {
	segments, err := parseDocumentSegments(body)
	if err != nil {
		return nil, err
	}
	sections := make([]ownedSection, 0)
	for _, segment := range segments {
		if segment.owned {
			sections = append(sections, segment.section)
		}
	}
	return sections, nil
}

// documentSegment is either literal text (owned=false) or one owned section.
type documentSegment struct {
	owned   bool
	literal string
	section ownedSection
}

// parseDocumentSegments splits a document into ordered literal and owned segments while
// enforcing marker integrity. Reassembling every segment reproduces the input byte for
// byte, which is what incremental replacement relies on.
func parseDocumentSegments(body string) ([]documentSegment, error) {
	lines := strings.SplitAfter(body, "\n")
	segments := make([]documentSegment, 0)
	seen := make(map[string]struct{})

	var literal strings.Builder
	flushLiteral := func() {
		if literal.Len() > 0 {
			segments = append(segments, documentSegment{literal: literal.String()})
			literal.Reset()
		}
	}

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSuffix(line, "\n")
		match := markerLinePattern.FindStringSubmatch(trimmed)
		if match == nil {
			literal.WriteString(line)
			continue
		}
		if match[1] == "end" {
			return nil, fmt.Errorf("ownership marker: unexpected end marker for topic %q key %q", match[2], match[3])
		}
		topic, key := match[2], match[3]
		identity := topic + "\x00" + key
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("ownership marker: duplicate section topic %q key %q", topic, key)
		}

		// Collect content lines until the matching end marker.
		var content strings.Builder
		beginLine := index
		closed := false
		for index++; index < len(lines); index++ {
			inner := strings.TrimSuffix(lines[index], "\n")
			innerMatch := markerLinePattern.FindStringSubmatch(inner)
			if innerMatch == nil {
				content.WriteString(lines[index])
				continue
			}
			if innerMatch[1] == "begin" {
				return nil, fmt.Errorf("ownership marker: nested begin marker inside topic %q key %q", topic, key)
			}
			if innerMatch[2] != topic || innerMatch[3] != key {
				return nil, fmt.Errorf("ownership marker: mismatched end marker topic %q key %q inside topic %q key %q", innerMatch[2], innerMatch[3], topic, key)
			}
			closed = true
			break
		}
		if !closed {
			return nil, fmt.Errorf("ownership marker: unterminated section topic %q key %q", topic, key)
		}
		seen[identity] = struct{}{}
		flushLiteral()
		segments = append(segments, documentSegment{
			owned: true,
			section: ownedSection{
				Topic:     topic,
				Key:       key,
				Content:   strings.Trim(content.String(), "\n"),
				beginLine: beginLine,
				endLine:   index,
			},
		})
	}
	flushLiteral()
	return segments, nil
}

// sectionID identifies one owned section by its topic and component key.
type sectionID struct {
	Topic string
	Key   string
}

// validateDocumentSections checks that a document's markers are balanced, unique, and
// appear in exactly the expected order. An empty expectation means the document must
// contain no markers at all.
func validateDocumentSections(body string, expected []sectionID) error {
	sections, err := parseOwnedSections(body)
	if err != nil {
		return err
	}
	if len(sections) != len(expected) {
		return fmt.Errorf("ownership marker: document has %d sections, expected %d", len(sections), len(expected))
	}
	for index, want := range expected {
		got := sections[index]
		if got.Topic != want.Topic || got.Key != want.Key {
			return fmt.Errorf("ownership marker: section %d is topic %q key %q, expected topic %q key %q", index, got.Topic, got.Key, want.Topic, want.Key)
		}
	}
	return nil
}
