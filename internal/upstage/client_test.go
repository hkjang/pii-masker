package upstage

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pii-masker/internal/config"
	"pii-masker/internal/document"
)

func TestParseDocumentConvertsJPEGToPNGForUpstream(t *testing.T) {
	t.Parallel()

	type observedRequest struct {
		FileName string
		MIMEType string
	}

	observed := observedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart reader: %v", err)
		}

		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			if part.FormName() != "document" {
				continue
			}
			observed.FileName = part.FileName()
			observed.MIMEType = normalizeObservedMIME(part.Header.Get("Content-Type"))
			if _, err := io.ReadAll(part); err != nil {
				t.Fatalf("read document part: %v", err)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "pii",
			"result": map[string]any{
				"fields": []any{},
				"metadata": map[string]any{
					"pages": []any{
						map[string]any{"page": 1, "width": 400, "height": 200},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewClient(config.UpstageConfig{
		BaseURL: server.URL,
		Timeout: 5 * time.Second,
		Model:   "pii",
		Lang:    "ko",
		Schema:  "oac",
	})

	attachment := document.NewAttachment("sample.jpg", "image/jpeg", createJPEG(t, 400, 200))
	result, statusCode, _, err := client.ParseDocument(context.Background(), attachment, ParseOptions{})
	if err != nil {
		t.Fatalf("parse document: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", statusCode)
	}
	if result.Attachment.MIMEType != "image/jpeg" {
		t.Fatalf("result attachment mime should stay original, got %s", result.Attachment.MIMEType)
	}
	if observed.MIMEType != "image/png" {
		t.Fatalf("expected upstream content type image/png, got %s", observed.MIMEType)
	}
	if observed.FileName != "sample.png" {
		t.Fatalf("expected upstream filename sample.png, got %s", observed.FileName)
	}
}

func createJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.White)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func normalizeObservedMIME(value string) string {
	if mediaType, _, err := mime.ParseMediaType(value); err == nil {
		return mediaType
	}
	return value
}
