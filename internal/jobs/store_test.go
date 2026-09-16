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

// A directory that holds an upload but no usable record is unreachable through the
// store and would never be swept, so loading removes it along with the document.
func TestLoadRemovesOrphanedJobDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	keptInput := seedJob(t, store, "kept-job", "completed", now)

	jobsDir := filepath.Join(root, "jobs")
	// The crash-between-writes case: the upload landed, job.json never did.
	noRecord := writeOrphanDir(t, jobsDir, "no-record", nil)
	// A record cut off mid write.
	corruptRecord := writeOrphanDir(t, jobsDir, "corrupt-record", []byte(`{"id":"corrupt-rec`))
	// A record that names a different job than the directory it sits in.
	mismatched := core.JobRecord{
		ID:       "other-job",
		Metadata: core.ProcessMetadata{JobID: "other-job", Status: "completed", UpdatedAt: now},
	}
	rawMismatched, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatalf("marshal mismatched record: %v", err)
	}
	mismatchedDir := writeOrphanDir(t, jobsDir, "mismatched-record", rawMismatched)

	reloaded, err := New(root)
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}

	for _, dir := range []string{noRecord, corruptRecord, mismatchedDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("expected orphaned directory %s to be removed, got %v", dir, err)
		}
	}
	for _, id := range []string{"no-record", "corrupt-record", "mismatched-record", "other-job"} {
		if _, ok, _ := reloaded.Get(id); ok {
			t.Fatalf("expected no job %q to be loaded from an orphaned directory", id)
		}
	}

	if _, ok, _ := reloaded.Get("kept-job"); !ok {
		t.Fatalf("expected the intact job to be reloaded")
	}
	if _, err := os.Stat(keptInput); err != nil {
		t.Fatalf("expected the intact job files to be kept: %v", err)
	}
}

// A job whose record cannot be written leaves neither an entry nor the upload it
// stored a moment earlier behind.
func TestCreateRemovesTheDirectoryWhenTheRecordCannotBePersisted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	inputPath, err := store.WriteInputFile("doomed-job", "sample.png", []byte("original"))
	if err != nil {
		t.Fatalf("WriteInputFile: %v", err)
	}
	// A directory in the record's place makes writing job.json fail.
	jobDir := filepath.Dir(inputPath)
	if err := os.Mkdir(filepath.Join(jobDir, "job.json"), 0o755); err != nil {
		t.Fatalf("block job.json: %v", err)
	}

	job := &core.JobRecord{
		ID:        "doomed-job",
		Metadata:  core.ProcessMetadata{JobID: "doomed-job", Status: "queued", UpdatedAt: time.Now().UTC()},
		InputPath: inputPath,
	}
	if err := store.Create(job); err == nil {
		t.Fatalf("expected Create to fail when job.json cannot be written")
	}

	if _, ok, _ := store.Get("doomed-job"); ok {
		t.Fatalf("expected the failed job to be absent from the store")
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("expected the job directory and its upload to be removed, got %v", err)
	}
}

// writeOrphanDir creates a job directory holding an upload and, when record is not
// nil, the given job.json bytes. It returns the directory path.
func writeOrphanDir(t *testing.T, jobsDir, name string, record []byte) string {
	t.Helper()

	dir := filepath.Join(jobsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create orphan dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input_sample.png"), []byte("original"), 0o644); err != nil {
		t.Fatalf("write orphan upload: %v", err)
	}
	if record != nil {
		if err := os.WriteFile(filepath.Join(dir, "job.json"), record, 0o644); err != nil {
			t.Fatalf("write orphan record: %v", err)
		}
	}
	return dir
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
