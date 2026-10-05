package masking

import "testing"

func TestMaskValueExamples(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		key      string
		value    string
		expected string
	}{
		{name: "resident registration", key: "개인정보.주민등록번호", value: "800901-1234567", expected: "800901-*******"},
		{name: "driver license", key: "개인정보.운전면허번호", value: "11-24-123456-62", expected: "11-24-******-62"},
		{name: "passport", key: "개인정보.여권번호", value: "12345ABCD", expected: "12345****"},
		{name: "alien registration", key: "개인정보.외국인등록번호", value: "123456-1234567", expected: "123456-*******"},
		{name: "mobile", key: "개인정보.휴대폰번호", value: "010-1234-5678", expected: "010-1234-****"},
		{name: "telephone", key: "개인정보.전화번호", value: "02-123-4567", expected: "02-123-****"},
		{name: "credit card", key: "개인정보.신용카드번호", value: "1234-5678-9012-3456", expected: "****-****-****-3456"},
		{name: "account", key: "개인정보.계좌번호", value: "123-456-789-123", expected: "***-***-***-123"},
		{name: "name", key: "개인정보.이름", value: "홍길동", expected: "홍*동"},
		{name: "email", key: "개인정보.이메일", value: "abcdefg@naver.com", expected: "abc****@naver.com"},
		{name: "email short local", key: "개인정보.이메일", value: "abcd@naver.com", expected: "****@naver.com"},
		{name: "ip", key: "개인정보.IP주소", value: "192.168.10.123", expected: "192.168.**.123"},
		{name: "address", key: "개인정보.주소", value: "서울 영등포구 국제금융로 10", expected: "서울 영등포구 국제금융로 **"},
		{name: "address with repeated spaces", key: "개인정보.주소", value: "서울  영등포구  국제금융로  10", expected: "서울  영등포구  국제금융로  **"},
		{name: "padded resident registration", key: "개인정보.주민등록번호", value: " 800901-1234567 ", expected: " 800901-******* "},
		{name: "padded name", key: "개인정보.이름", value: " 홍길동 ", expected: " 홍*동 "},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			masked := MaskValue(tt.key, tt.value)
			if masked.MaskedValue != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, masked.MaskedValue)
			}
		})
	}
}

// The masked value is reported back in pii_summary[].masked_value and stored in
// job.json, so a rule that matched but hid nothing would hand the original PII
// back to the caller under a rule name claiming it had been masked.
func TestMaskValueNeverReturnsTheOriginalValue(t *testing.T) {
	t.Parallel()

	values := []struct {
		name  string
		key   string
		value string
	}{
		{name: "single rune name", key: "이름", value: "이"},
		{name: "single rune latin name", key: "firstname", value: "J"},
		{name: "resident registration birth date only", key: "주민등록번호", value: "900101"},
		{name: "resident registration placeholder", key: "주민등록번호", value: "확인불가"},
		{name: "credit card placeholder", key: "신용카드번호", value: "카드없음"},
		{name: "account placeholder", key: "계좌번호", value: "미상"},
		{name: "telephone placeholder", key: "전화번호", value: "연락처없음"},
		{name: "driver license placeholder", key: "운전면허번호", value: "없음"},
	}

	for _, tt := range values {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			masked := MaskValue(tt.key, tt.value)
			if masked.Rule.RuleName == "empty" {
				t.Fatalf("expected a masking rule to match %q", tt.key)
			}
			if masked.MaskedValue == tt.value {
				t.Fatalf("rule %q reported %q unmasked", masked.Rule.RuleName, masked.MaskedValue)
			}
		})
	}
}

// An empty or whitespace-only value has nothing to hide, so it keeps the "empty"
// rule and is returned verbatim rather than going through the fallback.
func TestMaskValueLeavesBlankValuesAlone(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "   ", "\t\n"} {
		masked := MaskValue("주민등록번호", value)
		if masked.Rule.RuleName != "empty" {
			t.Fatalf("value %q matched rule %q, want empty", value, masked.Rule.RuleName)
		}
		if masked.MaskedValue != value {
			t.Fatalf("value %q was rewritten to %q", value, masked.MaskedValue)
		}
	}
}

func TestComputeMaskedRuneSpans(t *testing.T) {
	t.Parallel()

	spans := ComputeMaskedRuneSpans("홍길동", "홍*동")
	if len(spans) != 1 || spans[0][0] != 1 || spans[0][1] != 2 {
		t.Fatalf("unexpected spans: %#v", spans)
	}
}

func TestComputeMaskedRuneSpansIgnoresMisalignedValues(t *testing.T) {
	t.Parallel()

	if spans := ComputeMaskedRuneSpans(" 홍길동", "홍*동"); spans != nil {
		t.Fatalf("expected no spans for a misaligned pair, got %#v", spans)
	}
}

// Mask regions are derived by comparing the masked value rune by rune against the
// original value, so every rule must keep the rune count of its input.
func TestMaskValueKeepsRuneAlignment(t *testing.T) {
	t.Parallel()

	values := []struct {
		key   string
		value string
	}{
		{key: "개인정보.주민등록번호", value: " 800901-1234567"},
		{key: "개인정보.운전면허번호", value: "11-24-123456-62 "},
		{key: "개인정보.여권번호", value: "\t12345ABCD "},
		{key: "개인정보.외국인등록번호", value: " 123456-1234567 "},
		{key: "개인정보.휴대폰번호", value: " 010-1234-5678"},
		{key: "개인정보.전화번호", value: "02-123-4567 "},
		{key: "개인정보.신용카드번호", value: " 1234-5678-9012-3456 "},
		{key: "개인정보.계좌번호", value: " 123-456-789-123"},
		{key: "개인정보.계좌번호", value: " 1234567890123 "},
		{key: "개인정보.이름", value: " 홍 길 동 "},
		{key: "개인정보.이메일", value: " abcdefg@naver.com "},
		{key: "개인정보.IP주소", value: " 192.168.10.123 "},
		{key: "개인정보.주소", value: "  서울   영등포구   국제금융로   10  "},
		{key: "개인정보.주소", value: "서울특별시"},
		{key: "알수없는키", value: " 알 수 없는 값 "},
		// Values that no digit- or position-based helper can hide on its own, so
		// they reach the full-mask fallback and still have to stay rune aligned.
		{key: "이름", value: "이"},
		{key: "firstname", value: "J"},
		{key: "주민등록번호", value: " 900101 "},
		{key: "주민등록번호", value: "확인불가"},
		{key: "신용카드번호", value: "카드없음"},
		{key: "계좌번호", value: "미상"},
		{key: "전화번호", value: " 연락처 없음 "},
		{key: "운전면허번호", value: "없음"},
	}

	for _, tt := range values {
		tt := tt
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			t.Parallel()
			masked := MaskValue(tt.key, tt.value)
			originalRunes := []rune(tt.value)
			maskedRunes := []rune(masked.MaskedValue)
			if len(originalRunes) != len(maskedRunes) {
				t.Fatalf("rune count changed: original %q (%d) -> masked %q (%d)",
					tt.value, len(originalRunes), masked.MaskedValue, len(maskedRunes))
			}
			for index, character := range originalRunes {
				if isWhitespaceRune(character) && maskedRunes[index] != character {
					t.Fatalf("whitespace at rune %d was replaced: masked %q", index, masked.MaskedValue)
				}
			}
			if len(ComputeMaskedRuneSpans(tt.value, masked.MaskedValue)) == 0 {
				t.Fatalf("expected at least one masked span for %q -> %q", tt.value, masked.MaskedValue)
			}
		})
	}
}
