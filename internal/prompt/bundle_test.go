package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	sharedmodel "docify-repo/internal/model"
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

func TestCodebaseSummaryV2ExposesEveryFragmentContract(t *testing.T) {
	bundle := CodebaseSummaryV2()
	if bundle.Identifier() != FragmentIdentifier {
		t.Fatalf("Identifier() = %q, want %q", bundle.Identifier(), FragmentIdentifier)
	}
	if strings.TrimSpace(bundle.System()) == "" || strings.TrimSpace(bundle.Repair()) == "" {
		t.Fatal("fragment system and repair prompts must not be empty")
	}
	for _, kind := range sharedmodel.FragmentKinds() {
		t.Run(string(kind), func(t *testing.T) {
			fragmentPrompt, ok := bundle.FragmentPrompt(kind)
			if !ok || strings.TrimSpace(fragmentPrompt) == "" {
				t.Fatal("fragment prompt is missing")
			}
			if !strings.Contains(fragmentPrompt, string(kind)) {
				t.Fatalf("fragment prompt does not identify kind %q", kind)
			}
			schema, ok := bundle.FragmentSchema(kind)
			if !ok || !json.Valid(schema) {
				t.Fatal("fragment schema is missing or invalid JSON")
			}
			name, ok := bundle.FragmentSchemaName(kind)
			if !ok || name != "component_fragment_"+string(kind) {
				t.Fatalf("schema name = %q", name)
			}
			assertStrictObjects(t, schema)
			assertFragmentTopLevelContract(t, kind, schema)
		})
	}
	if !strings.HasPrefix(bundle.ContentHash(), "sha256:") {
		t.Fatalf("ContentHash() = %q, want sha256 prefix", bundle.ContentHash())
	}
}

func assertFragmentTopLevelContract(t *testing.T, kind sharedmodel.FragmentKind, schema []byte) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	properties, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties object")
	}
	expected := map[string]struct{}{"complete": {}, "omitted_count": {}, "items": {}}
	required := map[string]struct{}{"complete": {}, "items": {}}
	if kind == sharedmodel.FragmentOverviewCandidate {
		expected = map[string]struct{}{"title": {}, "purpose": {}, "source_paths": {}}
		required = expected
	}
	if len(properties) != len(expected) {
		t.Fatalf("top-level properties = %v, want only %v", properties, expected)
	}
	for name := range properties {
		if _, ok := expected[name]; !ok {
			t.Fatalf("top-level field %q belongs to another fragment contract", name)
		}
	}
	requiredValues, ok := root["required"].([]any)
	if !ok || len(requiredValues) != len(required) {
		t.Fatalf("required fields = %v, want %v", root["required"], required)
	}
	for _, value := range requiredValues {
		name, _ := value.(string)
		if _, ok := required[name]; !ok {
			t.Fatalf("unexpected required field %q", name)
		}
	}
}

func TestFragmentSchemaCopiesAreIsolated(t *testing.T) {
	bundle := CodebaseSummaryV2()
	for _, kind := range sharedmodel.FragmentKinds() {
		first, _ := bundle.FragmentSchema(kind)
		original := first[0]
		first[0] ^= 0xFF
		second, _ := bundle.FragmentSchema(kind)
		if second[0] != original {
			t.Fatalf("mutating %s schema copy affected later reads", kind)
		}
	}
}

func TestOverviewReducerContractIsStrictAndIsolated(t *testing.T) {
	bundle := CodebaseSummaryV2()
	if bundle.OverviewPrompt() == "" || bundle.OverviewSchemaName() != "component_overview_reducer" {
		t.Fatalf("overview reducer contract is incomplete")
	}
	first := bundle.OverviewSchema()
	assertStrictObjects(t, first)
	var root map[string]any
	if err := json.Unmarshal(first, &root); err != nil {
		t.Fatalf("decode overview schema: %v", err)
	}
	properties, ok := root["properties"].(map[string]any)
	if !ok || len(properties) != 2 || properties["title"] == nil || properties["purpose"] == nil {
		t.Fatalf("overview properties = %v, want title and purpose only", properties)
	}
	original := first[0]
	first[0] ^= 0xFF
	if second := bundle.OverviewSchema(); second[0] != original {
		t.Fatal("mutating overview schema copy affected later reads")
	}
}

func TestFragmentContentHashIncludesEveryResource(t *testing.T) {
	contents := make(map[string][]byte, len(fragmentResourcePaths))
	for _, path := range fragmentResourcePaths {
		data, err := resources.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		contents[path] = data
	}
	read := func(path string) []byte { return contents[path] }
	baseline := contentHash(FragmentIdentifier, fragmentResourcePaths, read)
	if baseline != CodebaseSummaryV2().ContentHash() {
		t.Fatalf("calculated hash = %q, bundle hash = %q", baseline, CodebaseSummaryV2().ContentHash())
	}
	for _, path := range fragmentResourcePaths {
		original := contents[path]
		contents[path] = append(append([]byte(nil), original...), 'x')
		if changed := contentHash(FragmentIdentifier, fragmentResourcePaths, read); changed == baseline {
			t.Fatalf("changing %s did not change content hash", path)
		}
		contents[path] = original
	}
}

func assertStrictObjects(t *testing.T, schema []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	var walk func(any)
	walk = func(candidate any) {
		switch typed := candidate.(type) {
		case map[string]any:
			if typed["type"] == "object" {
				strict, exists := typed["additionalProperties"]
				if !exists || strict != false {
					t.Fatalf("object schema lacks additionalProperties:false: %v", typed)
				}
			}
			for _, nested := range typed {
				walk(nested)
			}
		case []any:
			for _, nested := range typed {
				walk(nested)
			}
		}
	}
	walk(value)
}
