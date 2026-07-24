package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodebaseSummaryV1ExposesEveryResource(t *testing.T) {
	bundle := CodebaseSummaryV1()

	if bundle.Identifier() != Identifier {
		t.Fatalf("Identifier() = %q, want %q", bundle.Identifier(), Identifier)
	}
	for name, value := range map[string]string{
		"system":    bundle.System(),
		"component": bundle.Component(),
		"synthesis": bundle.Synthesis(),
		"repair":    bundle.Repair(),
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s prompt is empty", name)
		}
	}
	if !json.Valid(bundle.Schema()) {
		t.Fatal("Schema() is not valid JSON")
	}
	if !strings.HasPrefix(bundle.ContentHash(), "sha256:") {
		t.Fatalf("ContentHash() = %q, want sha256 prefix", bundle.ContentHash())
	}
}

func TestBundleSchemaCopyIsIsolated(t *testing.T) {
	bundle := CodebaseSummaryV1()
	first := bundle.Schema()
	if len(first) == 0 {
		t.Fatal("Schema() returned no bytes")
	}
	original := first[0]
	first[0] ^= 0xFF

	second := bundle.Schema()
	if second[0] != original {
		t.Fatal("mutating a returned Schema() copy affected later reads")
	}
}

func TestContentHashIsStable(t *testing.T) {
	if CodebaseSummaryV1().ContentHash() != CodebaseSummaryV1().ContentHash() {
		t.Fatal("ContentHash() is not stable across calls")
	}
}
