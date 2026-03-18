package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/errors"
	"net/http"
	"strconv"
	"time"
)

type OpenRouterProvider struct {
	APIKey   string
	Model    string
	BaseURL  string
	HTTPClient *http.Client
}

type openRouterRequest struct {
	Model          string              `json:"model"`
	Messages       []openRouterMessage `json:"messages"`
	MaxTokens      int                 `json:"max_tokens,omitempty"`
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

func NewOpenRouterProvider(apiKey, model string) *OpenRouterProvider {
	return &OpenRouterProvider{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: "https://openrouter.ai/api/v1",
		HTTPClient: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

func (p *OpenRouterProvider) Name() string {
	return "openrouter"
}

func (p *OpenRouterProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	messages := []openRouterMessage{
		{Role: "system", Content: req.System},
		{Role: "user", Content: req.User},
	}

	payload := openRouterRequest{
		Model:    p.Model,
		Messages: messages,
	}
	if req.MaxTokens > 0 {
		payload.MaxTokens = req.MaxTokens
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, err
	}

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
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter, _ := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64)
			return CompletionResponse{}, errors.NewRateLimitError(retryAfter, p.Model)
		}
		return CompletionResponse{}, errors.NewModelError(p.Model, resp.StatusCode)
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
