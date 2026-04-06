package upstage

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pii-masker/internal/config"
	"pii-masker/internal/document"
)

type Client struct {
	config config.UpstageConfig
}

type ParseOptions struct {
	Model   string
	Lang    string
	Schema  string
	Verbose bool
}

type ParseResponse struct {
	Result         json.RawMessage `json:"result"`
	Type           string          `json:"type"`
	NumBilledPages int             `json:"numBilledPages"`
	API            string          `json:"api"`
	Content        struct {
		HTML     string `json:"html"`
		Markdown string `json:"markdown"`
		Text     string `json:"text"`
	} `json:"content"`
	Model string `json:"model"`
	Usage struct {
		Pages int `json:"pages"`
	} `json:"usage"`
}

type DocumentResult struct {
	Attachment    document.Attachment
	Response      ParseResponse
	ResponseDebug ResponseDebug
	RequestDebug  RequestDebug
}

type RequestDebug struct {
	URL        string            `json:"url"`
	AuthMode   string            `json:"auth_mode"`
	Fields     map[string]string `json:"fields"`
	Attachment struct {
		Name      string `json:"name"`
		MIMEType  string `json:"mime_type"`
		Extension string `json:"extension"`
		Size      int64  `json:"size"`
	} `json:"attachment"`
}

type ResponseDebug struct {
	StatusCode int    `json:"status_code,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Body       string `json:"body,omitempty"`
}

type ConnectionStatus struct {
	OK         bool   `json:"ok"`
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
	ErrorCode  string `json:"error_code,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Retryable  bool   `json:"retryable"`
}

type CallError struct {
	Code        string
	Summary     string
	Detail      string
	Hint        string
	RequestURL  string
	StatusCode  int
	Retryable   bool
	InputDebug  string
	OutputDebug string
}

func NewClient(cfg config.UpstageConfig) *Client {
	return &Client{config: cfg}
}

func (e *CallError) Error() string {
	if e == nil {
		return ""
	}
	lines := []string{}
	if e.Summary != "" {
		lines = append(lines, e.Summary)
	}
	if e.Detail != "" {
		lines = append(lines, "상세: "+e.Detail)
	}
	if e.Hint != "" {
		lines = append(lines, "조치: "+e.Hint)
	}
	if e.StatusCode > 0 {
		lines = append(lines, fmt.Sprintf("HTTP 상태: %d", e.StatusCode))
	}
	return strings.Join(lines, "\n")
}

func (e *CallError) toConnectionStatus() ConnectionStatus {
	if e == nil {
		return ConnectionStatus{}
	}
	return ConnectionStatus{
		OK:         false,
		URL:        e.RequestURL,
		StatusCode: e.StatusCode,
		Message:    e.Summary,
		ErrorCode:  e.Code,
		Detail:     e.Detail,
		Hint:       e.Hint,
		Retryable:  e.Retryable,
	}
}

func (c *Client) ParseDocument(ctx context.Context, attachment document.Attachment, options ParseOptions) (DocumentResult, int, time.Duration, error) {
	fields := buildFormFields(c.config, options)
	upstreamAttachment, err := prepareUpstreamAttachment(attachment)
	requestDebug := buildRequestDebug(c.config, fields, upstreamAttachment)

	startedAt := time.Now()
	if err != nil {
		elapsed := time.Since(startedAt)
		callErr := newCallError(
			"upstream_prepare_failed",
			"JPG/JPEG 파일을 PII 추론용 형식으로 변환하지 못했습니다.",
			err.Error(),
			"손상되지 않은 JPG/JPEG 파일인지 확인한 뒤 다시 시도하세요.",
			c.config.BaseURL,
			0,
			false,
		)
		return DocumentResult{
			Attachment:   attachment,
			RequestDebug: requestDebug,
		}, 0, elapsed, callErr.withDebug(requestDebug, ResponseDebug{})
	}

	result, statusCode, err := c.performParseRequest(ctx, attachment, upstreamAttachment, fields, requestDebug)
	elapsed := time.Since(startedAt)
	return result, statusCode, elapsed, err
}

func (c *Client) TestConnection(ctx context.Context) (ConnectionStatus, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", c.config.Model); err != nil {
		return ConnectionStatus{}, err
	}
	if err := writer.Close(); err != nil {
		return ConnectionStatus{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, buildRequestURL(c.config.BaseURL, c.config.Model), &body)
	if err != nil {
		return ConnectionStatus{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	applyAuthHeader(request, c.config)

	response, err := c.httpClient().Do(request)
	if err != nil {
		return classifyRequestError(c.config.BaseURL, err).toConnectionStatus(), nil
	}
	defer response.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(response.Body, 32*1024))
	switch response.StatusCode {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return ConnectionStatus{
			OK:         true,
			URL:        c.config.BaseURL,
			StatusCode: response.StatusCode,
			Message:    "엔드포인트 연결과 인증은 확인되었습니다. 테스트 요청은 문서 파일이 없어 예상대로 거부되었습니다.",
		}, nil
	}
	if response.StatusCode >= http.StatusBadRequest {
		return classifyHTTPError(c.config.BaseURL, response.StatusCode, response.Header, bodyBytes).toConnectionStatus(), nil
	}

	return ConnectionStatus{
		OK:         true,
		URL:        c.config.BaseURL,
		StatusCode: response.StatusCode,
		Message:    defaultIfEmpty(strings.TrimSpace(string(bodyBytes)), "연결에 성공했습니다."),
	}, nil
}

func (c *Client) performParseRequest(ctx context.Context, originalAttachment document.Attachment, upstreamAttachment document.Attachment, fields map[string]string, requestDebug RequestDebug) (DocumentResult, int, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return DocumentResult{}, 0, fmt.Errorf("failed to write %s field: %w", key, err)
		}
	}

	part, err := createDocumentPart(writer, upstreamAttachment)
	if err != nil {
		return DocumentResult{}, 0, err
	}
	if _, err := io.Copy(part, bytes.NewReader(upstreamAttachment.Content)); err != nil {
		return DocumentResult{}, 0, err
	}
	if err := writer.Close(); err != nil {
		return DocumentResult{}, 0, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, buildRequestURL(c.config.BaseURL, fields["model"]), &body)
	if err != nil {
		return DocumentResult{}, 0, fmt.Errorf("failed to build upstream request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	applyAuthHeader(request, c.config)

	response, err := c.httpClient().Do(request)
	if err != nil {
		return DocumentResult{}, 0, attachDebug(classifyRequestError(c.config.BaseURL, err), requestDebug, ResponseDebug{})
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		callErr := newCallError("response_read_failed", "Upstage 응답 본문을 읽는 중 오류가 발생했습니다.", err.Error(), "Upstage 응답 크기 제한과 네트워크 상태를 확인하세요.", c.config.BaseURL, response.StatusCode, true)
		return DocumentResult{}, response.StatusCode, callErr.withDebug(requestDebug, buildResponseDebug(response.StatusCode, response.Header, nil, callErr))
	}

	if response.StatusCode >= http.StatusBadRequest {
		callErr := classifyHTTPError(c.config.BaseURL, response.StatusCode, response.Header, responseBody)
		return DocumentResult{}, response.StatusCode, attachDebug(callErr, requestDebug, buildResponseDebug(response.StatusCode, response.Header, responseBody, callErr))
	}

	var parsed ParseResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		callErr := newCallError("decode_failed", "Upstage 응답 JSON을 해석하지 못했습니다.", err.Error(), "엔드포인트 URL과 응답 형식을 확인하세요.", c.config.BaseURL, response.StatusCode, false)
		return DocumentResult{}, response.StatusCode, callErr.withDebug(requestDebug, buildResponseDebug(response.StatusCode, response.Header, responseBody, callErr))
	}
	if strings.TrimSpace(parsed.Model) == "" {
		parsed.Model = fields["model"]
	}

	return DocumentResult{
		Attachment:    originalAttachment,
		Response:      parsed,
		ResponseDebug: buildResponseDebug(response.StatusCode, response.Header, responseBody, nil),
		RequestDebug:  requestDebug,
	}, response.StatusCode, nil
}

func prepareUpstreamAttachment(attachment document.Attachment) (document.Attachment, error) {
	if !strings.EqualFold(strings.TrimSpace(attachment.MIMEType), "image/jpeg") {
		return attachment, nil
	}

	imageValue, _, err := image.Decode(bytes.NewReader(attachment.Content))
	if err != nil {
		return document.Attachment{}, fmt.Errorf("failed to decode jpeg: %w", err)
	}

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageValue); err != nil {
		return document.Attachment{}, fmt.Errorf("failed to encode png: %w", err)
	}

	converted := attachment
	converted.Content = buffer.Bytes()
	converted.Size = int64(buffer.Len())
	converted.MIMEType = "image/png"
	converted.Extension = "png"
	converted.Name = replaceExtension(attachment.Name, ".png")
	return converted, nil
}

func replaceExtension(name, ext string) string {
	baseExt := filepath.Ext(name)
	if baseExt == "" {
		return name + ext
	}
	return strings.TrimSuffix(name, baseExt) + ext
}

func buildFormFields(cfg config.UpstageConfig, options ParseOptions) map[string]string {
	fields := map[string]string{
		"model": defaultIfEmpty(strings.TrimSpace(options.Model), cfg.Model),
	}
	if lang := defaultIfEmpty(strings.TrimSpace(options.Lang), cfg.Lang); lang != "" {
		fields["lang"] = lang
	}
	if schema := defaultIfEmpty(strings.TrimSpace(options.Schema), cfg.Schema); schema != "" && schema != "oac" {
		fields["schema"] = schema
	}
	if options.Verbose || cfg.Verbose {
		fields["verbose"] = strconv.FormatBool(true)
	}
	return fields
}

func buildRequestURL(baseURL, model string) string {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	query := parsedURL.Query()
	if strings.TrimSpace(model) != "" {
		query.Set("model", model)
	}
	parsedURL.RawQuery = query.Encode()
	return parsedURL.String()
}

func buildRequestDebug(cfg config.UpstageConfig, fields map[string]string, attachment document.Attachment) RequestDebug {
	debug := RequestDebug{
		URL:      buildRequestURL(cfg.BaseURL, fields["model"]),
		AuthMode: cfg.AuthMode,
		Fields:   cloneFields(fields),
	}
	debug.Attachment.Name = attachment.Name
	debug.Attachment.MIMEType = attachment.MIMEType
	debug.Attachment.Extension = attachment.Extension
	debug.Attachment.Size = attachment.Size
	return debug
}

func createDocumentPart(writer *multipart.Writer, attachment document.Attachment) (io.Writer, error) {
	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name="document"; filename="%s"`, escapeMultipartValue(attachment.Name)))
	headers.Set("Content-Type", defaultIfEmpty(strings.TrimSpace(attachment.MIMEType), "application/octet-stream"))
	return writer.CreatePart(headers)
}

func escapeMultipartValue(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", `"`, "\\\"")
	return replacer.Replace(value)
}

func applyAuthHeader(request *http.Request, cfg config.UpstageConfig) {
	if cfg.AuthToken == "" {
		return
	}
	if cfg.AuthMode == "x-api-key" {
		request.Header.Set("x-api-key", cfg.AuthToken)
		return
	}
	request.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
}

func (c *Client) httpClient() *http.Client {
	timeout := c.config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func cloneFields(fields map[string]string) map[string]string {
	copied := make(map[string]string, len(fields))
	for key, value := range fields {
		copied[key] = value
	}
	return copied
}

func buildResponseDebug(statusCode int, headers http.Header, body []byte, callErr *CallError) ResponseDebug {
	responseDebug := ResponseDebug{
		StatusCode: statusCode,
		RequestID:  firstHeaderValue(headers, "X-Request-Id", "X-Request-ID", "X-Correlation-ID"),
		Body:       formatDebugBody(body),
	}
	if callErr != nil {
		responseDebug.ErrorCode = callErr.Code
		responseDebug.Summary = callErr.Summary
		responseDebug.Detail = callErr.Detail
		responseDebug.Hint = callErr.Hint
	}
	return responseDebug
}

func formatDebugBody(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	var payload any
	if err := json.Unmarshal(trimmed, &payload); err == nil {
		if pretty, err := json.MarshalIndent(payload, "", "  "); err == nil {
			return truncateString(string(pretty), 16*1024)
		}
	}
	return truncateString(string(trimmed), 16*1024)
}

func attachDebug(err error, requestDebug RequestDebug, responseDebug ResponseDebug) error {
	var callErr *CallError
	if !errors.As(err, &callErr) {
		return err
	}
	return callErr.withDebug(requestDebug, responseDebug)
}

func (e *CallError) withDebug(requestDebug RequestDebug, responseDebug ResponseDebug) *CallError {
	if e == nil {
		return nil
	}
	copyErr := *e
	copyErr.InputDebug = marshalDebugPayload(requestDebug)
	copyErr.OutputDebug = marshalDebugPayload(responseDebug)
	return &copyErr
}

func marshalDebugPayload(payload any) string {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}

func newCallError(code, summary, detail, hint, requestURL string, statusCode int, retryable bool) *CallError {
	return &CallError{
		Code:       strings.TrimSpace(code),
		Summary:    strings.TrimSpace(summary),
		Detail:     strings.TrimSpace(detail),
		Hint:       strings.TrimSpace(hint),
		RequestURL: strings.TrimSpace(requestURL),
		StatusCode: statusCode,
		Retryable:  retryable,
	}
}

func classifyHTTPError(requestURL string, statusCode int, headers http.Header, body []byte) *CallError {
	bodySummary := summarizeResponseBody(body)
	requestID := firstHeaderValue(headers, "X-Request-Id", "X-Request-ID", "X-Correlation-ID")
	if requestID != "" {
		bodySummary = strings.TrimSpace(bodySummary + " (Upstage request id: " + requestID + ")")
	}

	switch statusCode {
	case http.StatusBadRequest:
		return newCallError("bad_request", "PII 추론 요청이 거부되었습니다.", defaultIfEmpty(bodySummary, "입력 문서 또는 파라미터 형식이 PII API 요구사항과 맞지 않습니다."), "model, lang, schema, verbose 값과 업로드 문서 형식을 확인하세요.", requestURL, statusCode, false)
	case http.StatusUnauthorized, http.StatusForbidden:
		return newCallError("auth_failed", "PII API 인증에 실패했습니다.", defaultIfEmpty(bodySummary, "API 키가 유효하지 않거나 권한이 없습니다."), "인증 토큰과 헤더 방식을 확인하세요.", requestURL, statusCode, false)
	case http.StatusNotFound:
		return newCallError("not_found", "PII API 엔드포인트를 찾지 못했습니다.", defaultIfEmpty(bodySummary, "/inference 경로가 올바르지 않습니다."), "기본 URL이 /inference 엔드포인트인지 확인하세요.", requestURL, statusCode, false)
	case http.StatusTooManyRequests:
		return newCallError("rate_limited", "PII API 호출 한도에 걸렸습니다.", defaultIfEmpty(bodySummary, "잠시 후 다시 시도해야 합니다."), "요청 빈도를 줄이거나 잠시 후 다시 시도하세요.", requestURL, statusCode, true)
	case http.StatusRequestEntityTooLarge:
		return newCallError("file_too_large", "업로드한 문서 파일이 너무 큽니다.", defaultIfEmpty(bodySummary, "PII API가 파일 크기 제한을 초과한 요청을 거부했습니다."), "50MB 이하 파일인지 확인하고, 필요하면 파일을 분할하세요.", requestURL, statusCode, false)
	case http.StatusUnsupportedMediaType:
		return newCallError("unsupported_media_type", "PII API가 이 문서 형식을 지원하지 않습니다.", defaultIfEmpty(bodySummary, "지원되지 않는 파일 형식입니다."), "PDF 또는 PNG 파일인지 확인하세요.", requestURL, statusCode, false)
	default:
		if statusCode >= http.StatusInternalServerError {
			return newCallError("server_error", "PII API 서버 내부 오류가 발생했습니다.", defaultIfEmpty(bodySummary, "PII API 서버가 5xx 오류를 반환했습니다."), "잠시 후 다시 시도하고, 반복되면 Upstage 서버 상태와 로그를 확인하세요.", requestURL, statusCode, true)
		}
		return newCallError("unexpected_status", fmt.Sprintf("PII API가 예상하지 못한 HTTP 상태 %d 를 반환했습니다.", statusCode), bodySummary, "응답 본문과 PII API 설정을 함께 확인하세요.", requestURL, statusCode, statusCode >= 500)
	}
}

func classifyRequestError(requestURL string, err error) *CallError {
	detail := strings.TrimSpace(err.Error())
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return newCallError("network_timeout", "PII API 서버 연결이 시간 초과되었습니다.", detail, "PII API 서버 상태와 네트워크 지연, 타임아웃 설정을 확인하세요.", requestURL, 0, true)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newCallError("network_timeout", "PII API 서버 연결이 시간 초과되었습니다.", detail, "PII API 서버 상태와 타임아웃 값을 확인하세요.", requestURL, 0, true)
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return newCallError("dns_error", "PII API 호스트 이름을 찾지 못했습니다.", detail, "기본 URL의 도메인 이름과 DNS 설정을 확인하세요.", requestURL, 0, false)
	}

	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return newCallError("tls_hostname_error", "TLS 인증서의 호스트 이름이 PII API URL과 일치하지 않습니다.", detail, "인증서의 SAN/CN과 기본 URL 호스트가 일치하는지 확인하세요.", requestURL, 0, false)
	}

	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return newCallError("tls_untrusted_ca", "PII API TLS 인증서를 신뢰할 수 없습니다.", detail, "서버 인증서 체인과 신뢰 저장소를 확인하세요.", requestURL, 0, false)
	}

	return newCallError("network_error", "PII API 서버에 연결하지 못했습니다.", detail, "서버 주소, 방화벽, 네트워크 연결 상태를 확인하세요.", requestURL, 0, true)
}

func summarizeResponseBody(body []byte) string {
	text := extractTextFromBody(body)
	if text != "" {
		return truncateString(text, 280)
	}
	return truncateString(strings.TrimSpace(string(body)), 280)
}

func extractTextFromBody(body []byte) string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return strings.TrimSpace(string(body))
	}
	text := extractTextFromValue(payload)
	if text != "" {
		return text
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(pretty)
}

func extractTextFromValue(value any) string {
	candidates := make([]string, 0, 8)
	collectTextCandidates(value, &candidates)
	best := ""
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if len(candidate) > len(best) {
			best = candidate
		}
	}
	return best
}

func collectTextCandidates(value any, candidates *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			lowerKey := strings.ToLower(key)
			if isLikelyTextKey(lowerKey) {
				switch nestedValue := nested.(type) {
				case string:
					*candidates = append(*candidates, nestedValue)
				case map[string]any, []any:
					collectTextCandidates(nestedValue, candidates)
				}
				continue
			}
			collectTextCandidates(nested, candidates)
		}
	case []any:
		for _, item := range typed {
			collectTextCandidates(item, candidates)
		}
	case string:
		if strings.TrimSpace(typed) != "" {
			*candidates = append(*candidates, typed)
		}
	}
}

func isLikelyTextKey(key string) bool {
	return strings.Contains(key, "text") ||
		strings.Contains(key, "message") ||
		strings.Contains(key, "output") ||
		strings.Contains(key, "result") ||
		strings.Contains(key, "content") ||
		strings.Contains(key, "response") ||
		strings.Contains(key, "detail") ||
		strings.Contains(key, "error")
}

func truncateString(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if maxLength <= 0 || len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return value[:maxLength-3] + "..."
}

func defaultIfEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstHeaderValue(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
