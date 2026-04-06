package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"pii-masker/internal/config"
	"pii-masker/internal/core"
	"pii-masker/internal/document"
	"pii-masker/internal/jobs"
	"pii-masker/internal/masking"
	"pii-masker/internal/upstage"
)

type Service struct {
	config   config.Config
	client   *upstage.Client
	jobStore *jobs.Store
}

type ProcessInput struct {
	Attachment document.Attachment
	Options    upstage.ParseOptions
}

func New(cfg config.Config, client *upstage.Client, jobStore *jobs.Store) *Service {
	return &Service{
		config:   cfg,
		client:   client,
		jobStore: jobStore,
	}
}

func (s *Service) ProcessSync(ctx context.Context, input ProcessInput) (*core.ProcessMetadata, []byte, error) {
	requestID := uuid.NewString()
	return s.process(ctx, requestID, input)
}

func (s *Service) CreateJob(ctx context.Context, input ProcessInput) (*core.JobRecord, error) {
	jobID := uuid.NewString()
	now := time.Now().UTC()
	pages, _ := s.countPages(input.Attachment)

	inputPath, err := s.jobStore.WriteInputFile(jobID, input.Attachment.Name, input.Attachment.Content)
	if err != nil {
		return nil, err
	}

	job := &core.JobRecord{
		ID: jobID,
		Metadata: core.ProcessMetadata{
			RequestID: jobID,
			JobID:     jobID,
			Status:    "queued",
			Input: core.FileDescriptor{
				FileName: input.Attachment.Name,
				MIMEType: input.Attachment.MIMEType,
				Size:     input.Attachment.Size,
				Pages:    pages,
			},
			MaskPolicy: core.MaskPolicy{
				Mode:           "selective-redaction",
				SupportedRules: masking.SupportedRuleNames(),
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
		InputPath: inputPath,
	}

	if err := s.jobStore.Create(job); err != nil {
		return nil, err
	}

	go s.runJob(jobID, input)
	return job, nil
}

func (s *Service) runJob(jobID string, input ProcessInput) {
	job, ok, err := s.jobStore.Get(jobID)
	if err != nil || !ok {
		return
	}

	job.Metadata.Status = "running"
	job.Metadata.UpdatedAt = time.Now().UTC()
	_ = s.jobStore.Save(job)

	metadata, maskedContent, processErr := s.process(context.Background(), jobID, input)
	job.Metadata = *metadata
	job.Metadata.JobID = jobID
	job.Metadata.UpdatedAt = time.Now().UTC()

	if processErr == nil {
		outputPath, writeErr := s.jobStore.WriteOutputFile(jobID, metadata.Output.FileName, maskedContent)
		if writeErr != nil {
			job.Metadata.Status = "failed"
			job.Metadata.Error = &core.APIError{
				Code:      "storage_write_failed",
				Message:   "마스킹 결과 파일을 저장하지 못했습니다.",
				Detail:    writeErr.Error(),
				Retryable: true,
			}
		} else {
			job.OutputPath = outputPath
			job.Metadata.Status = "completed"
		}
	} else if job.Metadata.Error == nil {
		job.Metadata.Status = "failed"
		job.Metadata.Error = mapError(processErr)
	}

	_ = s.jobStore.Save(job)
}

func (s *Service) GetJob(id string) (*core.JobRecord, bool, error) {
	return s.jobStore.Get(id)
}

func (s *Service) ListJobs(limit int) ([]core.JobRecord, error) {
	return s.jobStore.List(limit)
}

func (s *Service) TestConnection(ctx context.Context) (upstage.ConnectionStatus, error) {
	return s.client.TestConnection(ctx)
}

func (s *Service) process(ctx context.Context, requestID string, input ProcessInput) (*core.ProcessMetadata, []byte, error) {
	now := time.Now().UTC()
	metadata := &core.ProcessMetadata{
		RequestID: requestID,
		Status:    "failed",
		MaskPolicy: core.MaskPolicy{
			Mode:           "selective-redaction",
			SupportedRules: masking.SupportedRuleNames(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.validateAttachment(input.Attachment); err != nil {
		metadata.Error = mapError(err)
		metadata.Input = core.FileDescriptor{
			FileName: input.Attachment.Name,
			MIMEType: input.Attachment.MIMEType,
			Size:     input.Attachment.Size,
		}
		return metadata, nil, err
	}

	pages, err := s.countPages(input.Attachment)
	if err != nil {
		metadata.Error = mapError(err)
		return metadata, nil, err
	}

	metadata.Input = core.FileDescriptor{
		FileName: input.Attachment.Name,
		MIMEType: input.Attachment.MIMEType,
		Size:     input.Attachment.Size,
		Pages:    pages,
	}

	result, upstreamStatus, duration, err := s.client.ParseDocument(ctx, input.Attachment, input.Options)
	if err != nil {
		metadata.Error = mapError(err)
		metadata.Engine = core.EngineInfo{
			Provider:       "upstage",
			Model:          defaultIfEmpty(input.Options.Model, s.config.Upstage.Model),
			Schema:         defaultIfEmpty(input.Options.Schema, s.config.Upstage.Schema),
			DurationMS:     duration.Milliseconds(),
			UpstreamStatus: upstreamStatus,
		}
		if s.config.Debug.EnableDebug {
			metadata.Engine.Debug = &core.DebugInfo{
				Request:  marshalDebug(result.RequestDebug),
				Response: marshalDebug(result.ResponseDebug),
			}
		}
		return metadata, nil, err
	}

	payload := masking.ParsePayload(result.Response.Result, result.ResponseDebug.Body)
	pageSizes := masking.ExtractPageSizes(payload)
	regions := masking.CollectMaskRegions(payload)
	fieldEntries := masking.BuildFieldEntries(payload)
	appliedRules, summary := summarizeFields(fieldEntries)
	metadata.MaskPolicy.AppliedRules = appliedRules
	metadata.PIISummary = summary
	metadata.Engine = core.EngineInfo{
		Provider:       "upstage",
		Model:          defaultIfEmpty(result.Response.Model, defaultIfEmpty(input.Options.Model, s.config.Upstage.Model)),
		Schema:         detectSchema(payload),
		DurationMS:     duration.Milliseconds(),
		UpstreamStatus: upstreamStatus,
	}
	if s.config.Debug.EnableDebug {
		metadata.Engine.Debug = &core.DebugInfo{
			Request:  marshalDebug(result.RequestDebug),
			Response: marshalDebug(result.ResponseDebug),
		}
	}

	if len(summary) > 0 && len(regions) == 0 {
		err = fmt.Errorf("PII fields were detected but the upstream response did not contain usable bounding boxes for partial visual masking")
		metadata.Error = mapError(err)
		return metadata, nil, err
	}

	maskedContent := input.Attachment.Content
	if len(regions) > 0 {
		switch {
		case strings.Contains(strings.ToLower(input.Attachment.MIMEType), "pdf"):
			maskedContent, err = masking.MaskPDFFile(input.Attachment.Content, regions, pageSizes)
		case strings.HasPrefix(strings.ToLower(input.Attachment.MIMEType), "image/"):
			maskedContent, err = masking.MaskImageFile(input.Attachment.Content, input.Attachment.MIMEType, regions, pageSizes)
		default:
			err = fmt.Errorf("unsupported mask output type %q", input.Attachment.MIMEType)
		}
		if err != nil {
			metadata.Error = mapError(err)
			return metadata, nil, err
		}
		if bytes.Equal(maskedContent, input.Attachment.Content) {
			err = fmt.Errorf("masking detected drawable regions but produced an output identical to the original document")
			metadata.Error = mapError(err)
			return metadata, nil, err
		}
	}

	metadata.Status = "completed"
	metadata.Output = core.FileDescriptor{
		FileName: document.MaskedFilename(input.Attachment.Name),
		MIMEType: input.Attachment.MIMEType,
		Size:     int64(len(maskedContent)),
		Pages:    pages,
	}
	metadata.UpdatedAt = time.Now().UTC()

	return metadata, maskedContent, nil
}

func (s *Service) validateAttachment(attachment document.Attachment) error {
	if attachment.Size == 0 {
		return fmt.Errorf("uploaded file is empty")
	}
	if attachment.Size > s.config.Limits.MaxFileSizeBytes {
		return fmt.Errorf("uploaded file exceeds the maximum size of %d bytes", s.config.Limits.MaxFileSizeBytes)
	}
	if err := document.ValidateMIMEType(attachment.MIMEType, s.config.Limits.SupportedMIMEs); err != nil {
		return err
	}
	return nil
}

func (s *Service) countPages(attachment document.Attachment) (int, error) {
	switch {
	case strings.Contains(strings.ToLower(attachment.MIMEType), "pdf"):
		pdfmodel.ConfigPath = "disable"
		conf := pdfmodel.NewDefaultConfiguration()
		conf.ValidationMode = pdfmodel.ValidationRelaxed
		dims, err := api.PageDims(bytes.NewReader(attachment.Content), conf)
		if err != nil {
			return 0, fmt.Errorf("failed to inspect pdf pages: %w", err)
		}
		if s.config.Limits.MaxPages > 0 && len(dims) > s.config.Limits.MaxPages {
			return 0, fmt.Errorf("uploaded pdf exceeds the maximum page count of %d", s.config.Limits.MaxPages)
		}
		return len(dims), nil
	case strings.HasPrefix(strings.ToLower(attachment.MIMEType), "image/"):
		return 1, nil
	default:
		return 0, fmt.Errorf("unsupported file type %q", attachment.MIMEType)
	}
}

func summarizeFields(entries []masking.FieldEntry) ([]string, []core.PIISummaryItem) {
	appliedSet := map[string]struct{}{}
	summary := make([]core.PIISummaryItem, 0, len(entries))
	for _, entry := range entries {
		appliedSet[entry.Rule.RuleName] = struct{}{}
		item := core.PIISummaryItem{
			Key:         entry.Key,
			MaskedValue: entry.MaskedValue,
			RuleName:    entry.Rule.RuleName,
			RuleLabel:   entry.Rule.DisplayName,
			Page:        entry.PageNumber,
		}
		if entry.HasConfidence {
			confidence := entry.Confidence
			item.Confidence = &confidence
		}
		summary = append(summary, item)
	}

	appliedRules := make([]string, 0, len(appliedSet))
	for rule := range appliedSet {
		appliedRules = append(appliedRules, rule)
	}
	sort.Strings(appliedRules)
	return appliedRules, summary
}

func detectSchema(payload any) string {
	root, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	if _, ok := root["fields"]; ok {
		return "oac"
	}
	for _, key := range []string{"document", "documents", "groups", "entities"} {
		if _, ok := root[key]; ok {
			return "ufp"
		}
	}
	return ""
}

func marshalDebug(payload any) string {
	raw, err := jsonMarshalIndent(payload)
	if err != nil {
		return ""
	}
	return raw
}

func jsonMarshalIndent(payload any) (string, error) {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func mapError(err error) *core.APIError {
	if err == nil {
		return nil
	}
	if callErr, ok := err.(*upstage.CallError); ok {
		return &core.APIError{
			Code:      callErr.Code,
			Message:   callErr.Summary,
			Detail:    callErr.Detail,
			Retryable: callErr.Retryable,
		}
	}
	return &core.APIError{
		Code:      "processing_failed",
		Message:   err.Error(),
		Retryable: false,
	}
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func JoinPublicURL(baseURL string, parts ...string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		cleaned = append(cleaned, strings.Trim(part, "/"))
	}
	return baseURL + "/" + filepath.ToSlash(strings.Join(cleaned, "/"))
}
