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

func TestComputeMaskedRuneSpans(t *testing.T) {
	t.Parallel()

	spans := ComputeMaskedRuneSpans("홍길동", "홍*동")
	if len(spans) != 1 || spans[0][0] != 1 || spans[0][1] != 2 {
		t.Fatalf("unexpected spans: %#v", spans)
	}
}
