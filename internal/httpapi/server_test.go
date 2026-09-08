package httpapi

import (
	"mime"
	"testing"
)

func TestHistoryLimitClampsRequestedPageSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		raw      string
		expected int
	}{
		{name: "missing", raw: "", expected: defaultHistoryLimit},
		{name: "blank", raw: "   ", expected: defaultHistoryLimit},
		{name: "not a number", raw: "many", expected: defaultHistoryLimit},
		{name: "zero", raw: "0", expected: defaultHistoryLimit},
		{name: "negative", raw: "-5", expected: defaultHistoryLimit},
		{name: "within range", raw: "7", expected: 7},
		{name: "padded", raw: " 7 ", expected: 7},
		{name: "at the maximum", raw: "100", expected: maxHistoryLimit},
		{name: "above the maximum", raw: "100000", expected: maxHistoryLimit},
		{name: "overflowing an int", raw: "999999999999999999999", expected: defaultHistoryLimit},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if limit := historyLimit(testCase.raw); limit != testCase.expected {
				t.Fatalf("historyLimit(%q) = %d, want %d", testCase.raw, limit, testCase.expected)
			}
		})
	}
}

func TestAttachmentDispositionEncodesNonASCIINames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		filename string
		expected string
	}{
		{
			name:     "ascii name stays a plain quoted parameter",
			filename: "masked_sample.pdf",
			expected: `attachment; filename="masked_sample.pdf"`,
		},
		{
			name:     "empty name falls back to a generic one",
			filename: "",
			expected: `attachment; filename="document"`,
		},
		{
			name:     "quotes and control characters are dropped",
			filename: "sa\"mp\\le\r\n.pdf",
			expected: `attachment; filename="sample.pdf"`,
		},
		{
			name:     "korean name keeps an ascii fallback and adds filename*",
			filename: "masked_계약서.pdf",
			expected: `attachment; filename="masked____.pdf"; filename*=UTF-8''masked_%EA%B3%84%EC%95%BD%EC%84%9C.pdf`,
		},
		{
			name:     "a name with no ascii left uses a generic fallback",
			filename: "계약서",
			expected: `attachment; filename="document"; filename*=UTF-8''%EA%B3%84%EC%95%BD%EC%84%9C`,
		},
		{
			name:     "ascii specials outside attr-char are percent encoded",
			filename: "보고서 (v2).pdf",
			expected: `attachment; filename="___ (v2).pdf"; filename*=UTF-8''%EB%B3%B4%EA%B3%A0%EC%84%9C%20%28v2%29.pdf`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := attachmentDisposition(testCase.filename); got != testCase.expected {
				t.Fatalf("attachmentDisposition(%q) = %q, want %q", testCase.filename, got, testCase.expected)
			}
		})
	}
}

// TestAttachmentDispositionRoundTripsThroughMIMEParser checks that a client that
// understands filename* recovers the original name byte for byte.
func TestAttachmentDispositionRoundTripsThroughMIMEParser(t *testing.T) {
	t.Parallel()

	for _, filename := range []string{"masked_sample.pdf", "masked_계약서.pdf", "masked_보고서 (v2).png", "masked_ünïcode.jpg"} {
		t.Run(filename, func(t *testing.T) {
			t.Parallel()

			disposition := attachmentDisposition(filename)
			mediaType, params, err := mime.ParseMediaType(disposition)
			if err != nil {
				t.Fatalf("parse %q: %v", disposition, err)
			}
			if mediaType != "attachment" {
				t.Fatalf("unexpected media type %q", mediaType)
			}
			if params["filename"] != filename {
				t.Fatalf("filename = %q, want %q", params["filename"], filename)
			}
		})
	}
}
