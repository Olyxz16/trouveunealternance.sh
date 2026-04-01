package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jobhunter/internal/errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type OpenRouterProvider struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
	logger     *zap.Logger
}

type openRouterRequest struct {
	Model          string              `json:"model"`
	Messages       []openRouterMessage `json:"messages"`
	MaxTokens      int                 `json:"max_tokens,omitempty"`
	ResponseFormat *openRouterFormat   `json:"response_format,omitempty"`
}

type openRouterFormat struct {
	Type string `json:"type"`
}

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func NewOpenRouterProvider(apiKey, model string, logger *zap.Logger) *OpenRouterProvider {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OpenRouterProvider{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: "https://openrouter.ai/api/v1",
		HTTPClient: &http.Client{
			Timeout: 300 * time.Second,
		},
		logger: logger,
	}
}

func (p *OpenRouterProvider) Name() string {
	return p.Model
}

func (p *OpenRouterProvider) ProviderName() string {
	return "openrouter"
}

func (p *OpenRouterProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// Truncate user content to avoid 400 errors with very large inputs
	// Most models have a limit around 32k-128k tokens, but some free models are more restrictive.
	maxChars := 100000 // approx 25k-30k tokens
	userContent := req.User
	if len(userContent) > maxChars {
		userContent = userContent[:maxChars] + "\n\n[TRUNCATED]"
	}

	messages := []openRouterMessage{
		{Role: "system", Content: req.System},
		{Role: "user", Content: userContent},
	}

	payload := openRouterRequest{
		Model:    p.Model,
		Messages: messages,
	}

	// Only enable response_format if JSONMode is requested AND it's NOT a free model.
	// Free models on OpenRouter are often unstable with explicit response_format.
	if req.JSONMode && !strings.Contains(strings.ToLower(p.Model), ":free") {
		payload.ResponseFormat = &openRouterFormat{Type: "json_object"}
	}

	if req.MaxTokens > 0 {
		payload.MaxTokens = req.MaxTokens
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, err
	}

	p.logger.Debug("OpenRouter request", zap.String("model", p.Model), zap.Int("payload_len", len(jsonData)))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return CompletionResponse{}, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("HTTP-Referer", "https://github.com/jobhunter")
	httpReq.Header.Set("X-Title", "JobHunter")

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter, _ := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64)
			return CompletionResponse{}, errors.NewRateLimitError(retryAfter, p.Model)
		}

		// If 400 and model ID looks like a free variant, try stripping :free and retrying once
		if resp.StatusCode == 400 && strings.Contains(p.Model, ":free") {
			p.logger.Warn("OpenRouter rejected free model ID, retrying with standard ID", zap.String("model", p.Model))
			p.Model = strings.Replace(p.Model, ":free", "", 1)
			return p.Complete(ctx, req)
		}

		return CompletionResponse{}, fmt.Errorf("openrouter error %d: %s", resp.StatusCode, string(body))
	}

	var orResp openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&orResp); err != nil {
		return CompletionResponse{}, err
	}

	if len(orResp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("no choices returned from OpenRouter")
	}

	costStr := resp.Header.Get("X-OpenRouter-Cost")
	cost, _ := strconv.ParseFloat(costStr, 64)

	return CompletionResponse{
		Content:          orResp.Choices[0].Message.Content,
		PromptTokens:     orResp.Usage.PromptTokens,
		CompletionTokens: orResp.Usage.CompletionTokens,
		CostUSD:          cost,
		EstimatedCost:    false,
	}, nil
}
