package app_test

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pii-masker/internal/app"
	"pii-masker/internal/config"
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

func startServer(t *testing.T, customize func(*config.Config)) (*app.App, string, <-chan error, context.CancelFunc) {
	t.Helper()

	upstreamMux := http.NewServeMux()
	upstreamMux.Handle("/inference", mock.UpstageHandler())
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
