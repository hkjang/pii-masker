package masking

import (
	"encoding/json"
	"testing"
)

func TestParsePayloadUnwrapsEnvelopeAndJSONStringResult(t *testing.T) {
	raw := json.RawMessage(`"{\"fields\":[{\"key\":\"개인정보.이름\",\"value\":\"홍길동\",\"boundingBox\":{\"x\":120,\"y\":45,\"width\":160,\"height\":33}}]}"`)

	payload := ParsePayload(raw, "")
	root, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("expected payload map, got %#v", payload)
	}

	fields, ok := root["fields"].([]any)
	if !ok || len(fields) != 1 {
		t.Fatalf("expected one field, got %#v", root["fields"])
	}

	regions := CollectMaskRegions(payload)
	if len(regions) == 0 {
		t.Fatalf("expected at least one region")
	}
}

func TestCollectMaskRegionsSupportsSingularBoundingBoxAndGenericFallback(t *testing.T) {
	payload := map[string]any{
		"result": map[string]any{
			"fields": []any{
				map[string]any{
					"value":       "1234-5678-9012-3456",
					"boundingBox": map[string]any{"x": 10, "y": 20, "width": 200, "height": 30},
				},
			},
		},
	}

	parsed := ParsePayload(nil, mustJSON(t, payload))
	regions := CollectMaskRegions(parsed)
	if len(regions) == 0 {
		t.Fatalf("expected regions to be detected")
	}

	entries := BuildFieldEntries(parsed)
	if len(entries) != 1 {
		t.Fatalf("expected one field entry, got %d", len(entries))
	}
	if entries[0].Rule.RuleName != "generic_full_mask" {
		t.Fatalf("expected generic fallback masking, got %#v", entries[0].Rule)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(raw)
}
