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
	mimeType = normalizeMIMEType(mimeType)
	for _, candidate := range allowed {
		if strings.EqualFold(normalizeMIMEType(candidate), mimeType) {
			return nil
		}
	}
	return fmt.Errorf("unsupported file type %q", mimeType)
}

func detectAttachmentMIMEType(name, declaredMIME string, content []byte) string {
	if value := strings.TrimSpace(declaredMIME); value != "" && !strings.EqualFold(value, "application/octet-stream") {
		return normalizeMIMEType(value)
	}
	if len(content) > 0 {
		if detected := http.DetectContentType(content); detected != "" && detected != "application/octet-stream" {
			return normalizeMIMEType(detected)
		}
	}
	if extension := strings.TrimSpace(filepath.Ext(name)); extension != "" {
		if detected := mime.TypeByExtension(extension); detected != "" {
			return normalizeMIMEType(detected)
		}
	}
	if len(content) > 0 {
		return normalizeMIMEType(http.DetectContentType(content))
	}
	return "application/octet-stream"
}

func normalizeMIMEType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if mediaType, _, err := mime.ParseMediaType(value); err == nil {
		value = mediaType
	}
	switch value {
	case "image/jpg", "image/pjpeg":
		return "image/jpeg"
	default:
		return value
	}
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
