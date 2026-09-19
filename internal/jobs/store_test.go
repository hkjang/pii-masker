package jobs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// The record is staged in a temporary file and moved into place, and a job
// directory holds nothing but the record and the documents once that is done.
func TestSaveLeavesNoTemporaryFileBehind(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	inputPath := seedJob(t, store, "steady-job", "queued", now)
	jobDir := filepath.Dir(inputPath)

	job, ok, err := store.Get("steady-job")
	if err != nil || !ok {
		t.Fatalf("expected the seeded job, ok=%v err=%v", ok, err)
	}
	for _, status := range []string{"running", "completed", "completed"} {
		job.Metadata.Status = status
		job.Metadata.UpdatedAt = time.Now().UTC()
		if err := store.Save(job); err != nil {
			t.Fatalf("Save(%s): %v", status, err)
		}
	}

	entries, err := os.ReadDir(jobDir)
	if err != nil {
		t.Fatalf("read job dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "job.json" || strings.HasPrefix(name, "input_") || strings.HasPrefix(name, "output_") {
			continue
		}
		t.Fatalf("unexpected entry %q left in the job directory", name)
	}
	stored, ok := readJobRecord(jobDir, "steady-job")
	if !ok || stored.Metadata.Status != "completed" {
		t.Fatalf("expected the last saved record on disk, ok=%v record=%#v", ok, stored)
	}
}

// A save that cannot stage the new record leaves the previous one untouched, so a
// crash mid write can never turn a job that was running into an unreadable record
// that the next start would remove together with its files.
func TestSaveKeepsThePreviousRecordWhenTheWriteFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	inputPath := seedJob(t, store, "running-job", "running", now)
	jobDir := filepath.Dir(inputPath)

	before, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		t.Fatalf("read job.json: %v", err)
	}
	// A directory in the temporary file's place makes staging the record fail.
	if err := os.Mkdir(filepath.Join(jobDir, recordTempName), 0o755); err != nil {
		t.Fatalf("block temporary record: %v", err)
	}

	job, ok, err := store.Get("running-job")
	if err != nil || !ok {
		t.Fatalf("expected the seeded job, ok=%v err=%v", ok, err)
	}
	job.Metadata.Status = "completed"
	job.Metadata.UpdatedAt = now.Add(time.Minute)
	if err := store.Save(job); err == nil {
		t.Fatalf("expected Save to fail when the record cannot be staged")
	}

	after, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		t.Fatalf("read job.json after the failed save: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("expected job.json to be left byte for byte as it was\nbefore: %s\nafter: %s", before, after)
	}
	stored, ok := readJobRecord(jobDir, "running-job")
	if !ok || stored.Metadata.Status != "running" {
		t.Fatalf("expected the previous running record on disk, ok=%v record=%#v", ok, stored)
	}
}

// A temporary record left by a crash sits next to a valid job.json, so the job is
// loaded as usual instead of being treated as an orphan, and the next save that
// stages a new record replaces the leftover.
func TestLoadIgnoresALeftoverTemporaryRecord(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	inputPath := seedJob(t, store, "survivor", "completed", now)
	jobDir := filepath.Dir(inputPath)
	leftover := filepath.Join(jobDir, recordTempName)
	if err := os.WriteFile(leftover, []byte(`{"id":"trunc`), 0o644); err != nil {
		t.Fatalf("write leftover temporary record: %v", err)
	}

	reloaded, err := New(root)
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}
	job, ok, err := reloaded.Get("survivor")
	if err != nil || !ok {
		t.Fatalf("expected the job to be reloaded despite the leftover, ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(inputPath); err != nil {
		t.Fatalf("expected the job directory to be kept: %v", err)
	}
	if job.InputPath != inputPath || !strings.HasPrefix(filepath.Base(job.OutputPath), "output_") {
		t.Fatalf("expected the stored documents to be found, got input=%q output=%q", job.InputPath, job.OutputPath)
	}
	if job.Metadata.Status != "completed" {
		t.Fatalf("expected the record next to the leftover to be loaded as is, got %#v", job.Metadata)
	}

	job.Metadata.UpdatedAt = now.Add(time.Minute)
	if err := reloaded.Save(job); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("expected the leftover temporary record to be gone after a save, got %v", err)
	}
	stored, ok := readJobRecord(jobDir, "survivor")
	if !ok || !stored.Metadata.UpdatedAt.Equal(job.Metadata.UpdatedAt) {
		t.Fatalf("expected the saved record on disk, ok=%v record=%#v", ok, stored)
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
