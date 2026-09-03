package document

import (
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
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

// maxUploadFilenameBytes keeps the stored name inside the 255 byte limit most file
// systems enforce, even after the jobs store adds its "input_"/"output_" prefix.
const maxUploadFilenameBytes = 120

// sanitizeUploadFilename reduces an uploaded file name to a single path element
// that is safe to embed in a multipart part header and to write to disk. Clients
// may send the name RFC 2231 encoded, so the decoded value can carry CR/LF or
// quotes that would otherwise break out of a Content-Disposition header.
func sanitizeUploadFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "document"
	}
	base := filepath.Base(name)
	if index := strings.LastIndexAny(base, `/\`); index >= 0 {
		base = base[index+1:]
	}
	base = strings.TrimSpace(stripUnsafeFilenameRunes(base))
	if base == "" || strings.Trim(base, ".") == "" {
		return "document"
	}
	return truncateFilename(base, maxUploadFilenameBytes)
}

// stripUnsafeFilenameRunes drops the characters that would let a file name escape
// the header field or the directory it is written to.
func stripUnsafeFilenameRunes(name string) string {
	var builder strings.Builder
	builder.Grow(len(name))
	for _, r := range name {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r):
			continue
		case r == '"', r == '\\', r == '/':
			continue
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// truncateFilename shortens a name to maxBytes while keeping its extension and
// never cutting a multi byte rune in half.
func truncateFilename(name string, maxBytes int) string {
	if len(name) <= maxBytes {
		return name
	}
	extension := filepath.Ext(name)
	if len(extension) > 16 || len(extension) >= maxBytes {
		extension = ""
	}
	stem := name[:len(name)-len(extension)]
	limit := maxBytes - len(extension)
	truncated := make([]byte, 0, limit)
	for _, r := range stem {
		if len(truncated)+utf8.RuneLen(r) > limit {
			break
		}
		truncated = utf8.AppendRune(truncated, r)
	}
	if len(truncated) == 0 {
		return "document" + extension
	}
	return string(truncated) + extension
}

func MaskedFilename(original string) string {
	return "masked_" + sanitizeUploadFilename(original)
}
