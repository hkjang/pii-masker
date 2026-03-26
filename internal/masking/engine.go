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

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

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
		key := normalizeFieldKey(extractFieldValue(typed["key"]))
		if key == "" {
			key = normalizeFieldKey(extractFieldValue(typed["name"]))
		}
		if key == "" {
			key = normalizeFieldKey(extractFieldValue(typed["label"]))
		}

		fieldValue := extractFieldValue(typed["refinedValue"])
		if fieldValue == "" {
			fieldValue = extractFieldValue(typed["value"])
		}
		if fieldValue == "" {
			fieldValue = extractFieldValue(typed["chips"])
		}
		if fieldValue == "" {
			fieldValue = extractFieldValue(typed["content"])
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
				if pageNumber, ok := intValue(typed["pageNumber"]); ok {
					entry.PageNumber = pageNumber
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
		key := strings.TrimSpace(extractFieldValue(typed["key"]))
		fieldValue := extractFieldValue(typed["refinedValue"])
		if fieldValue == "" {
			fieldValue = extractFieldValue(typed["value"])
		}
		if fieldValue == "" {
			fieldValue = extractFieldValue(typed["content"])
		}
		if key != "" && fieldValue != "" {
			masked := MaskValue(key, fieldValue)
			spans := ComputeMaskedRuneSpans(fieldValue, masked.MaskedValue)
			bboxes := parseBoundingBoxes(typed["boundingBoxes"])
			entityPage := 0
			if pageNumber, ok := intValue(typed["pageNumber"]); ok && pageNumber > 0 {
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
				subRegions := buildSubRegionsFromBBox(bbox.Polygon, fieldValue, spans)
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
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	var result []parsedBBox
	for _, item := range arr {
		if bbox, ok := parsePageVerticesObject(item); ok {
			result = append(result, bbox)
			continue
		}
		if poly, ok := parsePolygonPoints(item); ok {
			result = append(result, parsedBBox{Polygon: poly})
			continue
		}
		if poly, ok := parseRectObject(item); ok {
			result = append(result, parsedBBox{Polygon: poly})
			continue
		}
		if poly, ok := parseFlatCoords(item); ok {
			result = append(result, parsedBBox{Polygon: poly})
			continue
		}
	}
	return result
}

func parsePolygonPoints(item any) ([4][2]float64, bool) {
	polyArr, ok := item.([]any)
	if !ok || len(polyArr) != 4 {
		return [4][2]float64{}, false
	}
	var poly [4][2]float64
	for index, ptRaw := range polyArr {
		ptArr, ok := ptRaw.([]any)
		if !ok || len(ptArr) != 2 {
			return [4][2]float64{}, false
		}
		x, xOK := toFloat64(ptArr[0])
		y, yOK := toFloat64(ptArr[1])
		if !xOK || !yOK {
			return [4][2]float64{}, false
		}
		poly[index] = [2]float64{x, y}
	}
	return poly, true
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
	verts, ok := m["vertices"].([]any)
	if !ok || len(verts) != 4 {
		return parsedBBox{}, false
	}
	var poly [4][2]float64
	for index, vertex := range verts {
		vertexMap, ok := vertex.(map[string]any)
		if !ok {
			return parsedBBox{}, false
		}
		x, xOK := toFloat64(vertexMap["x"])
		y, yOK := toFloat64(vertexMap["y"])
		if !xOK || !yOK {
			return parsedBBox{}, false
		}
		poly[index] = [2]float64{x, y}
	}
	pageNumber := 0
	if value, ok := intValue(m["page"]); ok && value > 0 {
		pageNumber = value
	}
	return parsedBBox{Polygon: poly, PageNumber: pageNumber}, true
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

func MaskImageFile(content []byte, mimeType string, regions []Region, pageSizes map[int]PageSize) ([]byte, error) {
	img, format, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	scaleX, scaleY := 1.0, 1.0
	if ps, ok := pageSizes[1]; ok && ps.Width > 0 && ps.Height > 0 {
		scaleX = imgW / ps.Width
		scaleY = imgH / ps.Height
	}

	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, img, bounds.Min, draw.Src)

	black := image.NewUniform(color.Black)
	for _, region := range regions {
		minX, minY, maxX, maxY := polygonBounds(region.Polygon)
		rect := image.Rect(
			int(math.Floor(minX*scaleX)),
			int(math.Floor(minY*scaleY)),
			int(math.Ceil(maxX*scaleX)),
			int(math.Ceil(maxY*scaleY)),
		)
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
	pdfmodel.ConfigPath = "disable"
	conf := pdfmodel.NewDefaultConfiguration()
	conf.ValidationMode = pdfmodel.ValidationRelaxed

	pdfPageDims, err := api.PageDims(bytes.NewReader(content), conf)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF page dimensions: %w", err)
	}

	byPage := map[int][]Region{}
	for _, region := range regions {
		byPage[region.PageNumber] = append(byPage[region.PageNumber], region)
	}

	wmMap := map[int][]*pdfmodel.Watermark{}
	for pageNumber, pageRegions := range byPage {
		if pageNumber < 1 || pageNumber > len(pdfPageDims) {
			continue
		}
		pdfW := pdfPageDims[pageNumber-1].Width
		pdfH := pdfPageDims[pageNumber-1].Height

		apiW, apiH := pdfW, pdfH
		if ps, ok := pageSizes[pageNumber]; ok && ps.Width > 0 && ps.Height > 0 {
			apiW = ps.Width
			apiH = ps.Height
		}
		scaleX := pdfW / apiW
		scaleY := pdfH / apiH

		for _, region := range pageRegions {
			minX, minY, maxX, maxY := polygonBounds(region.Polygon)
			x := minX * scaleX
			y := minY * scaleY
			width := (maxX - minX) * scaleX
			height := (maxY - minY) * scaleY
			if width < 1 || height < 1 {
				continue
			}

			regionImg := createBlackPNG(int(math.Ceil(width)), int(math.Ceil(height)))
			pdfY := pdfH - y - height
			desc := fmt.Sprintf("position:bl, offset:%.1f %.1f, scalefactor:1.0 abs, rotation:0, opacity:1", x, pdfY)
			wm, wmErr := api.ImageWatermarkForReader(bytes.NewReader(regionImg), desc, true, false, types.POINTS)
			if wmErr != nil {
				return nil, fmt.Errorf("pdfcpu watermark create error on page %d: %w", pageNumber, wmErr)
			}
			wmMap[pageNumber] = append(wmMap[pageNumber], wm)
		}
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

func ParsePayload(raw json.RawMessage, debugBody string) any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" {
		var payload any
		if err := json.Unmarshal(raw, &payload); err == nil {
			return payload
		}
	}
	debugBody = strings.TrimSpace(debugBody)
	if debugBody == "" {
		return nil
	}
	var payload any
	if err := json.Unmarshal([]byte(debugBody), &payload); err == nil {
		return payload
	}
	return nil
}

func normalizeFieldKey(value string) string {
	return strings.TrimSpace(value)
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
