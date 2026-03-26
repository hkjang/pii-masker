package core

import "time"

type FileDescriptor struct {
	FileName    string `json:"file_name"`
	MIMEType    string `json:"mime_type"`
	Size        int64  `json:"size"`
	Pages       int    `json:"pages,omitempty"`
	Inline      bool   `json:"inline,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

type PIISummaryItem struct {
	Key         string   `json:"key"`
	MaskedValue string   `json:"masked_value"`
	RuleName    string   `json:"rule_name"`
	RuleLabel   string   `json:"rule_label"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Page        int      `json:"page,omitempty"`
}

type MaskPolicy struct {
	Mode           string   `json:"mode"`
	AppliedRules   []string `json:"applied_rules"`
	SupportedRules []string `json:"supported_rules,omitempty"`
}

type DebugInfo struct {
	Request  string `json:"request,omitempty"`
	Response string `json:"response,omitempty"`
}

type EngineInfo struct {
	Provider       string     `json:"provider"`
	Model          string     `json:"model"`
	Schema         string     `json:"schema"`
	DurationMS     int64      `json:"duration_ms"`
	UpstreamStatus int        `json:"upstream_status,omitempty"`
	Debug          *DebugInfo `json:"debug,omitempty"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Detail    string `json:"detail,omitempty"`
	Retryable bool   `json:"retryable"`
}

type ProcessMetadata struct {
	RequestID  string           `json:"request_id"`
	JobID      string           `json:"job_id,omitempty"`
	Status     string           `json:"status"`
	Input      FileDescriptor   `json:"input"`
	MaskPolicy MaskPolicy       `json:"mask_policy"`
	PIISummary []PIISummaryItem `json:"pii_summary"`
	Output     FileDescriptor   `json:"output"`
	Engine     EngineInfo       `json:"engine"`
	Error      *APIError        `json:"error,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

type JobRecord struct {
	ID         string          `json:"id"`
	Metadata   ProcessMetadata `json:"metadata"`
	InputPath  string          `json:"-"`
	OutputPath string          `json:"-"`
}
