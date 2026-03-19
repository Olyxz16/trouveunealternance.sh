package llm

import (
	"context"
)

type CompletionRequest struct {
	System    string
	User      string
	MaxTokens int
	JSONMode  bool
}

type CompletionResponse struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64
	EstimatedCost    bool // true when cost is estimated, not exact (Gemini CLI)
}

type Provider interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	Name() string         // Returns model name
	ProviderName() string // Returns provider name (e.g. 'openrouter', 'gemini_api')
}

// InitProviders creates the primary and fallback providers based on configuration.
func InitProviders(primaryName, fallbackName string, cfg interface {
	GetOpenRouterAPIKey() string
	GetOpenRouterModel() string
	GetGeminiAPIKey() string
	GetGeminiAPIModel() string
	GetGeminiCLIPath() string
}) (Provider, Provider) {
	create := func(name string) Provider {
		switch name {
		case "openrouter":
			return NewOpenRouterProvider(cfg.GetOpenRouterAPIKey(), cfg.GetOpenRouterModel())
		case "gemini_api":
			return NewGeminiAPIProvider(cfg.GetGeminiAPIKey(), cfg.GetGeminiAPIModel())
		case "gemini_cli":
			return NewGeminiCLIProvider(cfg.GetGeminiCLIPath())
		default:
			return nil
		}
	}

	return create(primaryName), create(fallbackName)
}
