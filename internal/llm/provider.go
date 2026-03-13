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
	Name() string
}
