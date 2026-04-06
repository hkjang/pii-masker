package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pii-masker/internal/config"
	"pii-masker/internal/document"
	"pii-masker/internal/jobs"
	"pii-masker/internal/upstage"
)

func TestProcessSyncFailsClosedWhenPIIHasNoBoundingBoxes(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":  "mock-pii",
			"model": "pii",
			"result": map[string]any{
				"fields": []any{
					map[string]any{
						"key":   "개인정보.이름",
						"value": "홍길동",
					},
				},
			},
		})
	}))
	defer upstream.Close()

	cfg := config.Config{
		Upstage: config.UpstageConfig{
			BaseURL:    upstream.URL,
			Timeout:    5 * time.Second,
			Model:      "pii",
			Lang:       "ko",
			Schema:     "oac",
			AllowHosts: []string{"127.0.0.1", "localhost"},
		},
		Limits: config.LimitsConfig{
			MaxFileSizeBytes: 5 * 1024 * 1024,
			MaxPages:         5,
			SupportedMIMEs:   []string{"image/png", "image/jpeg"},
		},
		Storage: config.StorageConfig{
			RootDir: t.TempDir(),
		},
	}

	jobStore, err := jobs.New(cfg.Storage.RootDir)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}

	svc := New(cfg, upstage.NewClient(cfg.Upstage), jobStore)
	input := ProcessInput{
		Attachment: document.NewAttachment("sample.png", "image/png", createWhitePNG(t, 300, 120)),
	}

	metadata, _, err := svc.ProcessSync(context.Background(), input)
	if err == nil {
		t.Fatalf("expected masking failure when bounding boxes are missing")
	}
	if metadata == nil || metadata.Error == nil {
		t.Fatalf("expected metadata error, got %#v", metadata)
	}
	if len(metadata.PIISummary) != 1 {
		t.Fatalf("expected pii summary to be preserved, got %#v", metadata.PIISummary)
	}
}

func createWhitePNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.White)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}
