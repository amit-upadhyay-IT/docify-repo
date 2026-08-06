package usecase

import (
	"strings"
	"testing"
)

func TestSourceLinkEscapesUntrustedRepositoryPath(t *testing.T) {
	path := "services/a](https://evil.example)\n\t\x1b\u202E`name`.go"
	link := sourceLink("docs/generated/architecture.md", path)
	if strings.Contains(link, "\n") {
		t.Fatalf("sourceLink() emitted unsafe Markdown: %q", link)
	}
	if !strings.HasPrefix(link, "[`` ") {
		t.Fatalf("sourceLink() did not contain the backtick in a longer code fence: %q", link)
	}
	if !strings.Contains(link, "%5D%28https://evil.example%29%0A%60name%60.go") {
		if !strings.Contains(link, "%5D%28https://evil.example%29%0A%09%1B%E2%80%AE%60name%60.go") {
			t.Fatalf("sourceLink() did not URL-encode the target: %q", link)
		}
	}
	for _, unsafe := range []string{"\n", "\t", "\x1b", "\u202E"} {
		if strings.Contains(link, unsafe) {
			t.Fatalf("sourceLink() retained unsafe display character %q: %q", unsafe, link)
		}
	}
}
