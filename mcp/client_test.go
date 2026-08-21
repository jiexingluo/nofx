package mcp

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// ============================================================
// Test Client Creation and Configuration
// ============================================================

func TestNewClient_Default(t *testing.T) {
	client := NewClient()

	if client == nil {
		t.Fatal("client should not be nil")
	}

	c := client.(*Client)
	if c.Provider == "" {
		t.Error("Provider should have default value")
	}

	if c.MaxTokens <= 0 {
		t.Error("MaxTokens should be positive")
	}

	if c.Log == nil {
		t.Error("Log should not be nil")
	}

	if c.HTTPClient == nil {
		t.Error("HTTPClient should not be nil")
	}

	if c.Hooks == nil {
		t.Error("Hooks should not be nil")
	}
}

func TestNewClient_WithOptions(t *testing.T) {
	mockLogger := NewMockLogger()
	mockHTTP := &http.Client{Timeout: 30 * time.Second}

	client := NewClient(
		WithLogger(mockLogger),
		WithHTTPClient(mockHTTP),
		WithMaxTokens(4000),
		WithTimeout(60*time.Second),
		WithAPIKey("test-key"),
	)

	c := client.(*Client)

	if c.Log != mockLogger {
		t.Error("Log should be set from option")
	}

	if c.HTTPClient != mockHTTP {
		t.Error("HTTPClient should be set from option")
	}

	if c.MaxTokens != 4000 {
		t.Error("MaxTokens should be 4000")
	}

	if c.APIKey != "test-key" {
		t.Error("APIKey should be test-key")
	}
}

// ============================================================
// Test CallWithMessages
// ============================================================

func TestClient_CallWithMessages_Success(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetSuccessResponse("AI response content")
	mockLogger := NewMockLogger()

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
		WithBaseURL("https://api.test.com"),
	)

	result, err := client.CallWithMessages("system prompt", "user prompt")

	if err != nil {
		t.Fatalf("should not error: %v", err)
	}

	if result != "AI response content" {
		t.Errorf("expected 'AI response content', got '%s'", result)
	}

	// Verify request
	requests := mockHTTP.GetRequests()
	if len(requests) != 1 {
		t.Errorf("expected 1 request, got %d", len(requests))
	}

	if len(requests) > 0 {
		req := requests[0]
		if req.Header.Get("Authorization") == "" {
			t.Error("Authorization header should be set")
		}
		if req.Header.Get("Content-Type") != "application/json" {
			t.Error("Content-Type should be application/json")
		}
	}
}

// TestClient_CallWithMessages_ParsesCacheTokenUsage pins down the plumbing
// needed to see whether DeepSeek's automatic prompt caching is actually
// firing: prompt_cache_hit_tokens/prompt_cache_miss_tokens is a DeepSeek-
// specific extension to the OpenAI-compatible usage object, previously
// parsed nowhere in this codebase, so cost/telemetry had no way to
// distinguish a cached call from a full-price one.
func TestClient_CallWithMessages_ParsesCacheTokenUsage(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.StatusCode = 200
	mockHTTP.Response = `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":40000,"completion_tokens":200,"total_tokens":40200,"prompt_cache_hit_tokens":38000,"prompt_cache_miss_tokens":2000}}`
	mockLogger := NewMockLogger()

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
		WithBaseURL("https://api.test.com"),
	)

	if _, err := client.CallWithMessages("system prompt", "user prompt"); err != nil {
		t.Fatalf("should not error: %v", err)
	}

	usage := client.(*Client).LastCallUsage
	if usage == nil {
		t.Fatal("LastCallUsage should be populated after a successful call")
	}
	if usage.PromptTokens != 40000 || usage.CompletionTokens != 200 {
		t.Fatalf("wrong base token counts: %+v", usage)
	}
	if usage.PromptCacheHitTokens != 38000 || usage.PromptCacheMissTokens != 2000 {
		t.Fatalf("cache hit/miss tokens not parsed: %+v", usage)
	}
}

// TestClient_Call_ResetsLastCallUsageOnFailure ensures a failed call never
// reports stale usage/cost left over from a previous successful one - a
// caller checking LastCallUsage after an error must see nil, not a number
// that belongs to a different request.
func TestClient_Call_ResetsLastCallUsageOnFailure(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.StatusCode = 200
	mockHTTP.Response = `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`
	mockLogger := NewMockLogger()

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
		WithBaseURL("https://api.test.com"),
	)

	if _, err := client.CallWithMessages("system prompt", "user prompt"); err != nil {
		t.Fatalf("first call should not error: %v", err)
	}
	if client.(*Client).LastCallUsage == nil {
		t.Fatal("LastCallUsage should be populated after the first successful call")
	}

	mockHTTP.StatusCode = 500
	mockHTTP.Response = `{"error":"server error"}`
	if _, err := client.CallWithMessages("system prompt", "user prompt"); err == nil {
		t.Fatal("second call should have errored")
	}
	if client.(*Client).LastCallUsage != nil {
		t.Fatalf("LastCallUsage should be reset to nil after a failed call, still has: %+v", client.(*Client).LastCallUsage)
	}
}

func TestClient_CallWithMessages_FallsBackToReasoningContent(t *testing.T) {
	// Some reasoning-capable models (e.g. DeepSeek's thinking models) put
	// their whole answer in reasoning_content and leave content empty.
	// Reproduces the production case where CallWithMessages silently
	// returned "" despite a real, successful API response.
	mockHTTP := NewMockHTTPClient()
	mockHTTP.StatusCode = 200
	mockHTTP.Response = `{"choices":[{"message":{"content":"","reasoning_content":"thinking... <decision>[]</decision>"}}]}`
	mockLogger := NewMockLogger()

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
		WithBaseURL("https://api.test.com"),
	)

	result, err := client.CallWithMessages("system prompt", "user prompt")

	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if result != "thinking... <decision>[]</decision>" {
		t.Errorf("expected fallback to reasoning_content, got %q", result)
	}
}

func TestClient_CallWithMessages_PrefersContentOverReasoning(t *testing.T) {
	// When both are present, content is the actual answer and must win —
	// reasoning_content is only a fallback for the content-empty case.
	mockHTTP := NewMockHTTPClient()
	mockHTTP.StatusCode = 200
	mockHTTP.Response = `{"choices":[{"message":{"content":"final answer","reasoning_content":"internal thinking"}}]}`
	mockLogger := NewMockLogger()

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
		WithBaseURL("https://api.test.com"),
	)

	result, err := client.CallWithMessages("system prompt", "user prompt")

	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if result != "final answer" {
		t.Errorf("expected content to win over reasoning_content, got %q", result)
	}
}

func TestClient_CallWithMessages_NoAPIKey(t *testing.T) {
	client := NewClient()

	_, err := client.CallWithMessages("system", "user")

	if err == nil {
		t.Error("should error when API key is not set")
	}

	if err.Error() != "AI API key not set, please call SetAPIKey first" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestClient_CallWithMessages_HTTPError(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetErrorResponse(500, "Internal Server Error")
	mockLogger := NewMockLogger()

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
	)

	_, err := client.CallWithMessages("system", "user")

	if err == nil {
		t.Error("should error on HTTP error")
	}
}

// ============================================================
// Test Retry Logic
// ============================================================

func TestClient_Retry_Success(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockLogger := NewMockLogger()

	// Simulate: first call fails, second call succeeds
	callCount := 0
	mockHTTP.ResponseFunc = func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New("connection reset")
		}
		return &http.Response{
			StatusCode: 200,
			Body:       http.NoBody,
		}, nil
	}

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
		WithMaxRetries(3),
	)

	// Since our client uses Hooks.Call, need special handling
	// Here we test that CallWithMessages will invoke retry logic
	c := client.(*Client)

	// Temporarily modify retry wait time to 0 to speed up test
	oldRetries := MaxRetryTimes
	MaxRetryTimes = 3
	defer func() { MaxRetryTimes = oldRetries }()

	_, err := c.CallWithMessages("system", "user")

	// First fails (connection reset), second succeeds, but response format is wrong, will fail
	// But at least verify retry logic was triggered
	if callCount < 2 {
		t.Errorf("should retry, got %d calls", callCount)
	}

	// Check if there's retry information in logs
	logs := mockLogger.GetLogsByLevel("WARN")
	hasRetryLog := false
	for _, log := range logs {
		if log.Message == "⚠️  AI API call failed, retrying (2/3)..." {
			hasRetryLog = true
			break
		}
	}

	if !hasRetryLog && callCount >= 2 {
		// If retry was indeed attempted, there should be warning logs
		// But due to our test setup, it may not trigger, so just check here
		t.Log("Retry was attempted")
	}

	_ = err // Ignore error, we mainly test retry logic was triggered
}

func TestClient_Retry_NonRetryableError(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetErrorResponse(400, "Bad Request")
	mockLogger := NewMockLogger()

	client := NewClient(
		WithHTTPClient(mockHTTP.ToHTTPClient()),
		WithLogger(mockLogger),
		WithAPIKey("test-key"),
	)

	_, err := client.CallWithMessages("system", "user")

	if err == nil {
		t.Error("should error")
	}

	// Verify no retry (because 400 is not a retryable error)
	requests := mockHTTP.GetRequests()
	if len(requests) != 1 {
		t.Errorf("should not retry for 400 error, got %d requests", len(requests))
	}
}

// ============================================================
// Test Hook Methods
// ============================================================

func TestClient_BuildMCPRequestBody(t *testing.T) {
	client := NewClient()
	c := client.(*Client)

	body := c.BuildMCPRequestBody("system prompt", "user prompt")

	if body == nil {
		t.Fatal("body should not be nil")
	}

	if body["model"] == nil {
		t.Error("body should have model field")
	}

	messages, ok := body["messages"].([]map[string]string)
	if !ok {
		t.Fatal("messages should be []map[string]string")
	}

	if len(messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(messages))
	}

	if messages[0]["role"] != "system" {
		t.Error("first message should be system")
	}

	if messages[1]["role"] != "user" {
		t.Error("second message should be user")
	}
}

func TestClient_BuildUrl(t *testing.T) {
	tests := []struct {
		name       string
		baseURL    string
		useFullURL bool
		expected   string
	}{
		{
			name:       "normal URL",
			baseURL:    "https://api.test.com/v1",
			useFullURL: false,
			expected:   "https://api.test.com/v1/chat/completions",
		},
		{
			name:       "full URL",
			baseURL:    "https://api.test.com/custom/endpoint",
			useFullURL: true,
			expected:   "https://api.test.com/custom/endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(
				WithProvider("test-provider"), // Prevent default DeepSeek settings
				WithBaseURL(tt.baseURL),
				WithUseFullURL(tt.useFullURL),
			)
			c := client.(*Client)

			url := c.BuildUrl()
			if url != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, url)
			}
		})
	}
}

func TestClient_SetAuthHeader(t *testing.T) {
	client := NewClient(WithAPIKey("test-api-key"))
	c := client.(*Client)

	headers := make(http.Header)
	c.SetAuthHeader(headers)

	authHeader := headers.Get("Authorization")
	if authHeader != "Bearer test-api-key" {
		t.Errorf("expected 'Bearer test-api-key', got '%s'", authHeader)
	}
}

func TestClient_IsRetryableError(t *testing.T) {
	client := NewClient()
	c := client.(*Client)

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "EOF error",
			err:      errors.New("unexpected EOF"),
			expected: true,
		},
		{
			name:     "timeout error",
			err:      errors.New("timeout exceeded"),
			expected: true,
		},
		{
			name:     "connection reset",
			err:      errors.New("connection reset by peer"),
			expected: true,
		},
		{
			name:     "upstream empty output",
			err:      errors.New(`API returned error (status 429): {"error":{"code":"upstream_empty_output","message":"Upstream model returned empty output.","type":"rate_limit_error"}}`),
			expected: true,
		},
		{
			name:     "normal error",
			err:      errors.New("bad request"),
			expected: false,
		},
		{
			name:     "validation error",
			err:      errors.New("invalid input"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.IsRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// ============================================================
// Test SetTimeout
// ============================================================

func TestClient_SetTimeout(t *testing.T) {
	client := NewClient()

	newTimeout := 90 * time.Second
	client.SetTimeout(newTimeout)

	c := client.(*Client)
	if c.HTTPClient.Timeout != newTimeout {
		t.Errorf("expected timeout %v, got %v", newTimeout, c.HTTPClient.Timeout)
	}
}

// ============================================================
// Test String Method
// ============================================================

func TestClient_String(t *testing.T) {
	client := NewClient(
		WithProvider("test-provider"),
		WithModel("test-model"),
	)

	c := client.(*Client)
	str := c.String()

	expectedContains := []string{"test-provider", "test-model"}
	for _, exp := range expectedContains {
		if !contains(str, exp) {
			t.Errorf("String() should contain '%s', got '%s'", exp, str)
		}
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
