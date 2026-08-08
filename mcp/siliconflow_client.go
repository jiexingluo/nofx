package mcp

const (
	DefaultSiliconFlowBaseURL = "https://api.siliconflow.cn/v1"
	DefaultSiliconFlowModel   = "Qwen/Qwen3-8B"
)

type SiliconFlowClient struct {
	*Client
}

// NewSiliconFlowClient creates SiliconFlow client (backward compatible)
func NewSiliconFlowClient() AIClient {
	return NewSiliconFlowClientWithOptions()
}

// NewSiliconFlowClientWithOptions creates SiliconFlow client (supports options pattern)
//
// SiliconFlow hosts many open-source models (Qwen, DeepSeek, LLaMA, etc.)
// via an OpenAI-compatible API at https://api.siliconflow.cn/v1
//
// Usage examples:
//
//	// Basic usage
//	client := mcp.NewSiliconFlowClientWithOptions()
//
//	// Custom configuration
//	client := mcp.NewSiliconFlowClientWithOptions(
//	    mcp.WithAPIKey("sk-xxx"),
//	    mcp.WithLogger(customLogger),
//	    mcp.WithTimeout(60*time.Second),
//	)
func NewSiliconFlowClientWithOptions(opts ...ClientOption) AIClient {
	// 1. Create SiliconFlow preset options
	sfOpts := []ClientOption{
		WithProvider(ProviderSiliconFlow),
		WithModel(DefaultSiliconFlowModel),
		WithBaseURL(DefaultSiliconFlowBaseURL),
	}

	// 2. Merge user options (user options have higher priority)
	allOpts := append(sfOpts, opts...)

	// 3. Create base client
	baseClient := NewClient(allOpts...).(*Client)

	// 4. Create SiliconFlow client
	sfClient := &SiliconFlowClient{
		Client: baseClient,
	}

	// 5. Set hooks to point to SiliconFlowClient (implement dynamic dispatch)
	baseClient.Hooks = sfClient

	return sfClient
}

func (sfClient *SiliconFlowClient) SetAPIKey(apiKey string, customURL string, customModel string) {
	sfClient.APIKey = apiKey

	if len(apiKey) > 8 {
		sfClient.Log.Infof("🔧 [MCP] SiliconFlow API Key: %s...%s", apiKey[:4], apiKey[len(apiKey)-4:])
	}
	if customURL != "" {
		sfClient.BaseURL = customURL
		sfClient.Log.Infof("🔧 [MCP] SiliconFlow using custom BaseURL: %s", customURL)
	} else {
		sfClient.Log.Infof("🔧 [MCP] SiliconFlow using default BaseURL: %s", sfClient.BaseURL)
	}
	if customModel != "" {
		sfClient.Model = customModel
		sfClient.Log.Infof("🔧 [MCP] SiliconFlow using custom Model: %s", customModel)
	} else {
		sfClient.Log.Infof("🔧 [MCP] SiliconFlow using default Model: %s", sfClient.Model)
	}
}

func init() {
	RegisterProvider(ProviderSiliconFlow, NewSiliconFlowClientWithOptions)
}
