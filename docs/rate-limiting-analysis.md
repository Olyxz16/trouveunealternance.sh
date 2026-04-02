# JobHunter: Rate Limiting & Enrichment Analysis

**Date:** April 2, 2026  
**Status:** Implementation Plan  
**Priority:** Critical  
**Last Updated:** April 2, 2026 (Architecture Review)

---

## 🔍 Executive Summary

This document analyzes the current state of the JobHunter scraping and enrichment engine, identifies critical rate limiting issues with OpenRouter's free models, and provides a comprehensive implementation plan to resolve them.

### Architecture Philosophy

**Separation of Concerns:**
- **YAML Config** (`config.yaml`): Business logic, model strategies, rate limits, parallelism
- **ENV Variables** (`.env`): Secrets (API keys), optional overrides (browser paths, display settings)
- **Unified Rate Limiter**: Single global limiter with per-provider tracking for accurate monitoring
- **Dual Methods**: Keep both batch and single-item processing for A/B testing
- **Simplified Discovery**: Gemini API → Browser (skip DDG intermediary layer)

**Future Consideration:**
- Separate engine architecture (independent service with API) vs current monolithic CLI
- Current focus: Fix rate limiting, then consider architectural refactor

### Current State
- ✅ Well-architected system with clean separation of concerns
- ⚠️ **Critical Issue:** Severe rate limiting causing 60% emergency fallback usage
- ⚠️ **Issue:** Incorrect OpenRouter model IDs returning 404 errors
- ⚠️ **Issue:** Parallel processing (10 workers) exhausting 60 RPM limit
- ⚠️ **Issue:** Configuration mixed between code, env, and constants.yaml

### Expected Improvements After Implementation
| Metric | Before | After (Optimized) |
|--------|--------|-------------------|
| Successful enrichments | ~50% | ~95% |
| Emergency fallbacks | ~60% of calls | <2% |
| Time per company | 2-5 min | 2-4 min |
| Companies/hour | 6-10 | 20-30 |

---

## 📊 Current Implementation Assessment

### ✅ Strengths

1. **Well-Architected System**
   - Clean separation of concerns (collector, enricher, scraper, LLM layer)
   - Robust cascade fetcher: Cache → HTTP → Browser fallback
   - Sophisticated enrichment pipeline with multiple stages
   - Good error handling with circuit breaker pattern

2. **Smart LLM Integration**
   - Multi-provider support (OpenRouter, Gemini API)
   - Three-tier fallback strategy: Primary → Fallback → Emergency free models
   - Circuit breaker to blacklist broken providers during runs
   - Token usage tracking for cost monitoring

3. **Data Quality Features**
   - Hallucination filtering for LLM-generated contacts
   - Multi-source cross-referencing (LinkedIn, website, external search)
   - Quality scoring for scraped content
   - Comprehensive caching to reduce duplicate work

### ⚠️ Critical Issues

#### 1. Rate Limiting Problem (Primary Concern)

**Current Behavior (from log analysis):**
- **Primary model**: `qwen/qwen3.6-plus-preview:free` (345 calls, 2.5M prompt tokens)
- **Emergency fallbacks**: `minimax/minimax-m2.5:free` (209 calls), `gemma-2-9b-it:free`, `openchat-7b:free`
- **Pattern**: System exhausts primary → tries fallback → cycles through ALL emergency models
- **Gemini API**: Only 14 calls (severely rate-limited at 60s retry intervals)

**Root Causes:**

1. **Parallel Processing Overload**
   - Running 10 parallel enrichments with a shared 60 RPM limit
   - Result: Each enrichment gets ~6 requests/minute
   - Reality: Each enrichment needs 8-15 LLM calls

2. **High LLM Call Volume per Company**
   Each enrichment makes 8-15 LLM calls:
   - URL discovery (1-2 calls)
   - Company info extraction (1 call)
   - People extraction from LinkedIn (1-2 calls)
   - Website exploration links extraction (1 call)
   - Individual profile enrichment (3 calls max)
   - Contact ranking (1 call)
   - External search fallback (2-3 calls if needed)

3. **Free Model Instability**
   - Logs show `404` errors for `qwen3.6-plus-free` model
   - Model ID is incorrect or model was removed from OpenRouter

4. **No Intelligent Backoff**
   - Rate limiter is per-minute (60 RPM) but doesn't account for burst usage
   - No respect for provider-specific `Retry-After` headers

#### 2. Implementation Issues

**A. Incorrect OpenRouter Model ID**
```yaml
# In config (line 18 of internal/config/config.go):
OPENROUTER_MODEL=google/gemini-2.0-flash-lite:free

# But logs show it's trying:
qwen3.6-plus-free  # Returns 404
```

**B. Gemini API Underutilized**
- Gemini API configured but hitting rate limits immediately
- Gemini API with search grounding is powerful for discovery but rate-limited to ~15 QPM for free tier
- Only 14 successful calls vs 345+ OpenRouter calls

**C. Shared Rate Limiter Issues**
- Line 171 in `cmd/enrich.go`: Creates ONE client for all parallel workers
- With 10 parallel workers sharing 60 RPM → each worker gets ~6 req/min
- When one company needs 10+ LLM calls, it blocks others

**D. Excessive DDG Searches**
- Falling back to DuckDuckGo scraping when Gemini/LLM fails
- DDG returns `202 Accepted` (anti-bot), requiring browser fetch
- Browser not available (Chrome not found in PATH)

---

## 💡 Implementation Plan

### Phase 1: Fix Critical Rate Limiting Issues ⚡ (CRITICAL)

#### Task 1.0: Reorganize Configuration Architecture
**Priority:** CRITICAL | **Time:** 2-3 hours

**New Configuration Structure:**

**File: `config.yaml`** (replaces hardcoded values and some ENV vars)
```yaml
# LLM Configuration
llm:
  # Model strategies by task type
  models:
    discovery:
      primary: "google/gemini-2.0-flash-exp:free"
      fallback: "meta-llama/llama-3.2-3b-instruct:free"
      provider: "openrouter"  # or "gemini_api"
    
    extraction:
      primary: "google/gemini-2.0-flash-exp:free"
      fallback: "meta-llama/llama-3.2-3b-instruct:free"
      provider: "openrouter"
    
    ranking:
      primary: "google/gemini-2.0-flash-exp:free"
      fallback: "meta-llama/llama-3.2-3b-instruct:free"
      provider: "openrouter"
    
    enrichment:
      primary: "google/gemini-2.0-flash-exp:free"
      fallback: "meta-llama/llama-3.2-3b-instruct:free"
      provider: "openrouter"
  
  # Emergency fallback chain (tried if both primary+fallback fail)
  emergency_fallbacks:
    - "mistralai/mistral-7b-instruct-v0.3:free"
    - "google/gemma-2-9b-it:free"
  
  # Rate limiting (global, unified limiter)
  rate_limits:
    requests_per_minute: 50
    requests_per_day: 50000
    burst_size: 3
    
    # Per-provider tracking (for monitoring only)
    provider_limits:
      openrouter:
        requests_per_minute: 50
      gemini_api:
        requests_per_minute: 12
    
    # Header-based rate limit detection
    respect_retry_after: true
    max_backoff_seconds: 120

# Enrichment Pipeline
enrichment:
  parallelism: 3  # Number of concurrent companies
  batch_size: 10  # Default batch size
  
  # Processing methods (keep both for A/B testing)
  enable_batch_ranking: true
  enable_single_ranking: true  # Keep for comparison
  
  enable_batch_enrichment: true
  enable_single_enrichment: true  # Keep for comparison
  
  # Discovery strategy (simplified)
  discovery:
    strategy: "gemini_then_browser"  # Skip DDG intermediary
    use_gemini_search_grounding: true
    use_browser_fallback: true
    skip_ddg_search: true  # Disable DDG intermediate step
    
    # Only use browser for high-value targets
    browser_fallback_min_score: 7
  
  # Conditional enrichment
  skip_low_score_enrichment: true
  low_score_threshold: 6
  max_profiles_to_enrich:
    high_priority: 3  # score >= 8
    medium_priority: 1  # 6 <= score < 8
    low_priority: 0  # score < 6

# Caching
cache:
  llm_responses:
    enabled: true
    ttl_hours:
      discovery: 24
      extraction: 168  # 7 days
      ranking: 12
  
  scrape:
    enabled: true
    # Already implemented

# Quality thresholds (from constants.yaml)
quality_thresholds:
  http_min: 0.7
  browser_min: 0.3
  discovery_min: 0.2
  enrich_min: 0.5

# Monitoring
monitoring:
  enable_rate_limit_alerts: true
  alert_threshold_per_hour: 10
  log_level: "info"  # debug, info, warn, error
```

**File: `.env`** (secrets and optional overrides only)
```bash
# API Keys (REQUIRED)
OPENROUTER_API_KEY=sk-or-...
GEMINI_API_KEY=...

# Optional Browser Configuration
BROWSER_COOKIES_PATH=data/browser_session.json
BROWSER_DISPLAY=
BROWSER_HEADLESS=true
BROWSER_BINARY_PATH=

# Optional Database Path Override
DB_PATH=data/jobs.db

# Optional Config Path Override
CONFIG_PATH=config.yaml
```

**Files to create/modify:**
- **NEW**: `config.yaml` (root directory)
- **MODIFY**: `internal/config/config.go` - Load YAML, merge with ENV
- **MODIFY**: Move constants from `internal/config/constants.yaml` into `config.yaml`
- **MODIFY**: `cmd/root.go` - Add `--config` flag

**Benefits:**
- ✅ Model strategy per task type (discovery can use Gemini, extraction can use cheaper models)
- ✅ Clear separation: Business logic in YAML, secrets in ENV
- ✅ Easy to version control and deploy different configs
- ✅ Can A/B test different strategies without code changes
- ✅ Unified rate limiting with per-provider monitoring

#### Task 1.1: Update OpenRouter Model Configuration
**Priority:** CRITICAL | **Time:** 15 minutes  
**Dependencies:** Task 1.0

**Changes:**
- Update `config.yaml` with valid model IDs
- Remove broken `qwen3.6-plus-free` from all configurations
- Add validation to check model availability on startup

**Valid OpenRouter Free Models (April 2026):**
- `google/gemini-2.0-flash-exp:free` (recommended)
- `meta-llama/llama-3.2-3b-instruct:free`
- `mistralai/mistral-7b-instruct-v0.3:free`
- `google/gemma-2-9b-it:free`

#### Task 1.2: Implement Unified Rate Limiter with Provider Tracking
**Priority:** HIGH | **Time:** 2-3 hours  
**Dependencies:** Task 1.0

**Philosophy:** Single global rate limiter for accurate control, with per-provider monitoring for observability.

**New file:** `internal/llm/rate_limiter.go`
```go
// UnifiedRateLimiter manages global rate limits with per-provider tracking
type UnifiedRateLimiter struct {
    // Single global limiter (source of truth)
    globalLimiter *rate.Limiter
    
    // Per-provider stats (monitoring only)
    providerStats map[string]*ProviderStats
    
    // Configuration
    config RateLimitConfig
    
    mu     sync.RWMutex
    logger *zap.Logger
}

type ProviderStats struct {
    RequestCount    int64
    LastRequestTime time.Time
    RateLimitHits   int
    QueuedRequests  int
}

type RateLimitConfig struct {
    RequestsPerMinute int
    RequestsPerDay    int
    BurstSize         int
    RespectRetryAfter bool
    MaxBackoffSeconds int
    
    // Provider limits (for monitoring/alerting only)
    ProviderLimits map[string]int
}

func (rl *UnifiedRateLimiter) Wait(ctx context.Context, provider string) error {
    // Single global wait (actual enforcement)
    err := rl.globalLimiter.Wait(ctx)
    
    // Track per-provider stats (monitoring)
    rl.trackRequest(provider)
    
    return err
}

func (rl *UnifiedRateLimiter) GetProviderStats(provider string) ProviderStats
func (rl *UnifiedRateLimiter) GetGlobalStats() GlobalStats
func (rl *UnifiedRateLimiter) CheckForAlerts() []Alert
```

**Files to modify:**
- `internal/llm/client.go`: Use unified limiter with provider name passed for tracking
- `internal/llm/provider.go`: Ensure all providers implement `ProviderName()`

**Benefits:**
- ✅ Single source of truth for rate limiting (accurate control)
- ✅ Per-provider tracking for monitoring and debugging
- ✅ Alerts when individual provider approaching their known limits
- ✅ Simpler logic than multiple independent limiters

#### Task 1.3: Improve Rate Limiter Algorithm
**Priority:** HIGH | **Time:** 1 hour

**File:** `internal/llm/client.go` (line 37)

**Current:**
```go
limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), 1)
```

**New (smoother distribution):**
```go
// Convert RPM to requests per second for smoother distribution
limiter = rate.NewLimiter(rate.Limit(rpm/60.0), burstSize)
```

**Benefits:**
- Allows small bursts (3-5 requests) for initial pipeline stages
- Maintains average RPM limit over time
- Reduces blocking on individual requests

#### Task 1.4: Add Intelligent Backoff for Rate Limits
**Priority:** HIGH | **Time:** 1.5 hours

**File:** `internal/llm/client.go` (lines 96-106)

**Changes:**
- Respect `Retry-After` header from provider responses
- Exponential backoff: Start at provider-suggested wait time, max 2 minutes
- Add per-provider cooldown tracking
- Log rate limit events with provider name and retry duration

#### Task 1.5: Move Parallelism to Config
**Priority:** CRITICAL | **Time:** 10 minutes  
**Dependencies:** Task 1.0

**Changes:**

**In `config.yaml`:**
```yaml
enrichment:
  parallelism: 3  # Default value
```

**In `cmd/enrich.go`:**
```go
// Read default from config, allow CLI override
defaultParallel := cfg.Enrichment.Parallelism
enrichCmd.Flags().IntVarP(&parallel, "parallel", "p", defaultParallel, "Number of companies to enrich in parallel")

// Validation
if parallel > 5 && usingFreeModels(cfg) {
    logger.Warn("High parallelism with free models may cause rate limiting",
        zap.Int("parallel", parallel),
        zap.Int("recommended_max", 5))
}
```

**Benefits:**
- ✅ Centralized configuration in YAML
- ✅ Easy to adjust for different environments (dev=1, prod=3)
- ✅ CLI flag still available for one-off overrides

---

### Phase 2: Reduce LLM Call Volume 🔄 (HIGH)

#### Task 2.1: Implement LLM Response Cache
**Priority:** HIGH | **Time:** 3-4 hours

**New file:** `internal/db/llm_cache.go`

**Database Migration:**
```sql
CREATE TABLE IF NOT EXISTS llm_response_cache (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    task TEXT NOT NULL,
    prompt_hash TEXT NOT NULL,
    response_json TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);
CREATE INDEX idx_llm_cache_lookup ON llm_response_cache(prompt_hash, task, expires_at);
CREATE INDEX idx_llm_cache_expires ON llm_response_cache(expires_at);
```

**Methods:**
```go
func (db *DB) GetCachedResponse(promptHash, task string) (*CachedResponse, error)
func (db *DB) SetCachedResponse(cache *CachedResponse) error
func (db *DB) PruneExpiredCache() error
```

**TTL Strategy:**
- Discovery tasks: 24 hours (company URLs change rarely)
- Extraction tasks: 7 days (company info relatively stable)
- Ranking tasks: 12 hours (preferences may change)

**File to modify:** `internal/llm/client.go`
- Add cache check before `Complete()` call
- Hash prompt using SHA256 for cache key
- Store successful responses in cache

#### Task 2.2: Add Batch Contact Ranking (Keep Single Method)
**Priority:** MEDIUM | **Time:** 2 hours

**File:** `internal/enricher/classifier.go`

**Approach:** Add batch method alongside existing single method for A/B testing.

**New Method:**
```go
func (c *Classifier) RankContactsBatch(ctx context.Context, contacts []IndividualContact, companyType, runID string) (*IndividualContact, error) {
    // Single LLM call with all contacts
    // Structured output with ranking for each
    // Return best contact
}
```

**Keep Existing:**
```go
func (c *Classifier) RankContacts(ctx context.Context, contacts []IndividualContact, companyType, runID string) (*IndividualContact, error) {
    // Original single/small-group ranking
}
```

**In `config.yaml`:**
```yaml
enrichment:
  enable_batch_ranking: true
  enable_single_ranking: true  # Keep for comparison
```

**In enrichment logic:**
```go
var best *IndividualContact
if cfg.Enrichment.EnableBatchRanking {
    best, _ = e.classifier.RankContactsBatch(ctx, contacts, info.CompanyType, runID)
} else {
    best, _ = e.classifier.RankContacts(ctx, contacts, info.CompanyType, runID)
}
```

**Benefits:**
- ✅ Can compare batch vs single performance
- ✅ Easy to switch between methods via config
- ✅ Gradual migration (test batch on subset first)

#### Task 2.3: Add Batch Profile Enrichment (Keep Single Method)
**Priority:** MEDIUM | **Time:** 2.5 hours

**File:** `internal/enricher/classifier.go`

**Approach:** Add batch method alongside existing single method for A/B testing.

**New Method:**
```go
func (c *Classifier) EnrichProfilesBatch(ctx context.Context, fetcher *scraper.CascadeFetcher, contacts []IndividualContact, runID string) ([]EnrichedProfile, error) {
    // Fetch all LinkedIn profiles
    // Single LLM call with structured output for all profiles
    // Return array of enriched profiles
}
```

**Keep Existing:**
```go
func (c *Classifier) EnrichIndividualProfile(ctx context.Context, fetcher *scraper.CascadeFetcher, contact IndividualContact, runID string) (EnrichedProfile, error) {
    // Original single-profile enrichment
}
```

**In enrichment logic:**
```go
if cfg.Enrichment.EnableBatchEnrichment {
    enriched = e.classifier.EnrichProfilesBatch(ctx, e.fetcher, realCandidates[:maxProfiles], runID)
} else {
    // Original loop with single enrichment
    for i, candidate := range realCandidates[:maxProfiles] {
        profile, _ := e.classifier.EnrichIndividualProfile(ctx, e.fetcher, candidate, runID)
        enriched = append(enriched, profile)
    }
}
```

**Benefits:**
- ✅ Can measure actual performance gain of batching
- ✅ Fallback to single method if batch has issues
- ✅ Collect data to inform future decisions

#### Task 2.4: Simplify Discovery Cascade (Remove DDG Intermediary)
**Priority:** MEDIUM | **Time:** 1.5 hours

**File:** `internal/enricher/discover.go` (lines 77-120)

**Current Cascade:** LLM → Gemini → DDG → External Search (4 levels)

**New Simplified Logic (per user feedback):**
```go
func (d *URLDiscoverer) DiscoverURLs(ctx context.Context, comp db.Company) (string, string, error) {
    // 1. Try LLM direct knowledge (fast, free, no rate limit concerns)
    website, linkedin, err := d.discoverWithLLM(ctx, comp)
    if err == nil && (website != "" || linkedin != "") {
        return website, linkedin, nil
    }
    
    // 2. Try Gemini with search grounding (accurate, rate-limited but preferred)
    if d.geminiAPI != nil && cfg.Discovery.UseGeminiSearchGrounding {
        website, linkedin, err := d.discoverWithGemini(ctx, comp)
        if err == nil && (website != "" || linkedin != "") {
            return website, linkedin, nil
        }
    }
    
    // 3. Skip DDG entirely - go straight to browser for high-value companies
    // Browser is more reliable than DDG intermediary
    if cfg.Discovery.UseBrowserFallback && comp.RelevanceScore >= cfg.Discovery.BrowserFallbackMinScore {
        return d.discoverWithBrowser(ctx, comp)
    }
    
    return "", "", errors.New("discovery exhausted for company")
}
```

**New Method (Browser-based discovery):**
```go
func (d *URLDiscoverer) discoverWithBrowser(ctx context.Context, comp db.Company) (string, string, error) {
    // Use browser to navigate and find company page directly
    // More reliable than DDG scraping, but slower
    // Only for high-value targets (score >= 7)
    
    // Try direct LinkedIn search
    // Try direct Google/Bing search with browser
    // Parse results and extract URLs
}
```

**Benefits:**
- ✅ Removes unreliable DDG intermediary (202 errors, anti-bot)
- ✅ Simpler cascade: LLM → Gemini → Browser
- ✅ Browser method more reliable for high-value companies
- ✅ Optimize browser method separately (future work)

#### Task 2.5: Conditional Enrichment
**Priority:** LOW | **Time:** 1 hour

**File:** `internal/enricher/enrich.go` (lines 382-413)

**Changes:**
```go
// Skip individual profile enrichment for low-priority companies
if comp.RelevanceScore < 6 {
    logger.Debug("Skipping profile enrichment for low-score company", 
        zap.String("company", comp.Name), 
        zap.Int("score", comp.RelevanceScore))
    maxProfiles = 0
} else if comp.RelevanceScore < 8 {
    maxProfiles = 1  // Only enrich top contact
} else {
    maxProfiles = 3  // Full enrichment for high-value companies
}
```

**Add `--thorough` flag:**
```go
enrichCmd.Flags().BoolVar(&thorough, "thorough", false, "Enable full enrichment for all companies")
```

**Benefit:** Save 2-6 LLM calls per low-priority company

---

### Phase 3: Advanced Rate Limiting Features 🚦 (MEDIUM)

#### Task 3.1: Global Request Queue with Priority
**Priority:** MEDIUM | **Time:** 4-5 hours

**New file:** `internal/llm/request_queue.go`

```go
type Priority int

const (
    PriorityHigh Priority = iota   // Discovery tasks (blocking pipeline start)
    PriorityMedium                 // Extraction tasks (company info, people)
    PriorityLow                    // Enrichment tasks (profiles, ranking)
)

type QueuedRequest struct {
    Request  CompletionRequest
    Priority Priority
    Provider string
    ResultCh chan<- CompletionResponse
    ErrorCh  chan<- error
}

type RequestQueue struct {
    queues   map[Priority]*priorityQueue
    limiters *ProviderRateLimiter
    workers  int
    logger   *zap.Logger
}

func (q *RequestQueue) Submit(req QueuedRequest) error
func (q *RequestQueue) Start(ctx context.Context)
func (q *RequestQueue) Stop()
func (q *RequestQueue) GetStats() QueueStats
```

**File to modify:** `internal/llm/client.go`
- Integrate queue: All `Complete()` calls go through queue
- Add `CompleteWithPriority(ctx, req, priority)` method

**Benefits:**
- High-priority requests (discovery) never wait behind low-priority (enrichment)
- Better utilization of rate limits
- Prevents pipeline stalls

#### Task 3.2: Dynamic Rate Adjustment
**Priority:** LOW | **Time:** 2-3 hours

**File:** `internal/llm/rate_limiter.go`

**Logic:**
```go
// Monitor success/failure rates per provider
func (rl *ProviderRateLimiter) AdjustLimits(ctx context.Context) {
    // If >20% rate limit errors in last 5min: Reduce limit by 20%
    if errorRate > 0.20 {
        newLimit := currentLimit * 0.8
        rl.SetLimit(provider, newLimit)
        rl.logger.Warn("Reducing rate limit due to errors", 
            zap.String("provider", provider),
            zap.Float64("new_limit", newLimit))
    }
    
    // If 0% errors for 10min: Increase limit by 10% (up to config max)
    if errorRate == 0 && duration > 10*time.Minute {
        newLimit := min(currentLimit*1.1, configMax)
        rl.SetLimit(provider, newLimit)
        rl.logger.Info("Increasing rate limit after stable period",
            zap.String("provider", provider),
            zap.Float64("new_limit", newLimit))
    }
}
```

**Benefits:**
- Automatically finds optimal rate limit for each provider
- Adapts to provider capacity changes
- Helps diagnose configuration issues

#### Task 3.3: Request Deduplication
**Priority:** LOW | **Time:** 1.5 hours

**File:** `internal/llm/client.go`

**Implementation:**
```go
type inFlightTracker struct {
    requests map[string]*sync.WaitGroup
    results  map[string]*cachedResult
    mu       sync.RWMutex
}

func (c *Client) Complete(ctx context.Context, req CompletionRequest, task, runID string) (CompletionResponse, error) {
    // Calculate prompt hash
    hash := hashPrompt(req)
    
    // Check if same request is already in-flight
    if wg, exists := c.inFlight.Get(hash); exists {
        // Wait for existing request to complete
        wg.Wait()
        // Return cached result
        return c.inFlight.GetResult(hash)
    }
    
    // Mark as in-flight
    wg := c.inFlight.Track(hash)
    defer c.inFlight.Complete(hash)
    
    // Execute request
    resp, err := c.provider.Complete(ctx, req)
    c.inFlight.CacheResult(hash, resp, err)
    return resp, err
}
```

**Benefits:**
- Prevents duplicate LLM calls when multiple workers discover same information
- Saves costs and rate limit capacity
- Especially useful for common discovery tasks

---

### Phase 4: Monitoring & Observability 📊 (LOW)

#### Task 4.1: Enhanced Rate Limit Logging
**Priority:** MEDIUM | **Time:** 1 hour

**Files to modify:**
- `internal/llm/client.go`: Add detailed logging for rate limit events
- `internal/llm/openrouter.go`: Parse and log `X-RateLimit-*` headers
- `internal/llm/gemini_api.go`: Log quota usage if available

**Log Format:**
```
2026-04-02T15:30:45.123 RATE_LIMIT provider=openrouter model=gemini-2.0-flash-exp retry_after=45s attempts=2/3 queue_depth=5
2026-04-02T15:31:30.456 RATE_OK provider=openrouter model=gemini-2.0-flash-exp latency=1.2s tokens_used=1250
```

**Add Structured Logging:**
```go
logger.Warn("Rate limit hit",
    zap.String("provider", provider),
    zap.String("model", model),
    zap.Duration("retry_after", retryAfter),
    zap.Int("attempt", attempt),
    zap.Int("max_attempts", maxAttempts),
    zap.Int("queue_depth", queueDepth))
```

#### Task 4.2: Real-time TUI Rate Monitor
**Priority:** LOW | **Time:** 2-3 hours

**File:** `internal/tui/stats_view.go`

**New Panel: "Rate Limits"**
```
╭─ Rate Limits ────────────────────────────────╮
│ Provider      │ Usage   │ Limit  │ Queue    │
│───────────────┼─────────┼────────┼──────────│
│ Gemini API    │ 8/12    │ 12 QPM │ 2 req    │
│ OpenRouter    │ 42/50   │ 50 QPM │ 0 req    │
│ DDG Browser   │ 3/10    │ 10 QPM │ 1 req    │
│───────────────┴─────────┴────────┴──────────│
│ Last Rate Limit: OpenRouter (23s ago)       │
│ Status: ✓ Healthy                           │
╰──────────────────────────────────────────────╯
```

**Update Model:**
```go
type RateLimitStats struct {
    Provider         string
    CurrentQPM       int
    LimitQPM         int
    QueueDepth       int
    LastRateLimitErr time.Time
    Status           string // "healthy", "throttled", "exhausted"
}

func (m PipelineModel) rateLimitView() string {
    // Render rate limit panel
}
```

#### Task 4.3: Token Usage Analytics
**Priority:** LOW | **Time:** 2 hours

**New file:** `cmd/usage_report.go`

**Command:**
```bash
# Show usage for last run
jobhunter usage-report

# Show usage for specific run
jobhunter usage-report --run-id=abc-123

# Group by task
jobhunter usage-report --by-task

# Show date range
jobhunter usage-report --from=2026-04-01 --to=2026-04-02
```

**Output:**
```
Token Usage Report (Last 24 hours)
═══════════════════════════════════════════════════

Provider Breakdown:
  OpenRouter (qwen3.6-plus): 2,510,560 prompt + 474,092 completion = 2,984,652 total
  Gemini API:                    6,807 prompt +   7,271 completion =    14,078 total
  ─────────────────────────────────────────────────────────────────────────────
  Total:                     2,517,367 prompt + 481,363 completion = 2,998,730 total
  Estimated Cost: $0.00 (free tier)

Task Breakdown:
  discovery_gemini:              500,234 tokens (17%)
  extract_company_info:          892,451 tokens (30%)
  extract_people:              1,205,678 tokens (40%)
  rank_contacts:                 234,567 tokens (8%)
  Other:                         165,800 tokens (5%)

Most Expensive Operations:
  1. extract_people            - 1,205,678 tokens - 345 calls - avg 3,495 tokens/call
  2. extract_company_info      -   892,451 tokens - 287 calls - avg 3,109 tokens/call
  3. discovery_gemini          -   500,234 tokens - 198 calls - avg 2,526 tokens/call

Recommendations:
  ✓ Consider caching extract_people results (40% of usage)
  ✓ Batch ranking operations to reduce overhead
  → Current efficiency: 65% (target: 80%+)
```

**File to modify:** `internal/db/models.go`
```go
func (db *DB) GetTokenUsageStats(from, to time.Time) (*UsageStats, error)
func (db *DB) GetTokenUsageByTask(from, to time.Time) ([]TaskUsage, error)
func (db *DB) GetTokenUsageByProvider(from, to time.Time) ([]ProviderUsage, error)
```

#### Task 4.4: Enrichment Success Metrics
**Priority:** LOW | **Time:** 2-3 hours

**Database Migration:**
```sql
ALTER TABLE companies ADD COLUMN enrichment_attempts INTEGER DEFAULT 0;
ALTER TABLE companies ADD COLUMN llm_calls_used INTEGER DEFAULT 0;
ALTER TABLE companies ADD COLUMN discovery_method TEXT;
ALTER TABLE companies ADD COLUMN enrichment_duration_seconds INTEGER;
ALTER TABLE companies ADD COLUMN last_enrichment_at DATETIME;
```

**File to modify:** `internal/enricher/enrich.go`
```go
func (e *Enricher) EnrichCompany(ctx context.Context, compID uint, runID string) error {
    startTime := time.Now()
    llmCallsUsed := 0
    
    // Track LLM calls
    enrichCtx := context.WithValue(ctx, "llm_counter", &llmCallsUsed)
    
    // ... existing enrichment logic ...
    
    // Update metrics
    duration := time.Since(startTime).Seconds()
    e.db.UpdateCompany(compID, map[string]interface{}{
        "enrichment_attempts": gorm.Expr("enrichment_attempts + 1"),
        "llm_calls_used": llmCallsUsed,
        "discovery_method": discoveryMethod,
        "enrichment_duration_seconds": int(duration),
        "last_enrichment_at": time.Now(),
    })
}
```

**Enhance `cmd/stats.go`:**
```bash
jobhunter stats --enrichment
```

**Output:**
```
Enrichment Pipeline Efficiency
═══════════════════════════════════════════════════

Success Rate: 95.2% (199/209 companies)
  ✓ TO_CONTACT:         189 (90.4%)
  ⚠ NO_CONTACT_FOUND:    10 (4.8%)
  ✗ FAILED:              10 (4.8%)

Discovery Method Breakdown:
  LLM Direct:    87 companies (43.7%) - avg 4.2 LLM calls
  Gemini Search: 56 companies (28.1%) - avg 6.8 LLM calls
  DDG Fallback:  46 companies (23.1%) - avg 12.3 LLM calls
  Failed:        10 companies (5.0%)   - avg 15.2 LLM calls

Performance Metrics:
  Avg LLM calls per company: 7.8 (target: <10)
  Avg duration per company: 3.2 minutes
  Cache hit rate: 45%
  
Bottlenecks:
  ⚠ DDG fallback companies take 2.8x longer than LLM direct
  ⚠ Failed companies attempted avg 15.2 calls (wasted resources)
  → Recommendation: Improve LLM direct success rate from 43.7% to 70%+
```

#### Task 4.5: Alert System for Rate Limit Issues
**Priority:** LOW | **Time:** 1.5 hours

**File:** `internal/llm/rate_limiter.go`

```go
type AlertLevel int

const (
    AlertNone AlertLevel = iota
    AlertWarning
    AlertCritical
)

type RateLimitAlert struct {
    Level       AlertLevel
    Provider    string
    Message     string
    Suggestion  string
    Timestamp   time.Time
}

func (rl *ProviderRateLimiter) CheckForAlerts(ctx context.Context) []RateLimitAlert {
    var alerts []RateLimitAlert
    
    for provider, stats := range rl.GetAllStats() {
        // Critical: Rate limit hit frequency > 10/hour
        if stats.RateLimitHitsLastHour > 10 {
            alerts = append(alerts, RateLimitAlert{
                Level:    AlertCritical,
                Provider: provider,
                Message:  fmt.Sprintf("%s hit rate limit %d times in last hour", provider, stats.RateLimitHitsLastHour),
                Suggestion: "Reduce --parallel setting or switch to paid tier",
            })
        }
        
        // Warning: Queue building up
        if stats.QueueDepth > 20 {
            alerts = append(alerts, RateLimitAlert{
                Level:    AlertWarning,
                Provider: provider,
                Message:  fmt.Sprintf("%s queue depth: %d", provider, stats.QueueDepth),
                Suggestion: "Pipeline may be stalled, consider reducing batch size",
            })
        }
        
        // Warning: Usage approaching limit
        if stats.CurrentQPM > stats.LimitQPM*0.9 {
            alerts = append(alerts, RateLimitAlert{
                Level:    AlertWarning,
                Provider: provider,
                Message:  fmt.Sprintf("%s usage at %.0f%% of limit", provider, float64(stats.CurrentQPM)/float64(stats.LimitQPM)*100),
                Suggestion: "Consider implementing request batching",
            })
        }
    }
    
    return alerts
}
```

**Integration with TUI:**
```go
// Show critical alerts as banner at top of TUI
╭─ ⚠️  ALERT ──────────────────────────────────────╮
│ OpenRouter hit rate limit 15 times in last hour │
│ → Reduce --parallel setting or switch to paid   │
╰──────────────────────────────────────────────────╯
```

---

## 📋 Implementation Order & Timeline

### Week 1: Critical Fixes (Days 1-3)

**Day 1 - Quick Wins:**
- ✅ Task 1.1: Update OpenRouter model IDs (15 min)
- ✅ Task 1.5: Reduce default parallelism (5 min)
- ✅ Test with single company enrichment

**Day 2-3 - Core Rate Limiting:**
- ✅ Task 1.2: Implement per-provider rate limiters (2-3 hours)
- ✅ Task 1.3: Improve rate limiter algorithm (1 hour)
- ✅ Task 1.4: Add intelligent backoff (1.5 hours)
- ✅ Test with batch of 10 companies

**Expected Results:** 80-90% success rate, <10% emergency fallbacks

### Week 2: Optimization (Days 4-7)

**Day 4-5 - Caching:**
- ✅ Task 2.1: Implement LLM response cache (3-4 hours)
- ✅ Task 2.4: Smart fallback cascade (2 hours)
- ✅ Test cache effectiveness

**Day 6-7 - Batching:**
- ✅ Task 2.2: Batch contact ranking (1.5 hours)
- ✅ Task 2.3: Batch profile enrichment (2 hours)
- ✅ Task 2.5: Conditional enrichment (1 hour)
- ✅ Performance benchmark

**Expected Results:** 30-40% reduction in LLM calls

### Week 3: Advanced Features (Days 8-12)

**Day 8-10 - Request Queue:**
- ✅ Task 3.1: Global request queue with priority (4-5 hours)
- ✅ Task 3.3: Request deduplication (1.5 hours)
- ✅ Integration testing

**Day 11-12 - Dynamic Adjustment:**
- ✅ Task 3.2: Dynamic rate adjustment (2-3 hours)
- ✅ Stress testing with high load

**Expected Results:** Zero queue stalls, smooth request flow

### Week 4: Observability (Days 13-15)

**Day 13 - Logging:**
- ✅ Task 4.1: Enhanced rate limit logging (1 hour)
- ✅ Task 4.5: Alert system (1.5 hours)

**Day 14 - Analytics:**
- ✅ Task 4.3: Token usage analytics (2 hours)
- ✅ Task 4.4: Enrichment success metrics (2-3 hours)

**Day 15 - UI:**
- ✅ Task 4.2: Real-time TUI rate monitor (2-3 hours)
- ✅ Final integration testing

**Expected Results:** Full visibility into pipeline performance

---

## 🔧 Configuration Reference

### Environment Variables

Add to `.env` file:

```bash
# ===== Rate Limiting =====
# Queries per minute for each provider
GEMINI_API_QPM=12                    # Gemini API (free tier: 15 QPM, use 12 for safety)
OPENROUTER_QPM=50                    # OpenRouter free tier (conservative)
RATE_LIMIT_BURST=3                   # Allow small bursts
ENABLE_RATE_ADJUSTMENT=true          # Dynamically adjust limits based on errors

# ===== LLM Response Caching =====
LLM_CACHE_ENABLED=true
LLM_CACHE_DISCOVERY_TTL_HOURS=24     # Cache URL discovery for 24 hours
LLM_CACHE_EXTRACTION_TTL_HOURS=168   # Cache extractions for 7 days
LLM_CACHE_RANKING_TTL_HOURS=12       # Cache rankings for 12 hours

# ===== Request Queue =====
REQUEST_QUEUE_ENABLED=true           # Enable priority request queue
REQUEST_QUEUE_MAX_SIZE=100           # Max queued requests before blocking
REQUEST_QUEUE_WORKERS=5              # Number of queue workers

# ===== Enrichment Optimization =====
BATCH_CONTACT_RANKING=true           # Rank all contacts in one LLM call
BATCH_PROFILE_ENRICHMENT=true        # Enrich profiles in batch
SKIP_LOW_SCORE_ENRICHMENT=true       # Skip full enrichment for score < 6
LOW_SCORE_THRESHOLD=6                # Threshold for reduced enrichment

# ===== Discovery Optimization =====
ENABLE_DDG_FALLBACK=true             # Allow DDG fallback (disable if Chrome not available)
DDG_FALLBACK_MIN_SCORE=7             # Only use DDG for high-value companies (score >= 7)

# ===== Monitoring =====
ENABLE_RATE_LIMIT_ALERTS=true       # Show alerts in TUI
ALERT_RATE_LIMIT_THRESHOLD=10       # Alert if >10 rate limits per hour
LOG_LEVEL=info                      # Options: debug, info, warn, error
```

### Command Line Options

```bash
# Enrichment with optimized settings
jobhunter enrich --batch=10 --parallel=3

# Aggressive mode (for experimentation)
jobhunter enrich --batch=20 --parallel=5 --aggressive

# Debug single company
jobhunter enrich --id=123 --no-tui --parallel=1

# Thorough enrichment (ignore optimizations)
jobhunter enrich --batch=5 --thorough

# Usage analytics
jobhunter usage-report
jobhunter usage-report --by-task
jobhunter usage-report --from=2026-04-01 --to=2026-04-02

# Enhanced stats
jobhunter stats --enrichment
```

---

## 🧪 Testing Strategy

### Phase 1 Testing (Critical Fixes)

```bash
# Test 1: Single company with verbose logging
./jobhunter enrich --id=1 --no-tui --parallel=1

# Expected:
# - No 404 errors for model IDs
# - Successful completion
# - <2 emergency fallback attempts

# Test 2: Small batch
./jobhunter enrich --batch=5 --parallel=2 --no-tui

# Expected:
# - 4-5 successful enrichments (80%+ success rate)
# - <10% emergency fallback usage
# - Smooth rate limit compliance

# Test 3: Monitor logs for rate limit events
tail -f jobhunter.log | grep -E "(RATE_LIMIT|emergency|fallback)"

# Expected:
# - Rare "RATE_LIMIT" events (< 1 per minute)
# - "emergency fallback" appears <5% of time
```

### Phase 2 Testing (Optimization)

```bash
# Test 4: Check cache effectiveness
sqlite3 data/jobs.db "SELECT COUNT(*), task FROM llm_response_cache GROUP BY task"

# Expected:
# - Discovery cache: 20-30 entries after 10 companies
# - Extraction cache: 15-25 entries
# - Cache hit rate: 30-40% on second batch

# Test 5: Benchmark LLM call reduction
# Before optimization:
time ./jobhunter enrich --batch=10 --parallel=3
# Note: Total LLM calls

# After optimization:
time ./jobhunter enrich --batch=10 --parallel=3
# Expected: 30-40% fewer LLM calls

# Test 6: Verify batching
# Check logs for "batch ranking" and "batch enrichment" messages
grep -E "(batch ranking|batch enrichment)" jobhunter.log

# Expected: Batched operations used for eligible companies
```

### Phase 3 Testing (Advanced Features)

```bash
# Test 7: Stress test with queue
./jobhunter enrich --batch=20 --parallel=5 --no-tui

# Monitor queue depth in logs
# Expected:
# - Queue depth stays <20
# - No queue stalls (requests complete within 5 minutes)
# - Smooth throughput

# Test 8: Verify request deduplication
# Run same batch twice in rapid succession
./jobhunter enrich --batch=5 --parallel=3 &
sleep 5
./jobhunter enrich --batch=5 --parallel=3

# Expected: Second run benefits from deduplication + cache
```

### Phase 4 Testing (Observability)

```bash
# Test 9: Verify TUI rate monitoring
./jobhunter enrich --batch=10 --parallel=3

# Expected:
# - TUI shows real-time rate limit usage
# - Alerts appear if rate limits hit
# - Clear status indicators

# Test 10: Usage report accuracy
./jobhunter usage-report --by-task

# Expected:
# - Accurate token counts
# - Helpful recommendations
# - Task breakdown makes sense

# Test 11: Enrichment metrics
./jobhunter stats --enrichment

# Expected:
# - Success rate >90%
# - Discovery method breakdown present
# - Performance metrics accurate
```

### Integration Testing

```bash
# Test 12: Full pipeline test
# Fresh database, 50 companies
./jobhunter scan --department=86 --limit=50
./jobhunter score --batch=50
./jobhunter enrich --batch=50 --parallel=3

# Expected:
# - 90-95% success rate
# - <5% emergency fallbacks
# - Completion in <2 hours
# - No rate limit stalls

# Test 13: Monitor system resources
# Run with monitoring
htop & ./jobhunter enrich --batch=20 --parallel=3

# Expected:
# - CPU usage: 50-150% (multi-threaded)
# - Memory: <500MB
# - No memory leaks
```

---

## 🎯 Success Criteria

After full implementation, the system should meet:

### Rate Limiting Success
- ✅ <2% emergency fallback invocations
- ✅ Zero 429 (rate limit) errors under normal load (parallel ≤ 3)
- ✅ Stable throughput: 20-30 companies/hour with parallel=3
- ✅ Graceful degradation under high load (parallel ≥ 5)

### LLM Efficiency
- ✅ 30-40% reduction in total LLM calls per company
- ✅ 50%+ cache hit rate after initial run
- ✅ Batch operations reduce ranking/enrichment calls by 60%
- ✅ Average <8 LLM calls per company (down from 12-15)

### Discovery Accuracy
- ✅ 70%+ success rate with LLM direct knowledge
- ✅ 90%+ success rate with Gemini search grounding
- ✅ <10% DDG fallback usage
- ✅ High-confidence URL discovery for 85%+ of companies

### Pipeline Performance
- ✅ 95%+ successful enrichment rate
- ✅ Average 2-4 minutes per company
- ✅ No pipeline stalls due to rate limits
- ✅ Consistent throughput over multi-hour runs

### Observability
- ✅ Real-time rate limit monitoring in TUI
- ✅ Detailed usage reports available
- ✅ Early warning for rate limit issues (alerts >10 hits/hour)
- ✅ Actionable recommendations in stats output

### Reliability
- ✅ Graceful degradation when providers are down
- ✅ Automatic provider failover works correctly
- ✅ Cache survives across runs
- ✅ No data loss on interruption

---

## 📚 Additional Recommendations

### Beyond Implementation

1. **Consider Paid Tier for High Volume:**
   - OpenRouter GPT-4o-mini: ~$0.15/M tokens
   - Gemini 2.0 Flash: ~$0.075/M tokens (input), $0.30/M tokens (output)
   - Cost estimate: $0.50-1.00 per day for 100 companies/day
   - Benefits: Higher rate limits (10,000+ QPM), better quality, no emergency fallbacks

2. **Implement Response Quality Scoring:**
   - Track LLM response quality per model
   - Prefer higher-quality models for critical tasks (discovery)
   - Use simpler models for basic tasks (link extraction)

3. **Add Retry Budget per Company:**
   - Limit total retries to prevent resource waste on impossible companies
   - Mark as "enrichment_impossible" after budget exhausted
   - Manual review queue for failed companies

4. **Browser Automation Improvements:**
   - Set up Chrome/Chromium properly for LinkedIn scrolling
   - Consider using authenticated sessions for better LinkedIn access
   - Implement CAPTCHA detection and manual intervention queue

5. **Data Quality Monitoring:**
   - Track hallucination rate over time
   - A/B test different prompts for extraction quality
   - Build confidence scoring for discovered URLs

6. **Webhook/Notification System:**
   - Alert when enrichment batch completes
   - Daily summary of enrichment success/failures
   - Integration with Slack/Discord for team updates

---

## 🔄 Maintenance & Updates

### Regular Maintenance Tasks

**Weekly:**
- Check OpenRouter for model availability changes
- Review rate limit alerts and adjust configuration
- Prune expired LLM cache entries
- Review and optimize worst-performing companies

**Monthly:**
- Analyze token usage trends
- Update model preferences based on quality metrics
- Review and optimize prompts based on extraction quality
- Database optimization (VACUUM, ANALYZE)

**Quarterly:**
- Re-evaluate provider selection (OpenRouter vs Gemini vs alternatives)
- Major prompt engineering improvements
- Performance benchmarking and optimization
- Cost-benefit analysis of paid tier vs free tier

### Model Updates

Monitor these resources for updates:
- OpenRouter model catalog: https://openrouter.ai/models
- Gemini API releases: https://ai.google.dev/gemini-api/docs
- Rate limit changes: Check provider documentation monthly

---

## 📞 Support & Resources

### Troubleshooting

**Problem: Still seeing high emergency fallback usage**
- Check: Model IDs are valid (run `jobhunter check-models`)
- Check: Parallel setting ≤ 3 for free tier
- Check: Rate limits configured correctly per provider
- Action: Enable debug logging and share logs

**Problem: Cache not improving performance**
- Check: `LLM_CACHE_ENABLED=true` in .env
- Check: Database has `llm_response_cache` table
- Action: Run cache stats query, verify TTLs

**Problem: Queue building up**
- Check: Rate limits not too low
- Check: Provider not experiencing outage
- Action: Reduce parallel workers temporarily

**Problem: Low discovery success rate**
- Check: Gemini API key configured correctly
- Check: DDG fallback enabled if Chrome available
- Action: Review discovery prompt effectiveness

### Getting Help

- GitHub Issues: Report bugs and feature requests
- Logs: Always attach `jobhunter.log` (last 200 lines)
- Database: Export relevant stats with `jobhunter stats --export`

---

## 🏗️ Future Architectural Consideration: Separate Engine

**Note:** This is documented for future consideration, NOT part of current implementation.

### Current Architecture
- Monolithic CLI application
- TUI tightly coupled with enrichment pipeline
- Single process runs everything

### Proposed Separated Architecture

**Engine Service** (Independent):
```
jobhunter-engine:
  - HTTP/gRPC API for job requests
  - Internal work queue
  - Rate limiting enforcement
  - Status reporting
  - Runs as daemon/systemd service
```

**Client Applications** (Multiple possible):
```
jobhunter-cli:
  - Submit jobs to engine
  - Query status
  - Stream logs

jobhunter-tui:
  - Rich terminal interface
  - Monitors engine status
  - Job management

jobhunter-web:
  - Web dashboard
  - Job scheduling
  - Analytics
```

### Benefits
- ✅ Engine runs independently (survives terminal disconnects)
- ✅ Multiple clients can monitor same engine
- ✅ Web dashboard + TUI simultaneously
- ✅ Better for production deployments
- ✅ API enables automation/integration

### Trade-offs
- ⚠️ Increased complexity
- ⚠️ More moving parts to maintain
- ⚠️ Requires IPC/API design

### Decision
**Defer this architectural change** until after rate limiting is solved. Focus on:
1. Fix rate limiting issues first
2. Validate performance improvements
3. Gather usage data
4. Re-evaluate if separation makes sense

If pursued later, the refactor would be cleaner with working rate limiting already in place.

---

## 📄 Document Change Log

| Date | Version | Changes | Author |
|------|---------|---------|--------|
| 2026-04-02 | 1.0 | Initial analysis and implementation plan | Claude |
| 2026-04-02 | 1.1 | Architecture review: YAML config, unified rate limiter, dual methods, simplified discovery | Claude |

---

**Document Status:** APPROVED FOR IMPLEMENTATION  
**Next Review Date:** After Phase 1 completion  
**Estimated Completion:** 3-4 weeks for all phases
