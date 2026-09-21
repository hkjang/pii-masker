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
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

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

// A 200 from the inference endpoint that carries nothing the masker understands is
// reported as an upstream failure, so no client ever downloads the unmasked upload
// under a success status.
func TestMaskReportsUninterpretableUpstreamResponseAsBadGateway(t *testing.T) {
	t.Parallel()

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"이름\":\"홍길동\"}"}}]}`))
	})
	serverURL, _ := startAppServerWithUpstream(t, upstream, nil)

	requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 400, 200), nil)
	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected 502, got %d: %s", response.StatusCode, string(body))
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("expected a JSON error body, got %q", response.Header.Get("Content-Type"))
	}
	var metadata core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode error metadata: %v", err)
	}
	if metadata.Status != "failed" || metadata.Error == nil || metadata.Error.Code != "upstream_payload_unrecognized" {
		t.Fatalf("expected upstream_payload_unrecognized failure, got %#v", metadata)
	}
}

// The same response on the async path must leave the job failed with no result file.
func TestAsyncJobFailsWhenUpstreamResponseIsNotUnderstood(t *testing.T) {
	t.Parallel()

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"}}]}`))
	})
	serverURL, _ := startAppServerWithUpstream(t, upstream, nil)

	requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 400, 200), nil)
	response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	var created core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode job metadata: %v", err)
	}

	var job core.ProcessMetadata
	for range 40 {
		time.Sleep(50 * time.Millisecond)
		jobResponse, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(created.JobID))
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		err = json.NewDecoder(jobResponse.Body).Decode(&job)
		jobResponse.Body.Close()
		if err != nil {
			t.Fatalf("decode job metadata: %v", err)
		}
		if job.Status == "completed" || job.Status == "failed" {
			break
		}
	}
	if job.Status != "failed" || job.Error == nil || job.Error.Code != "upstream_payload_unrecognized" {
		t.Fatalf("expected a failed job with upstream_payload_unrecognized, got %#v", job)
	}
	if job.Output.DownloadURL != "" {
		t.Fatalf("expected no download url on a failed job, got %q", job.Output.DownloadURL)
	}

	resultResponse, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(created.JobID) + "/result")
	if err != nil {
		t.Fatalf("get job result: %v", err)
	}
	defer resultResponse.Body.Close()
	if resultResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for the result of a failed job, got %d", resultResponse.StatusCode)
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
	if got := response.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("expected the page to be revalidated on every load, got Cache-Control %q", got)
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

// TestJobResultServesRangeRequests checks that the result is streamed off disk with
// range support instead of being buffered whole, so resuming a large download works.
func TestJobResultServesRangeRequests(t *testing.T) {
	t.Parallel()

	serverURL, _ := startAppServerWithConfig(t, nil)
	jobID := createCompletedPDFJob(t, serverURL)
	resultURL := serverURL + "/v1/jobs/" + url.PathEscape(jobID) + "/result"

	fullResponse, err := http.Get(resultURL)
	if err != nil {
		t.Fatalf("get job result: %v", err)
	}
	defer fullResponse.Body.Close()
	if fullResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected result status %d", fullResponse.StatusCode)
	}
	if fullResponse.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("expected byte range support, got %q", fullResponse.Header.Get("Accept-Ranges"))
	}
	fullBytes, _ := io.ReadAll(fullResponse.Body)
	if len(fullBytes) < 32 {
		t.Fatalf("expected a non-trivial pdf result, got %d bytes", len(fullBytes))
	}

	request, err := http.NewRequest(http.MethodGet, resultURL, nil)
	if err != nil {
		t.Fatalf("build range request: %v", err)
	}
	request.Header.Set("Range", "bytes=8-23")
	rangeResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get job result range: %v", err)
	}
	defer rangeResponse.Body.Close()

	if rangeResponse.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(rangeResponse.Body)
		t.Fatalf("unexpected range status %d: %s", rangeResponse.StatusCode, string(body))
	}
	expectedRange := "bytes 8-23/" + strconv.Itoa(len(fullBytes))
	if got := rangeResponse.Header.Get("Content-Range"); got != expectedRange {
		t.Fatalf("unexpected content-range %q, want %q", got, expectedRange)
	}
	rangeBytes, _ := io.ReadAll(rangeResponse.Body)
	if !bytes.Equal(rangeBytes, fullBytes[8:24]) {
		t.Fatalf("range body does not match the corresponding slice of the full result")
	}
	if !strings.Contains(rangeResponse.Header.Get("Content-Type"), "application/pdf") {
		t.Fatalf("unexpected range content type: %s", rangeResponse.Header.Get("Content-Type"))
	}
}

// TestJobResultAnswersHeadProbes checks that download managers, `curl -I` and proxies can
// probe a result's existence and size with HEAD: the same headers as GET, no body.
func TestJobResultAnswersHeadProbes(t *testing.T) {
	t.Parallel()

	serverURL, _ := startAppServerWithConfig(t, nil)
	jobID := createCompletedPDFJob(t, serverURL)
	resultURL := serverURL + "/v1/jobs/" + url.PathEscape(jobID) + "/result"

	getResponse, err := http.Get(resultURL)
	if err != nil {
		t.Fatalf("get job result: %v", err)
	}
	defer getResponse.Body.Close()
	if getResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected get status %d", getResponse.StatusCode)
	}
	fullBytes, _ := io.ReadAll(getResponse.Body)
	if len(fullBytes) < 32 {
		t.Fatalf("expected a non-trivial pdf result, got %d bytes", len(fullBytes))
	}

	headResponse, err := http.Head(resultURL)
	if err != nil {
		t.Fatalf("head job result: %v", err)
	}
	defer headResponse.Body.Close()
	if headResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected head status %d (allow=%q)", headResponse.StatusCode, headResponse.Header.Get("Allow"))
	}
	if headResponse.ContentLength != int64(len(fullBytes)) {
		t.Fatalf("head content-length %d does not match the get body length %d", headResponse.ContentLength, len(fullBytes))
	}
	if headBytes, _ := io.ReadAll(headResponse.Body); len(headBytes) != 0 {
		t.Fatalf("expected an empty head body, got %d bytes", len(headBytes))
	}
	for header, want := range map[string]string{
		"Accept-Ranges":          "bytes",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    getResponse.Header.Get("Content-Disposition"),
		"Content-Type":           getResponse.Header.Get("Content-Type"),
	} {
		if got := headResponse.Header.Get(header); got != want {
			t.Fatalf("head %s = %q, want %q", header, got, want)
		}
	}
	if !strings.Contains(headResponse.Header.Get("Content-Type"), "application/pdf") {
		t.Fatalf("unexpected head content type: %s", headResponse.Header.Get("Content-Type"))
	}
	if headResponse.Header.Get("Content-Disposition") == "" {
		t.Fatalf("expected a content-disposition header on the head response")
	}

	missingResponse, err := http.Head(serverURL + "/v1/jobs/does-not-exist/result")
	if err != nil {
		t.Fatalf("head missing job result: %v", err)
	}
	defer missingResponse.Body.Close()
	if missingResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected status %d for a missing job", missingResponse.StatusCode)
	}
}

// TestJobResultReturnsNotFoundWhenFileIsGone covers a result file that disappeared from
// disk after the job completed: that is a missing result, not an internal failure.
func TestJobResultReturnsNotFoundWhenFileIsGone(t *testing.T) {
	t.Parallel()

	serverURL, cfg := startAppServerWithConfig(t, nil)
	jobID := createCompletedPDFJob(t, serverURL)

	jobDir := filepath.Join(cfg.Storage.RootDir, "jobs", jobID)
	entries, err := os.ReadDir(jobDir)
	if err != nil {
		t.Fatalf("read job dir: %v", err)
	}
	removed := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "output_") {
			if err := os.Remove(filepath.Join(jobDir, entry.Name())); err != nil {
				t.Fatalf("remove result file: %v", err)
			}
			removed++
		}
	}
	if removed == 0 {
		t.Fatalf("expected a stored result file in %s", jobDir)
	}

	response, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(jobID) + "/result")
	if err != nil {
		t.Fatalf("get job result: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}
	var payload struct {
		Error core.APIError `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Error.Code != "job_result_not_found" {
		t.Fatalf("unexpected error code %q", payload.Error.Code)
	}
}

// TestDownloadsEncodeNonASCIIFilenames uploads a Korean file name and checks both
// places a name reaches a client: the multipart part of a synchronous mask and the
// job result download. A quoted-string carries latin-1, so without the RFC 6266
// filename* parameter the name is saved as mojibake.
func TestDownloadsEncodeNonASCIIFilenames(t *testing.T) {
	t.Parallel()

	const uploadName = "계약서.pdf"
	const expectedName = "masked_계약서.pdf"

	serverURL, _ := startAppServerWithConfig(t, nil)

	requestBody, contentType := buildMultipartBody(t, uploadName, "application/pdf", createBlankPDF(400, 400), nil)
	maskResponse, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer maskResponse.Body.Close()
	if maskResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(maskResponse.Body)
		t.Fatalf("unexpected mask status %d: %s", maskResponse.StatusCode, string(body))
	}

	mediaType, params, err := mime.ParseMediaType(maskResponse.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("unexpected mask content-type %q: %v", maskResponse.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(maskResponse.Body, params["boundary"])
	if _, err := reader.NextPart(); err != nil {
		t.Fatalf("read metadata part: %v", err)
	}
	filePart, err := reader.NextPart()
	if err != nil {
		t.Fatalf("read file part: %v", err)
	}
	assertEncodedDisposition(t, filePart.Header.Get("Content-Disposition"), expectedName)

	jobRequestBody, jobContentType := buildMultipartBody(t, uploadName, "application/pdf", createBlankPDF(400, 400), nil)
	jobResponse, err := http.Post(serverURL+"/v1/jobs", jobContentType, jobRequestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer jobResponse.Body.Close()
	if jobResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(jobResponse.Body)
		t.Fatalf("unexpected job status %d: %s", jobResponse.StatusCode, string(body))
	}
	var metadata core.ProcessMetadata
	if err := json.NewDecoder(jobResponse.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode job metadata: %v", err)
	}
	waitForJobStatus(t, serverURL, metadata.JobID, "completed")

	resultResponse, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(metadata.JobID) + "/result")
	if err != nil {
		t.Fatalf("get job result: %v", err)
	}
	defer resultResponse.Body.Close()
	assertEncodedDisposition(t, resultResponse.Header.Get("Content-Disposition"), expectedName)
}

// assertEncodedDisposition checks that a Content-Disposition offers the name both
// as an ASCII fallback and as an RFC 6266 filename* a modern client can decode.
func assertEncodedDisposition(t *testing.T, disposition, expectedName string) {
	t.Helper()

	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Fatalf("expected an encoded filename* parameter in %q", disposition)
	}
	for i := 0; i < len(disposition); i++ {
		if disposition[i] > unicode.MaxASCII {
			t.Fatalf("expected an ascii only header value, got %q", disposition)
		}
	}
	mediaType, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		t.Fatalf("parse content-disposition %q: %v", disposition, err)
	}
	if mediaType != "attachment" {
		t.Fatalf("unexpected disposition type %q", mediaType)
	}
	if params["filename"] != expectedName {
		t.Fatalf("filename = %q, want %q", params["filename"], expectedName)
	}
}

// TestDocumentResponsesAreNotCacheable walks every endpoint that hands back an
// uploaded document or the metadata derived from it. Without an explicit directive a
// 200 GET is heuristically cacheable, so a masked file or a PII summary could be kept
// by a shared proxy or the browser disk cache past the retention window.
func TestDocumentResponsesAreNotCacheable(t *testing.T) {
	t.Parallel()

	serverURL, _ := startAppServerWithConfig(t, nil)

	requestBody, contentType := buildMultipartBody(t, "sample.pdf", "application/pdf", createBlankPDF(400, 400), nil)
	maskResponse, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer maskResponse.Body.Close()
	if maskResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(maskResponse.Body)
		t.Fatalf("unexpected mask status %d: %s", maskResponse.StatusCode, string(body))
	}
	assertNotCacheable(t, "/v1/mask", maskResponse)

	jobRequestBody, jobContentType := buildMultipartBody(t, "sample.pdf", "application/pdf", createBlankPDF(400, 400), nil)
	jobResponse, err := http.Post(serverURL+"/v1/jobs", jobContentType, jobRequestBody)
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer jobResponse.Body.Close()
	if jobResponse.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(jobResponse.Body)
		t.Fatalf("unexpected job status %d: %s", jobResponse.StatusCode, string(body))
	}
	assertNotCacheable(t, "/v1/jobs", jobResponse)

	var metadata core.ProcessMetadata
	if err := json.NewDecoder(jobResponse.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode job metadata: %v", err)
	}
	waitForJobStatus(t, serverURL, metadata.JobID, "completed")

	for _, path := range []string{
		"/v1/jobs/" + url.PathEscape(metadata.JobID),
		"/v1/jobs/" + url.PathEscape(metadata.JobID) + "/result",
		"/v1/history",
	} {
		response, err := http.Get(serverURL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("unexpected status %d for %s: %s", response.StatusCode, path, string(body))
		}
		assertNotCacheable(t, path, response)
		response.Body.Close()
	}

	// The health probe carries no document, so it is deliberately left cacheable.
	healthResponse, err := http.Get(serverURL + "/v1/health")
	if err != nil {
		t.Fatalf("get /v1/health: %v", err)
	}
	defer healthResponse.Body.Close()
	if got := healthResponse.Header.Get("Cache-Control"); got != "" {
		t.Fatalf("unexpected cache-control %q on /v1/health", got)
	}
}

func assertNotCacheable(t *testing.T, path string, response *http.Response) {
	t.Helper()

	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q for %s, want %q", got, path, "no-store")
	}
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q for %s, want %q", got, path, "nosniff")
	}
}

func createCompletedPDFJob(t *testing.T, serverURL string) string {
	t.Helper()

	requestBody, contentType := buildMultipartBody(t, "sample.pdf", "application/pdf", createBlankPDF(400, 400), nil)
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
	waitForJobStatus(t, serverURL, metadata.JobID, "completed")
	return metadata.JobID
}

// TestAsyncJobsRunWithBoundedConcurrency holds every upstream call open, so the
// number of simultaneous upstream requests is exactly the number of accepted jobs
// the runner lets execute at once. Without a limit all of them would run together
// and each would pin its whole document in memory.
func TestAsyncJobsRunWithBoundedConcurrency(t *testing.T) {
	t.Parallel()

	const (
		concurrencyLimit = 2
		jobCount         = 5
	)

	gated, inFlight, maxInFlight, releaseGate := newGatedUpstream()
	serverURL, _ := startAppServerWithUpstream(t, gated, func(cfg *config.Config) {
		cfg.Limits.MaxConcurrentJobs = concurrencyLimit
		cfg.Upstage.Timeout = 60 * time.Second
	})
	// Registered after the servers so it runs before their cleanup: a failing
	// assertion must not leave requests parked in the handler, or shutdown would
	// block instead of reporting the failure.
	t.Cleanup(releaseGate)

	jobIDs := make([]string, 0, jobCount)
	for range jobCount {
		requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 400, 200), nil)
		response, err := http.Post(serverURL+"/v1/jobs", contentType, requestBody)
		if err != nil {
			t.Fatalf("post /v1/jobs: %v", err)
		}
		var metadata core.ProcessMetadata
		decodeErr := json.NewDecoder(response.Body).Decode(&metadata)
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("unexpected status %d", response.StatusCode)
		}
		if decodeErr != nil {
			t.Fatalf("decode job metadata: %v", decodeErr)
		}
		jobIDs = append(jobIDs, metadata.JobID)
	}

	waitForCondition(t, "upstream to reach the concurrency limit", func() bool {
		return atomic.LoadInt64(inFlight) >= concurrencyLimit
	})
	// Give any runner that ignored the limit a chance to reach the upstream too.
	time.Sleep(300 * time.Millisecond)
	if observed := atomic.LoadInt64(maxInFlight); observed != concurrencyLimit {
		t.Fatalf("expected at most %d concurrent jobs, got %d", concurrencyLimit, observed)
	}

	releaseGate()
	for _, jobID := range jobIDs {
		waitForCondition(t, "job "+jobID+" to complete", func() bool {
			return jobStatus(t, serverURL, jobID) == "completed"
		})
	}
	if observed := atomic.LoadInt64(maxInFlight); observed != concurrencyLimit {
		t.Fatalf("expected at most %d concurrent jobs, got %d", concurrencyLimit, observed)
	}
}

// TestSyncMaskRunsWithBoundedConcurrency is the /v1/mask counterpart of the async
// limit: a synchronous mask decodes and re-renders the whole document too, so the
// server must not start one per request.
func TestSyncMaskRunsWithBoundedConcurrency(t *testing.T) {
	t.Parallel()

	const (
		concurrencyLimit = 2
		requestCount     = 5
	)

	gated, inFlight, maxInFlight, releaseGate := newGatedUpstream()
	serverURL, _ := startAppServerWithUpstream(t, gated, func(cfg *config.Config) {
		cfg.Limits.MaxConcurrentSync = concurrencyLimit
		cfg.Limits.SyncQueueWait = 60 * time.Second
		cfg.Upstage.Timeout = 60 * time.Second
	})
	// Registered after the servers so it runs before their cleanup: a failing
	// assertion must not leave requests parked in the handler.
	t.Cleanup(releaseGate)

	// Bodies are built up front so the request goroutines never touch the test helper.
	// Every body carries its own multipart boundary, so the content types are kept
	// alongside them instead of being shared.
	bodies := make([]*bytes.Buffer, requestCount)
	contentTypes := make([]string, requestCount)
	for index := range bodies {
		bodies[index], contentTypes[index] = buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 400, 200), nil)
	}

	statuses := make([]int, requestCount)
	var wg sync.WaitGroup
	for index := range requestCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := http.Post(serverURL+"/v1/mask", contentTypes[index], bodies[index])
			if err != nil {
				return
			}
			defer response.Body.Close()
			_, _ = io.Copy(io.Discard, response.Body)
			statuses[index] = response.StatusCode
		}()
	}

	waitForCondition(t, "upstream to reach the concurrency limit", func() bool {
		return atomic.LoadInt64(inFlight) >= concurrencyLimit
	})
	// Give any request that ignored the limit a chance to reach the upstream too.
	time.Sleep(300 * time.Millisecond)
	if observed := atomic.LoadInt64(maxInFlight); observed != concurrencyLimit {
		t.Fatalf("expected at most %d concurrent masks, got %d", concurrencyLimit, observed)
	}

	releaseGate()
	wg.Wait()
	if observed := atomic.LoadInt64(maxInFlight); observed != concurrencyLimit {
		t.Fatalf("expected at most %d concurrent masks, got %d", concurrencyLimit, observed)
	}
	for index, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("queued mask %d returned %d, want 200", index, status)
		}
	}
}

// TestSyncMaskShedsRequestWhenSlotsAreBusy pins the only slot and checks that the
// next upload is turned away instead of being processed alongside it.
func TestSyncMaskShedsRequestWhenSlotsAreBusy(t *testing.T) {
	t.Parallel()

	gated, inFlight, _, releaseGate := newGatedUpstream()
	serverURL, _ := startAppServerWithUpstream(t, gated, func(cfg *config.Config) {
		cfg.Limits.MaxConcurrentSync = 1
		cfg.Limits.SyncQueueWait = 0
		cfg.Upstage.Timeout = 60 * time.Second
	})
	t.Cleanup(releaseGate)

	heldBody, heldContentType := buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 400, 200), nil)
	held := make(chan int, 1)
	go func() {
		response, err := http.Post(serverURL+"/v1/mask", heldContentType, heldBody)
		if err != nil {
			held <- 0
			return
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		held <- response.StatusCode
	}()

	waitForCondition(t, "the first mask to occupy the only slot", func() bool {
		return atomic.LoadInt64(inFlight) >= 1
	})

	requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 400, 200), nil)
	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected the second mask to be shed, got %d", response.StatusCode)
	}
	if retryAfter := response.Header.Get("Retry-After"); retryAfter == "" {
		t.Fatalf("expected a Retry-After header on a shed request")
	}
	var metadata core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode shed metadata: %v", err)
	}
	if metadata.Error == nil || metadata.Error.Code != "server_busy" {
		t.Fatalf("unexpected error payload %#v", metadata.Error)
	}
	if !metadata.Error.Retryable {
		t.Fatalf("expected a shed request to be retryable")
	}
	if observed := atomic.LoadInt64(inFlight); observed != 1 {
		t.Fatalf("expected the shed request to never reach the upstream, got %d in flight", observed)
	}

	releaseGate()
	if status := <-held; status != http.StatusOK {
		t.Fatalf("held mask returned %d, want 200", status)
	}
}

// newGatedUpstream parks every upstream call until the returned release function is
// called, so a test can count how many requests are being processed at the same time.
func newGatedUpstream() (http.Handler, *int64, *int64, func()) {
	var (
		inFlight    int64
		maxInFlight int64
		release     = make(chan struct{})
		releaseOnce sync.Once
		upstream    = mock.UpstageHandler()
	)
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt64(&inFlight, 1)
		for {
			observed := atomic.LoadInt64(&maxInFlight)
			if current <= observed || atomic.CompareAndSwapInt64(&maxInFlight, observed, current) {
				break
			}
		}
		<-release
		atomic.AddInt64(&inFlight, -1)
		upstream.ServeHTTP(w, r)
	})
	return gated, &inFlight, &maxInFlight, func() { releaseOnce.Do(func() { close(release) }) }
}

func TestExpiredJobFilesArePurgedOnStartup(t *testing.T) {
	t.Parallel()

	var jobsDir string
	now := time.Now().UTC()
	serverURL, _ := startAppServerWithConfig(t, func(cfg *config.Config) {
		cfg.Storage.JobRetention = 24 * time.Hour
		jobsDir = filepath.Join(cfg.Storage.RootDir, "jobs")
		seedStoredJobFiles(t, jobsDir, "stale-job", now.Add(-48*time.Hour))
		seedStoredJobFiles(t, jobsDir, "fresh-job", now.Add(-time.Minute))
	})

	waitForCondition(t, "the expired job directory to be removed", func() bool {
		_, err := os.Stat(filepath.Join(jobsDir, "stale-job"))
		return os.IsNotExist(err)
	})

	staleResponse, err := http.Get(serverURL + "/v1/jobs/stale-job")
	if err != nil {
		t.Fatalf("get expired job: %v", err)
	}
	defer staleResponse.Body.Close()
	if staleResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the expired job to be unknown, got %d", staleResponse.StatusCode)
	}

	if _, err := os.Stat(filepath.Join(jobsDir, "fresh-job", "input_sample.png")); err != nil {
		t.Fatalf("expected the recent job files to be kept: %v", err)
	}
	freshResponse, err := http.Get(serverURL + "/v1/jobs/fresh-job")
	if err != nil {
		t.Fatalf("get recent job: %v", err)
	}
	defer freshResponse.Body.Close()
	if freshResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected the recent job to be kept, got %d", freshResponse.StatusCode)
	}
}

// TestHistoryCapsTheNumberOfReturnedJobs pins the page size of the history listing:
// without a cap a single request could ask the server to clone and serialize every
// job the store still holds.
func TestHistoryCapsTheNumberOfReturnedJobs(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	const seededJobs = 150
	serverURL, _ := startAppServerWithConfig(t, func(cfg *config.Config) {
		jobsDir := filepath.Join(cfg.Storage.RootDir, "jobs")
		for index := range seededJobs {
			// The newest job is job-0, so the listing order is easy to assert on.
			seedStoredJobFiles(t, jobsDir, "job-"+strconv.Itoa(index), now.Add(-time.Duration(index)*time.Minute))
		}
	})

	cases := []struct {
		name     string
		query    string
		expected int
	}{
		{name: "default page", query: "", expected: 20},
		{name: "explicit page", query: "?limit=5", expected: 5},
		{name: "oversized page", query: "?limit=100000", expected: 100},
		{name: "negative page", query: "?limit=-1", expected: 20},
		{name: "unparsable page", query: "?limit=all", expected: 20},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			items := fetchHistory(t, serverURL+"/v1/history"+testCase.query)
			if len(items) != testCase.expected {
				t.Fatalf("history returned %d items, want %d", len(items), testCase.expected)
			}
			if items[0].JobID != "job-0" {
				t.Fatalf("expected the newest job first, got %q", items[0].JobID)
			}
		})
	}
}

func fetchHistory(t *testing.T, historyURL string) []core.ProcessMetadata {
	t.Helper()

	response, err := http.Get(historyURL)
	if err != nil {
		t.Fatalf("get %s: %v", historyURL, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}

	var payload struct {
		Items []core.ProcessMetadata `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	return payload.Items
}

// seedStoredJobFiles writes a finished job straight to disk, the way a previous run of
// the server would have left it behind.
func seedStoredJobFiles(t *testing.T, jobsDir, jobID string, updatedAt time.Time) {
	t.Helper()
	seedStoredJobFilesWithStatus(t, jobsDir, jobID, updatedAt, "completed")
}

func seedStoredJobFilesWithStatus(t *testing.T, jobsDir, jobID string, updatedAt time.Time, status string) {
	t.Helper()

	jobDir := filepath.Join(jobsDir, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("create job dir: %v", err)
	}
	record := core.JobRecord{
		ID: jobID,
		Metadata: core.ProcessMetadata{
			RequestID: jobID,
			JobID:     jobID,
			Status:    status,
			Output:    core.FileDescriptor{FileName: "sample_masked.png", MIMEType: "image/png", DownloadURL: "/stale-result"},
			CreatedAt: updatedAt,
			UpdatedAt: updatedAt,
		},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal job record: %v", err)
	}
	files := map[string][]byte{
		"job.json":                 raw,
		"input_sample.png":         []byte("original"),
		"output_sample_masked.png": []byte("masked"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(jobDir, name), content, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func waitForCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func jobStatus(t *testing.T, serverURL, jobID string) string {
	t.Helper()

	response, err := http.Get(serverURL + "/v1/jobs/" + url.PathEscape(jobID))
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	defer response.Body.Close()

	var metadata core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode job metadata: %v", err)
	}
	if metadata.Status == "failed" {
		t.Fatalf("job %s failed: %#v", jobID, metadata.Error)
	}
	return metadata.Status
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

func TestMaskRejectsUpstreamHostOutsideAllowList(t *testing.T) {
	t.Parallel()

	reached := false
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	serverURL, _ := startAppServerWithUpstream(t, upstream, func(cfg *config.Config) {
		cfg.Upstage.AllowHosts = []string{"api.upstage.ai"}
	})

	requestBody, contentType := buildMultipartBody(t, "sample.png", "image/png", createBlankPNG(t, 400, 200), nil)
	response, err := http.Post(serverURL+"/v1/mask", contentType, requestBody)
	if err != nil {
		t.Fatalf("post /v1/mask: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected status %d: %s", response.StatusCode, string(body))
	}

	var metadata core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata.Error == nil || metadata.Error.Code != "upstream_host_not_allowed" {
		t.Fatalf("unexpected error payload: %+v", metadata.Error)
	}
	if reached {
		t.Fatal("the upload must not reach an upstream host outside the allow list")
	}
}

func startAppServer(t *testing.T) string {
	t.Helper()

	serverURL, _ := startAppServerWithConfig(t, nil)
	return serverURL
}

func startAppServerWithConfig(t *testing.T, customize func(*config.Config)) (string, config.Config) {
	t.Helper()

	return startAppServerWithUpstream(t, mock.UpstageHandler(), customize)
}

func startAppServerWithUpstream(t *testing.T, upstream http.Handler, customize func(*config.Config)) (string, config.Config) {
	t.Helper()

	upstreamMux := http.NewServeMux()
	upstreamMux.Handle("/inference", upstream)
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
			SyncQueueWait:    10 * time.Second,
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
	t.Cleanup(application.Close)
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

func TestReloadedJobResultsRequireCompletion(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"queued", "running", "failed", "completed", "completed-missing"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			updatedAt := time.Now().UTC().Add(-time.Hour)
			available := status == "completed"
			for reload := 1; reload <= 2; reload++ {
				t.Run(strconv.Itoa(reload), func(t *testing.T) {
					serverURL, _ := startAppServerWithConfig(t, func(cfg *config.Config) {
						cfg.Storage.RootDir = root
						if reload == 1 {
							storedStatus := status
							if status == "completed-missing" {
								storedStatus = "completed"
							}
							seedStoredJobFilesWithStatus(t, filepath.Join(root, "jobs"), "job", updatedAt, storedStatus)
							if status == "completed-missing" {
								if err := os.Remove(filepath.Join(root, "jobs", "job", "output_sample_masked.png")); err != nil {
									t.Fatal(err)
								}
							}
						}
					})
					response, err := http.Get(serverURL + "/v1/jobs/job")
					if err != nil {
						t.Fatal(err)
					}
					var metadata core.ProcessMetadata
					err = json.NewDecoder(response.Body).Decode(&metadata)
					response.Body.Close()
					if err != nil || response.StatusCode != http.StatusOK {
						t.Fatalf("lookup: status=%d err=%v", response.StatusCode, err)
					}
					history := fetchHistory(t, serverURL+"/v1/history")
					if len(history) != 1 {
						t.Fatalf("history length = %d", len(history))
					}
					for _, item := range []core.ProcessMetadata{metadata, history[0]} {
						expectedStatus := "failed"
						if strings.HasPrefix(status, "completed") {
							expectedStatus = "completed"
						}
						if item.Status != expectedStatus || !item.UpdatedAt.Equal(updatedAt) {
							t.Errorf("status/timestamp changed: %#v", item)
						}
						if (status == "queued" || status == "running") && (item.Error == nil || item.Error.Code != "job_interrupted") {
							t.Errorf("missing interruption error: %#v", item.Error)
						}
						expectedURL := ""
						if available {
							expectedURL = "/v1/jobs/job/result"
						}
						if item.Output.DownloadURL != expectedURL {
							t.Errorf("download URL = %q, want %q", item.Output.DownloadURL, expectedURL)
						}
					}
					for _, probe := range []struct{ method, byteRange string }{{http.MethodGet, ""}, {http.MethodHead, ""}, {http.MethodGet, "bytes=1-3"}} {
						request, err := http.NewRequest(probe.method, serverURL+"/v1/jobs/job/result", nil)
						if err != nil {
							t.Fatal(err)
						}
						if probe.byteRange != "" {
							request.Header.Set("Range", probe.byteRange)
						}
						result, err := http.DefaultClient.Do(request)
						if err != nil {
							t.Fatal(err)
						}
						body, err := io.ReadAll(result.Body)
						result.Body.Close()
						if err != nil {
							t.Fatal(err)
						}
						wantStatus := http.StatusNotFound
						if available {
							wantStatus = http.StatusOK
							if probe.byteRange != "" {
								wantStatus = http.StatusPartialContent
							}
						}
						if result.StatusCode != wantStatus {
							t.Errorf("%s range=%q: status=%d want=%d", probe.method, probe.byteRange, result.StatusCode, wantStatus)
						}
						if result.Header.Get("Cache-Control") != "no-store" || result.Header.Get("X-Content-Type-Options") != "nosniff" {
							t.Errorf("missing document headers: %v", result.Header)
						}
						if probe.method == http.MethodHead {
							if len(body) != 0 {
								t.Errorf("HEAD body = %q", body)
							}
							if available && result.ContentLength != 6 {
								t.Errorf("HEAD length = %d", result.ContentLength)
							}
						} else if available {
							expectedBody := "masked"
							if probe.byteRange != "" {
								expectedBody = "ask"
								if result.Header.Get("Content-Range") != "bytes 1-3/6" {
									t.Errorf("Content-Range = %q", result.Header.Get("Content-Range"))
								}
							}
							if string(body) != expectedBody {
								t.Errorf("body = %q", body)
							}
						} else if !bytes.Contains(body, []byte(`"code":"job_result_not_found"`)) {
							t.Errorf("unexpected error body: %s", body)
						}
					}
					for _, name := range []string{"job.json", "input_sample.png", "output_sample_masked.png"} {
						if status == "completed-missing" && strings.HasPrefix(name, "output_") {
							continue
						}
						if _, err := os.Stat(filepath.Join(root, "jobs", "job", name)); err != nil {
							t.Errorf("file removed: %s: %v", name, err)
						}
					}
				})
			}
		})
	}
}

func TestUpstreamDiagnosticsPreserveUTF8(t *testing.T) {
	t.Parallel()

	message := strings.Repeat("가", 6000)
	payload := `{"message":"` + message + `","fields":[]}`
	for _, status := range []int{http.StatusInternalServerError, http.StatusOK} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, payload)
			})
			serverURL, _ := startAppServerWithUpstream(t, upstream, nil)
			assertDiagnostic := func(t *testing.T, got, want string, limit int) {
				t.Helper()
				if !utf8.ValidString(got) || strings.ContainsRune(got, '\uFFFD') {
					t.Errorf("diagnostic contains damaged UTF-8")
				}
				if got != want {
					t.Errorf("diagnostic differs from expected complete prefix and ellipsis (got %d bytes, want %d)", len(got), len(want))
				}
				if len(got) != len(want) || len(got) > limit {
					t.Errorf("unexpected diagnostic length %d, want %d within %d", len(got), len(want), limit)
				}
			}
			if status == http.StatusInternalServerError {
				t.Run("connection", func(t *testing.T) {
					response, err := http.Post(serverURL+"/v1/test-connection", "application/json", http.NoBody)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					if response.StatusCode != http.StatusOK {
						t.Fatalf("connection status = %d", response.StatusCode)
					}
					var result struct {
						OK        bool   `json:"ok"`
						ErrorCode string `json:"error_code"`
						Retryable bool   `json:"retryable"`
						Detail    string `json:"detail"`
					}
					if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
						t.Fatal(err)
					}
					if result.OK || result.ErrorCode != "server_error" || !result.Retryable {
						t.Fatalf("unexpected connection result: %#v", result)
					}
					assertDiagnostic(t, result.Detail, strings.Repeat("가", 92)+"...", 280)
				})
			}
			t.Run("mask", func(t *testing.T) {
				input := createBlankPNG(t, 40, 20)
				body, contentType := buildMultipartBody(t, "sample.png", "image/png", input, nil)
				response, err := http.Post(serverURL+"/v1/mask", contentType, body)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if status == http.StatusInternalServerError {
					if response.StatusCode != http.StatusBadGateway {
						t.Fatalf("mask status = %d", response.StatusCode)
					}
					var metadata core.ProcessMetadata
					if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
						t.Fatal(err)
					}
					if metadata.Status != "failed" || metadata.Error == nil ||
						metadata.Error.Code != "server_error" || !metadata.Error.Retryable {
						t.Fatalf("unexpected mask error: %#v", metadata)
					}
					assertDiagnostic(t, metadata.Error.Detail, strings.Repeat("가", 92)+"...", 280)
					return
				}
				if response.StatusCode != http.StatusOK {
					t.Fatalf("mask status = %d", response.StatusCode)
				}
				metadata, fileBytes := parseMultipartMaskResponse(t, response)
				if metadata.Status != "completed" || metadata.MaskPolicy.AppliedRegions != 0 || metadata.Error != nil {
					t.Fatalf("unexpected successful mask: %#v", metadata)
				}
				if metadata.Output.MIMEType != "image/png" || !bytes.Equal(fileBytes, input) {
					t.Fatal("expected unchanged PNG file part for empty fields")
				}
				if metadata.Engine.Debug == nil {
					t.Fatal("missing response debug")
				}
				var debug struct {
					Body string `json:"body"`
				}
				if err := json.Unmarshal([]byte(metadata.Engine.Debug.Response), &debug); err != nil {
					t.Fatal(err)
				}
				// formatDebugBody pretty-prints the JSON before applying its byte budget.
				prefix := "{\n  \"fields\": [],\n  \"message\": \""
				want := prefix + strings.Repeat("가", (16*1024-3-len(prefix))/3) + "..."
				assertDiagnostic(t, debug.Body, want, 16*1024)
			})
		})
	}
}
