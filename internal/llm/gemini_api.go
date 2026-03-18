package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jobhunter/internal/errors"
	"log"
	"net/http"
	"time"
)

const geminiAPIBase = "https://generativelanguage.googleapis.com/v1beta/models"

type GeminiAPIProvider struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

func NewGeminiAPIProvider(apiKey, model string) *GeminiAPIProvider {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	return &GeminiAPIProvider{
		APIKey: apiKey,
		Model:  model,
		HTTPClient: &http.Client{
			Timeout: 300 * time.Second, // generous — search grounding takes time
		},
	}
}

func (p *GeminiAPIProvider) Name() string {
	return "gemini_api"
}

// --- request/response structs ---

type geminiRequest struct {
	Contents          []geminiContent        `json:"contents"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	Tools             []geminiTool           `json:"tools,omitempty"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiTool struct {
	GoogleSearch *struct{} `json:"google_search,omitempty"`
}

type geminiGenerationConfig struct {
	ResponseMIMEType string `json:"responseMimeType,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete implements the Provider interface. No search grounding.
func (p *GeminiAPIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	return p.complete(ctx, req, false)
}

// CompleteWithSearch enables Google Search grounding. The model will search
// the web as part of generating its response.
// NOTE: JSON mode is disabled when search is enabled — parse JSON from text.
func (p *GeminiAPIProvider) CompleteWithSearch(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	return p.complete(ctx, req, true)
}

func (p *GeminiAPIProvider) complete(ctx context.Context, req CompletionRequest, withSearch bool) (CompletionResponse, error) {
	log.Printf("Gemini API call (model=%s, withSearch=%v)", p.Model, withSearch)
	payload := geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: req.User}}},
		},
		GenerationConfig: geminiGenerationConfig{},
	}

	if req.System != "" {
		payload.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: req.System}},
		}
	}

	if req.JSONMode && !withSearch {
		payload.GenerationConfig.ResponseMIMEType = "application/json"
	}

	if withSearch {
		payload.Tools = []geminiTool{
			{GoogleSearch: &struct{}{}},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, err
	}

	url := fmt.Sprintf("%s/%s:generateContent?key=%s",
		geminiAPIBase, p.Model, p.APIKey)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url,
		bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, err
	}

	if resp.StatusCode == 429 {
		return CompletionResponse{}, errors.NewRateLimitError(60, p.Model)
	}
	if resp.StatusCode != 200 {
		return CompletionResponse{}, errors.NewModelError(p.Model, resp.StatusCode)
	}

	var gemResp geminiResponse
	if err := json.Unmarshal(raw, &gemResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to parse Gemini response: %w", err)
	}

	if gemResp.Error != nil {
		return CompletionResponse{}, fmt.Errorf("gemini API error %d: %s",
			gemResp.Error.Code, gemResp.Error.Message)
	}

	if len(gemResp.Candidates) == 0 ||
		len(gemResp.Candidates[0].Content.Parts) == 0 {
		return CompletionResponse{}, fmt.Errorf("empty response from Gemini API")
	}

	content := gemResp.Candidates[0].Content.Parts[0].Text

	return CompletionResponse{
		Content:          content,
		PromptTokens:     gemResp.UsageMetadata.PromptTokenCount,
		CompletionTokens: gemResp.UsageMetadata.CandidatesTokenCount,
		CostUSD:          0,
		EstimatedCost:    false,
	}, nil
}
