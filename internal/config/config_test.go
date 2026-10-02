package config

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestLoadServerTimeoutDefaults(t *testing.T) {
	t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Server.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Fatalf("unexpected read header timeout %s", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Server.IdleTimeout != DefaultIdleTimeout {
		t.Fatalf("unexpected idle timeout %s", cfg.Server.IdleTimeout)
	}
	if cfg.Server.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("unexpected shutdown timeout %s", cfg.Server.ShutdownTimeout)
	}
}

func TestLoadServerTimeoutOverrides(t *testing.T) {
	t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
	t.Setenv("PII_MASKER_READ_HEADER_TIMEOUT_SECONDS", "5")
	t.Setenv("PII_MASKER_IDLE_TIMEOUT_SECONDS", "7")
	t.Setenv("PII_MASKER_SHUTDOWN_TIMEOUT_SECONDS", "9")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("unexpected read header timeout %s", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Server.IdleTimeout != 7*time.Second {
		t.Fatalf("unexpected idle timeout %s", cfg.Server.IdleTimeout)
	}
	if cfg.Server.ShutdownTimeout != 9*time.Second {
		t.Fatalf("unexpected shutdown timeout %s", cfg.Server.ShutdownTimeout)
	}
}

// The embedded mock is mounted on the very server that serves the API, so its default
// upstream has to follow the address that server listens on.
func TestLoadPointsTheEmbeddedMockAtTheServerAddress(t *testing.T) {
	cases := []struct {
		name    string
		address string
		want    string
	}{
		{name: "explicit host and port", address: "127.0.0.1:9191", want: "http://127.0.0.1:9191/internal/mock/upstage/inference"},
		{name: "host omitted", address: ":9090", want: "http://localhost:9090/internal/mock/upstage/inference"},
		{name: "ipv4 wildcard", address: "0.0.0.0:7000", want: "http://localhost:7000/internal/mock/upstage/inference"},
		{name: "ipv6 wildcard", address: "[::]:7000", want: "http://localhost:7000/internal/mock/upstage/inference"},
		{name: "ipv6 loopback", address: "[::1]:7000", want: "http://[::1]:7000/internal/mock/upstage/inference"},
		{name: "address unset", address: "", want: "http://localhost:8080/internal/mock/upstage/inference"},
		{name: "address is not host:port", address: "unix-socket", want: "http://localhost:8080/internal/mock/upstage/inference"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
			t.Setenv("PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK", "true")
			t.Setenv("PII_MASKER_UPSTAGE_BASE_URL", "")
			t.Setenv("PII_MASKER_ADDR", testCase.address)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			if cfg.Upstage.BaseURL != testCase.want {
				t.Fatalf("unexpected mock base url %q, want %q", cfg.Upstage.BaseURL, testCase.want)
			}
		})
	}
}

// An explicit upstream wins whether or not the embedded mock is enabled.
func TestLoadKeepsAnExplicitUpstreamBaseURL(t *testing.T) {
	for _, mockEnabled := range []string{"true", "false"} {
		t.Run("mock="+mockEnabled, func(t *testing.T) {
			t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
			t.Setenv("PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK", mockEnabled)
			t.Setenv("PII_MASKER_UPSTAGE_BASE_URL", "https://api.upstage.ai/v1/document-digitization")
			t.Setenv("PII_MASKER_ADDR", "127.0.0.1:9191")

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			if cfg.Upstage.BaseURL != "https://api.upstage.ai/v1/document-digitization" {
				t.Fatalf("unexpected base url %q", cfg.Upstage.BaseURL)
			}
		})
	}
}

// Without the embedded mock the default upstream stays the external placeholder.
func TestLoadKeepsTheExternalUpstreamDefaultWithoutTheMock(t *testing.T) {
	t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
	t.Setenv("PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK", "false")
	t.Setenv("PII_MASKER_UPSTAGE_BASE_URL", "")
	t.Setenv("PII_MASKER_ADDR", "127.0.0.1:9191")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Upstage.BaseURL != "http://localhost:8080/inference" {
		t.Fatalf("unexpected base url %q", cfg.Upstage.BaseURL)
	}
}

// A non-positive value would disable the guard, so it falls back to the default.
func TestLoadServerTimeoutRejectsNonPositiveValues(t *testing.T) {
	t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
	t.Setenv("PII_MASKER_READ_HEADER_TIMEOUT_SECONDS", "0")
	t.Setenv("PII_MASKER_IDLE_TIMEOUT_SECONDS", "-1")
	t.Setenv("PII_MASKER_SHUTDOWN_TIMEOUT_SECONDS", "abc")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Server.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Fatalf("unexpected read header timeout %s", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Server.IdleTimeout != DefaultIdleTimeout {
		t.Fatalf("unexpected idle timeout %s", cfg.Server.IdleTimeout)
	}
	if cfg.Server.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("unexpected shutdown timeout %s", cfg.Server.ShutdownTimeout)
	}
}

// service.countPages reads MaxPages as "0 means no page limit", so an operator has
// to be able to set that. The other limits in this group have no meaning at 0 --
// a 0 byte upload cap or 0 concurrent jobs would take the service offline -- so
// they keep falling back to their defaults.
func TestLoadMaxPagesAcceptsZeroAsUnlimited(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int
	}{
		{name: "unset keeps the default", value: "", want: defaultMaxPages},
		{name: "zero turns the page limit off", value: "0", want: 0},
		{name: "positive value is used", value: "5", want: 5},
		{name: "negative value falls back", value: "-1", want: defaultMaxPages},
		{name: "unparsable value falls back", value: "abc", want: defaultMaxPages},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
			if testCase.value != "" {
				t.Setenv("PII_MASKER_MAX_PAGES", testCase.value)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			if cfg.Limits.MaxPages != testCase.want {
				t.Fatalf("unexpected max pages %d, want %d", cfg.Limits.MaxPages, testCase.want)
			}
		})
	}
}

// Only MaxPages gained the explicit 0; these share the same parser and must not
// have picked it up, because none of them can do any work at 0.
func TestLoadRejectsZeroForLimitsThatWouldDisableTheService(t *testing.T) {
	t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
	t.Setenv("PII_MASKER_MAX_FILE_SIZE_MB", "0")
	t.Setenv("PII_MASKER_MAX_CONCURRENT_JOBS", "0")
	t.Setenv("PII_MASKER_MAX_CONCURRENT_SYNC", "0")
	t.Setenv("PII_MASKER_DEFAULT_TIMEOUT_SECONDS", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Limits.MaxFileSizeBytes != int64(defaultMaxFileSizeMB)*1024*1024 {
		t.Fatalf("unexpected max file size %d", cfg.Limits.MaxFileSizeBytes)
	}
	if cfg.Limits.MaxConcurrentJobs != defaultMaxConcurrentJobs {
		t.Fatalf("unexpected max concurrent jobs %d", cfg.Limits.MaxConcurrentJobs)
	}
	if cfg.Limits.MaxConcurrentSync != defaultMaxConcurrentSync {
		t.Fatalf("unexpected max concurrent sync %d", cfg.Limits.MaxConcurrentSync)
	}
	if cfg.Upstage.Timeout != time.Duration(defaultTimeoutSeconds)*time.Second {
		t.Fatalf("unexpected upstream timeout %s", cfg.Upstage.Timeout)
	}
}

// The MiB multiplication has the same overflow shape as the duration settings:
// an out of range value must come back as the default instead of a wrapped size,
// because every upload path compares against MaxFileSizeBytes.
func TestLoadMaxFileSizeBounds(t *testing.T) {
	const (
		key         = "PII_MASKER_MAX_FILE_SIZE_MB"
		mebibyte    = int64(1024 * 1024)
		maximumMB   = int64(9223372036854775807) / mebibyte // 8796093022207
		defaultSize = int64(defaultMaxFileSizeMB) * mebibyte
	)
	cases := []struct {
		name, value string
		want        int64
	}{
		{"unset", "", defaultSize},
		{"empty", "", defaultSize},
		{"whitespace", " \t ", defaultSize},
		{"one mebibyte", "1", mebibyte},
		{"trimmed", " \t1 ", mebibyte},
		{"zero", "0", defaultSize},
		{"negative", "-1", defaultSize},
		{"invalid", "abc", defaultSize},
		{"maximum", strconv.FormatInt(maximumMB, 10), maximumMB * mebibyte},
		{"above maximum wraps negative", strconv.FormatInt(maximumMB+1, 10), defaultSize},
		{"positive wraparound", "17592186044417", defaultSize},
		{"int64 maximum", "9223372036854775807", defaultSize},
		{"beyond int64", "9223372036854775808", defaultSize},
	}

	t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
	t.Setenv(key, "")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.value)
			if tc.name == "unset" {
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.Limits.MaxFileSizeBytes <= 0 {
				t.Fatalf("%s=%q: max file size %d is not a usable limit", key, tc.value, cfg.Limits.MaxFileSizeBytes)
			}
			if cfg.Limits.MaxFileSizeBytes != tc.want {
				t.Fatalf("%s=%q: got %d, want %d", key, tc.value, cfg.Limits.MaxFileSizeBytes, tc.want)
			}
		})
	}
}

func TestLoadDurationBounds(t *testing.T) {
	settings := []struct {
		key            string
		unit, fallback time.Duration
		allowZero      bool
		get            func(Config) time.Duration
	}{
		{"READ_HEADER_TIMEOUT_SECONDS", time.Second, 15 * time.Second, false, func(c Config) time.Duration { return c.Server.ReadHeaderTimeout }},
		{"IDLE_TIMEOUT_SECONDS", time.Second, 60 * time.Second, false, func(c Config) time.Duration { return c.Server.IdleTimeout }},
		{"SHUTDOWN_TIMEOUT_SECONDS", time.Second, 45 * time.Second, false, func(c Config) time.Duration { return c.Server.ShutdownTimeout }},
		{"DEFAULT_TIMEOUT_SECONDS", time.Second, 30 * time.Second, false, func(c Config) time.Duration { return c.Upstage.Timeout }},
		{"SYNC_QUEUE_WAIT_SECONDS", time.Second, 10 * time.Second, true, func(c Config) time.Duration { return c.Limits.SyncQueueWait }},
		{"JOB_RETENTION_HOURS", time.Hour, 24 * time.Hour, true, func(c Config) time.Duration { return c.Storage.JobRetention }},
	}
	t.Setenv("PII_MASKER_STORAGE_DIR", t.TempDir())
	t.Setenv("PII_MASKER_UPSTAGE_BASE_URL", "")
	t.Setenv("PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK", "")
	for _, setting := range settings {
		t.Setenv("PII_MASKER_"+setting.key, "")
	}
	for _, setting := range settings {
		t.Run(setting.key, func(t *testing.T) {
			maximum := int64(9223372036)
			wrapsPositive := "18446744074"
			if setting.unit == time.Hour {
				maximum = 2562047
				wrapsPositive = "5124096"
			}
			zero := setting.fallback
			if setting.allowZero {
				zero = 0
			}
			cases := []struct {
				name, value string
				want        time.Duration
			}{
				{"unset", "", setting.fallback},
				{"empty", "", setting.fallback},
				{"whitespace", " \t ", setting.fallback},
				{"negative", "-1", setting.fallback},
				{"invalid", "abc", setting.fallback},
				{"positive", "7", 7 * setting.unit},
				{"trimmed", " \t7 ", 7 * setting.unit},
				{"zero", "0", zero},
				{"maximum", strconv.FormatInt(maximum, 10), time.Duration(maximum) * setting.unit},
				{"above maximum", strconv.FormatInt(maximum+1, 10), setting.fallback},
				{"positive wraparound", wrapsPositive, setting.fallback},
				{"int64 maximum", "9223372036854775807", setting.fallback},
				{"beyond int64", "9223372036854775808", setting.fallback},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					key := "PII_MASKER_" + setting.key
					t.Setenv(key, tc.value)
					if tc.name == "unset" {
						if err := os.Unsetenv(key); err != nil {
							t.Fatal(err)
						}
					}
					cfg, err := Load()
					if err != nil {
						t.Fatalf("load: %v", err)
					}
					if got := setting.get(cfg); got != tc.want {
						t.Fatalf("%s=%q: got %s, want %s", key, tc.value, got, tc.want)
					}
				})
			}
		})
	}
}
