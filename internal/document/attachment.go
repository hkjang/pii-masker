package document

import (
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

type Attachment struct {
	Name      string
	MIMEType  string
	Extension string
	Size      int64
	Content   []byte
}

func NewAttachment(name, declaredMIME string, content []byte) Attachment {
	name = sanitizeUploadFilename(name)
	return Attachment{
		Name:      name,
		MIMEType:  detectAttachmentMIMEType(name, declaredMIME, content),
		Extension: strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")),
		Size:      int64(len(content)),
		Content:   content,
	}
}

func ValidateMIMEType(mimeType string, allowed []string) error {
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(mimeType)) {
			return nil
		}
	}
	return fmt.Errorf("unsupported file type %q", mimeType)
}

func detectAttachmentMIMEType(name, declaredMIME string, content []byte) string {
	if value := strings.TrimSpace(declaredMIME); value != "" && !strings.EqualFold(value, "application/octet-stream") {
		return value
	}
	if len(content) > 0 {
		if detected := http.DetectContentType(content); detected != "" && detected != "application/octet-stream" {
			return detected
		}
	}
	if extension := strings.TrimSpace(filepath.Ext(name)); extension != "" {
		if detected := mime.TypeByExtension(extension); detected != "" {
			return detected
		}
	}
	if len(content) > 0 {
		return http.DetectContentType(content)
	}
	return "application/octet-stream"
}

func sanitizeUploadFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "document"
	}
	base := filepath.Base(name)
	if strings.TrimSpace(base) == "" || base == "." || base == string(filepath.Separator) {
		return "document"
	}
	return base
}

func MaskedFilename(original string) string {
	return "masked_" + sanitizeUploadFilename(original)
}
