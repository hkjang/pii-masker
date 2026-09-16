package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pii-masker/internal/config"
	"pii-masker/internal/core"
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

// A real endpoint answers with the fields at the top level and, for a document with
// many of them, a body far larger than the truncated debug copy. That copy used to
// be the only fallback the parser saw, so every field was lost and the upload came
// back unchanged under a completed status.
func TestProcessSyncMasksLargeTopLevelResponses(t *testing.T) {
	t.Parallel()

	const fieldCount = 120
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := make([]any, 0, fieldCount)
		for index := range fieldCount {
			y := 2 + index*2
			fields = append(fields, map[string]any{
				"key":        "개인정보.휴대폰번호",
				"value":      "010-1234-5678",
				"confidence": 0.9,
				"boundingBoxes": []any{map[string]any{"page": 1, "vertices": []any{
					map[string]any{"x": 20, "y": y}, map[string]any{"x": 280, "y": y},
					map[string]any{"x": 280, "y": y + 2}, map[string]any{"x": 20, "y": y + 2},
				}}},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion":   "1.1",
			"documentType": "pii",
			"fields":       fields,
			"metadata":     map[string]any{"pages": []any{map[string]any{"page": 1, "width": 300, "height": 300}}},
		})
	}))
	defer upstream.Close()

	svc := newUpstreamService(t, upstream.URL)
	input := ProcessInput{
		Attachment: document.NewAttachment("sample.png", "image/png", createWhitePNG(t, 300, 300)),
	}

	metadata, masked, err := svc.ProcessSync(context.Background(), input)
	if err != nil {
		t.Fatalf("ProcessSync: %v", err)
	}
	if metadata.Status != "completed" {
		t.Fatalf("expected completed, got %#v", metadata)
	}
	if metadata.MaskPolicy.AppliedRegions != fieldCount {
		t.Fatalf("expected %d applied regions, got %d", fieldCount, metadata.MaskPolicy.AppliedRegions)
	}
	img, _, err := image.Decode(bytes.NewReader(masked))
	if err != nil {
		t.Fatalf("decode masked png: %v", err)
	}
	// "010-1234-5678" masks its last four digits, the right third of the box.
	if r, g, b, _ := img.At(270, 3).RGBA(); r != 0 || g != 0 || b != 0 {
		t.Fatalf("expected the trailing digits to be blacked out, got %d %d %d", r, g, b)
	}
	if r, g, b, _ := img.At(40, 3).RGBA(); r != 0xffff || g != 0xffff || b != 0xffff {
		t.Fatalf("expected the leading digits to stay visible, got %d %d %d", r, g, b)
	}
}

// A 200 whose body the parser cannot turn into fields is an upstream failure. It
// must not be mistaken for a document without PII and handed back unmasked.
func TestProcessSyncFailsClosedWhenTheResponseIsNotUnderstood(t *testing.T) {
	t.Parallel()

	cases := map[string]any{
		"chat completion body": map[string]any{
			"id":      "chatcmpl-1",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "{\"name\":\"홍길동\"}"}}},
		},
		"located items without readable values": map[string]any{
			"fields": []any{map[string]any{
				"id":            7,
				"extracted":     map[string]any{"string": "홍길동"},
				"boundingBoxes": []any{map[string]any{"page": 1, "vertices": []any{map[string]any{"x": 1, "y": 1}, map[string]any{"x": 5, "y": 1}, map[string]any{"x": 5, "y": 5}, map[string]any{"x": 1, "y": 5}}}},
			}},
		},
	}
	for name, body := range cases {
		body := body
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer upstream.Close()

			svc := newUpstreamService(t, upstream.URL)
			input := ProcessInput{
				Attachment: document.NewAttachment("sample.png", "image/png", createWhitePNG(t, 300, 120)),
			}

			metadata, masked, err := svc.ProcessSync(context.Background(), input)
			if err == nil {
				t.Fatalf("expected processing to fail")
			}
			if masked != nil {
				t.Fatalf("expected no document to be returned")
			}
			if metadata == nil || metadata.Error == nil || metadata.Error.Code != "upstream_payload_unrecognized" {
				t.Fatalf("expected upstream_payload_unrecognized, got %#v", metadata)
			}
			if metadata.Status != "failed" {
				t.Fatalf("expected failed status, got %q", metadata.Status)
			}
		})
	}
}

// An endpoint that finds nothing to mask is still a success: the file comes back
// unchanged, and the metadata says so through a zero region count.
func TestProcessSyncReturnsTheUploadWhenNoPIIIsReported(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "1.1",
			"fields":     []any{},
			"metadata":   map[string]any{"pages": []any{map[string]any{"page": 1, "width": 300, "height": 120}}},
		})
	}))
	defer upstream.Close()

	svc := newUpstreamService(t, upstream.URL)
	content := createWhitePNG(t, 300, 120)
	input := ProcessInput{Attachment: document.NewAttachment("sample.png", "image/png", content)}

	metadata, masked, err := svc.ProcessSync(context.Background(), input)
	if err != nil {
		t.Fatalf("ProcessSync: %v", err)
	}
	if metadata.Status != "completed" || metadata.MaskPolicy.AppliedRegions != 0 || len(metadata.PIISummary) != 0 {
		t.Fatalf("expected a completed result with nothing masked, got %#v", metadata)
	}
	if !bytes.Equal(masked, content) {
		t.Fatalf("expected the upload to be returned unchanged")
	}
}

func newUpstreamService(t *testing.T, upstreamURL string) *Service {
	t.Helper()

	cfg := config.Config{
		Upstage: config.UpstageConfig{
			BaseURL:    upstreamURL,
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
	return New(cfg, upstage.NewClient(cfg.Upstage), jobStore)
}

func TestJoinPublicURLFallsBackToARelativePath(t *testing.T) {
	t.Parallel()

	if got := JoinPublicURL("", "v1", "jobs", "abc", "result"); got != "/v1/jobs/abc/result" {
		t.Fatalf("expected a relative path without a base url, got %q", got)
	}
	if got := JoinPublicURL("https://pii.example.com/", "v1", "jobs", "abc", "result"); got != "https://pii.example.com/v1/jobs/abc/result" {
		t.Fatalf("unexpected absolute url %q", got)
	}
}

func TestLoadJobInputReadsStoredUpload(t *testing.T) {
	t.Parallel()

	content := createWhitePNG(t, 20, 10)
	inputPath := filepath.Join(t.TempDir(), "input_sample.png")
	if err := os.WriteFile(inputPath, content, 0o644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	job := &core.JobRecord{
		ID: "job-1",
		Metadata: core.ProcessMetadata{
			Input: core.FileDescriptor{FileName: "sample.png", MIMEType: "image/png"},
		},
		InputPath: inputPath,
	}

	input, err := loadJobInput(job, upstage.ParseOptions{Model: "pii"})
	if err != nil {
		t.Fatalf("loadJobInput: %v", err)
	}
	if input.Attachment.Name != "sample.png" || input.Attachment.MIMEType != "image/png" {
		t.Fatalf("unexpected attachment %#v", input.Attachment)
	}
	if input.Attachment.Size != int64(len(content)) || !bytes.Equal(input.Attachment.Content, content) {
		t.Fatalf("expected stored bytes to be restored, got %d bytes", input.Attachment.Size)
	}
	if input.Options.Model != "pii" {
		t.Fatalf("expected options to be preserved, got %#v", input.Options)
	}
}

func TestLoadJobInputFailsWhenStoredUploadIsMissing(t *testing.T) {
	t.Parallel()

	if _, err := loadJobInput(&core.JobRecord{ID: "job-1"}, upstage.ParseOptions{}); err == nil {
		t.Fatalf("expected an error when the job has no stored input path")
	}
	job := &core.JobRecord{ID: "job-1", InputPath: filepath.Join(t.TempDir(), "input_missing.png")}
	if _, err := loadJobInput(job, upstage.ParseOptions{}); err == nil {
		t.Fatalf("expected an error when the stored input file is gone")
	}
}

func TestPurgeExpiredJobsRemovesStoredUploads(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	jobStore, err := jobs.New(root)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	now := time.Now().UTC()
	stalePath := seedStoredJob(t, jobStore, "stale", now.Add(-48*time.Hour))
	freshPath := seedStoredJob(t, jobStore, "fresh", now.Add(-time.Minute))

	svc := newRetentionService(t, root, jobStore, 24*time.Hour)
	deleted, err := svc.PurgeExpiredJobs(now)
	if err != nil {
		t.Fatalf("PurgeExpiredJobs: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "stale" {
		t.Fatalf("expected only the stale job to be purged, got %v", deleted)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected the stored upload to be deleted, got %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("expected the recent upload to be kept: %v", err)
	}
	if _, ok, _ := svc.GetJob("stale"); ok {
		t.Fatalf("expected the purged job to be gone from the store")
	}
}

func TestPurgeExpiredJobsKeepsEverythingWhenRetentionIsDisabled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	jobStore, err := jobs.New(root)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	stalePath := seedStoredJob(t, jobStore, "stale", time.Now().UTC().Add(-1000*time.Hour))

	svc := newRetentionService(t, root, jobStore, 0)
	deleted, err := svc.PurgeExpiredJobs(time.Now().UTC())
	if err != nil {
		t.Fatalf("PurgeExpiredJobs: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("expected no purge when retention is disabled, got %v", deleted)
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Fatalf("expected the stored upload to be kept: %v", err)
	}
}

func TestRetentionSweeperPurgesUntilTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	jobStore, err := jobs.New(root)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	stalePath := seedStoredJob(t, jobStore, "stale", time.Now().UTC().Add(-48*time.Hour))

	svc := newRetentionService(t, root, jobStore, 24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.runRetentionSweeper(ctx, 5*time.Millisecond)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(stalePath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("expected the sweeper to delete the expired upload")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the sweeper to stop when the context is cancelled")
	}
}

func TestRetentionSweepInterval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		retention time.Duration
		want      time.Duration
	}{
		{retention: time.Minute, want: time.Minute},
		{retention: time.Hour, want: 15 * time.Minute},
		{retention: 24 * time.Hour, want: time.Hour},
	}
	for _, testCase := range cases {
		if got := retentionSweepInterval(testCase.retention); got != testCase.want {
			t.Fatalf("retentionSweepInterval(%s) = %s, want %s", testCase.retention, got, testCase.want)
		}
	}
}

func TestAcquireSyncSlotShedsWhenEverySlotIsTaken(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, 0)
	release, err := svc.acquireSyncSlot(context.Background())
	if err != nil {
		t.Fatalf("acquireSyncSlot: %v", err)
	}

	var busy *ServerBusyError
	if _, err := svc.acquireSyncSlot(context.Background()); !errors.As(err, &busy) {
		t.Fatalf("expected a ServerBusyError, got %v", err)
	}
	if busy.Limit != 1 {
		t.Fatalf("expected the limit to be reported, got %#v", busy)
	}

	release()
	if _, err := svc.acquireSyncSlot(context.Background()); err != nil {
		t.Fatalf("expected the released slot to be reusable, got %v", err)
	}
}

func TestAcquireSyncSlotWaitsForAReleasedSlot(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, 5*time.Second)
	release, err := svc.acquireSyncSlot(context.Background())
	if err != nil {
		t.Fatalf("acquireSyncSlot: %v", err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		release()
	}()

	if _, err := svc.acquireSyncSlot(context.Background()); err != nil {
		t.Fatalf("expected the waiting caller to get the freed slot, got %v", err)
	}
}

func TestAcquireSyncSlotStopsWhenTheCallerGoesAway(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, time.Minute)
	if _, err := svc.acquireSyncSlot(context.Background()); err != nil {
		t.Fatalf("acquireSyncSlot: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.acquireSyncSlot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancelled context to be reported, got %v", err)
	}
}

func TestProcessSyncReportsBusyWithoutTouchingTheUpload(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, 0)
	if _, err := svc.acquireSyncSlot(context.Background()); err != nil {
		t.Fatalf("acquireSyncSlot: %v", err)
	}

	input := ProcessInput{Attachment: document.NewAttachment("sample.png", "image/png", createWhitePNG(t, 20, 10))}
	metadata, content, err := svc.ProcessSync(context.Background(), input)

	var busy *ServerBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("expected a ServerBusyError, got %v", err)
	}
	if content != nil {
		t.Fatalf("expected no masked content for a shed request")
	}
	if metadata == nil || metadata.Error == nil {
		t.Fatalf("expected metadata describing the rejection, got %#v", metadata)
	}
	if metadata.Error.Code != "server_busy" || !metadata.Error.Retryable {
		t.Fatalf("unexpected error payload %#v", metadata.Error)
	}
	if metadata.Status != "failed" {
		t.Fatalf("unexpected status %q", metadata.Status)
	}
	if metadata.Input.FileName != "sample.png" || metadata.Input.Size != input.Attachment.Size {
		t.Fatalf("expected the upload to be described, got %#v", metadata.Input)
	}
}

func TestShutdownReturnsWhenNoJobIsRunning(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, 0)
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestShutdownWaitsForARunningJob(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, 0)
	svc.runners.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := svc.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the shutdown to wait for the runner, got %v", err)
	}

	svc.runners.Done()
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected the second shutdown to return once the runner finished, got %v", err)
	}
}

func TestStartJobRunnerRefusesOnceTheServiceIsDraining(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, 0)
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if svc.startJobRunner("job-1", upstage.ParseOptions{}) {
		t.Fatal("expected no runner to be started while draining")
	}
}

func TestAcquireJobSlotGivesUpWhenTheServiceDrains(t *testing.T) {
	t.Parallel()

	svc := newSyncService(t, 1, 0)
	// Every slot is taken, so the next runner has to wait for one.
	for range cap(svc.jobSlots) {
		svc.jobSlots <- struct{}{}
	}

	acquired := make(chan bool, 1)
	go func() { acquired <- svc.acquireJobSlot() }()

	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case got := <-acquired:
		if got {
			t.Fatal("expected the waiting runner to give up instead of starting work")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the runner to give up")
	}
}

// TestCreateJobRecordsAnInterruptedJobWhileDraining covers the race between accepting
// an upload and the service starting to stop: the job is persisted either way, so it
// has to be reported as interrupted rather than left queued forever.
func TestCreateJobRecordsAnInterruptedJobWhileDraining(t *testing.T) {
	t.Parallel()

	svc := newUploadService(t)
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	input := ProcessInput{Attachment: document.NewAttachment("sample.png", "image/png", createWhitePNG(t, 20, 10))}
	job, err := svc.CreateJob(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if job.Metadata.Status != "failed" {
		t.Fatalf("unexpected status %q", job.Metadata.Status)
	}
	if job.Metadata.Error == nil || job.Metadata.Error.Code != "job_interrupted" {
		t.Fatalf("unexpected error payload %#v", job.Metadata.Error)
	}

	stored, ok, err := svc.GetJob(job.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s) = %v, %v", job.ID, ok, err)
	}
	if stored.Metadata.Status != "failed" {
		t.Fatalf("the stored job kept status %q", stored.Metadata.Status)
	}
}

func newSyncService(t *testing.T, limit int, queueWait time.Duration) *Service {
	t.Helper()

	root := t.TempDir()
	jobStore, err := jobs.New(root)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	cfg := config.Config{
		Limits:  config.LimitsConfig{MaxConcurrentSync: limit, SyncQueueWait: queueWait},
		Storage: config.StorageConfig{RootDir: root},
	}
	return New(cfg, upstage.NewClient(cfg.Upstage), jobStore)
}

// newUploadService accepts the uploads the other helpers do not, so a job can be
// created without standing up an upstream for it.
func newUploadService(t *testing.T) *Service {
	t.Helper()

	root := t.TempDir()
	jobStore, err := jobs.New(root)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	cfg := config.Config{
		Limits: config.LimitsConfig{
			MaxFileSizeBytes: 5 * 1024 * 1024,
			MaxPages:         5,
			SupportedMIMEs:   []string{"image/png"},
		},
		Storage: config.StorageConfig{RootDir: root},
	}
	return New(cfg, upstage.NewClient(cfg.Upstage), jobStore)
}

func newRetentionService(t *testing.T, root string, jobStore *jobs.Store, retention time.Duration) *Service {
	t.Helper()

	cfg := config.Config{
		Limits:  config.LimitsConfig{MaxConcurrentJobs: 1},
		Storage: config.StorageConfig{RootDir: root, JobRetention: retention},
	}
	return New(cfg, upstage.NewClient(cfg.Upstage), jobStore)
}

// seedStoredJob writes a completed job with its uploaded file and returns that path.
func seedStoredJob(t *testing.T, jobStore *jobs.Store, id string, updatedAt time.Time) string {
	t.Helper()

	inputPath, err := jobStore.WriteInputFile(id, "sample.png", []byte("original"))
	if err != nil {
		t.Fatalf("WriteInputFile: %v", err)
	}
	job := &core.JobRecord{
		ID: id,
		Metadata: core.ProcessMetadata{
			RequestID: id,
			JobID:     id,
			Status:    "completed",
			CreatedAt: updatedAt,
			UpdatedAt: updatedAt,
		},
		InputPath: inputPath,
	}
	if err := jobStore.Create(job); err != nil {
		t.Fatalf("jobs.Create: %v", err)
	}
	return inputPath
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
