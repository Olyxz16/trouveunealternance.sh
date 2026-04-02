package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/errors"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

type Client struct {
	provider        Provider
	fallback        Provider
	limiter         *rate.Limiter       // Legacy: kept for backward compatibility
	rateLimiter     *UnifiedRateLimiter // New unified rate limiter
	db              *db.DB
	logger          *zap.Logger
	brokenProviders map[string]bool
	mu              sync.RWMutex
}

func NewClient(provider Provider, fallback Provider, rpm int, database *db.DB, logger *zap.Logger) *Client {
	return NewClientWithSharedLimiter(provider, fallback, rpm, database, logger, nil)
}

func NewClientWithSharedLimiter(provider Provider, fallback Provider, rpm int, database *db.DB, logger *zap.Logger, sharedLimiter *rate.Limiter) *Client {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Create or use shared rate limiter (for backward compatibility)
	limiter := sharedLimiter
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), 1)
	}

	// Create unified rate limiter with tracking (no daily limit for now, set to 0)
	rateLimiter := NewUnifiedRateLimiter(rpm, 0, logger)

	return &Client{
		provider:        provider,
		fallback:        fallback,
		limiter:         limiter,
		rateLimiter:     rateLimiter,
		db:              database,
		logger:          logger,
		brokenProviders: make(map[string]bool),
	}
}

func (c *Client) isBroken(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.brokenProviders[name]
}

func (c *Client) markBroken(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.brokenProviders[name] {
		c.logger.Warn("Circuit Breaker: blacklisting provider for this run", zap.String("provider", name))
		c.brokenProviders[name] = true
	}
}

// GetRateLimiterStats returns current rate limiter statistics
func (c *Client) GetRateLimiterStats() map[string]ProviderStats {
	return c.rateLimiter.GetStats()
}

// GetRateLimiterSummary returns a formatted summary of rate limiter stats
func (c *Client) GetRateLimiterSummary() string {
	return c.rateLimiter.GetSummary()
}

var freeFallbackModels = []string{
	"google/gemini-2.0-flash-exp:free",
	"meta-llama/llama-3.2-3b-instruct:free",
	"mistralai/mistral-7b-instruct-v0.3:free",
	"google/gemma-2-9b-it:free",
}

func (c *Client) Complete(ctx context.Context, req CompletionRequest, task, runID string) (CompletionResponse, error) {
	var lastErr error
	maxRetries := 3
	backoff := 2 * time.Second

	// 1. Try Primary
	if !c.isBroken(c.provider.Name()) {
		for i := 0; i <= maxRetries; i++ {
			// Wait for rate limit and record request
			if err := c.rateLimiter.Wait(ctx); err != nil {
				return CompletionResponse{}, err
			}
			c.rateLimiter.RecordRequest(c.provider.ProviderName())

			attemptCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
			resp, err := c.provider.Complete(attemptCtx, req)
			cancel()

			if err == nil {
				c.rateLimiter.RecordSuccess(c.provider.ProviderName(), resp.PromptTokens+resp.CompletionTokens)
				c.logUsage(resp, task, runID)
				return resp, nil
			}
			lastErr = err
			c.rateLimiter.RecordFailure(c.provider.ProviderName())

			shouldRetry := false
			isFatal := false

			if _, ok := err.(*errors.RateLimitError); ok {
				c.rateLimiter.RecordRateLimitHit(c.provider.ProviderName())
				shouldRetry = true
				backoff = 10 * time.Second // Aggressive cooldown for rate limits
			} else if modelErr, ok := err.(*errors.ModelError); ok {
				if modelErr.StatusCode >= 500 || modelErr.StatusCode == 429 {
					if modelErr.StatusCode == 429 {
						c.rateLimiter.RecordRateLimitHit(c.provider.ProviderName())
					}
					shouldRetry = true
				}
				if modelErr.StatusCode == 402 || modelErr.StatusCode == 400 || modelErr.StatusCode == 404 {
					isFatal = true
				}
			}

			if isFatal {
				c.markBroken(c.provider.Name())
				break
			}

			if shouldRetry && i < maxRetries {
				c.logger.Warn("Primary LLM hit retryable error, cooling down...",
					zap.String("provider", c.provider.Name()),
					zap.Duration("wait", backoff))
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
	}

	// 2. Try configured Fallback
	if c.fallback != nil && !c.isBroken(c.fallback.Name()) {
		c.logger.Info("Attempting configured fallback", zap.String("fallback", c.fallback.Name()))

		// Wait for rate limit and record request
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return CompletionResponse{}, err
		}
		c.rateLimiter.RecordRequest(c.fallback.ProviderName())

		attemptCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		resp, err := c.fallback.Complete(attemptCtx, req)
		cancel()

		if err == nil {
			c.rateLimiter.RecordSuccess(c.fallback.ProviderName(), resp.PromptTokens+resp.CompletionTokens)
			c.logUsage(resp, task, runID)
			return resp, nil
		}

		c.rateLimiter.RecordFailure(c.fallback.ProviderName())
		if modelErr, ok := err.(*errors.ModelError); ok {
			if modelErr.StatusCode == 429 {
				c.rateLimiter.RecordRateLimitHit(c.fallback.ProviderName())
			}
			if modelErr.StatusCode == 402 || modelErr.StatusCode == 400 || modelErr.StatusCode == 404 {
				c.markBroken(c.fallback.Name())
			}
		}
		lastErr = err
	}

	// 3. Try "Emergency" Free Fallback Chain
	if orProvider, ok := c.provider.(*OpenRouterProvider); ok {
		c.logger.Info("All primary/fallback options exhausted. Cycling emergency free models...")

		originalModel := orProvider.Model
		defer func() { orProvider.Model = originalModel }()

		for _, model := range freeFallbackModels {
			if model == originalModel || c.isBroken(model) {
				continue
			}

			c.logger.Debug("Emergency fallback trial", zap.String("model", model))

			// Wait for rate limit and record request
			if err := c.rateLimiter.Wait(ctx); err != nil {
				return CompletionResponse{}, err
			}
			c.rateLimiter.RecordRequest("openrouter_emergency")

			orProvider.Model = model
			attemptCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			resp, err := orProvider.Complete(attemptCtx, req)
			cancel()

			if err == nil {
				c.logger.Info("Emergency model succeeded!", zap.String("model", model))
				c.rateLimiter.RecordSuccess("openrouter_emergency", resp.PromptTokens+resp.CompletionTokens)
				c.logUsage(resp, task, runID)
				return resp, nil
			}

			c.rateLimiter.RecordFailure("openrouter_emergency")
			if modelErr, ok := err.(*errors.ModelError); ok {
				if modelErr.StatusCode == 429 {
					c.rateLimiter.RecordRateLimitHit("openrouter_emergency")
				}
				if modelErr.StatusCode == 402 || modelErr.StatusCode == 400 {
					c.markBroken(model)
				}
			}
		}
	}

	return CompletionResponse{}, lastErr
}

func extractJSON(content string) string {
	cleanJSON := strings.TrimSpace(content)

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

	startCurly := strings.Index(cleanJSON, "{")
	startSquare := strings.Index(cleanJSON, "[")

	start := -1
	if startCurly != -1 && (startSquare == -1 || startCurly < startSquare) {
		start = startCurly
	} else {
		start = startSquare
	}

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

	if !strings.Contains(strings.ToUpper(req.System), "JSON") {
		req.System = fmt.Sprintf("%s\n\nReturn ONLY a valid JSON object. Do not include any explanation.", req.System)
	}

	resp, err := c.Complete(ctx, req, task, runID)
	if err != nil {
		return err
	}

	cleanJSON := extractJSON(resp.Content)

	if err := json.Unmarshal([]byte(cleanJSON), target); err != nil {
		if strings.HasPrefix(cleanJSON, "[") {
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
