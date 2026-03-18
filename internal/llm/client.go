package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/errors"
	"log"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

type Client struct {
	provider Provider
	fallback Provider
	limiter  *rate.Limiter
	db       *db.DB
}

func NewClient(provider Provider, fallback Provider, rpm int, database *db.DB) *Client {
	return &Client{
		provider: provider,
		fallback: fallback,
		limiter:  rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), 1),
		db:       database,
	}
}

func (c *Client) Complete(ctx context.Context, req CompletionRequest, task, runID string) (CompletionResponse, error) {
	var lastErr error
	maxRetries := 3
	backoff := 1 * time.Second

	// 1. Try Primary with retries
	for i := 0; i <= maxRetries; i++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return CompletionResponse{}, err
		}

		// Per-attempt timeout to avoid hanging
		attemptCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
		resp, err := c.provider.Complete(attemptCtx, req)
		cancel()

		if err == nil {
			c.logUsage(resp, task, runID)
			return resp, nil
		}
		lastErr = err

		// Check if it's an error that warrants a retry
		shouldRetry := false
		if _, ok := err.(*errors.RateLimitError); ok {
			shouldRetry = true
		} else if modelErr, ok := err.(*errors.ModelError); ok {
			// Retry on 5xx errors or 429
			if modelErr.StatusCode >= 500 || modelErr.StatusCode == 429 {
				shouldRetry = true
			}
		}

		if shouldRetry && i < maxRetries {
			log.Printf("Primary LLM %s failed (attempt %d/%d), retrying in %v: %v", c.provider.Name(), i+1, maxRetries+1, backoff, err)
			select {
			case <-ctx.Done():
				return CompletionResponse{}, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
				continue
			}
		}
		break
	}

	// 2. Try Fallback if primary failed
	if c.fallback != nil {
		log.Printf("Primary LLM provider failed, trying fallback %s: %v", c.fallback.Name(), lastErr)
		// Per-attempt timeout for fallback too
		attemptCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		resp, err := c.fallback.Complete(attemptCtx, req)
		cancel()

		if err == nil {
			c.logUsage(resp, task, runID)
			return resp, nil
		}
		lastErr = err
	}

	return CompletionResponse{}, lastErr
}

func extractJSON(content string) string {
	cleanJSON := strings.TrimSpace(content)
	
	// If it contains markdown fences, extract the content
	if strings.Contains(cleanJSON, "```json") {
		parts := strings.Split(cleanJSON, "```json")
		if len(parts) > 1 {
			cleanJSON = strings.Split(parts[1], "```")[0]
		}
	} else if strings.Contains(cleanJSON, "```") {
		parts := strings.Split(cleanJSON, "```")
		if len(parts) > 1 {
			cleanJSON = strings.Split(parts[1], "```")[0]
		}
	}

	// If there is still text around the JSON, find the first '{' and last '}'
	start := strings.Index(cleanJSON, "{")
	end := strings.LastIndex(cleanJSON, "}")
	if start != -1 && end != -1 && end > start {
		cleanJSON = cleanJSON[start : end+1]
	}

	return strings.TrimSpace(cleanJSON)
}

func (c *Client) CompleteJSON(ctx context.Context, req CompletionRequest, task, runID string, target interface{}) error {
	req.JSONMode = true

	// Inject JSON instructions into system prompt
	if !strings.Contains(strings.ToUpper(req.System), "JSON") {
		req.System = fmt.Sprintf("%s\n\nReturn ONLY a valid JSON object. Do not include any explanation.", req.System)
	}

	resp, err := c.Complete(ctx, req, task, runID)
	if err != nil {
		return err
	}

	cleanJSON := extractJSON(resp.Content)

	if err := json.Unmarshal([]byte(cleanJSON), target); err != nil {
		log.Printf("JSON unmarshal failed, retrying once with error feedback: %v", err)
		req.User = fmt.Sprintf("%s\n\nYour previous response was not valid JSON: %s\nError: %v\nPlease return ONLY the valid JSON object.", req.User, resp.Content, err)
		
		resp, err = c.Complete(ctx, req, task, runID)
		if err != nil {
			return err
		}
		
		cleanJSON = extractJSON(resp.Content)
		if err := json.Unmarshal([]byte(cleanJSON), target); err != nil {
			return errors.NewParseError(resp.Content, fmt.Sprintf("%T", target))
		}
	}

	return nil
}

func (c *Client) logUsage(resp CompletionResponse, task, runID string) {
	if c.db == nil {
		return
	}
	var err error
	if resp.EstimatedCost {
		err = c.db.InsertGeminiUsage(runID, task, resp.PromptTokens, resp.CompletionTokens)
	} else {
		err = c.db.InsertLLMUsage(runID, task, c.provider.Name(), resp.PromptTokens, resp.CompletionTokens, resp.CostUSD)
	}
	if err != nil {
		log.Printf("Failed to log LLM usage: %v", err)
	}
}
