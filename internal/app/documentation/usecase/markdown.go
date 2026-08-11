package usecase

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// markdownInlineEscaper neutralizes the inline Markdown metacharacters that could appear
// in validated plain-text prose. The dossier validator already rejects links, HTML,
// headings, lists, fences, and diagram syntax, so this only guards stray emphasis, code
// spans, and bracket runs. It never touches newlines, so paragraph structure is
// preserved.
var markdownInlineEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	"*", `\*`,
	"_", `\_`,
	"[", `\[`,
	"]", `\]`,
	"<", `\<`,
	">", `\>`,
)

// escapeInline escapes a prose value for use in flowing Markdown text.
func escapeInline(value string) string {
	return markdownInlineEscaper.Replace(value)
}

// escapeCell escapes a prose value for use inside a Markdown table cell: inline
// metacharacters are escaped, pipes are escaped, and any newline collapses to a space so
// the cell stays on one row.
func escapeCell(value string) string {
	escaped := escapeInline(value)
	escaped = strings.ReplaceAll(escaped, "|", `\|`)
	escaped = strings.ReplaceAll(escaped, "\r\n", " ")
	escaped = strings.ReplaceAll(escaped, "\n", " ")
	escaped = strings.ReplaceAll(escaped, "\r", " ")
	return escaped
}

// docDepth returns how many "../" segments reach the repository root from a document.
// A repository-relative document path such as docs/generated/architecture.md has two
// slashes, so its directory is two levels below the root.
func docDepth(docPath string) int {
	return strings.Count(docPath, "/")
}

// sourceLink renders one validated evidence path as a repository-relative Markdown link
// from the given document. The link text is the path in an inline code span; the target
// climbs out of the generated tree to the repository root and then down to the file.
func sourceLink(docPath, evidencePath string) string {
	prefix := strings.Repeat("../", docDepth(docPath))
	segments := strings.Split(evidencePath, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return "[" + inlineCodePath(evidencePath) + "](" + prefix + strings.Join(segments, "/") + ")"
}

func inlineCodePath(value string) string {
	var safe strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			safe.WriteString(fmt.Sprintf("\\u{%X}", character))
			continue
		}
		safe.WriteRune(character)
	}
	value = safe.String()
	longest := 0
	current := 0
	for _, character := range value {
		if character == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	if longest == 0 {
		return "`" + value + "`"
	}
	fence := strings.Repeat("`", longest+1)
	return fence + " " + value + " " + fence
}

// sourcePathsLine renders a sorted, comma-separated list of source links for a set of
// evidence paths. It assumes the paths are already validated.
func sourcePathsLine(docPath string, paths []string) string {
	links := make([]string, 0, len(paths))
	for _, path := range paths {
		links = append(links, sourceLink(docPath, path))
	}
	return strings.Join(links, ", ")
}

// mdBuilder accumulates Markdown with deterministic spacing. It guarantees LF line
// endings, no trailing whitespace introduced by templates, and exactly one terminal
// newline via finish.
type mdBuilder struct {
	builder strings.Builder
}

func (m *mdBuilder) line(text string) {
	m.builder.WriteString(text)
	m.builder.WriteByte('\n')
}

// blank writes a single blank line, collapsing consecutive requests so the document
// never accumulates multiple blank lines from template composition.
func (m *mdBuilder) blank() {
	current := m.builder.String()
	if current == "" || strings.HasSuffix(current, "\n\n") {
		return
	}
	m.builder.WriteByte('\n')
}

// raw appends already-formatted content verbatim (for example a rendered Mermaid block
// or a marker-wrapped section). The caller owns its internal newlines.
func (m *mdBuilder) raw(text string) {
	m.builder.WriteString(text)
}

// finish returns the document with trailing blank lines collapsed to exactly one
// terminal newline.
func (m *mdBuilder) finish() string {
	text := strings.TrimRight(m.builder.String(), "\n")
	if text == "" {
		return "\n"
	}
	return text + "\n"
}
