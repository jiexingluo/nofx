package provider

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"nofx/mcp"
)

const (
	// DefaultCodexModel is a cost-tracking label only — the model actually
	// used is whatever ~/.codex/config.toml specifies on the host running
	// the codex binary. nofx never passes -m/--model to the CLI (see
	// Call()), by design: deferring to the CLI's own config avoids nofx and
	// config.toml drifting out of sync about which model is "current".
	DefaultCodexModel = "gpt-5.6"

	// codexDefaultTimeout mirrors the precedent in mcp/payment/x402.go's
	// X402Timeout for another CLI-adjacent, non-trivial-latency provider.
	// Validated against real ~50-57K token trading prompts in production:
	// consistently 12-16s, well under this budget.
	codexDefaultTimeout = 5 * time.Minute

	// codexReasoningSummaryConfig enables reasoning-summary items in the
	// --json event stream (see extractReasoningSummary). Passed per-call via
	// -c rather than written into ~/.codex/config.toml, so it only affects
	// nofx's own invocations and never changes how the user's own
	// interactive `codex` sessions on this host behave. Verified live against
	// a real production prompt at the model_reasoning_effort the deployed
	// config.toml actually uses ("medium") - without this override, no
	// reasoning item appears in the stream even when reasoning_output_tokens
	// is non-zero, so the model's reasoning happened but was never surfaced.
	codexReasoningSummaryConfig = "model_reasoning_summary=detailed"
)

// ErrCodexCLIUnsupported is returned by the AIClient methods this provider
// doesn't implement. Only CallWithMessages (via the Call hook below) is used
// by the live trading-decision path (kernel/engine_analysis.go).
var ErrCodexCLIUnsupported = errors.New("codex_cli: not supported for this provider (only CallWithMessages is implemented)")

//go:embed codex_cli_schema.json
var codexDecisionSchemaJSON []byte

// codexRunner abstracts subprocess execution so tests can substitute a fake
// implementation without invoking the real `codex` binary. stdin carries the
// full combined prompt; stdout/stderr are captured whole.
type codexRunner func(ctx context.Context, binPath string, args []string, stdin io.Reader) (stdout, stderr []byte, err error)

func execCodexRunner(ctx context.Context, binPath string, args []string, stdin io.Reader) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	// MUST be explicit: `codex exec` reads from stdin whenever the prompt
	// arg is "-", and even hangs indefinitely waiting on stdin in some
	// invocations if it's left as the zero value instead of an inherited
	// terminal-like fd - verified this gotcha by hand against the real
	// binary before finding the fix.
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// CodexClient implements mcp.AIClient by shelling out to a local `codex`
// CLI binary (OpenAI Codex CLI) instead of making an HTTP call. Used when
// the account authenticates via a ChatGPT/Codex subscription login
// (~/.codex/auth.json OAuth tokens) rather than a metered API key - there is
// no HTTP endpoint or API key to speak of, only a locally-authenticated
// binary invocation.
type CodexClient struct {
	*mcp.Client

	workDir    string // -C root; isolated, kept empty by codex's own --sandbox read-only
	schemaPath string // fixed path to the embedded output-schema JSON; self-healing
	runsDir    string // per-call -o output files live here, unique name per call, removed after use

	runner codexRunner // execCodexRunner by default; swappable in tests
}

func (c *CodexClient) BaseClient() *mcp.Client { return c.Client }

func init() {
	mcp.RegisterProvider(mcp.ProviderCodexCLI, func(opts ...mcp.ClientOption) mcp.AIClient {
		return NewCodexClientWithOptions(opts...)
	})
}

// NewCodexClientWithOptions creates a codex_cli client with options.
func NewCodexClientWithOptions(opts ...mcp.ClientOption) mcp.AIClient {
	baseOpts := []mcp.ClientOption{
		mcp.WithProvider(mcp.ProviderCodexCLI),
		mcp.WithModel(DefaultCodexModel),
		mcp.WithTimeout(codexDefaultTimeout),
		// Disable the generic HTTP-style outer retry: a blind retry of a
		// failed/timed-out `codex exec` doubles both latency and real
		// (accepted-but-not-to-be-wasted) token cost. A failed call surfaces
		// as a plain error to the trading loop, which already has its own
		// provider-agnostic circuit breaker (consecutive-failure safe mode) -
		// leaning on that existing, tested mechanism is safer than inventing
		// new retry semantics here. Same rationale as claw402's MaxRetries=1.
		mcp.WithMaxRetries(1),
	}
	allOpts := append(baseOpts, opts...)
	baseClient := mcp.NewClient(allOpts...).(*mcp.Client)

	stateDir := os.Getenv("CODEX_CLI_STATE_DIR")
	if stateDir == "" {
		stateDir = "codex-cli" // relative to the process cwd, mirroring store/driver.go's "data/data.db" convention
	}
	c := &CodexClient{
		Client:     baseClient,
		workDir:    filepath.Join(stateDir, "workdir"),
		schemaPath: filepath.Join(stateDir, "decision-schema.json"),
		runsDir:    filepath.Join(stateDir, "runs"),
		runner:     execCodexRunner,
	}
	baseClient.Hooks = c
	return c
}

// SetAPIKey satisfies the AIClient contract but does not authenticate
// anything: codex CLI authenticates via `codex login` (ChatGPT/Codex
// subscription OAuth, ~/.codex/auth.json) on the host, independent of
// anything nofx sends. apiKey is accepted only because
// (*mcp.Client).CallWithMessages guards on a non-empty client.APIKey before
// dispatching to Call() - an empty value here would make every call fail
// with "AI API key not set" despite there being nothing to configure.
func (c *CodexClient) SetAPIKey(apiKey, customURL, customModel string) {
	if apiKey == "" {
		apiKey = "codex-cli-local-auth-placeholder"
	}
	c.APIKey = apiKey

	if customURL != "" {
		c.Log.Warnf("🔧 [MCP] codex_cli: custom_api_url %q ignored (no HTTP endpoint for this provider)", customURL)
	}
	if customModel != "" {
		c.Model = customModel // cost-tracking label only, per DefaultCodexModel's doc comment
	}
}

// LastCallCostUSD always reports a real $0: codex CLI is billed through a
// flat-rate ChatGPT/Codex subscription, not per-call, so there is no
// metered cost for nofx to estimate. Implementing this (returning ok=true)
// is what makes trader/auto_trader_loop.go's charge-recording logic skip
// its flat per-model dollar estimate entirely, exactly as it already does
// for claw402's real settled cost - see that call site for the priority
// chain this participates in.
func (c *CodexClient) LastCallCostUSD() (float64, bool) { return 0, true }

func (c *CodexClient) CallWithRequest(*mcp.Request) (string, error) {
	return "", ErrCodexCLIUnsupported
}

func (c *CodexClient) CallWithRequestStream(*mcp.Request, func(string)) (string, error) {
	return "", ErrCodexCLIUnsupported
}

func (c *CodexClient) CallWithRequestFull(*mcp.Request) (*mcp.LLMResponse, error) {
	return nil, ErrCodexCLIUnsupported
}

// Call implements the ClientHooks.Call hook that the embedded *mcp.Client's
// CallWithMessages (promoted, not overridden here) dispatches to via
// c.Hooks.Call(...). This is the only method on the live trading-decision
// path that actually does anything.
func (c *CodexClient) Call(systemPrompt, userPrompt string) (string, error) {
	binPath, err := resolveCodexBinPath()
	if err != nil {
		return "", fmt.Errorf("codex_cli: %w", err)
	}
	if err := c.ensureWorkspace(); err != nil {
		return "", fmt.Errorf("codex_cli: %w", err)
	}

	// Unique per call, never reused: a failed/timed-out call must never be
	// able to read a *previous* successful call's leftover output and
	// mistake it for this cycle's decision. That would mean silently acting
	// on stale, possibly hours-old trading decisions - a real risk for a
	// live-money bot, not a hypothetical one, which is why this is a fresh
	// filename per call rather than a fixed path.
	outPath := filepath.Join(c.runsDir, "out-"+uuid.NewString()+".json")
	defer os.Remove(outPath)

	args := []string{
		"exec", "-",
		"--sandbox", "read-only",
		"--ephemeral",
		"--skip-git-repo-check",
		"-C", c.workDir,
		"--output-schema", c.schemaPath,
		"-o", outPath,
		// --json streams structured events (reasoning/agent_message/usage)
		// on stdout, independent of the -o file above - -o remains the sole
		// source of the actual decision, this is purely additive visibility
		// into what the model reasoned about before answering. --color never
		// is explicit belt-and-suspenders: a non-TTY buffer already implies
		// no color under the default "auto", but this removes any doubt.
		"--json",
		"--color", "never",
		"-c", codexReasoningSummaryConfig,
	}

	timeout := codexDefaultTimeout
	if c.HTTPClient != nil && c.HTTPClient.Timeout > 0 {
		timeout = c.HTTPClient.Timeout // reuses the field SetTimeout already maintains
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	prompt := combinePrompts(systemPrompt, userPrompt)
	stdout, stderr, runErr := c.runner(ctx, binPath, args, strings.NewReader(prompt))

	// Any failure path returns immediately without ever reading outPath -
	// this is the other half of the stale-result guard above: a file that
	// happens to exist from some unrelated cause must never be trusted
	// just because it's present.
	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("codex_cli: exec timed out after %s: %w — stderr: %s", timeout, runErr, truncate(stderr, 4096))
		}
		return "", fmt.Errorf("codex_cli: exec failed: %w — stderr: %s", runErr, truncate(stderr, 4096))
	}
	if len(stderr) > 0 {
		c.Log.Warnf("🔧 [MCP] codex_cli: exit 0 but stderr non-empty: %s", truncate(stderr, 2048))
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		return "", fmt.Errorf("codex_cli: exit 0 but output file unreadable: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", errors.New("codex_cli: exit 0 but output file was empty")
	}

	result, err := unwrapDecisions(raw)
	if err != nil {
		return "", fmt.Errorf("codex_cli: %w — raw output: %s", err, truncate(raw, 2048))
	}

	// Best-effort only: the decision above is already final and correct
	// regardless of what follows. A reasoning summary, when the model
	// produced one, is prepended as a <reasoning> tag so it flows through
	// kernel/engine_analysis.go's existing extractCoTTrace (which checks for
	// exactly this tag first) with zero changes elsewhere in the pipeline.
	if reasoning := extractReasoningSummary(stdout, c.Log); reasoning != "" {
		return "<reasoning>" + reasoning + "</reasoning>\n" + result, nil
	}
	return result, nil
}

// codexJSONLEvent is the minimal shape needed from a `codex exec --json`
// event line - only thread/turn-completed events carry other fields, none
// of which are needed here.
type codexJSONLEvent struct {
	Type string `json:"type"`
	Item *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

// extractReasoningSummary scans a `codex exec --json` stdout stream for
// item.completed events of type "reasoning" and joins their text, in order.
// Real reasoning content is genuinely short - OpenAI's reasoning-summary
// feature returns brief section-style summaries (e.g. "**Defining concise
// hold decision schema**"), not a verbose chain-of-thought transcript -
// confirmed live against real production prompts, so callers should not
// expect DeepSeek-style depth here.
//
// Malformed lines are skipped, not fatal: this is purely additive visibility
// on top of the decision already read from the -o file, so a parsing hiccup
// here must never surface as a call failure. Any item type other than
// "reasoning" or "agent_message" - e.g. a tool-call attempt the read-only
// sandbox would otherwise silently block - is logged at WARN so it stays
// visible, mirroring the existing non-empty-stderr WARN below it in Call().
func extractReasoningSummary(stdout []byte, log mcp.Logger) string {
	var parts []string
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event codexJSONLEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type != "item.completed" || event.Item == nil {
			continue
		}
		switch event.Item.Type {
		case "reasoning":
			if text := strings.TrimSpace(event.Item.Text); text != "" {
				parts = append(parts, text)
			}
		case "agent_message":
			// expected - the final answer, already read from -o.
		default:
			log.Warnf("🔧 [MCP] codex_cli: unexpected --json item type %q: %s", event.Item.Type, truncate([]byte(event.Item.Text), 512))
		}
	}
	return strings.Join(parts, "\n\n")
}

// ensureWorkspace creates the isolated workdir/runs directories if missing
// and (re)writes the embedded schema file if it's missing or was truncated
// to empty by something external - self-healing rather than a one-time
// deploy-time setup step, since the cost of checking is a stat() call next
// to a multi-second subprocess invocation.
func (c *CodexClient) ensureWorkspace() error {
	if err := os.MkdirAll(c.workDir, 0o700); err != nil {
		return fmt.Errorf("create workdir: %w", err)
	}
	if err := os.MkdirAll(c.runsDir, 0o700); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}
	info, statErr := os.Stat(c.schemaPath)
	if statErr != nil || info.Size() == 0 {
		if err := os.WriteFile(c.schemaPath, codexDecisionSchemaJSON, 0o600); err != nil {
			return fmt.Errorf("write schema file: %w", err)
		}
	}
	return nil
}

// combinePrompts flattens nofx's separate system/user prompts into the
// single positional prompt `codex exec` accepts, piped over stdin.
func combinePrompts(systemPrompt, userPrompt string) string {
	var b strings.Builder
	b.WriteString("SYSTEM INSTRUCTIONS (authoritative — follow exactly; do not attempt any file, shell, or code actions):\n")
	b.WriteString(systemPrompt)
	b.WriteString("\n\n---\n\nUSER REQUEST:\n")
	b.WriteString(userPrompt)
	return b.String()
}

// unwrapDecisions extracts the bare decisions array from the
// {"decisions": [...]} object codex_cli_schema.json requires as the root
// shape (OpenAI's structured-output API rejects an array root schema
// outright - verified live, not assumed). Returns the array's raw bytes
// unmodified rather than re-marshaling, so numeric fields aren't put through
// an extra encode/decode round-trip; kernel's extractDecisions bare-array
// fallback parses this the same way it would parse a text response that
// happened to contain nothing but a JSON array.
//
// Deliberately does not import nofx/kernel to unmarshal into kernel.Decision
// directly - mcp/provider has no dependency on kernel today, and adding one
// just to reuse a struct shape isn't worth it. This does mean
// codex_cli_schema.json's fields must be kept in sync by hand with
// kernel.Decision (kernel/engine.go, the "Decision" type) if that struct
// ever changes - there is no compiler-enforced link between the two.
func unwrapDecisions(raw []byte) (string, error) {
	var wrapper struct {
		Decisions json.RawMessage `json:"decisions"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return "", fmt.Errorf("failed to parse schema output as JSON: %w", err)
	}
	if len(wrapper.Decisions) == 0 {
		return "", errors.New("schema output has no 'decisions' key")
	}
	return string(wrapper.Decisions), nil
}

// resolveCodexBinPath finds the codex binary. CODEX_CLI_BIN_PATH takes
// priority over PATH lookup because the systemd unit running nofx does not
// necessarily have the interactive shell's PATH (confirmed: codex installs
// to ~/.local/bin, which is not on a typical systemd unit's default PATH).
func resolveCodexBinPath() (string, error) {
	if p := os.Getenv("CODEX_CLI_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("codex"); err == nil {
		return p, nil
	}
	return "", errors.New("codex binary not found: set CODEX_CLI_BIN_PATH to its absolute path, or add it to PATH")
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}
