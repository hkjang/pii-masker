package document

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestSanitizeUploadFilename(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "keeps a plain name", input: "sample.png", expected: "sample.png"},
		{name: "keeps a korean name", input: "주민등록증.png", expected: "주민등록증.png"},
		{name: "trims surrounding space", input: "  sample.pdf \t", expected: "sample.pdf"},
		{name: "drops posix directories", input: "../../etc/passwd", expected: "passwd"},
		{name: "drops windows directories", input: `C:\Users\admin\sample.png`, expected: "sample.png"},
		{name: "drops carriage return and line feed", input: "sample\r\nX-Injected: yes.png", expected: "sampleX-Injected: yes.png"},
		{name: "drops quotes", input: `sa"mple.png`, expected: "sample.png"},
		{name: "treats a trailing backslash segment as a directory", input: `sa"mp\le.png`, expected: "le.png"},
		{name: "drops other control characters", input: "sam\x00p\x7fle.png", expected: "sample.png"},
		{name: "falls back for an empty name", input: "   ", expected: "document"},
		{name: "falls back for a dot name", input: ".", expected: "document"},
		{name: "falls back for a parent name", input: "..", expected: "document"},
		{name: "falls back when only unsafe runes remain", input: "\r\n\"\\", expected: "document"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := sanitizeUploadFilename(testCase.input); got != testCase.expected {
				t.Fatalf("sanitizeUploadFilename(%q) = %q, want %q", testCase.input, got, testCase.expected)
			}
		})
	}
}

func TestSanitizeUploadFilenameTruncatesLongNames(t *testing.T) {
	t.Parallel()

	sanitized := sanitizeUploadFilename(strings.Repeat("가", 300) + ".png")
	if len(sanitized) > maxUploadFilenameBytes {
		t.Fatalf("expected at most %d bytes, got %d", maxUploadFilenameBytes, len(sanitized))
	}
	if !strings.HasSuffix(sanitized, ".png") {
		t.Fatalf("expected the extension to be kept, got %q", sanitized)
	}
	if !utf8.ValidString(sanitized) {
		t.Fatalf("expected valid utf-8, got %q", sanitized)
	}
}

func TestNewAttachmentSanitizesName(t *testing.T) {
	t.Parallel()

	attachment := NewAttachment("scan\r\nContent-Type: text/html\r\n\r\n.png", "image/png", []byte("data"))
	assertNoControlRunes(t, attachment.Name)
	if attachment.Extension != "png" {
		t.Fatalf("unexpected extension %q", attachment.Extension)
	}
}

func TestMaskedFilenameSanitizesName(t *testing.T) {
	t.Parallel()

	masked := MaskedFilename("scan\r\nX-Injected: yes.pdf")
	assertNoControlRunes(t, masked)
	if !strings.HasPrefix(masked, "masked_") {
		t.Fatalf("expected masked prefix, got %q", masked)
	}
}

func assertNoControlRunes(t *testing.T, value string) {
	t.Helper()

	for _, r := range value {
		if unicode.IsControl(r) {
			t.Fatalf("expected no control runes in %q", value)
		}
	}
}
