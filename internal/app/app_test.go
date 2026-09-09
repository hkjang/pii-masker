package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pii-masker/internal/app"
	"pii-masker/internal/config"
	"pii-masker/internal/core"
	"pii-masker/internal/mock"
)

func TestServeStopsAcceptingAfterContextCancel(t *testing.T) {
	t.Parallel()

	_, address, serveErr, cancel := startServer(t, nil)

	response, err := http.Get("http://" + address + "/v1/health")
	if err != nil {
		t.Fatalf("get /v1/health: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected health status %d", response.StatusCode)
	}

	cancel()
	if err := waitForServe(t, serveErr); err != nil {
		t.Fatalf("serve returned %v", err)
	}

	if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		conn.Close()
		t.Fatalf("expected the listener to be closed after shutdown")
	}
}

func TestServeDrainsInFlightRequestOnShutdown(t *testing.T) {
	t.Parallel()

	_, address, serveErr, cancel := startServer(t, nil)

	body, contentType := longMultipartBody(t)
	head, tail := body[:len(body)/2], body[len(body)/2:]

	pipeReader, pipeWriter := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/v1/mask", pipeReader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", contentType)

	type result struct {
		response *http.Response
		err      error
	}
	results := make(chan result, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		results <- result{response: response, err: err}
	}()

	// The write returns once the transport has picked the bytes up, so the request
	// is on the wire and the handler is blocked reading the rest of the body.
	if _, err := pipeWriter.Write(head); err != nil {
		t.Fatalf("write request head: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	cancel()
	time.Sleep(100 * time.Millisecond)

	if _, err := pipeWriter.Write(tail); err != nil {
		t.Fatalf("in-flight request was dropped by the shutdown: %v", err)
	}
	pipeWriter.Close()

	select {
	case got := <-results:
		if got.err != nil {
			t.Fatalf("in-flight request was dropped by the shutdown: %v", got.err)
		}
		defer got.response.Body.Close()
		// The upload carries no file part, so the drained request is answered with
		// the usual validation error instead of a broken connection.
		if got.response.StatusCode != http.StatusBadRequest {
			t.Fatalf("unexpected status %d", got.response.StatusCode)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the in-flight response")
	}

	if err := waitForServe(t, serveErr); err != nil {
		t.Fatalf("serve returned %v", err)
	}
}

func TestServeClosesConnectionsThatStallOnHeaders(t *testing.T) {
	t.Parallel()

	_, address, serveErr, cancel := startServer(t, func(cfg *config.Config) {
		cfg.Server.ReadHeaderTimeout = 250 * time.Millisecond
	})
	defer func() {
		cancel()
		<-serveErr
	}()

	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Headers are never terminated, so the request stays incomplete forever.
	if _, err := conn.Write([]byte("GET /v1/health HTTP/1.1\r\nHost: example\r\n")); err != nil {
		t.Fatalf("write partial headers: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, err := conn.Read(make([]byte, 512)); err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatalf("server kept the stalled connection open")
		}
	}
}

// TestServeWaitsForRunningAsyncJobOnShutdown covers the work that is not a request:
// an async job masks its document in its own goroutine, so draining the HTTP server
// does not wait for it and a nearly finished mask would otherwise be thrown away.
func TestServeWaitsForRunningAsyncJobOnShutdown(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	upstream, entered, releaseGate := gatedUpstream(t)
	_, address, serveErr, cancel := startServerWithUpstream(t, upstream, func(cfg *config.Config) {
		cfg.Storage.RootDir = rootDir
	})
	// Registered after the servers so it runs before their cleanup: a failing
	// assertion must not leave a request parked in the upstream handler, or the
	// teardown would block instead of reporting the failure.
	t.Cleanup(releaseGate)

	jobID := postJob(t, address)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the job to reach the upstream")
	}

	cancel()
	// The job is parked in its upstream call, so the shutdown has to be waiting on it.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-serveErr:
		t.Fatalf("serve returned %v while a job was still masking a document", err)
	default:
	}

	releaseGate()
	if err := waitForServe(t, serveErr); err != nil {
		t.Fatalf("serve returned %v", err)
	}

	record := readStoredJob(t, rootDir, jobID)
	if record.Metadata.Status != "completed" {
		t.Fatalf("expected the running job to finish, got %q (%#v)", record.Metadata.Status, record.Metadata.Error)
	}
}

// TestServeMarksQueuedJobsInterruptedOnShutdown pins the other half of the drain: a
// job that never claimed a slot must not hold the shutdown open waiting for work the
// process has no time left to do.
func TestServeMarksQueuedJobsInterruptedOnShutdown(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	upstream, entered, releaseGate := gatedUpstream(t)
	_, address, serveErr, cancel := startServerWithUpstream(t, upstream, func(cfg *config.Config) {
		cfg.Storage.RootDir = rootDir
		cfg.Limits.MaxConcurrentJobs = 1
	})
	t.Cleanup(releaseGate)

	runningID := postJob(t, address)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the job to reach the upstream")
	}
	// The only slot is taken, so this one stays queued until the shutdown releases it.
	queuedID := postJob(t, address)

	cancel()
	waitForStoredJob(t, rootDir, queuedID, func(record core.JobRecord) bool {
		return record.Metadata.Status == "failed"
	})
	releaseGate()
	if err := waitForServe(t, serveErr); err != nil {
		t.Fatalf("serve returned %v", err)
	}

	queued := readStoredJob(t, rootDir, queuedID)
	if queued.Metadata.Error == nil || queued.Metadata.Error.Code != "job_interrupted" {
		t.Fatalf("unexpected error on the queued job: %#v", queued.Metadata.Error)
	}
	if running := readStoredJob(t, rootDir, runningID); running.Metadata.Status != "completed" {
		t.Fatalf("expected the running job to finish, got %q", running.Metadata.Status)
	}
}

// gatedUpstream reports when the first inference call arrives and holds every call
// open until the returned function is called. Callers must register that function as
// a cleanup after starting the servers, so a failing assertion releases the parked
// requests before the servers are torn down.
func gatedUpstream(t *testing.T) (http.Handler, <-chan struct{}, func()) {
	t.Helper()

	entered := make(chan struct{})
	release := make(chan struct{})
	inner := mock.UpstageHandler()
	var enteredOnce, releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		inner.ServeHTTP(w, r)
	})
	return handler, entered, releaseGate
}

func postJob(t *testing.T, address string) string {
	t.Helper()

	body, contentType := pngUploadBody(t)
	response, err := http.Post("http://"+address+"/v1/jobs", contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post /v1/jobs: %v", err)
	}
	defer response.Body.Close()

	var metadata core.ProcessMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode job metadata: %v", err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("unexpected status %d", response.StatusCode)
	}
	return metadata.JobID
}

// readStoredJob reads the record the job store keeps on disk, which is the only view
// of a job left once the HTTP server has stopped serving.
func readStoredJob(t *testing.T, rootDir, jobID string) core.JobRecord {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(rootDir, "jobs", jobID, "job.json"))
	if err != nil {
		t.Fatalf("read job record: %v", err)
	}
	var record core.JobRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode job record: %v", err)
	}
	return record
}

func waitForStoredJob(t *testing.T, rootDir, jobID string, condition func(core.JobRecord) bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(rootDir, "jobs", jobID, "job.json"))
		if err == nil {
			var record core.JobRecord
			if json.Unmarshal(raw, &record) == nil && condition(record) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %s", jobID)
}

func pngUploadBody(t *testing.T) ([]byte, string) {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := range 200 {
		for x := range 400 {
			img.Set(x, y, color.White)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "sample.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(encoded.Bytes()); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func startServer(t *testing.T, customize func(*config.Config)) (*app.App, string, <-chan error, context.CancelFunc) {
	t.Helper()

	return startServerWithUpstream(t, mock.UpstageHandler(), customize)
}

func startServerWithUpstream(t *testing.T, upstreamHandler http.Handler, customize func(*config.Config)) (*app.App, string, <-chan error, context.CancelFunc) {
	t.Helper()

	upstreamMux := http.NewServeMux()
	upstreamMux.Handle("/inference", upstreamHandler)
	upstream := httptest.NewServer(upstreamMux)
	t.Cleanup(upstream.Close)

	cfg := config.Config{
		Server: config.ServerConfig{
			Address:         "127.0.0.1:0",
			ShutdownTimeout: 20 * time.Second,
		},
		Upstage: config.UpstageConfig{
			BaseURL:    upstream.URL + "/inference",
			AuthMode:   "bearer",
			Timeout:    5 * time.Second,
			Model:      "pii",
			Lang:       "ko",
			Schema:     "oac",
			AllowHosts: []string{"127.0.0.1"},
		},
		Limits: config.LimitsConfig{
			MaxFileSizeBytes:  5 * 1024 * 1024,
			MaxPages:          10,
			MaxConcurrentSync: 4,
			SyncQueueWait:     10 * time.Second,
			SupportedMIMEs:    []string{"application/pdf", "image/png", "image/jpeg"},
		},
		Storage: config.StorageConfig{RootDir: t.TempDir()},
	}
	if customize != nil {
		customize(&cfg)
	}

	application, err := app.New(cfg)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(application.Close)

	listener, err := net.Listen("tcp", cfg.Server.Address)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- application.Serve(ctx, listener)
	}()

	return application, listener.Addr().String(), serveErr, cancel
}

func waitForServe(t *testing.T, serveErr <-chan error) error {
	t.Helper()

	select {
	case err := <-serveErr:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the server to stop")
		return nil
	}
}

// longMultipartBody builds a well formed upload without a file part that is big
// enough to be split across two writes.
func longMultipartBody(t *testing.T) ([]byte, string) {
	t.Helper()

	var buffer strings.Builder
	writer := multipart.NewWriter(&buffer)
	if err := writer.WriteField("model", strings.Repeat("a", 64*1024)); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return []byte(buffer.String()), writer.FormDataContentType()
}
