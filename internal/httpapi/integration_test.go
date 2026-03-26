package httpapi_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"pii-masker/internal/app"
	"pii-masker/internal/config"
	"pii-masker/internal/core"
	"pii-masker/internal/mock"
)

func TestMaskEndpointReturnsMaskedPNG(t *testing.T) {
	t.Parallel()

	serverURL := startAppServer(t)
	pngBytes := createBlankPNG(t, 400, 200)

	requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", pngBytes, nil)
	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}

	metadata, fileBytes := parseMultipartMaskResponse(t, response)
	if metadata.Status != "completed" {
		t.Fatalf("expected completed status, got %q", metadata.Status)
	}
	if metadata.Output.MIMEType != "image/png" {
		t.Fatalf("unexpected output mime: %s", metadata.Output.MIMEType)
	}
	if len(metadata.PIISummary) == 0 {
		t.Fatalf("expected pii summary")
	}
	if metadata.PIISummary[0].MaskedValue != "홍*동" {
		t.Fatalf("unexpected first masked summary: %#v", metadata.PIISummary[0])
	}

	img, _, err := image.Decode(bytes.NewReader(fileBytes))
	if err != nil {
		t.Fatalf("decode masked png: %v", err)
	}

	blackR, blackG, blackB, _ := img.At(75, 28).RGBA()
	if blackR != 0 || blackG != 0 || blackB != 0 {
		t.Fatalf("expected masked pixel to be black, got %d %d %d", blackR, blackG, blackB)
	}

	whiteR, whiteG, whiteB, _ := img.At(45, 28).RGBA()
	if whiteR != 0xffff || whiteG != 0xffff || whiteB != 0xffff {
		t.Fatalf("expected unmasked pixel to remain white, got %d %d %d", whiteR, whiteG, whiteB)
	}
}

func TestAsyncPDFJobFlow(t *testing.T) {
	t.Parallel()

	serverURL := startAppServer(t)
	pdfBytes := createBlankPDF(400, 400)

	requestBody, contentType := buildMultipartBody(t, "sample.pdf", "application/pdf", pdfBytes, nil)
	response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}

	var metadata core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode job metadata: %v", err)
	}
	if metadata.JobID == "" {
		t.Fatalf("expected job id")
	}

	var jobMetadata core.ProcessMetadata
	for range 20 {
		time.Sleep(50 * time.Millisecond)
		jobResponse, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(metadata.JobID))
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if err := json.NewDecoder(jobResponse.Body).Decode(&jobMetadata); err != nil {
			jobResponse.Body.Close()
			t.Fatalf("decode job metadata: %v", err)
		}
		jobResponse.Body.Close()
		if jobMetadata.Status == "completed" {
			break
		}
	}

	if jobMetadata.Status != "completed" {
		t.Fatalf("expected completed job, got %#v", jobMetadata)
	}

	resultResponse, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(metadata.JobID) + "/result")
	if err != nil {
		t.Fatalf("get job result: %v", err)
	}
	defer resultResponse.Body.Close()

	if resultResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resultResponse.Body)
		t.Fatalf("unexpected result status %d: %s", resultResponse.StatusCode, string(body))
	}
	if !strings.Contains(resultResponse.Header.Get("Content-Type"), "application/pdf") {
		t.Fatalf("unexpected result content type: %s", resultResponse.Header.Get("Content-Type"))
	}
	resultBytes, _ := io.ReadAll(resultResponse.Body)
	if len(resultBytes) == 0 {
		t.Fatalf("expected non-empty pdf result")
	}
}

func TestTestConnectionEndpoint(t *testing.T) {
	t.Parallel()

	serverURL := startAppServer(t)
	response, err := http.Post(serverURL+"/v1/test-connection", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("post /v1/test-connection: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}

	var status map[string]any
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatalf("decode connection status: %v", err)
	}
	if ok, _ := status["ok"].(bool); !ok {
		t.Fatalf("expected connection ok, got %#v", status)
	}
}

func TestIndexPageIsServed(t *testing.T) {
	t.Parallel()

	serverURL := startAppServer(t)
	response, err := http.Get(serverURL + "/")
	if err != nil {
		t.Fatalf("get index page: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read index page: %v", err)
	}
	if !strings.Contains(string(body), "PII Masker API Playground") {
		t.Fatalf("unexpected index body: %s", string(body))
	}
}

func startAppServer(t *testing.T) string {
	t.Helper()

	upstreamMux := http.NewServeMux()
	upstreamMux.Handle("/inference", mock.UpstageHandler())
	upstreamServer := httptest.NewServer(upstreamMux)
	t.Cleanup(upstreamServer.Close)

	cfg := config.Config{
		Server: config.ServerConfig{
			Address:       ":0",
			PublicBaseURL: "",
		},
		Upstage: config.UpstageConfig{
			BaseURL:    upstreamServer.URL + "/inference",
			AuthMode:   "bearer",
			Timeout:    5 * time.Second,
			Model:      "pii",
			Lang:       "ko",
			Schema:     "oac",
			AllowHosts: []string{"127.0.0.1", "localhost"},
		},
		Limits: config.LimitsConfig{
			MaxFileSizeBytes: 5 * 1024 * 1024,
			MaxPages:         10,
			SupportedMIMEs:   []string{"application/pdf", "image/png"},
		},
		Storage: config.StorageConfig{
			RootDir: t.TempDir(),
		},
		Debug: config.DebugConfig{
			EnableDebug: true,
		},
	}

	application, err := app.New(cfg)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	server := httptest.NewServer(application.Handler())
	t.Cleanup(server.Close)
	return server.URL
}

func buildMultipartBody(t *testing.T, filename, contentType string, content []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	return &body, writer.FormDataContentType()
}

func parseMultipartMaskResponse(t *testing.T, response *http.Response) (core.ProcessMetadata, []byte) {
	t.Helper()

	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse content-type: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("unexpected media type: %s", mediaType)
	}

	reader := multipart.NewReader(response.Body, params["boundary"])
	firstPart, err := reader.NextPart()
	if err != nil {
		t.Fatalf("read first part: %v", err)
	}
	var metadata core.ProcessMetadata
	if err := json.NewDecoder(firstPart).Decode(&metadata); err != nil {
		t.Fatalf("decode metadata part: %v", err)
	}

	secondPart, err := reader.NextPart()
	if err != nil {
		t.Fatalf("read second part: %v", err)
	}
	fileBytes, err := io.ReadAll(secondPart)
	if err != nil {
		t.Fatalf("read file part: %v", err)
	}
	return metadata, fileBytes
}

func createBlankPNG(t *testing.T, width, height int) []byte {
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

func createBlankPDF(width, height int) []byte {
	objects := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		strings.TrimSpace(
			"3 0 obj\n"+
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 "+itoa(width)+" "+itoa(height)+"] /Contents 4 0 R >>\n"+
				"endobj\n",
		) + "\n",
		"4 0 obj\n<< /Length 0 >>\nstream\n\nendstream\nendobj\n",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects)+1)
	offsets = append(offsets, 0)
	for _, object := range objects {
		offsets = append(offsets, buf.Len())
		buf.WriteString(object)
	}
	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 5\n")
	buf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		buf.WriteString(padOffset(offset) + " 00000 n \n")
	}
	buf.WriteString("trailer\n<< /Size 5 /Root 1 0 R >>\n")
	buf.WriteString("startxref\n")
	buf.WriteString(itoa(xrefOffset) + "\n")
	buf.WriteString("%%EOF\n")
	return buf.Bytes()
}

func padOffset(value int) string {
	text := itoa(value)
	for len(text) < 10 {
		text = "0" + text
	}
	return text
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
