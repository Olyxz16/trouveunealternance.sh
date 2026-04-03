# JobHunter: Daemon & Distributed Architecture

**Date:** April 3, 2026  
**Status:** Design Document  
**Phase:** Planning

---

## 📋 Overview

This document describes the planned transition from a CLI-only tool to a daemonized, distributed job queue system. The goal is to enable autonomous data aggregation at scale while maintaining CLI/TUI capabilities for ad-hoc testing.

---

## 🏗️ Architecture

### Current State
```
CLI (single process) → SQLite → Enrichment Pipeline
```

### Target State
```
┌─────────────────────────────────────────────────┐
│                  PostgreSQL                      │
│  ┌──────────┐ ┌──────┐ ┌────────────┐           │
│  │companies │ │jobs  │ │rate_limits │           │
│  │contacts  │ │scrape_cache│ │company_cooldowns│ │
│  │enrichment│ │llm_cache│ │token_usage│         │
│  └──────────┘ └──────┘ └────────────┘           │
└────────┬────────────────────────────┬───────────┘
         │                            │
    ┌────▼─────┐              ┌───────▼──────┐
    │  Worker  │              │   Web App    │
    │ (Pod 1)  │              │  (HTMX+Go)   │
    │ ┌──────┐ │              │              │
    │ │Chrome│ │              │ - Queue mgmt │
    │ └──────┘ │              │ - Job submit │
    │          │              │ - Monitoring │
    └──────────┘              └────────────────┘
    ┌──────────┐
    │  Worker  │
    │ (Pod 2)  │
    │ ┌──────┐ │
    │ │Chrome│ │
    │ └──────┘ │
    └──────────┘
```

### Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Database** | PostgreSQL | Already a dependency, K8s-ready, single DB for queue + data |
| **Job Queue** | PostgreSQL table | No Redis needed at current scale; row-level locking for distributed workers |
| **Rate Limiter** | PostgreSQL-backed token bucket | Atomic operations, works across pods |
| **Chrome** | One per worker pod | Failure isolation, simpler K8s, ~200MB savings not worth complexity |
| **Web Dashboard** | Go + HTMX | Single binary, simple deployment |
| **CLI/TUI** | Kept as-is (direct mode) | Ad-hoc testing, no queue dependency |

---

## 📊 Database Schema Additions

### Jobs Table
```sql
CREATE TABLE jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type TEXT NOT NULL,              -- 'enrich_company', 'scan_department', 'scan_region', 're_enrich_failed'
    status TEXT NOT NULL DEFAULT 'pending',  -- 'pending', 'running', 'completed', 'failed', 'cancelled'
    payload JSONB NOT NULL,          -- job-specific parameters
    priority INTEGER DEFAULT 0,      -- higher = processed first
    attempts INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 3,
    error TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    worker_id TEXT                   -- which worker picked up this job
);

CREATE INDEX idx_jobs_status_priority ON jobs(status, priority DESC, created_at);
CREATE INDEX idx_jobs_type ON jobs(type);
```

### Rate Limits Table
```sql
CREATE TABLE rate_limits (
    id TEXT PRIMARY KEY,             -- 'global', 'openrouter', 'gemini_api'
    tokens DECIMAL NOT NULL,
    last_refill TIMESTAMP NOT NULL,
    max_tokens DECIMAL NOT NULL,
    refill_rate DECIMAL NOT NULL,    -- tokens per second
    daily_limit INTEGER,             -- 0 = unlimited
    daily_used INTEGER DEFAULT 0,
    daily_reset_at TIMESTAMP
);

-- Initial state: 30 RPM, burst=1, 500/day
INSERT INTO rate_limits VALUES 
    ('global', 1.0, NOW(), 1.0, 0.5, 500, 0, NOW() + INTERVAL '1 day'),
    ('openrouter', 1.0, NOW(), 1.0, 0.5, 500, 0, NOW() + INTERVAL '1 day'),
    ('gemini_api', 1.0, NOW(), 1.0, 0.2, 1000, 0, NOW() + INTERVAL '1 day');
```

### Company Cooldowns Table
```sql
CREATE TABLE company_cooldowns (
    company_id INTEGER PRIMARY KEY REFERENCES companies(id),
    last_scraped_at TIMESTAMP,
    next_allowed_at TIMESTAMP,
    scrape_count INTEGER DEFAULT 0,
    last_status TEXT                 -- 'success', 'blocked', 'failed'
);
```

---

## 🔧 Rate Limiter: PostgreSQL Implementation

### Token Bucket Algorithm
```sql
-- Acquire a token (atomic)
UPDATE rate_limits 
SET tokens = tokens - 1, last_refill = NOW()
WHERE id = 'global' 
  AND tokens >= 1
  AND (daily_limit = 0 OR daily_used < daily_limit)
RETURNING id;
```

If the UPDATE returns 0 rows → rate limited. Worker sleeps and retries.

### Token Refill (handled by workers on each request)
```sql
UPDATE rate_limits 
SET tokens = LEAST(max_tokens, tokens + refill_rate * EXTRACT(EPOCH FROM (NOW() - last_refill))),
    last_refill = NOW()
WHERE id = 'global';
```

### Daily Limit Reset (handled by workers on startup)
```sql
UPDATE rate_limits 
SET daily_used = 0, daily_reset_at = NOW() + INTERVAL '1 day'
WHERE daily_reset_at <= NOW();
```

---

## 🔄 Job Queue: Worker Model

### Worker Lifecycle
```
1. Worker starts, registers itself (worker_id, heartbeat)
2. Polls for available jobs:
   SELECT * FROM jobs 
   WHERE status = 'pending' 
     AND (next_allowed_at IS NULL OR next_allowed_at <= NOW())
   ORDER BY priority DESC, created_at ASC
   LIMIT 1
   FOR UPDATE SKIP LOCKED
3. Marks job as 'running', updates worker_id
4. Processes job (enrichment pipeline)
5. Updates job status to 'completed' or 'failed'
6. Updates company_cooldowns
7. Repeats from step 2
```

### Job Types

| Type | Payload | Description |
|------|---------|-------------|
| `enrich_company` | `{"company_id": 138}` | Enrich a single company |
| `scan_department` | `{"department": "86", "limit": 100}` | Scan a French department |
| `scan_region` | `{"region": "Nouvelle-Aquitaine"}` | Scan a region |
| `re_enrich_failed` | `{"max_attempts": 3}` | Retry failed companies |
| `discover_urls` | `{"company_ids": [1,2,3]}` | Discover URLs for companies without them |

### Retry Logic
- Failed jobs are retried up to `max_attempts` times
- Exponential backoff: `retry_delay = 2^attempt * 5 minutes`
- Provider 404/403 errors: longer backoff (30 min) — likely infrastructure issues
- Company blocked (`ENRICHMENT_BLOCKED`): no retry, requires manual review

---

## 🌐 Web Dashboard

### Tech Stack
- **Backend**: Go HTTP server (same binary as workers)
- **Frontend**: HTMX + Tailwind CSS
- **Real-time updates**: Server-Sent Events (SSE)

### Pages
1. **Dashboard** — Queue status, active workers, rate limit usage
2. **Jobs** — List, filter, add new jobs
3. **Companies** — Search by name, region, sector, status
4. **Workers** — Active workers, last heartbeat, jobs processed
5. **Rate Limits** — Current RPM, daily usage, alerts

### API Endpoints
```
GET  /api/jobs              — List jobs (filterable)
POST /api/jobs              — Create job
GET  /api/jobs/{id}         — Job details
POST /api/jobs/{id}/cancel  — Cancel job
POST /api/jobs/{id}/retry   — Retry failed job

GET  /api/companies         — Search companies
GET  /api/companies/{id}    — Company details + contacts

GET  /api/workers           — List active workers
GET  /api/workers/{id}      — Worker details

GET  /api/rate-limits       — Current rate limit status
GET  /api/stats             — Aggregate statistics

GET  /events                — SSE stream for real-time updates
```

---

## 🖥️ CLI Commands (Updated)

### Existing Commands (Direct Mode — Unchanged)
```bash
jobhunter enrich --id=138          # Direct enrichment, bypasses queue
jobhunter enrich --batch=10        # Direct batch enrichment
jobhunter enrich --tui             # TUI mode
jobhunter scan --department=86     # Direct scan
jobhunter stats                    # Statistics
```

### New Commands (Queue Mode)
```bash
# Job management
jobhunter enqueue --id=138         # Add single company to queue
jobhunter enqueue --batch=30       # Add 30 companies to queue
jobhunter enqueue --department=86  # Add department scan to queue
jobhunter enqueue --region="NA"    # Add region scan to queue
jobhunter enqueue --retry-failed   # Add failed companies to queue

# Queue monitoring
jobhunter queue status             # Show queue status
jobhunter queue list               # List pending/running jobs
jobhunter queue cancel <job-id>    # Cancel a job
jobhunter queue retry <job-id>     # Retry a failed job

# Worker mode
jobhunter worker --listen          # Start worker (long-running)
jobhunter worker --process=10      # Process 10 jobs and exit

# Status & monitoring
jobhunter status                   # Overall system status
jobhunter rate-limits              # Current rate limit usage
jobhunter workers                  # List active workers
```

---

## 🐳 Kubernetes Deployment

### Worker Pod
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jobhunter-worker
spec:
  replicas: 2
  selector:
    matchLabels:
      app: jobhunter-worker
  template:
    metadata:
      labels:
        app: jobhunter-worker
    spec:
      containers:
      - name: worker
        image: jobhunter/worker:latest
        command: ["jobhunter", "worker", "--listen"]
        env:
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: jobhunter-secrets
              key: database-url
        - name: OPENROUTER_API_KEY
          valueFrom:
            secretKeyRef:
              name: jobhunter-secrets
              key: openrouter-api-key
        - name: GEMINI_API_KEY
          valueFrom:
            secretKeyRef:
              name: jobhunter-secrets
              key: gemini-api-key
        - name: BROWSER_COOKIES_PATH
          value: "/data/browser_session.json"
        - name: BROWSER_HEADLESS
          value: "true"
        resources:
          requests:
            memory: "500Mi"
            cpu: "500m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
        volumeMounts:
        - name: browser-data
          mountPath: /data
      volumes:
      - name: browser-data
        persistentVolumeClaim:
          claimName: browser-data-pvc
```

### Web App Pod
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jobhunter-web
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: web
        image: jobhunter/web:latest
        command: ["jobhunter", "web", "--listen", ":8080"]
        ports:
        - containerPort: 8080
```

### Scaling Considerations
- **Workers**: Scale based on queue depth (HPA on `jobs.pending` count)
- **Chrome memory**: ~350MB per worker pod (Chrome + Go process)
- **Database**: PostgreSQL with connection pooling (PgBouncer for 10+ workers)

---

## 📈 Monitoring & Observability

### Metrics to Track
| Metric | Source | Alert Threshold |
|--------|--------|----------------|
| Queue depth | `jobs` table | > 100 pending |
| Worker health | Heartbeat (every 30s) | No heartbeat for 2 min |
| Rate limit usage | `rate_limits` table | > 80% of daily limit |
| Enrichment success rate | Company status | < 50% TO_CONTACT |
| Provider error rate | Token usage + errors | > 20% 404/403 errors |
| Average processing time | Job timestamps | > 5 min per company |

### Log Structure
```json
{
  "level": "info",
  "ts": "2026-04-03T17:00:00Z",
  "worker_id": "worker-abc123",
  "job_id": "job-xyz789",
  "company": "TATA CONSULTANCY SERVICES FRANCE",
  "action": "enrich_completed",
  "status": "TO_CONTACT",
  "contacts_found": 7,
  "duration_seconds": 45,
  "llm_calls": 12,
  "cache_hits": 8
}
```

---

## 🚀 Implementation Phases

### Phase 1: Job Queue Foundation (2-3 days)
- [ ] Migrate from SQLite to PostgreSQL (GORM handles this)
- [ ] Add `jobs` table and job queue logic
- [ ] Add `rate_limits` table + PostgreSQL-backed rate limiter
- [ ] Add `company_cooldowns` table
- [ ] Create `jobhunter worker --listen` command
- [ ] Create `jobhunter enqueue` commands
- [ ] Keep existing CLI commands working in direct mode
- [ ] Add exponential backoff retry logic

### Phase 2: Web Dashboard (3-4 days)
- [ ] Go HTTP server with REST API
- [ ] HTMX + Tailwind web UI
- [ ] Job management (add, cancel, retry)
- [ ] Company search and filtering
- [ ] Real-time queue status via SSE
- [ ] Rate limit monitoring panel

### Phase 3: K8s Readiness (1-2 days)
- [ ] Health check endpoints (`/healthz`, `/readyz`)
- [ ] Graceful shutdown (SIGTERM handling)
- [ ] Config via environment variables
- [ ] Dockerfile for workers and web app
- [ ] Kubernetes manifests (deployment, service, HPA)

### Phase 4: Advanced Features (Future)
- [ ] Dynamic rate adjustment based on error rates
- [ ] Provider health checks before sending requests
- [ ] Email notification on job completion/failure
- [ ] Export results (CSV, JSON)
- [ ] LinkedIn message drafting (`jobhunter draft --id=138`)

---

## ⚠️ Known Limitations & Tradeoffs

| Limitation | Impact | Mitigation |
|------------|--------|------------|
| **PostgreSQL job queue** | Not as fast as Redis | Fine for < 100 jobs/min; switch to Redis if needed |
| **One Chrome per worker** | ~350MB RAM per pod | Acceptable at 2-3 workers; shared Chrome later if needed |
| **Per-company cooldown (24h)** | Can't re-enrich same company same day | Manual override via `--force` flag |
| **Google CAPTCHA risk** | Heavy automated searching may trigger CAPTCHA | Monitor logs, reduce search frequency if detected |
| **OpenRouter provider instability** | 404 errors from provider infrastructure | Retry with backoff, provider health checks |

---

## 📝 Notes

- **CLI/TUI is NOT being replaced** — it remains for ad-hoc testing and direct enrichment
- **PostgreSQL is the single source of truth** — queue, data, rate limits, cooldowns
- **Workers are stateless** — safe to scale up/down in K8s
- **Web dashboard is optional** — CLI commands work without it
- **Free-tier LLMs are sufficient** — no need to switch to paid models unless reliability becomes critical

---

**Document Status:** APPROVED FOR IMPLEMENTATION  
**Last Updated:** April 3, 2026
