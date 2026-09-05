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
