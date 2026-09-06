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
