package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pii-masker/internal/core"
)

func TestDeleteExpiredRemovesStoredFilesOfOldJobs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := time.Now().UTC()
	stale := seedJob(t, store, "stale", "completed", now.Add(-48*time.Hour))
	fresh := seedJob(t, store, "fresh", "completed", now.Add(-time.Hour))

	deleted, err := store.DeleteExpired(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "stale" {
		t.Fatalf("expected only the stale job to be deleted, got %v", deleted)
	}

	if _, ok, _ := store.Get("stale"); ok {
		t.Fatalf("expected the stale job to be gone from the store")
	}
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Fatalf("expected the stale job directory to be removed, got %v", err)
	}

	if _, ok, _ := store.Get("fresh"); !ok {
		t.Fatalf("expected the fresh job to be kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("expected the fresh input file to be kept: %v", err)
	}
}

func TestDeleteExpiredKeepsUnfinishedJobs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := time.Now().UTC()
	queuedInput := seedJob(t, store, "queued-job", "queued", now.Add(-72*time.Hour))
	runningInput := seedJob(t, store, "running-job", "running", now.Add(-72*time.Hour))

	deleted, err := store.DeleteExpired(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("expected unfinished jobs to be kept, got %v", deleted)
	}
	for _, path := range []string{queuedInput, runningInput} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to be kept: %v", path, err)
		}
	}
}

func TestDeleteExpiredIsAppliedToReloadedJobs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	seedJob(t, store, "stale", "completed", now.Add(-48*time.Hour))

	reloaded, err := New(root)
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}
	job, ok, err := reloaded.Get("stale")
	if err != nil || !ok {
		t.Fatalf("expected the job to be reloaded, ok=%v err=%v", ok, err)
	}
	if job.InputPath == "" || job.OutputPath == "" {
		t.Fatalf("expected reloaded file paths, got %#v", job)
	}

	deleted, err := reloaded.DeleteExpired(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "stale" {
		t.Fatalf("expected the reloaded job to be deleted, got %v", deleted)
	}
	if _, err := os.Stat(job.InputPath); !os.IsNotExist(err) {
		t.Fatalf("expected the reloaded input file to be removed, got %v", err)
	}
}

// A job that was interrupted keeps the timestamp it had, so the upload it stored
// is still swept once the retention window measured from that moment has passed.
func TestLoadKeepsTheTimestampOfInterruptedJobs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	interruptedAt := now.Add(-48 * time.Hour)
	seedJob(t, store, "running-job", "running", interruptedAt)

	reloaded, err := New(root)
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}
	job, ok, err := reloaded.Get("running-job")
	if err != nil || !ok {
		t.Fatalf("expected the job to be reloaded, ok=%v err=%v", ok, err)
	}
	if job.Metadata.Status != "failed" || job.Metadata.Error == nil || job.Metadata.Error.Code != "job_interrupted" {
		t.Fatalf("expected an interrupted job, got %#v", job.Metadata)
	}
	if !job.Metadata.UpdatedAt.Equal(interruptedAt) {
		t.Fatalf("expected the stored timestamp %s, got %s", interruptedAt, job.Metadata.UpdatedAt)
	}

	deleted, err := reloaded.DeleteExpired(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "running-job" {
		t.Fatalf("expected the interrupted job to be deleted, got %v", deleted)
	}
	if _, err := os.Stat(job.InputPath); !os.IsNotExist(err) {
		t.Fatalf("expected the stored upload to be removed, got %v", err)
	}
}

// The interrupted state is written back, so restarting again does not have to redo
// the transition and cannot keep moving the retention deadline forward.
func TestLoadPersistsInterruptedJobs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	queuedAt := time.Now().UTC().Add(-time.Hour)
	seedJob(t, store, "queued-job", "queued", queuedAt)

	if _, err := New(root); err != nil {
		t.Fatalf("New (reload): %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "jobs", "queued-job", "job.json"))
	if err != nil {
		t.Fatalf("read job.json: %v", err)
	}
	var stored core.JobRecord
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode job.json: %v", err)
	}
	if stored.Metadata.Status != "failed" || stored.Metadata.Error == nil || stored.Metadata.Error.Code != "job_interrupted" {
		t.Fatalf("expected the interrupted state on disk, got %#v", stored.Metadata)
	}
	if !stored.Metadata.UpdatedAt.Equal(queuedAt) {
		t.Fatalf("expected the stored timestamp %s, got %s", queuedAt, stored.Metadata.UpdatedAt)
	}
}

// seedJob stores a job with an input and an output file and returns the input path.
func seedJob(t *testing.T, store *Store, id, status string, updatedAt time.Time) string {
	t.Helper()

	job := &core.JobRecord{
		ID: id,
		Metadata: core.ProcessMetadata{
			RequestID: id,
			JobID:     id,
			Status:    status,
			CreatedAt: updatedAt,
			UpdatedAt: updatedAt,
		},
	}
	inputPath, err := store.WriteInputFile(id, "sample.png", []byte("original"))
	if err != nil {
		t.Fatalf("WriteInputFile: %v", err)
	}
	if _, err := store.WriteOutputFile(id, "sample_masked.png", []byte("masked")); err != nil {
		t.Fatalf("WriteOutputFile: %v", err)
	}
	job.InputPath = inputPath
	if err := store.Create(job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return inputPath
}
