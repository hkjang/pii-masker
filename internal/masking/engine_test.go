package masking

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
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

func TestFieldEntriesTakeThePageFromTheBoundingBox(t *testing.T) {
	t.Parallel()

	payload := map[string]any{"fields": []any{map[string]any{
		"key":   "개인정보.이름",
		"value": "홍길동",
		"boundingBoxes": []any{map[string]any{"page": 3, "vertices": []any{
			map[string]any{"x": 1, "y": 1}, map[string]any{"x": 5, "y": 1}, map[string]any{"x": 5, "y": 5}, map[string]any{"x": 1, "y": 5},
		}}},
	}}}
	entries := BuildFieldEntries(payload)
	if len(entries) != 1 || entries[0].PageNumber != 3 {
		t.Fatalf("expected page 3 from the bounding box, got %#v", entries)
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

// Each refusal below states the same fact: the coordinates the inference endpoint
// reported cannot be mapped onto a page whose size is known. Callers classify that
// as an upstream failure, not a bad upload, so one errors.As has to cover all of
// them. A page that reports no size is the upload's fault and is excluded - see
// TestMaskPDFFileBlamesTheDocumentForAPageWithoutDimensions.
func TestPlaceRegionReportsUntrustedCoordinatesAsRegionPlacementError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		region     Region
		apiSize    PageSize
		hasAPISize bool
		target     PageSize
	}{
		{
			name:   "coordinates that are not numbers",
			region: Region{PageNumber: 1, Polygon: [4][2]float64{{math.NaN(), 10}, {20, 10}, {20, 20}, {10, 20}}},
			target: PageSize{Width: 595, Height: 842},
		},
		{
			name:   "coordinates that are infinite",
			region: Region{PageNumber: 1, Polygon: [4][2]float64{{10, 10}, {math.Inf(1), 10}, {20, 20}, {10, 20}}},
			target: PageSize{Width: 595, Height: 842},
		},
		{
			name:   "pixels without a reported page size",
			region: Region{PageNumber: 1, Polygon: [4][2]float64{{1200, 1600}, {1500, 1600}, {1500, 1650}, {1200, 1650}}},
			target: PageSize{Width: 595, Height: 842},
		},
		{
			name:       "box scaled outside the page",
			region:     Region{PageNumber: 3, Polygon: [4][2]float64{{1100, 10}, {1200, 10}, {1200, 20}, {1100, 20}}},
			apiSize:    PageSize{Width: 1000, Height: 1000},
			hasAPISize: true,
			target:     PageSize{Width: 500, Height: 500},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := placeRegion(testCase.region, testCase.apiSize, testCase.hasAPISize, testCase.target)
			if err == nil {
				t.Fatalf("expected placeRegion to refuse the region")
			}
			var placement *RegionPlacementError
			if !errors.As(err, &placement) {
				t.Fatalf("error %q (%T) is not a *RegionPlacementError", err, err)
			}
			if placement.PageNumber != testCase.region.PageNumber {
				t.Fatalf("reported page %d, want %d", placement.PageNumber, testCase.region.PageNumber)
			}
			if placement.Error() != err.Error() {
				t.Fatalf("wrapped message %q differs from %q", placement.Error(), err.Error())
			}
		})
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

// The encoded bytes have to match the MIME type the response advertises: the same
// mimeType value becomes Output.MIMEType and the result download is served with
// X-Content-Type-Options: nosniff, so a browser handed PNG bytes labelled
// image/jpeg renders nothing at all.
func TestMaskImageFileEncodesTheFormatTheMIMETypeAdvertises(t *testing.T) {
	t.Parallel()

	regions := []Region{{PageNumber: 1, Polygon: [4][2]float64{{10, 10}, {50, 10}, {50, 30}, {10, 30}}}}
	cases := []struct {
		name     string
		content  []byte
		mimeType string
		want     string
	}{
		{name: "declared png with png bytes stays png", content: whitePNG(t, 100, 100), mimeType: "image/png", want: "png"},
		{name: "declared png with jpeg bytes becomes png", content: whiteJPEG(t, 100, 100), mimeType: "image/png", want: "png"},
		{name: "declared jpeg with png bytes becomes jpeg", content: whitePNG(t, 100, 100), mimeType: "image/jpeg", want: "jpeg"},
		{name: "declared jpeg with jpeg bytes stays jpeg", content: whiteJPEG(t, 100, 100), mimeType: "image/jpeg", want: "jpeg"},
		{name: "no declared type follows the decoded png format", content: whitePNG(t, 100, 100), mimeType: "", want: "png"},
		{name: "no declared type falls back to jpeg for other formats", content: whiteJPEG(t, 100, 100), mimeType: "", want: "jpeg"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			masked, err := MaskImageFile(testCase.content, testCase.mimeType, regions, nil)
			if err != nil {
				t.Fatalf("mask image: %v", err)
			}
			_, format, err := image.Decode(bytes.NewReader(masked))
			if err != nil {
				t.Fatalf("decode masked image: %v", err)
			}
			if format != testCase.want {
				t.Fatalf("mimeType %q produced %s bytes, want %s", testCase.mimeType, format, testCase.want)
			}
		})
	}
}

func TestMaskPDFFileFailsForRegionsOnMissingPages(t *testing.T) {
	t.Parallel()

	regions := []Region{{PageNumber: 3, Polygon: [4][2]float64{{10, 10}, {50, 10}, {50, 30}, {10, 30}}}}
	if _, err := MaskPDFFile(blankPDF(200, 200), regions, nil); err == nil {
		t.Fatalf("expected masking to fail for a page the document does not have")
	}
}

// A page that declares no size is a property of the uploaded document, not of the
// coordinates the inference endpoint reported: only a PDF whose MediaBox is empty
// reaches this refusal, because an image with a zero dimension is rejected while
// its header is read. Sending the same file again cannot succeed, so this must not
// be reported as an unusable upstream answer that is worth a retry.
func TestMaskPDFFileBlamesTheDocumentForAPageWithoutDimensions(t *testing.T) {
	t.Parallel()

	regions := []Region{{PageNumber: 1, Polygon: [4][2]float64{{10, 10}, {100, 10}, {100, 40}, {10, 40}}}}
	_, err := MaskPDFFile(blankPDF(0, 0), regions, nil)
	if err == nil {
		t.Fatalf("expected masking to fail for a page with no usable dimensions")
	}
	var placement *RegionPlacementError
	if errors.As(err, &placement) {
		t.Fatalf("error %q is a *RegionPlacementError, but the zero-sized page came from the upload", err)
	}

	// The same coordinates on a page that does report a size are placed fine, so the
	// refusal above is about the document rather than the region.
	if _, err := MaskPDFFile(blankPDF(595, 842), regions, nil); err != nil {
		t.Fatalf("masking a page that reports its size: %v", err)
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

func whiteJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg encode: %v", err)
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
