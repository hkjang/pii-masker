package masking

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

type AppliedRule struct {
	RuleName    string `json:"rule_name"`
	DisplayName string `json:"display_name"`
}

type MaskedValue struct {
	MaskedValue string
	Rule        AppliedRule
}

var (
	driverLicensePattern = regexp.MustCompile(`^(\d{2}[- ]?\d{2}[- ]?)(\d{6})([- ]?\d{2})$`)
	emailPattern         = regexp.MustCompile(`^([^@]+)@(.+)$`)
	ipPattern            = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.)(\d{1,3})(\.\d{1,3})$`)
)

func MaskValue(key, value string) MaskedValue {
	normalizedKey := normalizeKey(key)
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return MaskedValue{MaskedValue: value, Rule: AppliedRule{RuleName: "empty", DisplayName: "빈 값"}}
	}

	switch {
	case containsAny(normalizedKey, "주민등록번호", "rrn"):
		return wrapMasked(maskDigitsAfter(trimmedValue, 6), "resident_registration_number", "주민등록번호 뒤 7자리 마스킹")
	case containsAny(normalizedKey, "운전면허", "driverlicense"):
		return wrapMasked(maskDriverLicense(trimmedValue), "driver_license_number", "운전면허번호 세 번째 묶음 마스킹")
	case containsAny(normalizedKey, "여권", "passport"):
		return wrapMasked(maskTrailingRunes(trimmedValue, 4), "passport_number", "여권번호 뒤 4자리 마스킹")
	case containsAny(normalizedKey, "외국인등록", "alienregistration"):
		return wrapMasked(maskDigitsAfter(trimmedValue, 6), "alien_registration_number", "외국인등록번호 뒤 7자리 마스킹")
	case containsAny(normalizedKey, "휴대폰", "휴대전화", "mobile", "cellphone"):
		return wrapMasked(maskLastDigits(trimmedValue, 4), "mobile_phone_number", "휴대폰번호 뒤 4자리 마스킹")
	case containsAny(normalizedKey, "전화번호", "유선전화", "tel", "phone"):
		return wrapMasked(maskLastDigits(trimmedValue, 4), "telephone_number", "전화번호 뒤 4자리 마스킹")
	case containsAny(normalizedKey, "신용카드", "카드번호", "creditcard"):
		return wrapMasked(maskKeepTrailingDigits(trimmedValue, 4), "credit_card_number", "신용카드번호 앞 12자리 마스킹")
	case containsAny(normalizedKey, "계좌", "account"):
		return wrapMasked(maskAccountNumber(trimmedValue), "bank_account_number", "계좌번호 뒤 묶음 제외 마스킹")
	case containsAny(normalizedKey, "이름", "성명", "name"):
		return wrapMasked(maskEveryEvenRune(trimmedValue), "name", "이름 짝수 자리 마스킹")
	case containsAny(normalizedKey, "이메일", "메일", "email"):
		return wrapMasked(maskEmail(trimmedValue), "email_address", "이메일 ID 3자리 제외 마스킹")
	case containsAny(normalizedKey, "ip", "아이피"):
		return wrapMasked(maskIPAddress(trimmedValue), "ip_address", "IP 주소 C 클래스 마스킹")
	case containsAny(normalizedKey, "주소", "address"):
		return wrapMasked(maskAddress(trimmedValue), "address", "주소 하위 정보 마스킹")
	default:
		return wrapMasked(maskAllVisible(trimmedValue), "generic_full_mask", "알 수 없는 PII 전체 마스킹")
	}
}

func SupportedRuleNames() []string {
	return []string{
		"resident_registration_number",
		"driver_license_number",
		"passport_number",
		"alien_registration_number",
		"mobile_phone_number",
		"telephone_number",
		"credit_card_number",
		"bank_account_number",
		"name",
		"email_address",
		"ip_address",
		"address",
	}
}

func ComputeMaskedRuneSpans(original, masked string) [][2]int {
	originalRunes := []rune(original)
	maskedRunes := []rune(masked)
	maxLen := len(originalRunes)
	if len(maskedRunes) < maxLen {
		maxLen = len(maskedRunes)
	}

	spans := make([][2]int, 0, 4)
	start := -1
	for index := 0; index < maxLen; index++ {
		if originalRunes[index] != maskedRunes[index] && maskedRunes[index] == '*' {
			if start == -1 {
				start = index
			}
			continue
		}
		if start != -1 {
			spans = append(spans, [2]int{start, index})
			start = -1
		}
	}
	if start != -1 {
		spans = append(spans, [2]int{start, maxLen})
	}
	return spans
}

func wrapMasked(value, ruleName, displayName string) MaskedValue {
	return MaskedValue{
		MaskedValue: value,
		Rule: AppliedRule{
			RuleName:    ruleName,
			DisplayName: displayName,
		},
	}
}

func normalizeKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "", ".", "", "_", "", "-", "")
	return replacer.Replace(value)
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, normalizeKey(candidate)) {
			return true
		}
	}
	return false
}

func maskDigitsAfter(value string, keepDigits int) string {
	seenDigits := 0
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if character < '0' || character > '9' {
			builder.WriteRune(character)
			continue
		}
		if seenDigits < keepDigits {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('*')
		}
		seenDigits++
	}
	return builder.String()
}

func maskKeepTrailingDigits(value string, trailingDigits int) string {
	totalDigits := countDigits(value)
	if totalDigits <= trailingDigits {
		return maskAllDigits(value)
	}

	visibleFrom := totalDigits - trailingDigits
	seenDigits := 0
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if character < '0' || character > '9' {
			builder.WriteRune(character)
			continue
		}
		if seenDigits >= visibleFrom {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('*')
		}
		seenDigits++
	}
	return builder.String()
}

func maskLastDigits(value string, lastDigits int) string {
	totalDigits := countDigits(value)
	if totalDigits <= lastDigits {
		return maskAllDigits(value)
	}

	maskFrom := totalDigits - lastDigits
	seenDigits := 0
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if character < '0' || character > '9' {
			builder.WriteRune(character)
			continue
		}
		if seenDigits < maskFrom {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('*')
		}
		seenDigits++
	}
	return builder.String()
}

func maskDriverLicense(value string) string {
	if matches := driverLicensePattern.FindStringSubmatch(value); len(matches) == 4 {
		return matches[1] + strings.Repeat("*", utf8.RuneCountInString(matches[2])) + matches[3]
	}
	return maskDigitsRange(value, 4, 2)
}

func maskDigitsRange(value string, keepLeadingDigits, keepTrailingDigits int) string {
	totalDigits := countDigits(value)
	if totalDigits == 0 {
		return value
	}
	if keepLeadingDigits+keepTrailingDigits >= totalDigits {
		return maskAllDigits(value)
	}

	maskAfter := keepLeadingDigits
	maskBefore := totalDigits - keepTrailingDigits
	seenDigits := 0
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if character < '0' || character > '9' {
			builder.WriteRune(character)
			continue
		}
		if seenDigits < maskAfter || seenDigits >= maskBefore {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('*')
		}
		seenDigits++
	}
	return builder.String()
}

func maskTrailingRunes(value string, trailing int) string {
	runes := []rune(value)
	if len(runes) <= trailing {
		return strings.Repeat("*", len(runes))
	}
	for index := len(runes) - trailing; index < len(runes); index++ {
		if !isWhitespaceRune(runes[index]) {
			runes[index] = '*'
		}
	}
	return string(runes)
}

func maskAccountNumber(value string) string {
	lastSeparator := strings.LastIndexAny(value, "- ")
	if lastSeparator >= 0 && lastSeparator < len(value)-1 {
		prefix := value[:lastSeparator+1]
		suffix := value[lastSeparator+1:]
		return maskAllDigits(prefix) + suffix
	}
	return maskKeepTrailingDigits(value, 4)
}

func maskEveryEvenRune(value string) string {
	runes := []rune(value)
	count := 0
	for index, character := range runes {
		if isWhitespaceRune(character) {
			continue
		}
		count++
		if count%2 == 0 {
			runes[index] = '*'
		}
	}
	return string(runes)
}

func maskEmail(value string) string {
	matches := emailPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return maskAllVisible(value)
	}
	local := []rune(matches[1])
	for index := range local {
		if len(local) <= 4 || index >= 3 {
			local[index] = '*'
		}
	}
	return string(local) + "@" + matches[2]
}

func maskIPAddress(value string) string {
	matches := ipPattern.FindStringSubmatch(value)
	if len(matches) != 4 {
		return maskAllVisible(value)
	}
	return matches[1] + strings.Repeat("*", len(matches[2])) + matches[3]
}

func maskAddress(value string) string {
	tokens := strings.Fields(value)
	if len(tokens) == 0 {
		return value
	}
	cutoff := len(tokens)
	for index, token := range tokens {
		if hasAddressBoundarySuffix(token) {
			cutoff = index + 1
			break
		}
	}
	if cutoff == len(tokens) && len(tokens) > 3 {
		cutoff = 3
	}
	if cutoff >= len(tokens) {
		if len(tokens) == 1 {
			return maskAllVisible(value)
		}
		cutoff = len(tokens) - 1
	}
	for index := cutoff; index < len(tokens); index++ {
		tokens[index] = maskAllVisible(tokens[index])
	}
	return strings.Join(tokens, " ")
}

func hasAddressBoundarySuffix(token string) bool {
	for _, suffix := range []string{"로", "길", "동", "읍", "면", "리"} {
		if strings.HasSuffix(token, suffix) {
			return true
		}
	}
	return false
}

func maskAllDigits(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if character >= '0' && character <= '9' {
			builder.WriteByte('*')
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func maskAllVisible(value string) string {
	runes := []rune(value)
	for index, character := range runes {
		if !isWhitespaceRune(character) {
			runes[index] = '*'
		}
	}
	return string(runes)
}

func countDigits(value string) int {
	count := 0
	for _, character := range value {
		if character >= '0' && character <= '9' {
			count++
		}
	}
	return count
}

func isWhitespaceRune(value rune) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}
