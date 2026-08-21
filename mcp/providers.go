package mcp

// Provider name constants — kept in the mcp package so that client.go can
// reference them for default configuration without importing sub-packages.
// Provider sub-packages re-use these same values.
const (
	ProviderDeepSeek    = "deepseek"
	ProviderOpenAI      = "openai"
	ProviderClaude      = "claude"
	ProviderQwen        = "qwen"
	ProviderGemini      = "gemini"
	ProviderGrok        = "grok"
	ProviderKimi        = "kimi"
	ProviderMiniMax     = "minimax"
	ProviderSiliconFlow = "siliconflow"

	ProviderClaw402 = "claw402"

	// ProviderCodexCLI shells out to a local `codex` binary (OpenAI Codex
	// CLI) instead of making an HTTP call — see mcp/provider/codex_cli.go.
	ProviderCodexCLI = "codex_cli"

	// Default DeepSeek configuration (used as fallback in NewClient)
	DefaultDeepSeekBaseURL = "https://api.deepseek.com"
	DefaultDeepSeekModel   = "deepseek-chat"

	// Default Qwen configuration (used by WithQwenConfig convenience option)
	DefaultQwenBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	DefaultQwenModel   = "qwen3-max"

	// Default MiniMax configuration (used by WithMiniMaxConfig convenience option)
	DefaultMiniMaxBaseURL = "https://api.minimax.io/v1"
	DefaultMiniMaxModel   = "MiniMax-M2.7"
)

// noAPIKeyProviders holds providers authenticated outside of nofx entirely
// (e.g. a local CLI logged in via its own subscription flow), mirroring the
// frontend's AIProviderConfig.noApiKeyRequired (web/src/components/trader/
// model-constants.ts). Both must stay in sync by hand: the frontend flag
// controls whether the save form requires a key, this one controls whether
// launch preflight (api/launch_preflight.go) requires one.
var noAPIKeyProviders = map[string]bool{
	ProviderCodexCLI: true,
}

// ProviderNeedsAPIKey reports whether provider stores credentials in
// AIModel.APIKey. False only for providers like codex_cli that authenticate
// out-of-band (a local subscription login) and never have a key to store.
func ProviderNeedsAPIKey(provider string) bool {
	return !noAPIKeyProviders[provider]
}
