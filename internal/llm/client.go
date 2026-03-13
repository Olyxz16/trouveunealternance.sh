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
	if err := c.limiter.Wait(ctx); err != nil {
		return CompletionResponse{}, err
	}

	var resp CompletionResponse
	var err error

	// Try primary provider
	resp, err = c.provider.Complete(ctx, req)
	if err != nil && c.fallback != nil {
		log.Printf("Primary LLM provider failed, trying fallback: %v", err)
		resp, err = c.fallback.Complete(ctx, req)
	}

	if err != nil {
		return CompletionResponse{}, err
	}

	c.logUsage(resp, task, runID)
	return resp, nil
}

func (c *Client) CompleteJSON(ctx context.Context, req CompletionRequest, task, runID string, target interface{}) error {
	req.JSONMode = true
	// We append schema info if needed or rely on the provider
	// For simplicity, we just try to unmarshal the result

	resp, err := c.Complete(ctx, req, task, runID)
	if err != nil {
		return err
	}

	cleanJSON := strings.TrimSpace(resp.Content)
	cleanJSON = strings.TrimPrefix(cleanJSON, "```json")
	cleanJSON = strings.TrimPrefix(cleanJSON, "```")
	cleanJSON = strings.TrimSuffix(cleanJSON, "```")
	cleanJSON = strings.TrimSpace(cleanJSON)

	if err := json.Unmarshal([]byte(cleanJSON), target); err != nil {
		// Retry once if unmarshal fails
		log.Printf("JSON unmarshal failed, retrying once: %v", err)
		req.User = fmt.Sprintf("%s\n\nThe previous response was not valid JSON: %v. Please fix it.", req.User, err)
		resp, err = c.Complete(ctx, req, task, runID)
		if err != nil {
			return err
		}
		cleanJSON = strings.TrimSpace(resp.Content)
		cleanJSON = strings.TrimPrefix(cleanJSON, "```json")
		cleanJSON = strings.TrimPrefix(cleanJSON, "```")
		cleanJSON = strings.TrimSuffix(cleanJSON, "```")
		cleanJSON = strings.TrimSpace(cleanJSON)
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
