package mcp

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"nofx/logger"
	"nofx/security"
)

// Config client configuration (centralized management of all configurations)
type Config struct {
	// Provider configuration
	Provider string
	APIKey   string
	BaseURL  string
	Model    string

	// Behavior configuration
	MaxTokens   int
	MaxContext  int // Model's max context window in tokens (0 = no limit)
	Temperature float64
	UseFullURL  bool

	// Retry configuration
	MaxRetries      int
	RetryWaitBase   time.Duration
	RetryableErrors []string

	// Timeout configuration
	Timeout time.Duration

	// Dependency injection
	Logger     Logger
	HTTPClient *http.Client
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	// AI_TIMEOUT_SECONDS overrides the 120s default (mirrors AI_MAX_TOKENS
	// below) - the non-streaming Call() path blocks on io.ReadAll(resp.Body)
	// for the model's ENTIRE generation time (reasoning + answer), not just
	// connection setup. Observed failing at exactly ~120s in production on a
	// large (~100K+ char) prompt, both right after a restart and 9+ hours
	// into stable operation - a response-time-budget issue, not a cold-start
	// one, so it needs real headroom rather than a code fix.
	timeout := time.Duration(getEnvInt("AI_TIMEOUT_SECONDS", int(DefaultTimeout/time.Second))) * time.Second

	return &Config{
		// Default values
		MaxTokens:       getEnvInt("AI_MAX_TOKENS", 2000),
		Temperature:     MCPClientTemperature,
		MaxRetries:      MaxRetryTimes,
		RetryWaitBase:   2 * time.Second,
		Timeout:         timeout,
		RetryableErrors: retryableErrors,

		// Default dependencies (use global logger)
		Logger:     logger.NewMCPLogger(),
		HTTPClient: security.SafeHTTPClient(timeout),
	}
}

// getEnvInt reads integer from environment variable, returns default value if failed
func getEnvInt(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultValue
}

// getEnvString reads string from environment variable, returns default value if empty
func getEnvString(key string, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}
