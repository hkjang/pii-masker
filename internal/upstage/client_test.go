package upstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestHostAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		target  string
		allowed []string
		want    bool
	}{
		{name: "exact host", target: "https://api.upstage.ai/inference", allowed: []string{"api.upstage.ai"}, want: true},
		{name: "host with port entry", target: "http://127.0.0.1:9100/inference", allowed: []string{"127.0.0.1:9100"}, want: true},
		{name: "port ignored for bare host entry", target: "https://api.upstage.ai:8443/inference", allowed: []string{"api.upstage.ai"}, want: true},
		{name: "case insensitive", target: "https://API.Upstage.AI/inference", allowed: []string{" Api.Upstage.ai "}, want: true},
		{name: "other host rejected", target: "https://evil.example/inference", allowed: []string{"api.upstage.ai"}, want: false},
		{name: "other port rejected", target: "http://127.0.0.1:9101/inference", allowed: []string{"127.0.0.1:9100"}, want: false},
		{name: "empty allow list rejects", target: "https://api.upstage.ai/inference", allowed: nil, want: false},
		{name: "blank entries ignored", target: "https://api.upstage.ai/inference", allowed: []string{"", "  "}, want: false},
		{name: "url without host rejected", target: "/inference", allowed: []string{"api.upstage.ai"}, want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsedURL, err := url.Parse(testCase.target)
			if err != nil {
				t.Fatalf("parse target: %v", err)
			}
			if got := hostAllowed(parsedURL, testCase.allowed); got != testCase.want {
				t.Fatalf("hostAllowed(%q, %v) = %v, want %v", testCase.target, testCase.allowed, got, testCase.want)
			}
		})
	}
}

func TestAllowedHostsFallsBackToBaseURLHost(t *testing.T) {
	t.Parallel()

	client := NewClient(config.UpstageConfig{BaseURL: "https://api.upstage.ai:443/inference"})
	hosts := client.allowedHosts()
	if len(hosts) != 1 || hosts[0] != "api.upstage.ai" {
		t.Fatalf("expected the base URL host as the default allow list, got %v", hosts)
	}
}

func TestParseDocumentRejectsHostOutsideAllowList(t *testing.T) {
	t.Parallel()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(config.UpstageConfig{
		BaseURL:    server.URL + "/inference",
		Timeout:    5 * time.Second,
		Model:      "pii",
		AllowHosts: []string{"api.upstage.ai"},
	})

	attachment := document.NewAttachment("sample.png", "image/png", createPNG(t, 40, 20))
	_, statusCode, _, err := client.ParseDocument(context.Background(), attachment, ParseOptions{})
	if err == nil {
		t.Fatal("expected the blocked host to fail the request")
	}
	var callErr *CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *CallError, got %T", err)
	}
	if callErr.Code != "upstream_host_not_allowed" {
		t.Fatalf("unexpected error code %q", callErr.Code)
	}
	if statusCode != 0 {
		t.Fatalf("expected no upstream status, got %d", statusCode)
	}
	if called {
		t.Fatal("the document must not reach a host outside the allow list")
	}
}

func TestParseDocumentRejectsRedirectToDisallowedHost(t *testing.T) {
	t.Parallel()

	redirectTargetCalled := false
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()

	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/inference", http.StatusTemporaryRedirect)
	}))
	defer entry.Close()

	client := NewClient(config.UpstageConfig{
		BaseURL:    entry.URL + "/inference",
		Timeout:    5 * time.Second,
		Model:      "pii",
		AllowHosts: []string{hostPort(t, entry.URL)},
	})

	attachment := document.NewAttachment("sample.png", "image/png", createPNG(t, 40, 20))
	_, _, _, err := client.ParseDocument(context.Background(), attachment, ParseOptions{})
	if err == nil {
		t.Fatal("expected the redirect to a disallowed host to fail the request")
	}
	var callErr *CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *CallError, got %T", err)
	}
	if callErr.Code != "upstream_host_not_allowed" {
		t.Fatalf("unexpected error code %q", callErr.Code)
	}
	if redirectTargetCalled {
		t.Fatal("the document must not follow a redirect off the allow list")
	}
}

func TestParseDocumentFollowsRedirectToAllowedHost(t *testing.T) {
	t.Parallel()

	documentReceived := false
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart form: %v", err)
		} else if _, _, err := r.FormFile("document"); err == nil {
			documentReceived = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":  "pii",
			"result": map[string]any{"fields": []any{}},
		})
	}))
	defer redirectTarget.Close()

	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/inference", http.StatusTemporaryRedirect)
	}))
	defer entry.Close()

	client := NewClient(config.UpstageConfig{
		BaseURL:    entry.URL + "/inference",
		Timeout:    5 * time.Second,
		Model:      "pii",
		AllowHosts: []string{hostPort(t, entry.URL), hostPort(t, redirectTarget.URL)},
	})

	attachment := document.NewAttachment("sample.png", "image/png", createPNG(t, 40, 20))
	_, statusCode, _, err := client.ParseDocument(context.Background(), attachment, ParseOptions{})
	if err != nil {
		t.Fatalf("parse document: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", statusCode)
	}
	if !documentReceived {
		t.Fatal("expected the redirect within the allow list to deliver the document")
	}
}

func TestTestConnectionRejectsHostOutsideAllowList(t *testing.T) {
	t.Parallel()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(config.UpstageConfig{
		BaseURL:    server.URL + "/inference",
		Timeout:    5 * time.Second,
		Model:      "pii",
		AllowHosts: []string{"api.upstage.ai"},
	})

	status, err := client.TestConnection(context.Background())
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if status.OK {
		t.Fatal("expected the connection test to fail for a disallowed host")
	}
	if status.ErrorCode != "upstream_host_not_allowed" {
		t.Fatalf("unexpected error code %q", status.ErrorCode)
	}
	if called {
		t.Fatal("the connection test must not reach a host outside the allow list")
	}
}

func hostPort(t *testing.T, rawURL string) string {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsedURL.Host
}

func createPNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.White)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
