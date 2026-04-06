package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func UpstageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		reader, err := r.MultipartReader()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "multipart request required"})
			return
		}

		var (
			filename string
			mimeType string
			content  []byte
		)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			if part.FormName() != "document" {
				_, _ = io.Copy(io.Discard, part)
				continue
			}
			filename = part.FileName()
			mimeType = part.Header.Get("Content-Type")
			content, err = io.ReadAll(part)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
		}

		if len(content) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "document part is required"})
			return
		}

		mimeType = normalizeMockMIMEType(mimeType)
		if !isMockSupportedMIMEType(mimeType) {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{
				"error": map[string]any{
					"message": "unsupported document format",
					"detail":  "embedded mock accepts only PDF and PNG upstream payloads",
				},
			})
			return
		}

		width, height, pages := detectDimensions(content, mimeType)
		payload := buildPayload(width, height, pages)
		response := map[string]any{
			"type":   "mock-pii",
			"api":    "embedded-mock",
			"model":  "pii",
			"result": payload,
			"usage": map[string]any{
				"pages": pages,
			},
			"numBilledPages": pages,
			"content": map[string]any{
				"text": fmt.Sprintf("mock response for %s", filename),
			},
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func buildPayload(width, height float64, pages int) map[string]any {
	box := func(x, y, w, h float64) []any {
		return []any{
			map[string]any{
				"page": 1,
				"vertices": []any{
					map[string]any{"x": x, "y": y},
					map[string]any{"x": x + w, "y": y},
					map[string]any{"x": x + w, "y": y + h},
					map[string]any{"x": x, "y": y + h},
				},
			},
		}
	}

	return map[string]any{
		"apiVersion":   "1.1",
		"documentType": "pii",
		"confidence":   0.99,
		"fields": []any{
			map[string]any{"key": "개인정보.이름", "value": "홍길동", "confidence": 0.99, "boundingBoxes": box(width*0.10, height*0.10, width*0.18, height*0.08)},
			map[string]any{"key": "개인정보.주민등록번호", "value": "800901-1234567", "confidence": 0.97, "boundingBoxes": box(width*0.10, height*0.24, width*0.28, height*0.08)},
			map[string]any{"key": "개인정보.휴대폰번호", "value": "010-1234-5678", "confidence": 0.96, "boundingBoxes": box(width*0.10, height*0.38, width*0.26, height*0.08)},
			map[string]any{"key": "개인정보.이메일", "value": "abcdefg@naver.com", "confidence": 0.95, "boundingBoxes": box(width*0.10, height*0.52, width*0.40, height*0.08)},
			map[string]any{"key": "개인정보.주소", "value": "서울 영등포구 국제금융로 10", "confidence": 0.94, "boundingBoxes": box(width*0.10, height*0.66, width*0.46, height*0.08)},
		},
		"metadata": map[string]any{
			"pages": []any{
				map[string]any{
					"page":   1,
					"width":  width,
					"height": height,
				},
			},
		},
	}
}

func detectDimensions(content []byte, mimeType string) (float64, float64, int) {
	if strings.Contains(strings.ToLower(mimeType), "pdf") {
		pdfmodel.ConfigPath = "disable"
		conf := pdfmodel.NewDefaultConfiguration()
		conf.ValidationMode = pdfmodel.ValidationRelaxed
		if dims, err := api.PageDims(bytes.NewReader(content), conf); err == nil && len(dims) > 0 {
			return dims[0].Width, dims[0].Height, len(dims)
		}
		return 595, 842, 1
	}

	if cfg, _, err := image.DecodeConfig(bytes.NewReader(content)); err == nil {
		return float64(cfg.Width), float64(cfg.Height), 1
	}
	return 800, 600, 1
}

func normalizeMockMIMEType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if mediaType, _, err := mime.ParseMediaType(value); err == nil {
		value = mediaType
	}
	switch value {
	case "image/jpg", "image/pjpeg":
		return "image/jpeg"
	default:
		return value
	}
}

func isMockSupportedMIMEType(mimeType string) bool {
	switch mimeType {
	case "application/pdf", "image/png":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
