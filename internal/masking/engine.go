package masking

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"pii-masker/internal/document"
)

// pdfcpu keeps its config path in a package level variable, so it is disabled once
// at startup instead of on every request to avoid concurrent writes.
func init() {
	pdfmodel.ConfigPath = "disable"
}

type Region struct {
	PageNumber int
	Polygon    [4][2]float64
}

type PageSize struct {
	Width  float64
	Height float64
}

type FieldEntry struct {
	Key           string      `json:"key"`
	Value         string      `json:"-"`
	MaskedValue   string      `json:"masked_value"`
	Rule          AppliedRule `json:"rule"`
	Confidence    float64     `json:"confidence,omitempty"`
	HasConfidence bool        `json:"-"`
	PageNumber    int         `json:"page,omitempty"`
}

func BuildFieldEntries(payload any) []FieldEntry {
	entries := make([]FieldEntry, 0, 16)
	seen := map[string]struct{}{}
	collectFieldEntriesRecursive(payload, seen, &entries)
	return entries
}

func collectFieldEntriesRecursive(value any, seen map[string]struct{}, entries *[]FieldEntry) {
	switch typed := value.(type) {
	case map[string]any:
		key := extractEntityKey(typed)
		fieldValue := extractEntityValue(typed)
		if key == "" && fieldValue != "" && hasGeometryHints(typed) {
			key = "generic"
		}
		if key != "" && fieldValue != "" {
			signature := key + "\x00" + fieldValue
			if _, ok := seen[signature]; !ok {
				masked := MaskValue(key, fieldValue)
				entry := FieldEntry{
					Key:         key,
					Value:       fieldValue,
					MaskedValue: masked.MaskedValue,
					Rule:        masked.Rule,
				}
				if confidence, ok := numberValue(typed["confidence"]); ok {
					entry.Confidence = confidence
					entry.HasConfidence = true
				} else if confidence, ok := numberValue(typed["entityConfidence"]); ok {
					entry.Confidence = confidence
					entry.HasConfidence = true
				}
				if pageNumber, ok := extractEntityPageNumber(typed); ok {
					entry.PageNumber = pageNumber
				} else if bboxes := parseBoundingBoxes(extractGeometryCandidate(typed)); len(bboxes) > 0 {
					// Endpoints that put the page on each box rather than on
					// the field still get a page in the summary.
					entry.PageNumber = bboxes[0].PageNumber
				}
				seen[signature] = struct{}{}
				*entries = append(*entries, entry)
			}
		}
		for _, nested := range typed {
			collectFieldEntriesRecursive(nested, seen, entries)
		}
	case []any:
		for _, item := range typed {
			collectFieldEntriesRecursive(item, seen, entries)
		}
	}
}

func CollectMaskRegions(payload any) []Region {
	var regions []Region
	collectMaskRegionsRecursive(payload, &regions)
	return regions
}

func collectMaskRegionsRecursive(value any, regions *[]Region) {
	switch typed := value.(type) {
	case map[string]any:
		key := extractEntityKey(typed)
		fieldValue := extractEntityValue(typed)
		if key == "" && fieldValue != "" && hasGeometryHints(typed) {
			key = "generic"
		}
		if key != "" && fieldValue != "" {
			masked := MaskValue(key, fieldValue)
			spans := ComputeMaskedRuneSpans(fieldValue, masked.MaskedValue)
			bboxes := parseBoundingBoxes(extractGeometryCandidate(typed))
			entityPage := 0
			if pageNumber, ok := extractEntityPageNumber(typed); ok && pageNumber > 0 {
				entityPage = pageNumber
			}
			for _, bbox := range bboxes {
				pageNumber := bbox.PageNumber
				if pageNumber == 0 {
					pageNumber = entityPage
				}
				if pageNumber == 0 {
					pageNumber = 1
				}
				// A value that comes with several boxes is wrapped over lines or
				// repeated on the document, and nothing says which runes each box
				// holds. Spreading the masked spans over every box would blank
				// the wrong characters and leave the sensitive ones readable, so
				// every box is covered in full instead.
				var subRegions []Region
				if len(bboxes) == 1 {
					subRegions = buildSubRegionsFromBBox(bbox.Polygon, fieldValue, spans)
				}
				if len(subRegions) == 0 {
					subRegions = []Region{{PageNumber: pageNumber, Polygon: bbox.Polygon}}
				}
				for _, region := range subRegions {
					region.PageNumber = pageNumber
					*regions = append(*regions, region)
				}
			}
		}
		for _, nested := range typed {
			collectMaskRegionsRecursive(nested, regions)
		}
	case []any:
		for _, item := range typed {
			collectMaskRegionsRecursive(item, regions)
		}
	}
}

func buildSubRegionsFromBBox(poly [4][2]float64, original string, spans [][2]int) []Region {
	if len(spans) == 0 {
		return nil
	}
	runes := []rune(original)
	if len(runes) == 0 {
		return nil
	}

	minX, minY, maxX, maxY := polygonBounds(poly)
	totalRunes := float64(len(runes))
	regions := make([]Region, 0, len(spans))
	for _, span := range spans {
		startX := minX + ((maxX - minX) * float64(span[0]) / totalRunes)
		endX := minX + ((maxX - minX) * float64(span[1]) / totalRunes)
		if endX-startX < 1 {
			endX = startX + 1
		}
		regions = append(regions, Region{
			Polygon: [4][2]float64{
				{startX, minY},
				{endX, minY},
				{endX, maxY},
				{startX, maxY},
			},
		})
	}
	return regions
}

type parsedBBox struct {
	Polygon    [4][2]float64
	PageNumber int
}

func parseBoundingBoxes(raw any) []parsedBBox {
	switch typed := unwrapJSONValue(raw).(type) {
	case nil:
		return nil
	case map[string]any:
		if bbox, ok := parsePageVerticesObject(typed); ok {
			return []parsedBBox{bbox}
		}
		if poly, ok := parsePolygonObject(typed); ok {
			return []parsedBBox{{Polygon: poly, PageNumber: extractPageNumberFromMap(typed)}}
		}
		if poly, ok := parseRectObject(typed); ok {
			return []parsedBBox{{Polygon: poly, PageNumber: extractPageNumberFromMap(typed)}}
		}
		return nil
	case []any:
		if len(typed) == 0 {
			return nil
		}
		var result []parsedBBox
		for _, item := range typed {
			result = append(result, parseBoundingBoxes(item)...)
		}
		return result
	default:
		return nil
	}
}

func parsePolygonPoints(item any) ([4][2]float64, bool) {
	polyArr, ok := item.([]any)
	if !ok || len(polyArr) != 4 {
		return [4][2]float64{}, false
	}
	var poly [4][2]float64
	for index, ptRaw := range polyArr {
		if ptArr, ok := ptRaw.([]any); ok && len(ptArr) == 2 {
			x, xOK := toFloat64(ptArr[0])
			y, yOK := toFloat64(ptArr[1])
			if !xOK || !yOK {
				return [4][2]float64{}, false
			}
			poly[index] = [2]float64{x, y}
			continue
		}
		ptMap, ok := ptRaw.(map[string]any)
		if !ok {
			return [4][2]float64{}, false
		}
		x, xOK := toFloat64(ptMap["x"])
		y, yOK := toFloat64(ptMap["y"])
		if !xOK || !yOK {
			return [4][2]float64{}, false
		}
		poly[index] = [2]float64{x, y}
	}
	return poly, true
}

func parsePolygonObject(item any) ([4][2]float64, bool) {
	m, ok := item.(map[string]any)
	if !ok {
		return [4][2]float64{}, false
	}
	for _, key := range []string{"polygon", "poly", "points", "quad", "coordinates"} {
		if poly, ok := parsePolygonPoints(m[key]); ok {
			return poly, true
		}
		if poly, ok := parseFlatCoords(m[key]); ok {
			return poly, true
		}
	}
	return [4][2]float64{}, false
}

func parseRectObject(item any) ([4][2]float64, bool) {
	m, ok := item.(map[string]any)
	if !ok {
		return [4][2]float64{}, false
	}
	x, xOK := rectFloat(m, "x")
	y, yOK := rectFloat(m, "y")
	if !xOK || !yOK {
		return [4][2]float64{}, false
	}
	w, wOK := rectFloat(m, "width", "w")
	h, hOK := rectFloat(m, "height", "h")
	if !wOK || !hOK {
		return [4][2]float64{}, false
	}
	return [4][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}, true
}

func rectFloat(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := toFloat64(m[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func parseFlatCoords(item any) ([4][2]float64, bool) {
	arr, ok := item.([]any)
	if !ok || len(arr) != 8 {
		return [4][2]float64{}, false
	}
	var poly [4][2]float64
	for index := 0; index < 4; index++ {
		x, xOK := toFloat64(arr[index*2])
		y, yOK := toFloat64(arr[index*2+1])
		if !xOK || !yOK {
			return [4][2]float64{}, false
		}
		poly[index] = [2]float64{x, y}
	}
	return poly, true
}

func parsePageVerticesObject(item any) (parsedBBox, bool) {
	m, ok := item.(map[string]any)
	if !ok {
		return parsedBBox{}, false
	}
	for _, key := range []string{"vertices", "points"} {
		if poly, ok := parsePolygonPoints(m[key]); ok {
			return parsedBBox{Polygon: poly, PageNumber: extractPageNumberFromMap(m)}, true
		}
	}
	return parsedBBox{}, false
}

func ExtractPageSizes(payload any) map[int]PageSize {
	sizes := map[int]PageSize{}
	extractPageSizesRecursive(payload, sizes)
	return sizes
}

func extractPageSizesRecursive(value any, sizes map[int]PageSize) {
	switch typed := value.(type) {
	case map[string]any:
		if psRaw, ok := typed["pageSizes"]; ok {
			if psArr, ok := psRaw.([]any); ok {
				for index, item := range psArr {
					if m, ok := item.(map[string]any); ok {
						width, widthOK := toFloat64(m["width"])
						height, heightOK := toFloat64(m["height"])
						if widthOK && heightOK && width > 0 && height > 0 {
							sizes[index+1] = PageSize{Width: width, Height: height}
						}
					}
				}
			}
		}
		for _, key := range []string{"pages"} {
			if psRaw, ok := typed[key]; ok {
				if psArr, ok := psRaw.([]any); ok {
					for index, item := range psArr {
						if m, ok := item.(map[string]any); ok {
							width, widthOK := toFloat64(m["width"])
							height, heightOK := toFloat64(m["height"])
							if widthOK && heightOK && width > 0 && height > 0 {
								pageNumber := index + 1
								if pn, ok := intValue(m["page"]); ok && pn > 0 {
									pageNumber = pn
								}
								sizes[pageNumber] = PageSize{Width: width, Height: height}
							}
						}
					}
				}
			}
		}
		for _, nested := range typed {
			extractPageSizesRecursive(nested, sizes)
		}
	case []any:
		for _, item := range typed {
			extractPageSizesRecursive(item, sizes)
		}
	}
}

func polygonBounds(poly [4][2]float64) (minX, minY, maxX, maxY float64) {
	minX, minY = poly[0][0], poly[0][1]
	maxX, maxY = minX, minY
	for _, point := range poly[1:] {
		minX = math.Min(minX, point[0])
		minY = math.Min(minY, point[1])
		maxX = math.Max(maxX, point[0])
		maxY = math.Max(maxY, point[1])
	}
	return
}

// placedRegion is a mask rectangle in the target page's own units, with the
// origin at the top left corner.
type placedRegion struct {
	minX, minY, maxX, maxY float64
}

// minPlacedSize is the smallest box, in target units, still considered to land on
// the page. Anything thinner was clipped away or never overlapped the page.
const minPlacedSize = 0.5

// placeRegion maps a region reported by the inference endpoint onto a page of the
// given size. Coordinates come in three flavours: normalized to the page (0..1),
// pixels of the page image the endpoint rendered (whose size it reports alongside),
// or the page's own units when no size is reported. A region that would end up off
// the page is an error rather than a stamp nobody can see: it means the units were
// misread, and returning the document as "masked" would leak the field it covers.
func placeRegion(region Region, apiSize PageSize, hasAPISize bool, target PageSize) (placedRegion, error) {
	if target.Width <= 0 || target.Height <= 0 {
		return placedRegion{}, fmt.Errorf("page %d has no usable dimensions", region.PageNumber)
	}
	minX, minY, maxX, maxY := polygonBounds(region.Polygon)
	if math.IsNaN(minX+minY+maxX+maxY) || math.IsInf(minX+minY+maxX+maxY, 0) {
		return placedRegion{}, fmt.Errorf("mask region on page %d has invalid coordinates", region.PageNumber)
	}

	scaleX, scaleY := 1.0, 1.0
	switch {
	case isNormalizedBox(minX, minY, maxX, maxY):
		scaleX, scaleY = target.Width, target.Height
	case hasAPISize && apiSize.Width > 0 && apiSize.Height > 0:
		scaleX = target.Width / apiSize.Width
		scaleY = target.Height / apiSize.Height
	default:
		// Without a reported page size the coordinates are taken as page units.
		// If they overflow the page they were pixels of some unknown rendering,
		// and scaling them cannot be guessed.
		if maxX > target.Width*(1+overflowTolerance) || maxY > target.Height*(1+overflowTolerance) {
			return placedRegion{}, fmt.Errorf(
				"mask region on page %d spans (%.1f, %.1f)-(%.1f, %.1f) but the page is %.1f x %.1f and the upstream response reported no page size",
				region.PageNumber, minX, minY, maxX, maxY, target.Width, target.Height)
		}
	}

	placed := placedRegion{
		minX: math.Max(minX*scaleX, 0),
		minY: math.Max(minY*scaleY, 0),
		maxX: math.Min(maxX*scaleX, target.Width),
		maxY: math.Min(maxY*scaleY, target.Height),
	}
	if placed.maxX-placed.minX < minPlacedSize || placed.maxY-placed.minY < minPlacedSize {
		return placedRegion{}, fmt.Errorf(
			"mask region on page %d spans (%.1f, %.1f)-(%.1f, %.1f) and does not land on the %.1f x %.1f page",
			region.PageNumber, minX, minY, maxX, maxY, target.Width, target.Height)
	}
	// A box that shrank to a hairline keeps at least one unit so it still shows.
	if placed.maxX-placed.minX < 1 {
		placed.maxX = math.Min(placed.minX+1, target.Width)
		placed.minX = placed.maxX - 1
	}
	if placed.maxY-placed.minY < 1 {
		placed.maxY = math.Min(placed.minY+1, target.Height)
		placed.minY = placed.maxY - 1
	}
	return placed, nil
}

// overflowTolerance is how far past the page edge an unscaled coordinate may reach
// before it is treated as being in the wrong units rather than a slightly loose box.
const overflowTolerance = 0.05

// isNormalizedBox reports whether every coordinate lies within the unit square, the
// convention of endpoints that report positions as fractions of the page.
func isNormalizedBox(minX, minY, maxX, maxY float64) bool {
	const slack = 1e-6
	return minX >= -slack && minY >= -slack && maxX <= 1+slack && maxY <= 1+slack && (maxX > 0 || maxY > 0)
}

func MaskImageFile(content []byte, mimeType string, regions []Region, pageSizes map[int]PageSize) ([]byte, error) {
	img, format, err := document.DecodeImage(content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	target := PageSize{Width: float64(bounds.Dx()), Height: float64(bounds.Dy())}
	apiSize, hasAPISize := pageSizes[1]

	rects := make([]image.Rectangle, 0, len(regions))
	for _, region := range regions {
		placed, err := placeRegion(region, apiSize, hasAPISize, target)
		if err != nil {
			return nil, err
		}
		rects = append(rects, image.Rect(
			bounds.Min.X+int(math.Floor(placed.minX)),
			bounds.Min.Y+int(math.Floor(placed.minY)),
			bounds.Min.X+int(math.Ceil(placed.maxX)),
			bounds.Min.Y+int(math.Ceil(placed.maxY)),
		))
	}

	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, img, bounds.Min, draw.Src)

	black := image.NewUniform(color.Black)
	for _, rect := range rects {
		draw.Draw(dst, rect, black, image.Point{}, draw.Src)
	}

	var buf bytes.Buffer
	switch {
	case format == "png" || strings.Contains(strings.ToLower(mimeType), "png"):
		err = png.Encode(&buf, dst)
	default:
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 95})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to encode masked image: %w", err)
	}
	return buf.Bytes(), nil
}

func MaskPDFFile(content []byte, regions []Region, pageSizes map[int]PageSize) (result []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("pdfcpu panic: %v", recovered)
			result = nil
		}
	}()
	return maskPDFFileInternal(content, regions, pageSizes)
}

func maskPDFFileInternal(content []byte, regions []Region, pageSizes map[int]PageSize) ([]byte, error) {
	conf := pdfmodel.NewDefaultConfiguration()
	conf.ValidationMode = pdfmodel.ValidationRelaxed

	pdfPageDims, err := api.PageDims(bytes.NewReader(content), conf)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF page dimensions: %w", err)
	}

	wmMap := map[int][]*pdfmodel.Watermark{}
	for _, region := range regions {
		pageNumber := region.PageNumber
		if pageNumber < 1 || pageNumber > len(pdfPageDims) {
			return nil, fmt.Errorf("mask region refers to page %d but the document has %d page(s)", pageNumber, len(pdfPageDims))
		}
		target := PageSize{Width: pdfPageDims[pageNumber-1].Width, Height: pdfPageDims[pageNumber-1].Height}
		apiSize, hasAPISize := pageSizes[pageNumber]

		placed, err := placeRegion(region, apiSize, hasAPISize, target)
		if err != nil {
			return nil, err
		}
		width := placed.maxX - placed.minX
		height := placed.maxY - placed.minY

		// The stamp image is drawn at one point per pixel, so its size is rounded
		// up and the box grows outward rather than leaving a sliver uncovered.
		regionImg := createBlackPNG(int(math.Ceil(width)), int(math.Ceil(height)))
		pdfY := target.Height - placed.minY - math.Ceil(height)
		desc := fmt.Sprintf("position:bl, offset:%.2f %.2f, scalefactor:1.0 abs, rotation:0, opacity:1", placed.minX, pdfY)
		wm, wmErr := api.ImageWatermarkForReader(bytes.NewReader(regionImg), desc, true, false, types.POINTS)
		if wmErr != nil {
			return nil, fmt.Errorf("pdfcpu watermark create error on page %d: %w", pageNumber, wmErr)
		}
		wmMap[pageNumber] = append(wmMap[pageNumber], wm)
	}

	if len(wmMap) == 0 {
		return nil, fmt.Errorf("no valid watermark regions to apply")
	}

	var buf bytes.Buffer
	if err := api.AddWatermarksSliceMap(bytes.NewReader(content), &buf, wmMap, conf); err != nil {
		return nil, fmt.Errorf("failed to apply PDF stamps: %w", err)
	}
	return buf.Bytes(), nil
}

func createBlackPNG(width, height int) []byte {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ParsePayload decodes the entity payload of an inference response. The "result"
// member is preferred; a response that carries its fields at the top level is read
// from body, which must be the complete body as received. A truncated or pretty
// printed copy would fail to decode and silently report a document with no PII.
func ParsePayload(raw json.RawMessage, body []byte) any {
	if payload := parseJSONPayload(raw); payload != nil {
		return unwrapPayloadEnvelope(payload)
	}
	if payload := parseJSONPayload(body); payload != nil {
		return unwrapPayloadEnvelope(payload)
	}
	return nil
}

// ContainsGeometry reports whether any object in payload carries bounding box
// style coordinates. A response that located something on the page but yielded no
// field entry was not understood, which is different from a document without PII.
func ContainsGeometry(payload any) bool {
	switch typed := payload.(type) {
	case map[string]any:
		if hasGeometryHints(typed) {
			return true
		}
		for _, nested := range typed {
			if ContainsGeometry(nested) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if ContainsGeometry(item) {
				return true
			}
		}
	}
	return false
}

func parseJSONPayload(raw []byte) any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil
	}
	return payload
}

func unwrapPayloadEnvelope(value any) any {
	current := value
	for range 8 {
		switch typed := current.(type) {
		case string:
			next := parseJSONPayload([]byte(typed))
			if next == nil {
				return current
			}
			current = next
		case map[string]any:
			switch {
			case looksLikeEnvelopeMap(typed) && typed["result"] != nil:
				current = typed["result"]
			case looksLikeEnvelopeMap(typed) && typed["payload"] != nil:
				current = typed["payload"]
			case looksLikeEnvelopeMap(typed) && typed["data"] != nil:
				current = typed["data"]
			default:
				return current
			}
		default:
			return current
		}
	}
	return current
}

func looksLikeEnvelopeMap(value map[string]any) bool {
	for _, key := range []string{"type", "api", "model", "usage", "content", "elements", "status", "result"} {
		if _, ok := value[key]; ok {
			return true
		}
	}
	return false
}

func unwrapJSONValue(value any) any {
	switch typed := value.(type) {
	case string:
		if parsed := parseJSONPayload([]byte(typed)); parsed != nil {
			return parsed
		}
		return value
	default:
		return value
	}
}

func normalizeFieldKey(value string) string {
	return strings.TrimSpace(value)
}

func extractEntityKey(value map[string]any) string {
	for _, key := range []string{"key", "name", "label", "fieldName", "fieldType", "type", "category", "class"} {
		if extracted := normalizeFieldKey(extractFieldValue(value[key])); extracted != "" {
			return extracted
		}
	}
	return ""
}

// extractEntityValue returns the text of an entity. The value as it is printed on
// the document comes first: masked character positions are mapped onto the
// bounding box by rune index, so a refined form such as "01012345678" for a
// printed "010-1234-5678" would shift the mask onto the wrong characters.
func extractEntityValue(value map[string]any) string {
	for _, key := range []string{"value", "rawValue", "text", "ocrText", "content", "normalizedValue", "refinedValue", "chips", "label"} {
		if extracted := extractFieldValue(value[key]); extracted != "" {
			return extracted
		}
	}
	return ""
}

func extractGeometryCandidate(value map[string]any) any {
	for _, key := range []string{"boundingBoxes", "boundingBox", "bounding_boxes", "bounding_box", "bbox", "bboxes", "box", "boxes", "polygon", "poly", "vertices", "points", "quad", "coordinates", "position"} {
		if candidate, ok := value[key]; ok && candidate != nil {
			return candidate
		}
	}
	return nil
}

func hasGeometryHints(value map[string]any) bool {
	return extractGeometryCandidate(value) != nil
}

func extractEntityPageNumber(value map[string]any) (int, bool) {
	pageNumber := extractPageNumberFromMap(value)
	return pageNumber, pageNumber > 0
}

func extractPageNumberFromMap(value map[string]any) int {
	for _, key := range []string{"pageNumber", "page", "pageNo", "pageIndex", "page_index"} {
		if pageNumber, ok := intValue(value[key]); ok && pageNumber > 0 {
			return pageNumber
		}
	}
	return 0
}

func extractFieldValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if inner := extractFieldValue(item); inner != "" {
				parts = append(parts, inner)
			}
		}
		return strings.TrimSpace(strings.Join(parts, ", "))
	case map[string]any:
		for _, key := range []string{"value", "text", "content", "label"} {
			if inner := extractFieldValue(typed[key]); inner != "" {
				return inner
			}
		}
	}
	return ""
}

func toFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
