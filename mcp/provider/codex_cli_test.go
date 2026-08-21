package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nofx/mcp"
)

// newTestCodexClient builds a CodexClient with an isolated temp-dir
// workspace and a base *mcp.Client, bypassing the registry factory so tests
// can inject a fake runner instead of invoking the real codex binary. Pins
// CODEX_CLI_BIN_PATH to a fixed, never-executed placeholder so
// resolveCodexBinPath() (called unconditionally at the top of Call(), before
// the fake runner is ever reached) can't fall through to a real PATH lookup -
// without this, these tests only pass by accident on any machine that
// happens to have a `codex` binary on PATH, and fail everywhere else (caught
// via a real failure over SSH on a host with no such binary on its
// non-interactive PATH).
func newTestCodexClient(t *testing.T, runner codexRunner) *CodexClient {
	t.Helper()
	t.Setenv("CODEX_CLI_BIN_PATH", "/nonexistent/codex-test-placeholder")
	base := mcp.NewClient(mcp.WithProvider(mcp.ProviderCodexCLI)).(*mcp.Client)

	dir := t.TempDir()
	c := &CodexClient{
		Client:     base,
		workDir:    filepath.Join(dir, "workdir"),
		schemaPath: filepath.Join(dir, "schema.json"),
		runsDir:    filepath.Join(dir, "runs"),
		runner:     runner,
	}
	base.Hooks = c
	return c
}

// findFlagValue returns the argument immediately following the given flag,
// mirroring how a real exec.Cmd's args slice would be inspected.
func findFlagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestCall_HappyPath(t *testing.T) {
	var capturedArgs []string
	var capturedStdin string

	runner := func(_ context.Context, binPath string, args []string, stdin io.Reader) ([]byte, []byte, error) {
		capturedArgs = args
		b, _ := io.ReadAll(stdin)
		capturedStdin = string(b)

		outPath, ok := findFlagValue(args, "-o")
		if !ok {
			t.Fatal("runner: no -o flag in args")
		}
		payload := `{"decisions":[{"symbol":"BTCUSDT","action":"hold","leverage":null,"position_size_usd":null,"stop_loss":null,"take_profit":null,"price":null,"quantity":null,"confidence":null,"risk_usd":null,"reasoning":"no signal"}]}`
		if err := os.WriteFile(outPath, []byte(payload), 0o600); err != nil {
			t.Fatalf("runner: failed to write fake output: %v", err)
		}
		return []byte("codex\n" + payload), nil, nil
	}

	c := newTestCodexClient(t, runner)
	result, err := c.Call("SYSTEM RULES", "candidate data here")
	if err != nil {
		t.Fatalf("Call() returned error: %v", err)
	}

	// Must be the unwrapped bare array, not the {"decisions": ...} wrapper -
	// this is what lets kernel's extractDecisions bare-array fallback parse
	// it with zero changes to any existing parsing code.
	if !strings.HasPrefix(strings.TrimSpace(result), "[") {
		t.Fatalf("expected unwrapped bare array, got: %s", result)
	}
	if !strings.Contains(result, `"symbol":"BTCUSDT"`) {
		t.Fatalf("result missing expected content: %s", result)
	}

	// Sandbox/isolation flags must all be present.
	for _, want := range []string{"exec", "-", "--sandbox", "read-only", "--ephemeral", "--skip-git-repo-check", "--json"} {
		found := false
		for _, a := range capturedArgs {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected arg %q in %v", want, capturedArgs)
		}
	}
	if v, ok := findFlagValue(capturedArgs, "-C"); !ok || v != c.workDir {
		t.Errorf("-C = %q, want %q", v, c.workDir)
	}
	if v, ok := findFlagValue(capturedArgs, "--output-schema"); !ok || v != c.schemaPath {
		t.Errorf("--output-schema = %q, want %q", v, c.schemaPath)
	}
	if v, ok := findFlagValue(capturedArgs, "--color"); !ok || v != "never" {
		t.Errorf("--color = %q, want %q", v, "never")
	}
	if v, ok := findFlagValue(capturedArgs, "-c"); !ok || v != "model_reasoning_summary=detailed" {
		t.Errorf("-c = %q, want %q (needed for reasoning items to appear in --json output at all - see codexReasoningSummaryConfig)", v, "model_reasoning_summary=detailed")
	}

	if !strings.Contains(capturedStdin, "SYSTEM RULES") || !strings.Contains(capturedStdin, "candidate data here") {
		t.Fatalf("stdin missing combined prompt content: %s", capturedStdin)
	}

	// Schema file must have been written by ensureWorkspace.
	if _, err := os.Stat(c.schemaPath); err != nil {
		t.Fatalf("schema file was not created: %v", err)
	}
}

// TestCall_ReasoningSummaryPresent_WrapsInTag is the core regression guard
// for the whole --json integration: when the model actually produced a
// reasoning item, Call() must prepend it as a <reasoning> tag so
// kernel/engine_analysis.go's extractCoTTrace (which checks for exactly this
// tag first, before any other fallback) picks it up with zero changes to
// that package.
func TestCall_ReasoningSummaryPresent_WrapsInTag(t *testing.T) {
	runner := func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, []byte, error) {
		outPath, _ := findFlagValue(args, "-o")
		payload := `{"decisions":[{"symbol":"BTCUSDT","action":"hold","leverage":null,"position_size_usd":null,"stop_loss":null,"take_profit":null,"price":null,"quantity":null,"confidence":null,"risk_usd":null,"reasoning":"no signal"}]}`
		os.WriteFile(outPath, []byte(payload), 0o600)

		stdout := strings.Join([]string{
			`{"type":"thread.started","thread_id":"t1"}`,
			`{"type":"turn.started"}`,
			`{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"**Defining concise hold decision schema**"}}`,
			`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"` + strings.ReplaceAll(payload, `"`, `\"`) + `"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":57647,"reasoning_output_tokens":132}}`,
			``, // trailing newline, matching real output
		}, "\n")
		return []byte(stdout), nil, nil
	}

	c := newTestCodexClient(t, runner)
	result, err := c.Call("sys", "user")
	if err != nil {
		t.Fatalf("Call() returned error: %v", err)
	}

	const wantReasoning = "**Defining concise hold decision schema**"
	if !strings.HasPrefix(result, "<reasoning>"+wantReasoning+"</reasoning>\n[") {
		t.Fatalf("expected result to start with the reasoning tag followed by the bare array, got: %s", result)
	}
	if !strings.Contains(result, `"symbol":"BTCUSDT"`) {
		t.Fatalf("result missing the decision content: %s", result)
	}
}

// TestCall_ReasoningSummaryAbsent_ReturnsBareArray covers the common case
// (confirmed live: at the deployed model_reasoning_effort, most decision
// cycles produce no reasoning item at all) - Call() must not invent an empty
// <reasoning></reasoning> wrapper, just return the bare array exactly as
// before this feature existed.
func TestCall_ReasoningSummaryAbsent_ReturnsBareArray(t *testing.T) {
	runner := func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, []byte, error) {
		outPath, _ := findFlagValue(args, "-o")
		payload := `{"decisions":[]}`
		os.WriteFile(outPath, []byte(payload), 0o600)

		stdout := `{"type":"thread.started","thread_id":"t1"}` + "\n" +
			`{"type":"turn.started"}` + "\n" +
			`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"{\"decisions\":[]}"}}` + "\n" +
			`{"type":"turn.completed","usage":{"input_tokens":100,"reasoning_output_tokens":0}}` + "\n"
		return []byte(stdout), nil, nil
	}

	c := newTestCodexClient(t, runner)
	result, err := c.Call("sys", "user")
	if err != nil {
		t.Fatalf("Call() returned error: %v", err)
	}
	if strings.Contains(result, "<reasoning>") {
		t.Fatalf("expected no reasoning tag when the model produced no reasoning item, got: %s", result)
	}
	if strings.TrimSpace(result) != "[]" {
		t.Fatalf("expected bare empty array unchanged, got: %s", result)
	}
}

// TestCall_MalformedJSONLLine_DoesNotFailCall guards the "best-effort only"
// contract: --json parsing is purely additive on top of the decision already
// read from -o, so any hiccup there (a partial line, a stray non-JSON line)
// must never surface as a call failure.
func TestCall_MalformedJSONLLine_DoesNotFailCall(t *testing.T) {
	runner := func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, []byte, error) {
		outPath, _ := findFlagValue(args, "-o")
		payload := `{"decisions":[{"symbol":"ETHUSDT","action":"wait","leverage":null,"position_size_usd":null,"stop_loss":null,"take_profit":null,"price":null,"quantity":null,"confidence":null,"risk_usd":null,"reasoning":"waiting"}]}`
		os.WriteFile(outPath, []byte(payload), 0o600)

		stdout := "not valid json at all\n{also not valid\n" +
			`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"ok"}}` + "\n"
		return []byte(stdout), nil, nil
	}

	c := newTestCodexClient(t, runner)
	result, err := c.Call("sys", "user")
	if err != nil {
		t.Fatalf("Call() should tolerate malformed --json lines, got error: %v", err)
	}
	if !strings.Contains(result, `"symbol":"ETHUSDT"`) {
		t.Fatalf("decision from -o file should be unaffected by malformed stdout, got: %s", result)
	}
}

func TestCall_NonZeroExit_ErrorIncludesStderr(t *testing.T) {
	runner := func(context.Context, string, []string, io.Reader) ([]byte, []byte, error) {
		return nil, []byte("rate limited"), errors.New("exit status 1")
	}
	c := newTestCodexClient(t, runner)

	_, err := c.Call("sys", "user")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error should include stderr, got: %v", err)
	}
}

func TestCall_Timeout_ProducesTimeoutSpecificError(t *testing.T) {
	runner := func(ctx context.Context, _ string, _ []string, _ io.Reader) ([]byte, []byte, error) {
		<-ctx.Done() // simulate a hang until the context deadline fires
		return nil, nil, ctx.Err()
	}
	c := newTestCodexClient(t, runner)
	c.HTTPClient.Timeout = 20 * time.Millisecond

	_, err := c.Call("sys", "user")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout-specific error, got: %v", err)
	}
}

func TestCall_ExitZeroButOutputFileMissing(t *testing.T) {
	runner := func(context.Context, string, []string, io.Reader) ([]byte, []byte, error) {
		return []byte("codex\n"), nil, nil // never writes the -o file
	}
	c := newTestCodexClient(t, runner)

	_, err := c.Call("sys", "user")
	if err == nil {
		t.Fatal("expected an error when the output file was never written")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("expected an 'unreadable' error, got: %v", err)
	}
}

func TestCall_ExitZeroButOutputEmpty(t *testing.T) {
	runner := func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, []byte, error) {
		outPath, _ := findFlagValue(args, "-o")
		os.WriteFile(outPath, []byte(""), 0o600)
		return nil, nil, nil
	}
	c := newTestCodexClient(t, runner)

	_, err := c.Call("sys", "user")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected an 'empty output' error, got: %v", err)
	}
}

func TestCall_ExitZeroButMalformedJSON(t *testing.T) {
	runner := func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, []byte, error) {
		outPath, _ := findFlagValue(args, "-o")
		os.WriteFile(outPath, []byte("{not json"), 0o600)
		return nil, nil, nil
	}
	c := newTestCodexClient(t, runner)

	_, err := c.Call("sys", "user")
	if err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Fatalf("expected a JSON-parse error, got: %v", err)
	}
}

func TestCall_ExitZeroButMissingDecisionsKey(t *testing.T) {
	runner := func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, []byte, error) {
		outPath, _ := findFlagValue(args, "-o")
		os.WriteFile(outPath, []byte(`{"something_else": 1}`), 0o600)
		return nil, nil, nil
	}
	c := newTestCodexClient(t, runner)

	_, err := c.Call("sys", "user")
	if err == nil || !strings.Contains(err.Error(), "decisions") {
		t.Fatalf("expected a missing-'decisions'-key error, got: %v", err)
	}
}

// TestCall_OutputPathUniqueAndCleanedUp guards directly against the stale-
// result class of bug: two sequential calls must never share an -o path
// (which would let a failed call silently read a previous success's file),
// and each call's temp output must be removed afterward.
func TestCall_OutputPathUniqueAndCleanedUp(t *testing.T) {
	var seenPaths []string
	runner := func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, []byte, error) {
		outPath, _ := findFlagValue(args, "-o")
		seenPaths = append(seenPaths, outPath)
		os.WriteFile(outPath, []byte(`{"decisions":[]}`), 0o600)
		return nil, nil, nil
	}
	c := newTestCodexClient(t, runner)

	if _, err := c.Call("sys", "user1"); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := c.Call("sys", "user2"); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	if len(seenPaths) != 2 || seenPaths[0] == seenPaths[1] {
		t.Fatalf("expected two distinct output paths, got: %v", seenPaths)
	}
	for _, p := range seenPaths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %q to be cleaned up after the call, stat err: %v", p, err)
		}
	}
}

func TestSetAPIKey(t *testing.T) {
	t.Run("empty key gets a placeholder so the base client's non-empty guard doesn't block calls", func(t *testing.T) {
		c := newTestCodexClient(t, nil)
		c.SetAPIKey("", "", "")
		if c.APIKey == "" {
			t.Fatal("APIKey should be non-empty after SetAPIKey(\"\", ...)")
		}
	})

	t.Run("non-empty customModel is stored as the cost-tracking label", func(t *testing.T) {
		c := newTestCodexClient(t, nil)
		c.SetAPIKey("", "", "gpt-5.6-sol")
		if c.Model != "gpt-5.6-sol" {
			t.Fatalf("Model = %q, want %q", c.Model, "gpt-5.6-sol")
		}
	})
}

// TestLastCallCostUSD_AlwaysZero is the single guarantee that
// trader/auto_trader_loop.go's cost-recording logic never fabricates a
// dollar cost for this subscription-billed provider. A regression here
// would silently reintroduce a misleading per-call estimate.
func TestLastCallCostUSD_AlwaysZero(t *testing.T) {
	c := newTestCodexClient(t, nil)
	cost, ok := c.LastCallCostUSD()
	if !ok {
		t.Fatal("LastCallCostUSD should always report ok=true")
	}
	if cost != 0 {
		t.Fatalf("LastCallCostUSD = %v, want 0", cost)
	}
}

func TestUnsupportedMethods(t *testing.T) {
	c := newTestCodexClient(t, nil)

	if _, err := c.CallWithRequest(&mcp.Request{}); !errors.Is(err, ErrCodexCLIUnsupported) {
		t.Errorf("CallWithRequest error = %v, want ErrCodexCLIUnsupported", err)
	}
	if _, err := c.CallWithRequestStream(&mcp.Request{}, func(string) {}); !errors.Is(err, ErrCodexCLIUnsupported) {
		t.Errorf("CallWithRequestStream error = %v, want ErrCodexCLIUnsupported", err)
	}
	if _, err := c.CallWithRequestFull(&mcp.Request{}); !errors.Is(err, ErrCodexCLIUnsupported) {
		t.Errorf("CallWithRequestFull error = %v, want ErrCodexCLIUnsupported", err)
	}
}

func TestBaseClient(t *testing.T) {
	c := newTestCodexClient(t, nil)
	if c.BaseClient() == nil {
		t.Fatal("BaseClient() should not return nil")
	}
	if c.BaseClient().Provider != mcp.ProviderCodexCLI {
		t.Fatalf("BaseClient().Provider = %q, want %q", c.BaseClient().Provider, mcp.ProviderCodexCLI)
	}
}

func TestProviderRegistration(t *testing.T) {
	client := mcp.NewAIClientByProvider(mcp.ProviderCodexCLI)
	if client == nil {
		t.Fatal("NewAIClientByProvider(codex_cli) returned nil - registration missing?")
	}
	if _, ok := client.(*CodexClient); !ok {
		t.Fatalf("expected *CodexClient, got %T", client)
	}
}

func TestUnwrapDecisions(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"valid wrapper", `{"decisions":[{"symbol":"BTCUSDT","action":"hold"}]}`, `[{"symbol":"BTCUSDT","action":"hold"}]`, false},
		{"empty array is valid", `{"decisions":[]}`, `[]`, false},
		{"missing decisions key", `{"foo":1}`, "", true},
		{"malformed json", `{not json`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unwrapDecisions([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCombinePrompts(t *testing.T) {
	got := combinePrompts("SYS", "USER")
	if !strings.Contains(got, "SYS") || !strings.Contains(got, "USER") {
		t.Fatalf("combinePrompts dropped content: %s", got)
	}
	// System content must appear before user content.
	if strings.Index(got, "SYS") > strings.Index(got, "USER") {
		t.Fatalf("expected system prompt before user prompt: %s", got)
	}
}

func TestResolveCodexBinPath(t *testing.T) {
	t.Run("CODEX_CLI_BIN_PATH takes priority", func(t *testing.T) {
		t.Setenv("CODEX_CLI_BIN_PATH", "/custom/path/codex")
		p, err := resolveCodexBinPath()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p != "/custom/path/codex" {
			t.Fatalf("got %q, want /custom/path/codex", p)
		}
	})

	t.Run("falls back to PATH lookup, errors clearly when absent", func(t *testing.T) {
		t.Setenv("CODEX_CLI_BIN_PATH", "")
		t.Setenv("PATH", "") // guarantee lookup fails regardless of the host's real PATH
		_, err := resolveCodexBinPath()
		if err == nil {
			t.Fatal("expected an error when neither CODEX_CLI_BIN_PATH nor PATH resolve")
		}
	})
}
