package httpapi_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

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

func TestMaskEndpointReturnsMaskedJPEG(t *testing.T) {
	t.Parallel()

	serverURL := startAppServer(t)
	jpegBytes := createBlankJPEG(t, 400, 200)

	requestBody, contentType := buildMultipartBody(t, "sample.jpg", "image/jpeg", jpegBytes, nil)
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
	if metadata.Output.MIMEType != "image/jpeg" {
		t.Fatalf("unexpected output mime: %s", metadata.Output.MIMEType)
	}

	img, _, err := image.Decode(bytes.NewReader(fileBytes))
	if err != nil {
		t.Fatalf("decode masked jpeg: %v", err)
	}

	r, g, b, _ := img.At(75, 28).RGBA()
	if r > 0x1111 || g > 0x1111 || b > 0x1111 {
		t.Fatalf("expected masked jpeg pixel to be near black, got %d %d %d", r, g, b)
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

func TestCreateJobRejectsUnsupportedTypeWithoutPersisting(t *testing.T) {
	t.Parallel()

	serverURL, cfg := startAppServerWithConfig(t, nil)

	requestBody, contentType := buildMultipartBody(t, "notes.txt", "text/plain", []byte("주민등록번호 800901-1234567"), nil)
	response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	if code := decodeErrorCode(t, response); code != "invalid_request" {
		t.Fatalf("unexpected error code %q", code)
	}
	assertNoStoredJobs(t, cfg.Storage.RootDir)
}

func TestCreateJobRejectsOversizedUploadWithoutPersisting(t *testing.T) {
	t.Parallel()

	serverURL, cfg := startAppServerWithConfig(t, func(cfg *config.Config) {
		cfg.Limits.MaxFileSizeBytes = 256
	})

	requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 200, 200), nil)
	response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	if code := decodeErrorCode(t, response); code != "invalid_request" {
		t.Fatalf("unexpected error code %q", code)
	}
	assertNoStoredJobs(t, cfg.Storage.RootDir)
}

func TestCreateJobRejectsPDFExceedingPageLimitWithoutPersisting(t *testing.T) {
	t.Parallel()

	serverURL, cfg := startAppServerWithConfig(t, func(cfg *config.Config) {
		cfg.Limits.MaxPages = 0
	})

	requestBody, contentType := buildMultipartBody(t, "broken.pdf", "application/pdf", []byte("%PDF-1.4\nnot a real pdf\n"), nil)
	response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	assertNoStoredJobs(t, cfg.Storage.RootDir)
}

func TestMaskRejectsRequestBodyBeyondTheUploadLimit(t *testing.T) {
	t.Parallel()

	serverURL, _ := startAppServerWithConfig(t, func(cfg *config.Config) {
		cfg.Limits.MaxFileSizeBytes = 4096
	})

	requestBody, contentType := buildMultipartBody(t, "huge.png", "image/png", make([]byte, 512*1024), nil)
	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	if code := decodeErrorCode(t, response); code != "payload_too_large" {
		t.Fatalf("unexpected error code %q", code)
	}
}

func TestCreateJobRejectsRequestBodyBeyondTheUploadLimitWithoutPersisting(t *testing.T) {
	t.Parallel()

	serverURL, cfg := startAppServerWithConfig(t, func(cfg *config.Config) {
		cfg.Limits.MaxFileSizeBytes = 4096
	})

	requestBody, contentType := buildMultipartBody(t, "huge.png", "image/png", make([]byte, 512*1024), nil)
	response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	if code := decodeErrorCode(t, response); code != "payload_too_large" {
		t.Fatalf("unexpected error code %q", code)
	}
	assertNoStoredJobs(t, cfg.Storage.RootDir)
}

func TestMaskAcceptsUploadUsingTheFullConfiguredFileSize(t *testing.T) {
	t.Parallel()

	pngBytes := createBlankPNG(t, 400, 200)
	serverURL, _ := startAppServerWithConfig(t, func(cfg *config.Config) {
		cfg.Limits.MaxFileSizeBytes = int64(len(pngBytes))
	})

	requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", pngBytes, map[string]string{"lang": "ko"})
	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	metadata, _ := parseMultipartMaskResponse(t, response)
	if metadata.Status != "completed" {
		t.Fatalf("expected completed status, got %q", metadata.Status)
	}
}

func TestMaskRejectsImageWithOversizedDeclaredResolution(t *testing.T) {
	t.Parallel()

	serverURL := startAppServer(t)

	requestBody, contentType := buildMultipartBody(t, "bomb.png", "image/png", createPixelBombPNG(t, 40000, 40000), nil)
	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}

	var metadata core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata.Error == nil || metadata.Error.Code != "processing_failed" {
		t.Fatalf("unexpected metadata error %#v", metadata.Error)
	}
	if !strings.Contains(metadata.Error.Message, "exceeds the maximum") {
		t.Fatalf("unexpected error message %q", metadata.Error.Message)
	}
}

func TestCreateJobRejectsImageWithOversizedDeclaredResolutionWithoutPersisting(t *testing.T) {
	t.Parallel()

	serverURL, cfg := startAppServerWithConfig(t, nil)

	requestBody, contentType := buildMultipartBody(t, "bomb.png", "image/png", createPixelBombPNG(t, 40000, 40000), nil)
	response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	if code := decodeErrorCode(t, response); code != "invalid_request" {
		t.Fatalf("unexpected error code %q", code)
	}
	assertNoStoredJobs(t, cfg.Storage.RootDir)
}

func TestMaskSanitizesInjectedUploadFilename(t *testing.T) {
	t.Parallel()

	serverURL := startAppServer(t)
	requestBody, contentType := buildMultipartBodyWithFilenameParam(t,
		`filename*=utf-8''sample%0D%0AX-Injected%3A%20yes.png`,
		"image/png", createBlankPNG(t, 400, 200))

	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}

	rawBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if strings.Contains(string(rawBody), "\r\nX-Injected:") {
		t.Fatalf("expected no injected header line in the response body: %s", string(rawBody))
	}

	response.Body = io.NopCloser(bytes.NewReader(rawBody))
	metadata, fileBytes := parseMultipartMaskResponse(t, response)
	if len(fileBytes) == 0 {
		t.Fatalf("expected a masked file part")
	}
	assertNoControlCharacters(t, metadata.Output.FileName)
	assertNoControlCharacters(t, metadata.Input.FileName)
	if !strings.HasPrefix(metadata.Output.FileName, "masked_sample") {
		t.Fatalf("unexpected output file name %q", metadata.Output.FileName)
	}
}

func TestJobResultSanitizesInjectedUploadFilename(t *testing.T) {
	t.Parallel()

	serverURL, cfg := startAppServerWithConfig(t, nil)
	requestBody, contentType := buildMultipartBodyWithFilenameParam(t,
		`filename*=utf-8''sample%0D%0AX-Injected%3A%20yes.pdf`,
		"application/pdf", createBlankPDF(400, 400))

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
	assertNoControlCharacters(t, metadata.Input.FileName)

	jobMetadata := waitForJobStatus(t, serverURL, metadata.JobID, "completed")
	assertNoControlCharacters(t, jobMetadata.Output.FileName)

	entries, err := os.ReadDir(filepath.Join(cfg.Storage.RootDir, "jobs", metadata.JobID))
	if err != nil {
		t.Fatalf("read job dir: %v", err)
	}
	for _, entry := range entries {
		assertNoControlCharacters(t, entry.Name())
	}

	resultResponse, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(metadata.JobID) + "/result")
	if err != nil {
		t.Fatalf("get job result: %v", err)
	}
	defer resultResponse.Body.Close()

	disposition := resultResponse.Header.Get("Content-Disposition")
	assertNoControlCharacters(t, disposition)
	if strings.Count(disposition, `"`) != 2 {
		t.Fatalf("unexpected content-disposition %q", disposition)
	}
	if _, params, err := mime.ParseMediaType(disposition); err != nil {
		t.Fatalf("parse content-disposition %q: %v", disposition, err)
	} else if !strings.HasPrefix(params["filename"], "masked_sample") {
		t.Fatalf("unexpected download filename %q", params["filename"])
	}
}

func waitForJobStatus(t *testing.T, serverURL, jobID, status string) core.ProcessMetadata {
	t.Helper()

	var jobMetadata core.ProcessMetadata
	for range 20 {
		time.Sleep(50 * time.Millisecond)
		jobResponse, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(jobID))
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if err := json.NewDecoder(jobResponse.Body).Decode(&jobMetadata); err != nil {
			jobResponse.Body.Close()
			t.Fatalf("decode job metadata: %v", err)
		}
		jobResponse.Body.Close()
		if jobMetadata.Status == status {
			return jobMetadata
		}
	}
	t.Fatalf("expected job status %q, got %#v", status, jobMetadata)
	return jobMetadata
}

func assertNoControlCharacters(t *testing.T, value string) {
	t.Helper()

	for _, r := range value {
		if unicode.IsControl(r) {
			t.Fatalf("expected no control characters in %q", value)
		}
	}
}

func decodeErrorCode(t *testing.T, response *http.Response) string {
	t.Helper()

	var payload struct {
		Error core.APIError `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	return payload.Error.Code
}

func assertNoStoredJobs(t *testing.T, rootDir string) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(rootDir, "jobs"))
	if err != nil {
		t.Fatalf("read jobs dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no persisted job, got %d entries", len(entries))
	}
}

func startAppServer(t *testing.T) string {
	t.Helper()

	serverURL, _ := startAppServerWithConfig(t, nil)
	return serverURL
}

func startAppServerWithConfig(t *testing.T, customize func(*config.Config)) (string, config.Config) {
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
			SupportedMIMEs:   []string{"application/pdf", "image/png", "image/jpeg"},
		},
		Storage: config.StorageConfig{
			RootDir: t.TempDir(),
		},
		Debug: config.DebugConfig{
			EnableDebug: true,
		},
	}
	if customize != nil {
		customize(&cfg)
	}

	application, err := app.New(cfg)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	server := httptest.NewServer(application.Handler())
	t.Cleanup(server.Close)
	return server.URL, cfg
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

// buildMultipartBodyWithFilenameParam writes the file part with a verbatim
// Content-Disposition filename parameter so a test can send the RFC 2231 encoded
// names that browsers use and that can decode to control characters.
func buildMultipartBodyWithFilenameParam(t *testing.T, filenameParam, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; `+filenameParam)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
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

// createPixelBombPNG rewrites the IHDR header of a 1x1 PNG so that it declares a
// huge resolution while the file itself stays tiny, which is how a decompression
// bomb reaches the image decoder.
func createPixelBombPNG(t *testing.T, width, height uint32) []byte {
	t.Helper()

	content := createBlankPNG(t, 1, 1)
	const ihdrTypeOffset = 12
	const ihdrDataOffset = 16
	if header := string(content[ihdrTypeOffset:ihdrDataOffset]); header != "IHDR" {
		t.Fatalf("unexpected first png chunk %q", header)
	}

	binary.BigEndian.PutUint32(content[ihdrDataOffset:], width)
	binary.BigEndian.PutUint32(content[ihdrDataOffset+4:], height)
	binary.BigEndian.PutUint32(content[ihdrDataOffset+13:], crc32.ChecksumIEEE(content[ihdrTypeOffset:ihdrDataOffset+13]))
	return content
}

func createBlankJPEG(t *testing.T, width, height int) []byte {
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
