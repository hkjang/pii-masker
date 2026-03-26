package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"pii-masker/internal/core"
)

type Store struct {
	root string
	mu   sync.RWMutex
	jobs map[string]*core.JobRecord
}

func New(root string) (*Store, error) {
	store := &Store{
		root: filepath.Join(root, "jobs"),
		jobs: map[string]*core.JobRecord{},
	}
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create jobs dir: %w", err)
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Create(job *core.JobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = cloneJob(job)
	return s.persistLocked(job.ID)
}

func (s *Store) Save(job *core.JobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = cloneJob(job)
	return s.persistLocked(job.ID)
}

func (s *Store) Get(id string) (*core.JobRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return nil, false, nil
	}
	return cloneJob(job), true, nil
}

func (s *Store) List(limit int) ([]core.JobRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]core.JobRecord, 0, len(s.jobs))
	for _, job := range s.jobs {
		items = append(items, *cloneJob(job))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Metadata.UpdatedAt.After(items[j].Metadata.UpdatedAt)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) WriteInputFile(jobID, filename string, content []byte) (string, error) {
	return s.writeFile(jobID, "input_"+filename, content)
}

func (s *Store) WriteOutputFile(jobID, filename string, content []byte) (string, error) {
	return s.writeFile(jobID, "output_"+filename, content)
}

func (s *Store) writeFile(jobID, filename string, content []byte) (string, error) {
	jobDir := filepath.Join(s.root, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return "", err
	}
	fullPath := filepath.Join(jobDir, filename)
	if err := os.WriteFile(fullPath, content, 0o644); err != nil {
		return "", err
	}
	return fullPath, nil
}

func (s *Store) load() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return fmt.Errorf("failed to read jobs dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.root, entry.Name(), "job.json"))
		if err != nil {
			continue
		}
		var job core.JobRecord
		if err := json.Unmarshal(raw, &job); err != nil {
			continue
		}
		jobDir := filepath.Join(s.root, entry.Name())
		job.InputPath = firstExistingFile(jobDir, "input_")
		job.OutputPath = firstExistingFile(jobDir, "output_")
		if job.Metadata.Status == "queued" || job.Metadata.Status == "running" {
			job.Metadata.Status = "failed"
			job.Metadata.Error = &core.APIError{
				Code:      "job_interrupted",
				Message:   "서버 재기동으로 작업이 중단되었습니다.",
				Retryable: true,
			}
			job.Metadata.UpdatedAt = time.Now().UTC()
		}
		s.jobs[job.ID] = cloneJob(&job)
	}
	return nil
}

func (s *Store) persistLocked(jobID string) error {
	job, ok := s.jobs[jobID]
	if !ok {
		return nil
	}
	jobDir := filepath.Join(s.root, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(jobDir, "job.json"), raw, 0o644)
}

func cloneJob(job *core.JobRecord) *core.JobRecord {
	if job == nil {
		return nil
	}
	copyJob := *job
	copyJob.Metadata.PIISummary = append([]core.PIISummaryItem(nil), job.Metadata.PIISummary...)
	copyJob.Metadata.MaskPolicy.AppliedRules = append([]string(nil), job.Metadata.MaskPolicy.AppliedRules...)
	copyJob.Metadata.MaskPolicy.SupportedRules = append([]string(nil), job.Metadata.MaskPolicy.SupportedRules...)
	return &copyJob
}

func firstExistingFile(dir, prefix string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if len(entry.Name()) >= len(prefix) && entry.Name()[:len(prefix)] == prefix {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}
