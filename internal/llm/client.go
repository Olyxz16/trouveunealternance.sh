package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/errors"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

type Client struct {
	provider Provider
	fallback Provider
	limiter  *rate.Limiter
	db       *db.DB
	logger   *zap.Logger
}

func NewClient(provider Provider, fallback Provider, rpm int, database *db.DB, logger *zap.Logger) *Client {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Client{
		provider: provider,
		fallback: fallback,
		limiter:  rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), 1),
		db:       database,
		logger:   logger,
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
			c.logger.Warn("Primary LLM failed, retrying",
				zap.String("provider", c.provider.Name()),
				zap.Int("attempt", i+1),
				zap.Duration("backoff", backoff),
				zap.Error(err))
			
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
		c.logger.Info("Primary LLM failed, trying fallback",
			zap.String("fallback", c.fallback.Name()),
			zap.Error(lastErr))
		
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

	// Find the first '{' or '['
	startCurly := strings.Index(cleanJSON, "{")
	startSquare := strings.Index(cleanJSON, "[")
	
	start := -1
	if startCurly != -1 && (startSquare == -1 || startCurly < startSquare) {
		start = startCurly
	} else {
		start = startSquare
	}

	// Find the last '}' or ']'
	endCurly := strings.LastIndex(cleanJSON, "}")
	endSquare := strings.LastIndex(cleanJSON, "]")
	
	end := -1
	if endCurly != -1 && (endSquare == -1 || endCurly > endSquare) {
		end = endCurly
	} else {
		end = endSquare
	}

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
		// Attempt fallback for array-to-struct if applicable
		if strings.HasPrefix(cleanJSON, "[") {
			c.logger.Debug("JSON is array, attempting wrapping", zap.String("target_type", fmt.Sprintf("%T", target)))
			if strings.Contains(fmt.Sprintf("%T", target), "PeoplePageData") {
				wrapped := fmt.Sprintf(`{"contacts": %s}`, cleanJSON)
				if err2 := json.Unmarshal([]byte(wrapped), target); err2 == nil {
					return nil
				}
			}
		}

		c.logger.Warn("JSON unmarshal failed, retrying with error feedback", zap.Error(err))
		
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

	usage := &db.TokenUsage{
		RunID:            runID,
		Task:             task,
		Model:            c.provider.Name(),
		Provider:         c.provider.ProviderName(),
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
		CostUSD:          resp.CostUSD,
		IsEstimated:      resp.EstimatedCost,
	}

	err := c.db.InsertTokenUsage(usage)
	if err != nil {
		c.logger.Error("Failed to log token usage", zap.Error(err))
	}
}
