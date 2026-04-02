package llm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// UnifiedRateLimiter provides centralized rate limiting with per-provider tracking
type UnifiedRateLimiter struct {
	limiter *rate.Limiter
	logger  *zap.Logger

	// Per-provider statistics (for monitoring only, not enforcement)
	mu         sync.RWMutex
	stats      map[string]*ProviderStats
	startTime  time.Time
	dailyCount int
	dailyLimit int
}

// ProviderStats tracks usage statistics for a provider
type ProviderStats struct {
	RequestCount  int
	SuccessCount  int
	FailureCount  int
	TotalTokens   int
	LastUsedAt    time.Time
	RateLimitHits int
}

// NewUnifiedRateLimiter creates a new centralized rate limiter
func NewUnifiedRateLimiter(requestsPerMinute, requestsPerDay int, logger *zap.Logger) *UnifiedRateLimiter {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Create token bucket limiter: refills at rate, allows burst of 1
	limiter := rate.NewLimiter(rate.Every(time.Minute/time.Duration(requestsPerMinute)), 1)

	return &UnifiedRateLimiter{
		limiter:    limiter,
		logger:     logger,
		stats:      make(map[string]*ProviderStats),
		startTime:  time.Now(),
		dailyLimit: requestsPerDay,
	}
}

// Wait blocks until a request can proceed according to rate limits
func (r *UnifiedRateLimiter) Wait(ctx context.Context) error {
	// Check daily limit
	r.mu.RLock()
	if r.dailyLimit > 0 && r.dailyCount >= r.dailyLimit {
		r.mu.RUnlock()
		r.logger.Warn("Daily rate limit reached",
			zap.Int("daily_count", r.dailyCount),
			zap.Int("daily_limit", r.dailyLimit))
		return context.DeadlineExceeded
	}
	r.mu.RUnlock()

	// Wait for token from unified limiter
	return r.limiter.Wait(ctx)
}

// RecordRequest records a request for a specific provider
func (r *UnifiedRateLimiter) RecordRequest(provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.stats[provider]; !exists {
		r.stats[provider] = &ProviderStats{}
	}

	r.stats[provider].RequestCount++
	r.stats[provider].LastUsedAt = time.Now()
	r.dailyCount++
}

// RecordSuccess records a successful request
func (r *UnifiedRateLimiter) RecordSuccess(provider string, tokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if stats, exists := r.stats[provider]; exists {
		stats.SuccessCount++
		stats.TotalTokens += tokens
	}
}

// RecordFailure records a failed request
func (r *UnifiedRateLimiter) RecordFailure(provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if stats, exists := r.stats[provider]; exists {
		stats.FailureCount++
	}
}

// RecordRateLimitHit records when a rate limit was hit
func (r *UnifiedRateLimiter) RecordRateLimitHit(provider string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if stats, exists := r.stats[provider]; exists {
		stats.RateLimitHits++
	}
}

// GetStats returns a copy of current statistics
func (r *UnifiedRateLimiter) GetStats() map[string]ProviderStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]ProviderStats)
	for provider, stats := range r.stats {
		result[provider] = *stats
	}
	return result
}

// GetSummary returns a formatted summary of rate limiter stats
func (r *UnifiedRateLimiter) GetSummary() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	uptime := time.Since(r.startTime)
	summary := fmt.Sprintf("Rate Limiter Stats (uptime: %s)\n", uptime.Round(time.Second))
	summary += fmt.Sprintf("Total requests: %d (daily limit: %d)\n", r.dailyCount, r.dailyLimit)
	summary += "\nPer-provider breakdown:\n"

	for provider, stats := range r.stats {
		successRate := 0.0
		if stats.RequestCount > 0 {
			successRate = float64(stats.SuccessCount) / float64(stats.RequestCount) * 100
		}
		summary += fmt.Sprintf("  %s: %d requests, %.1f%% success, %d tokens, %d rate limit hits\n",
			provider, stats.RequestCount, successRate, stats.TotalTokens, stats.RateLimitHits)
	}

	return summary
}

// ResetDaily should be called daily to reset the daily counter
func (r *UnifiedRateLimiter) ResetDaily() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.logger.Info("Resetting daily rate limit counter",
		zap.Int("previous_count", r.dailyCount),
		zap.Time("reset_time", time.Now()))

	r.dailyCount = 0
	r.startTime = time.Now()
}
