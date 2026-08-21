package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	// Registers every mcp provider (including codex_cli) so
	// mcp.IsRegisteredProvider sees the same registry the real server has
	// at runtime - without this blank import, UpdateWithName would silently
	// fall through to the legacy split-on-"_" heuristic this test guards
	// against, and the test would pass for the wrong reason.
	_ "nofx/mcp/provider"
)

func newTestAIModelStore(t *testing.T) *AIModelStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	s := NewAIModelStore(db)
	if err := s.initTables(); err != nil {
		t.Fatalf("init ai_models table: %v", err)
	}
	return s
}

// TestUpdateWithName_NewRecord_UnderscoreProviderName guards against a real,
// live-caught bug: creating a new model for a provider whose own name
// contains an underscore (codex_cli) used to derive Provider by splitting
// the catalog id on "_" and taking the last segment, silently saving
// Provider="cli" - a name nothing ever registers, so the real client was
// never reachable. A user hit this through the actual "Add Model" UI flow.
func TestUpdateWithName_NewRecord_UnderscoreProviderName(t *testing.T) {
	s := newTestAIModelStore(t)

	if err := s.Update("admin-default", "codex_cli", true, "", "", "gpt-5.6-sol"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	model, err := s.Get("admin-default", "admin-default_codex_cli")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if model.Provider != "codex_cli" {
		t.Fatalf("Provider = %q, want %q (regression: got mis-split substring)", model.Provider, "codex_cli")
	}
	if model.ID != "admin-default_codex_cli" {
		t.Fatalf("ID = %q, want the userID_provider shape", model.ID)
	}
	if model.CustomModelName != "gpt-5.6-sol" {
		t.Fatalf("CustomModelName = %q, want %q", model.CustomModelName, "gpt-5.6-sol")
	}
}

// TestUpdateWithName_NewRecord_SingleWordProviders is a regression guard:
// the fix must not change behavior for every existing provider, whose names
// happen to contain no underscore and so were already handled correctly by
// the old split heuristic.
func TestUpdateWithName_NewRecord_SingleWordProviders(t *testing.T) {
	s := newTestAIModelStore(t)

	for _, provider := range []string{"openai", "claude", "gemini", "grok", "kimi", "minimax", "deepseek", "qwen"} {
		t.Run(provider, func(t *testing.T) {
			if err := s.Update("admin-default", provider, true, "", "", ""); err != nil {
				t.Fatalf("Update(%s): %v", provider, err)
			}
			model, err := s.Get("admin-default", "admin-default_"+provider)
			if err != nil {
				t.Fatalf("Get(%s): %v", provider, err)
			}
			if model.Provider != provider {
				t.Fatalf("Provider = %q, want %q", model.Provider, provider)
			}
		})
	}
}
