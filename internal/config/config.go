package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress        = ":8080"
	defaultTimeoutSeconds = 30
	defaultMaxFileSizeMB  = 50
	defaultMaxPages       = 20
	defaultModel          = "pii"
	defaultLang           = "ko"
	defaultSchema         = "oac"
)

type Config struct {
	Server  ServerConfig
	Upstage UpstageConfig
	Limits  LimitsConfig
	Storage StorageConfig
	Debug   DebugConfig
	Mock    MockConfig
}

type ServerConfig struct {
	Address       string
	PublicBaseURL string
}

type UpstageConfig struct {
	BaseURL    string
	AuthMode   string
	AuthToken  string
	AllowHosts []string
	Timeout    time.Duration
	Model      string
	Lang       string
	Schema     string
	Verbose    bool
}

type LimitsConfig struct {
	MaxFileSizeBytes int64
	MaxPages         int
	SupportedMIMEs   []string
}

type StorageConfig struct {
	RootDir string
}

type DebugConfig struct {
	EnableDebug bool
}

type MockConfig struct {
	EnableEmbeddedUpstageMock bool
}

func Load() (Config, error) {
	rootDir := envOrDefault("PII_MASKER_STORAGE_DIR", filepath.Join(".", "data"))
	baseURL := strings.TrimSpace(os.Getenv("PII_MASKER_UPSTAGE_BASE_URL"))
	mockEnabled := envBool("PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK", false)
	if baseURL == "" && mockEnabled {
		baseURL = "http://localhost:8080/internal/mock/upstage/inference"
	}
	if baseURL == "" {
		baseURL = "http://localhost:8080/inference"
	}

	normalizedBaseURL, parsedBaseURL, err := normalizeEndpointURL(baseURL)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Server: ServerConfig{
			Address:       envOrDefault("PII_MASKER_ADDR", defaultAddress),
			PublicBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("PII_MASKER_PUBLIC_BASE_URL")), "/"),
		},
		Upstage: UpstageConfig{
			BaseURL:    normalizedBaseURL,
			AuthMode:   normalizeAuthMode(envOrDefault("PII_MASKER_UPSTAGE_AUTH_MODE", "bearer")),
			AuthToken:  strings.TrimSpace(os.Getenv("PII_MASKER_UPSTAGE_AUTH_TOKEN")),
			AllowHosts: normalizeAllowHosts(os.Getenv("PII_MASKER_ALLOW_HOSTS"), parsedBaseURL),
			Timeout:    time.Duration(envInt("PII_MASKER_DEFAULT_TIMEOUT_SECONDS", defaultTimeoutSeconds)) * time.Second,
			Model:      envOrDefault("PII_MASKER_DEFAULT_MODEL", defaultModel),
			Lang:       normalizePIILang(envOrDefault("PII_MASKER_DEFAULT_LANG", defaultLang)),
			Schema:     normalizePIISchema(envOrDefault("PII_MASKER_DEFAULT_SCHEMA", defaultSchema)),
			Verbose:    envBool("PII_MASKER_DEFAULT_VERBOSE", false),
		},
		Limits: LimitsConfig{
			MaxFileSizeBytes: int64(envInt("PII_MASKER_MAX_FILE_SIZE_MB", defaultMaxFileSizeMB)) * 1024 * 1024,
			MaxPages:         envInt("PII_MASKER_MAX_PAGES", defaultMaxPages),
			SupportedMIMEs:   []string{"application/pdf", "image/png", "image/jpeg"},
		},
		Storage: StorageConfig{
			RootDir: rootDir,
		},
		Debug: DebugConfig{
			EnableDebug: envBool("PII_MASKER_ENABLE_DEBUG", false),
		},
		Mock: MockConfig{
			EnableEmbeddedUpstageMock: mockEnabled,
		},
	}

	if err := os.MkdirAll(cfg.Storage.RootDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("failed to create storage dir: %w", err)
	}
	return cfg, nil
}

func normalizeEndpointURL(raw string) (string, *url.URL, error) {
	raw = strings.TrimSpace(raw)
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return "", nil, fmt.Errorf("invalid upstream URL: %w", err)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", nil, fmt.Errorf("upstream URL must include scheme and host")
	}
	parsedURL.Path = strings.TrimRight(parsedURL.Path, "/")
	if parsedURL.Path == "" {
		parsedURL.Path = "/inference"
	}
	return parsedURL.String(), parsedURL, nil
}

func normalizeAllowHosts(raw string, parsedBaseURL *url.URL) []string {
	parts := strings.Split(raw, ",")
	hosts := make([]string, 0, len(parts)+1)
	seen := map[string]struct{}{}

	appendHost := func(value string) {
		host := strings.ToLower(strings.TrimSpace(value))
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}

	for _, part := range parts {
		appendHost(part)
	}
	if len(hosts) == 0 && parsedBaseURL != nil {
		appendHost(parsedBaseURL.Hostname())
	}
	return hosts
}

func normalizeAuthMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "bearer":
		return "bearer"
	case "x-api-key":
		return "x-api-key"
	default:
		return "bearer"
	}
}

func normalizePIILang(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ko", "en", "ja", "zh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "ko"
	}
}

func normalizePIISchema(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "oac":
		return "oac"
	case "ufp":
		return "ufp"
	default:
		return "oac"
	}
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}
