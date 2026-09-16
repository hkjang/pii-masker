package masking

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"testing"
)

func TestParsePayloadUnwrapsEnvelopeAndJSONStringResult(t *testing.T) {
	raw := json.RawMessage(`"{\"fields\":[{\"key\":\"개인정보.이름\",\"value\":\"홍길동\",\"boundingBox\":{\"x\":120,\"y\":45,\"width\":160,\"height\":33}}]}"`)

	payload := ParsePayload(raw, nil)
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

	parsed := ParsePayload(nil, []byte(mustJSON(t, payload)))
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

// A response of the size a real endpoint returns for a document with many fields
// would not survive the 16KB truncation of the debug copy, so ParsePayload has to
// be handed the raw body and read every field out of it.
func TestParsePayloadReadsLargeTopLevelBodies(t *testing.T) {
	t.Parallel()

	fields := make([]any, 0, 120)
	for index := range 120 {
		fields = append(fields, map[string]any{
			"key":   "개인정보.휴대폰번호",
			"value": fmt.Sprintf("010-1234-%04d", index),
			"boundingBoxes": []any{map[string]any{"page": 1, "vertices": []any{
				map[string]any{"x": 10, "y": 10 + index*12}, map[string]any{"x": 100, "y": 10 + index*12},
				map[string]any{"x": 100, "y": 20 + index*12}, map[string]any{"x": 10, "y": 20 + index*12},
			}}},
		})
	}
	body := mustJSON(t, map[string]any{"apiVersion": "1.1", "fields": fields})
	if len(body) < 16*1024 {
		t.Fatalf("fixture must exceed the debug truncation limit, got %d bytes", len(body))
	}

	payload := ParsePayload(nil, []byte(body))
	if payload == nil {
		t.Fatalf("expected the raw body to be parsed")
	}
	if entries := BuildFieldEntries(payload); len(entries) != 120 {
		t.Fatalf("expected 120 field entries, got %d", len(entries))
	}
}

func TestParsePayloadRejectsTruncatedBody(t *testing.T) {
	t.Parallel()

	body := `{"fields":[{"key":"개인정보.이름","value":"홍길동","boundingBox":{"x":1,"y":2,"wid...`
	if payload := ParsePayload(nil, []byte(body)); payload != nil {
		t.Fatalf("expected nil payload for a truncated body, got %#v", payload)
	}
}

func TestContainsGeometry(t *testing.T) {
	t.Parallel()

	withBoxes := map[string]any{"fields": []any{map[string]any{"id": 1, "boundingBoxes": []any{map[string]any{"x": 1, "y": 1, "width": 2, "height": 2}}}}}
	if !ContainsGeometry(withBoxes) {
		t.Fatalf("expected geometry to be found")
	}
	if ContainsGeometry(map[string]any{"fields": []any{}, "metadata": map[string]any{"pages": []any{}}}) {
		t.Fatalf("expected no geometry in an empty result")
	}
}

// The mask is positioned by rune index, so the text drawn on the document has to
// win over a refined form whose length differs from it.
func TestEntityValuePrefersPrintedTextOverRefinedValue(t *testing.T) {
	t.Parallel()

	payload := map[string]any{"fields": []any{map[string]any{
		"key":          "개인정보.휴대폰번호",
		"value":        "010-1234-5678",
		"refinedValue": "01012345678",
		"boundingBox":  map[string]any{"x": 0, "y": 0, "width": 130, "height": 10},
	}}}

	entries := BuildFieldEntries(payload)
	if len(entries) != 1 || entries[0].Value != "010-1234-5678" {
		t.Fatalf("expected the printed value to be used, got %#v", entries)
	}
	if entries[0].MaskedValue != "010-1234-****" {
		t.Fatalf("unexpected masked value %q", entries[0].MaskedValue)
	}
}

// Several boxes for one value give no way to tell which characters each holds,
// so every box is covered whole rather than partially at a guessed offset.
func TestMultiBoxFieldsAreCoveredInFull(t *testing.T) {
	t.Parallel()

	box := func(y float64) map[string]any {
		return map[string]any{"page": 1, "vertices": []any{
			map[string]any{"x": 10, "y": y}, map[string]any{"x": 210, "y": y},
			map[string]any{"x": 210, "y": y + 10}, map[string]any{"x": 10, "y": y + 10},
		}}
	}
	payload := map[string]any{"fields": []any{map[string]any{
		"key":           "개인정보.주소",
		"value":         "서울 영등포구 국제금융로 10",
		"boundingBoxes": []any{box(0), box(20)},
	}}}

	regions := CollectMaskRegions(payload)
	if len(regions) != 2 {
		t.Fatalf("expected one region per box, got %#v", regions)
	}
	for _, region := range regions {
		minX, _, maxX, _ := polygonBounds(region.Polygon)
		if minX != 10 || maxX != 210 {
			t.Fatalf("expected the whole box to be covered, got %#v", region)
		}
	}

	single := map[string]any{"fields": []any{map[string]any{
		"key":           "개인정보.주소",
		"value":         "서울 영등포구 국제금융로 10",
		"boundingBoxes": []any{box(0)},
	}}}
	regions = CollectMaskRegions(single)
	if len(regions) != 1 {
		t.Fatalf("expected one partial region, got %#v", regions)
	}
	if minX, _, maxX, _ := polygonBounds(regions[0].Polygon); minX <= 10 || maxX != 210 {
		t.Fatalf("expected only the trailing token to be covered, got %#v", regions[0])
	}
}

func TestPlaceRegionScalesNormalizedCoordinates(t *testing.T) {
	t.Parallel()

	region := Region{PageNumber: 1, Polygon: [4][2]float64{{0.25, 0.5}, {0.75, 0.5}, {0.75, 0.6}, {0.25, 0.6}}}
	placed, err := placeRegion(region, PageSize{}, false, PageSize{Width: 400, Height: 200})
	if err != nil {
		t.Fatalf("placeRegion: %v", err)
	}
	if placed.minX != 100 || placed.maxX != 300 || placed.minY != 100 || placed.maxY != 120 {
		t.Fatalf("unexpected placement %#v", placed)
	}
}

func TestPlaceRegionScalesByReportedPageSize(t *testing.T) {
	t.Parallel()

	region := Region{PageNumber: 1, Polygon: [4][2]float64{{200, 400}, {600, 400}, {600, 500}, {200, 500}}}
	placed, err := placeRegion(region, PageSize{Width: 800, Height: 1000}, true, PageSize{Width: 400, Height: 500})
	if err != nil {
		t.Fatalf("placeRegion: %v", err)
	}
	if placed.minX != 100 || placed.maxX != 300 || placed.minY != 200 || placed.maxY != 250 {
		t.Fatalf("unexpected placement %#v", placed)
	}
}

// Pixel coordinates without a reported page size cannot be mapped, and drawing
// them as page units would put the box off the page and leave the field visible.
func TestPlaceRegionRejectsCoordinatesBeyondThePageWithoutAPageSize(t *testing.T) {
	t.Parallel()

	region := Region{PageNumber: 1, Polygon: [4][2]float64{{1200, 1600}, {1500, 1600}, {1500, 1650}, {1200, 1650}}}
	if _, err := placeRegion(region, PageSize{}, false, PageSize{Width: 595, Height: 842}); err == nil {
		t.Fatalf("expected an error for a region beyond the page")
	}
}

func TestPlaceRegionRejectsBoxesOutsideThePage(t *testing.T) {
	t.Parallel()

	region := Region{PageNumber: 1, Polygon: [4][2]float64{{1100, 10}, {1200, 10}, {1200, 20}, {1100, 20}}}
	if _, err := placeRegion(region, PageSize{Width: 1000, Height: 1000}, true, PageSize{Width: 500, Height: 500}); err == nil {
		t.Fatalf("expected an error for a region that lands outside the page")
	}
}

func TestMaskImageFileFailsWhenARegionMissesTheImage(t *testing.T) {
	t.Parallel()

	regions := []Region{
		{PageNumber: 1, Polygon: [4][2]float64{{10, 10}, {50, 10}, {50, 30}, {10, 30}}},
		{PageNumber: 1, Polygon: [4][2]float64{{700, 10}, {750, 10}, {750, 30}, {700, 30}}},
	}
	if _, err := MaskImageFile(whitePNG(t, 100, 100), "image/png", regions, nil); err == nil {
		t.Fatalf("expected masking to fail when a region cannot be drawn")
	}
}

func TestMaskPDFFileFailsForRegionsOnMissingPages(t *testing.T) {
	t.Parallel()

	regions := []Region{{PageNumber: 3, Polygon: [4][2]float64{{10, 10}, {50, 10}, {50, 30}, {10, 30}}}}
	if _, err := MaskPDFFile(blankPDF(200, 200), regions, nil); err == nil {
		t.Fatalf("expected masking to fail for a page the document does not have")
	}
}

func whitePNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

func blankPDF(width, height int) []byte {
	var buf bytes.Buffer
	offsets := make([]int, 0, 4)
	write := func(s string) { buf.WriteString(s) }
	write("%PDF-1.4\n")
	offsets = append(offsets, buf.Len())
	write("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	offsets = append(offsets, buf.Len())
	write("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	offsets = append(offsets, buf.Len())
	write(fmt.Sprintf("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R >>\nendobj\n", width, height))
	offsets = append(offsets, buf.Len())
	write("4 0 obj\n<< /Length 0 >>\nstream\n\nendstream\nendobj\n")
	xref := buf.Len()
	write("xref\n0 5\n0000000000 65535 f \n")
	for _, offset := range offsets {
		write(fmt.Sprintf("%010d 00000 n \n", offset))
	}
	write(fmt.Sprintf("trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref))
	return buf.Bytes()
}
