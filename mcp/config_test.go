package mcp

import (
	"testing"
	"time"
)

func TestDefaultConfigTimeoutDefaultsTo120Seconds(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Timeout != 120*time.Second {
		t.Fatalf("Timeout = %v, want 120s", cfg.Timeout)
	}
	if cfg.HTTPClient.Timeout != 120*time.Second {
		t.Fatalf("HTTPClient.Timeout = %v, want 120s", cfg.HTTPClient.Timeout)
	}
}

// TestDefaultConfigTimeoutRespectsEnvOverride pins down a real production
// failure: the non-streaming Call() path blocks on io.ReadAll(resp.Body) for
// the model's entire generation time, and a large prompt occasionally
// exceeds the hardcoded 120s budget (observed both right after a restart
// and 9+ hours into stable operation). AI_TIMEOUT_SECONDS must actually
// reach both Config.Timeout and the HTTPClient that enforces it - a caller
// only bumping one of the two would silently keep the old ceiling.
func TestDefaultConfigTimeoutRespectsEnvOverride(t *testing.T) {
	t.Setenv("AI_TIMEOUT_SECONDS", "240")

	cfg := DefaultConfig()

	if cfg.Timeout != 240*time.Second {
		t.Fatalf("Timeout = %v, want 240s", cfg.Timeout)
	}
	if cfg.HTTPClient.Timeout != 240*time.Second {
		t.Fatalf("HTTPClient.Timeout = %v, want 240s (the field the http.Client actually enforces)", cfg.HTTPClient.Timeout)
	}
}

func TestDefaultConfigTimeoutIgnoresInvalidEnvOverride(t *testing.T) {
	t.Setenv("AI_TIMEOUT_SECONDS", "not-a-number")

	cfg := DefaultConfig()

	if cfg.Timeout != 120*time.Second {
		t.Fatalf("Timeout = %v, want fallback of 120s for an unparseable override", cfg.Timeout)
	}
}
