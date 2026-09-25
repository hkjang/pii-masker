package config

import (
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
