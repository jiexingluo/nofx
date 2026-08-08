package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nofx/auth"
	"nofx/config"

	"github.com/gin-gonic/gin"
)

// TestAuthMiddleware_BypassWithoutToken tests that requests without
// an Authorization header are allowed through with default admin context.
func TestAuthMiddleware_BypassWithoutToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Setup a minimal server with the middleware.
	testSecret := strings.Repeat("test-secret-", 4)
	t.Setenv("JWT_SECRET", testSecret)
	config.Init()
	auth.SetJWTSecret(testSecret)
	cfg := config.Get()
	originalBypassSetting := cfg.LocalAdminBypassEnabled
	t.Cleanup(func() { cfg.LocalAdminBypassEnabled = originalBypassSetting })
	s := &Server{}
	handler := s.authMiddleware()

	// Create test router
	router := gin.New()
	router.GET("/test", handler, func(c *gin.Context) {
		userID := c.GetString("user_id")
		email := c.GetString("email")
		c.JSON(http.StatusOK, gin.H{
			"user_id": userID,
			"email":   email,
		})
	})

	// Test: Request without Authorization header should succeed
	t.Run("no auth header should inject default admin", func(t *testing.T) {
		cfg.LocalAdminBypassEnabled = true
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if body == "" {
			t.Fatal("Response body is empty")
		}

		// Verify default admin user context was injected
		if !contains(body, "admin-default") {
			t.Errorf("Expected user_id 'admin-default' in response, got: %s", body)
		}
		if !contains(body, "admin@localhost") {
			t.Errorf("Expected email 'admin@localhost' in response, got: %s", body)
		}
	})

	t.Run("no auth header should be rejected when bypass is disabled", func(t *testing.T) {
		cfg.LocalAdminBypassEnabled = false
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected status 401, got %d", w.Code)
		}
	})

	// Test: Request with valid JWT should still work normally
	t.Run("valid token should use token claims", func(t *testing.T) {
		cfg.LocalAdminBypassEnabled = false
		token, err := auth.GenerateJWT("user-123", "user@example.com")
		if err != nil {
			t.Fatalf("Failed to generate JWT: %v", err)
		}

		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !contains(body, "user-123") {
			t.Errorf("Expected user_id 'user-123' in response, got: %s", body)
		}
		if !contains(body, "user@example.com") {
			t.Errorf("Expected email 'user@example.com' in response, got: %s", body)
		}
	})

	// Test: Request with bypass-token should use default admin
	t.Run("bypass-token should inject default admin", func(t *testing.T) {
		cfg.LocalAdminBypassEnabled = true
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer bypass-token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !contains(body, "admin-default") {
			t.Errorf("Expected user_id 'admin-default' in response, got: %s", body)
		}
		if !contains(body, "admin@localhost") {
			t.Errorf("Expected email 'admin@localhost' in response, got: %s", body)
		}
	})

	t.Run("bypass-token should be rejected when bypass is disabled", func(t *testing.T) {
		cfg.LocalAdminBypassEnabled = false
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer bypass-token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected status 401, got %d", w.Code)
		}
	})

	// Test: Request with invalid token should be rejected
	t.Run("invalid token should be rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected status 401, got %d", w.Code)
		}
	})

	// Test: Request with malformed Authorization header should be rejected
	t.Run("malformed auth header should be rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("Authorization", "NotBearer token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Expected status 401, got %d", w.Code)
		}
	})
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
