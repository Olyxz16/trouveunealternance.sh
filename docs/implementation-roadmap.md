# Implementation Roadmap - Rate Limiting Fixes

**Date:** April 2, 2026  
**Status:** Phase 1 & 2 Complete

---

## 📋 Updated Priority List

Based on architectural review, here's the revised implementation order:

### 🔴 **Phase 1: Critical Fixes** (Week 1)

#### Day 1: Configuration Architecture (CRITICAL)
- ✅ **Task 1.0**: Reorganize config architecture (2-3 hours)
  - Create `config.yaml` with model strategies
  - Update `internal/config/config.go` to load YAML
  - Move constants from `constants.yaml` into `config.yaml`
  - Update `.env` to only contain secrets
  
- ✅ **Task 1.1**: Update OpenRouter model IDs (15 min)
  - Fix broken model IDs in new `config.yaml`
  
- ✅ **Task 1.5**: Move parallelism to config (10 min)
  - Add to `config.yaml`
  - Update `cmd/enrich.go` to read from config

**Testing:** Single company enrichment should work with new config

#### Day 2-3: Unified Rate Limiter
- ✅ **Task 1.2**: Implement unified rate limiter (2-3 hours)
  - Create `internal/llm/rate_limiter.go`
  - Single global limiter + per-provider tracking
  - Update `internal/llm/client.go`

- ✅ **Task 1.3**: Improve rate limiter algorithm (1 hour)
  - Smoother distribution (rate.Limit instead of rate.Every)
  - Add burst capacity

- ✅ **Task 1.4**: Intelligent backoff (1.5 hours)
  - Respect Retry-After headers
  - Exponential backoff with provider-specific logic

**Testing:** Batch of 10 companies with rate limit monitoring

**Expected Result:** 80-90% success rate, <10% emergency fallbacks

---

### 🟡 **Phase 2: Optimization** (Week 2)

#### Day 4-5: Caching & Discovery
- ✅ **Task 2.1**: LLM response cache (3-4 hours)
  - Database migration
  - Cache logic in `internal/llm/client.go`
  
- ✅ **Task 2.4**: Simplify discovery (1.5 hours)
  - Remove DDG intermediary
  - LLM → Gemini → Browser (direct)

**Testing:** Verify cache hit rate, test discovery reliability

#### Day 6-7: Batching (A/B Testing)
- ✅ **Task 2.2**: Batch contact ranking (2 hours)
  - Add batch method, keep single method
  - Config flag to toggle
  
- ✅ **Task 2.3**: Batch profile enrichment (2.5 hours)
  - Add batch method, keep single method
  - Config flag to toggle

- ✅ **Task 2.5**: Conditional enrichment (1 hour)
  - Skip low-score enrichment
  - Configurable thresholds

**Testing:** Compare batch vs single performance metrics

**Expected Result:** 30-40% reduction in LLM calls

---

### 🟢 **Phase 3: Advanced Features** (Week 3)

Not yet started:
- **Task 3.1**: Global request queue with priority
- **Task 3.2**: Dynamic rate adjustment
- **Task 3.3**: Request deduplication

### 🔵 **Phase 4: Observability** (Week 4)

Not yet started:
- **Task 4.1**: Enhanced rate limit logging
- **Task 4.2**: Real-time TUI rate monitor
- **Task 4.3**: Token usage analytics
- **Task 4.4**: Enrichment success metrics
- **Task 4.5**: Alert system

---

## 🎯 Immediate Next Steps

1. ~~Start with Task 1.0~~ ✅ Done
2. ~~Create `config.yaml` template~~ ✅ Done
3. ~~Update config loading logic~~ ✅ Done
4. ~~Test with existing system~~ ✅ Done
5. ~~Proceed to rate limiter~~ ✅ Done

---

## 📊 Success Metrics (Phase 1 Only)

After Phase 1 completion, we should see:
- ✅ No 404 model errors
- ✅ 80-90% successful enrichments
- ✅ <10% emergency fallback usage
- ✅ Stable throughput with parallel=3
- ✅ Clear configuration in YAML

---

## 🚀 Ready to Start?

Execute tasks in this order:
1. ~~Config architecture (Task 1.0)~~ ✅
2. ~~Fix model IDs (Task 1.1)~~ ✅
3. ~~Move parallelism (Task 1.5)~~ ✅
4. ~~Unified rate limiter (Task 1.2-1.4)~~ ✅
5. ~~Test and validate~~ ✅

## ✅ Additional Improvements (Beyond Roadmap)

- **LinkedIn people extraction**: HTML-based regex extractor bypassing LLM confusion
- **JSON parsing reliability**: `isLikelyNonJSON()` detection with strict retry prompt
- **Name validation**: `isValidPersonName()` filter rejecting job titles used as names
- **Anti-bot detection**: Detect LinkedIn blocking via `/in/` URL presence, fail fast with `ENRICHMENT_BLOCKED` status
- **DDG skip**: Wired up `skip_ddg_search` config in both discovery and people search

---

Let's begin! 🎉
