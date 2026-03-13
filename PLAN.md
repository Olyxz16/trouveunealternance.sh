# JobHunter Go — Transition & Pipeline Architecture

> This document covers: the Go pipeline tooling decision, the full pipeline
> design across all four stages, how to handle browser automation, metrics,
> and LLM cost tracking — and the concrete steps to rebuild the project.

---

## 1. The Pipeline Tooling Question

You asked for "a Dagster equivalent for Go." The short answer is: **there isn't
one worth using.** Here is why, and what to do instead.

### What exists in Go

| Tool | Backed by | Problem |
|---|---|---|
| Temporal | Temporal server (Docker) | Needs a server, designed for distributed teams, enormous ops overhead for a single-user tool |
| River | PostgreSQL | Requires Postgres — you're on SQLite |
| Asynq / Machinery | Redis | Same: external broker required |
| Watermill | Kafka / Redis / AMQP | Pub/sub framework, not a pipeline orchestrator |

None of these are a good fit. They all assume you're building a distributed
system with multiple workers and external infrastructure.

### What you actually need

Dagster's value for your use case comes down to three things:

1. **Run tracking** — every pipeline execution has an ID, a start time, a status
2. **Step tracking** — each step within a run has its own status, duration, error
3. **Resumability** — if a run fails halfway, you can re-run only the failed steps

You already have tables for this: `run_log`. The missing piece is a thin
**pipeline engine** built directly on SQLite. This is 200–300 lines of Go, not
a third-party service.

### The design: a lightweight pipeline engine

The core idea is a `Pipeline` struct that wraps steps, persists every state
transition to SQLite, and exposes the same observability Dagster would give you
— but with zero external dependencies.

```
Run
 ├── Step: collect          (ok / error / skipped)
 ├── Step: import           (ok / error / skipped)
 ├── Step: enrich[company]  (ok / error / needs_review)
 └── Step: generate[company](ok / error / skipped)
```

Each step is a Go function with a fixed signature:

```go
type StepFn func(ctx context.Context, run *Run) error

type Step struct {
    Name    string
    Fn      StepFn
    Timeout time.Duration
}
```

The engine runs steps in order, catches panics, writes to `run_log` on every
state transition, and returns a structured result. Steps can be run in parallel
with a configurable concurrency limit (e.g. 3 companies enriched at once).

The dashboard (already built) reads from `run_log` and renders exactly this —
which means **you already have the Dagster UI equivalent**, it just needs the
Go engine writing to the same tables.

**This is the approach. No third-party orchestrator needed.**

---

## 2. Browser Automation Decision

### Keep Blueprint MCP

Blueprint MCP is an HTTP server that proxies to your real Firefox session. Your
Go code calls it the same way the Python code did: plain HTTP POST to
`http://localhost:3000`. The session persistence (LinkedIn cookies, WTTJ login)
is its core value — a headless Chrome library won't have this.

```go
// From Go, calling Blueprint MCP is just:
resp, err := http.Post("http://localhost:3000/browser/navigate",
    "application/json",
    strings.NewReader(`{"url": "https://linkedin.com/company/acme"}`))
```

### When to use what

Jina (`r.jina.ai/{url}`) and Cloudflare Browser Rendering both convert pages to
markdown, but they are not equivalent for this use case. Jina is a managed proxy
with zero config — prepend it to any URL and get markdown back. Cloudflare
requires an account, API token, and per-browser-second billing. Critically,
**neither bypasses bot detection** — both are identified as bots by sites that
care. For anything that requires a logged-in session (LinkedIn), both fail
identically. Blueprint MCP is irreplaceable for those cases.

Use Jina for the cheap first pass. Use MCP when sessions matter. Never reach for
Cloudflare Browser Rendering — it adds account complexity for no gain here.

| Source | Method | Reason |
|---|---|---|
| Company website / careers page | Jina (`r.jina.ai/{url}`) | Zero config, fast, sufficient for static/lightly dynamic pages |
| LinkedIn company page | Blueprint MCP | Requires real session, anti-bot |
| LinkedIn People tab | Blueprint MCP | Requires real session |
| WTTJ job listings (V3) | Jina first, MCP fallback | Usually works without auth |
| Indeed France (V3) | Jina first, MCP fallback | Usually works without auth |

### LLM provider interface

Rather than hardcoding OpenRouter calls and bolting Gemini CLI on as a special
case, model every LLM backend as an implementation of a single `Provider`
interface. The client sits above it and owns all cross-cutting concerns: rate
limiting, retry, usage logging. Swapping providers — or falling back — requires
zero changes outside `internal/llm`.

```go
// internal/llm/provider.go

type CompletionRequest struct {
    System    string
    User      string
    MaxTokens int
    JSONMode  bool
}

type CompletionResponse struct {
    Content          string
    PromptTokens     int
    CompletionTokens int
    CostUSD          float64
    EstimatedCost    bool   // true when cost is estimated, not exact (Gemini CLI)
}

type Provider interface {
    Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
    Name() string
}
```

Three implementations, all satisfying the same interface:

```go
// internal/llm/openrouter.go
type OpenRouterProvider struct { /* api key, model, http client */ }
// Returns exact token counts + cost from API response headers.

// internal/llm/gemini_cli.go
type GeminiCLIProvider struct { BinaryPath string }
// Pipes prompt to stdin of the `gemini` binary, captures stdout.
// EstimatedCost: true. Token count estimated as (chars / 4).
// Note: Gemini CLI is interactive by design — keep prompts fully
// self-contained so it never pauses waiting for input.

// internal/llm/openai.go  (future-proofing, trivial to add)
type OpenAIProvider struct { /* api key, model */ }
```

The `Client` wraps any `Provider` and handles everything else:

```go
// internal/llm/client.go

type Client struct {
    provider Provider
    limiter  *rate.Limiter
    db       *db.DB
    logger   *zap.Logger
}

func (c *Client) Complete(ctx context.Context, req CompletionRequest, task, runID string) (CompletionResponse, error) {
    // 1. Wait for rate limiter token
    // 2. Call provider (with retry + exponential backoff on 429/5xx)
    // 3. Log usage to correct table based on resp.EstimatedCost
    // 4. Return response
}

func (c *Client) CompleteJSON(ctx context.Context, req CompletionRequest, task, runID string, target any) error {
    // Complete → json.Unmarshal into target
    // On failure: retry once with error appended to prompt
}

func (c *Client) logUsage(resp CompletionResponse, task, runID string) {
    if resp.EstimatedCost {
        c.db.InsertGeminiUsage(runID, task, resp.PromptTokens, resp.CompletionTokens)
    } else {
        c.db.InsertLLMUsage(runID, task, c.provider.Name(), resp.PromptTokens, resp.CompletionTokens, resp.CostUSD)
    }
}
```

Usage logging is unified through `logUsage` regardless of which provider ran.
The dashboard Usage tab queries both `llm_usage` (exact) and `gemini_usage`
(estimated) and renders them together — "OpenRouter: $0.06 / Gemini CLI: ~3 200 tokens estimated."

**Fallback wiring:** the pipeline configures a primary provider (OpenRouter) and
an optional fallback (Gemini CLI). If OpenRouter returns a hard error after
retries exhausted, the client transparently retries on the fallback provider
and logs which one was actually used. Configured in `.env`:

```env
LLM_PRIMARY=openrouter
LLM_FALLBACK=gemini_cli          # leave empty to disable fallback
OPENROUTER_API_KEY=sk-or-...
OPENROUTER_MODEL=google/gemini-2.5-flash-lite
GEMINI_CLI_PATH=gemini           # binary name if on PATH, or full path
```

---

## 3. The Four Pipeline Stages

### Stage 1 — Data Collection

**What it does:** Pull company data from external sources into a staging area.

**Sources:**
- SIRENE Parquet (`data/sirene.parquet`) — already downloaded, read locally
- `recherche-entreprises.api.gouv.fr` — free API, no auth, returns dirigeants
- French Tech 5000 CSV (optional one-time import)

**Go implementation:**

```
internal/collector/
  sirene.go          — stream Parquet, filter by dept + NAF + headcount
  recherche.go       — HTTP client for recherche-entreprises API
  frenchtech.go      — CSV import
```

SIRENE Parquet reading in Go: use `xitongsys/parquet-go` for pure-Go streaming
reads. It handles the 2GB file in batches. Alternatively, shell out to DuckDB
(`duckdb -c "SELECT ... FROM 'sirene.parquet' WHERE ..."`) if the Parquet
library is painful — DuckDB has a static binary, zero config.

**Output:** Raw company rows written to a `staging_companies` table (not yet
deduplicated or classified). The stage completes when all sources are exhausted.

---

### Stage 2 — Import & Pre-filter

**What it does:** Move staging rows into `companies`, deduplicate by SIREN,
apply the two-step classification heuristic, discard obvious non-tech.

**Steps:**
1. Dedup by SIREN (or name+city if no SIREN)
2. Apply NAF heuristic:
   - NAF 62xx / 63xx → `company_type = TECH`, pass through
   - Non-tech NAF + headcount ≥ 100 → `company_type = UNKNOWN`, pass through for LLM check
   - Non-tech NAF + headcount < 100 → `status = NOT_TECH`, skip
3. For `UNKNOWN` rows: call LLM classifier (`gpt-oss-120b:free`, free tier) to
   confirm `TECH_ADJACENT` or `NON_TECH`
4. Cap `relevance_score` at 7 for `TECH_ADJACENT`
5. Set `status = NEW` for passing companies

**Output:** `companies` table populated, each row has `company_type` set,
`NOT_TECH` companies marked and excluded from further stages.

---

### Stage 3 — Enrichment Pipeline (the core)

This is the most important stage and the one that was never properly implemented
in Python. For each company with `status = NEW` and no `primary_contact_id`:

```
For each company:
  3a. Discover URLs
  3b. Fetch content (Jina → MCP → NEEDS_REVIEW)
  3c. Extract structured data (LLM)
  3d. Find contacts (LLM + MCP LinkedIn search)
  3e. Save results
```

**3a. Discover URLs**

If the company has no `website` or `linkedin_url`, try to find them:
- Query `recherche-entreprises.api.gouv.fr/{siren}` — sometimes returns website
- Search Jina: `https://r.jina.ai/search?q={company_name}+{city}+site:linkedin.com`
- Fallback: construct LinkedIn search URL and fetch via MCP

**3b. Fetch content**

Ordered by cost (cheapest first):
1. Check `scrape_cache` (SQLite) — if fresh, return immediately
2. Try Jina for company website / careers page
3. Try MCP (Blueprint) for LinkedIn company page — always needed for contacts
4. If both fail → `status = NEEDS_REVIEW`, log to `run_log`, continue to next company

Cache TTL: 24h for career pages, 7 days for LinkedIn company pages (they change
slowly), 1h for job board listings (V3).

**3c. Extract structured data**

Send fetched markdown to `careers_page.go` parser:

```go
type RawCompanyPage struct {
    Name                  string   `json:"name"`
    Description           string   `json:"description"`
    City                  string   `json:"city"`
    Headcount             string   `json:"headcount"`
    TechStack             []string `json:"tech_stack"`
    GithubOrg             string   `json:"github_org"`
    EngineeringBlogURL    string   `json:"engineering_blog_url"`
    OpenSourceMentioned   bool     `json:"open_source_mentioned"`
    InfraKeywords         []string `json:"infrastructure_keywords"`
    CompanyType           string   `json:"company_type"`   // TECH | TECH_ADJACENT | NON_TECH
    HasInternalTechTeam   bool     `json:"has_internal_tech_team"`
    TechTeamSignals       []string `json:"tech_team_signals"`
}
```

Write back to `companies`. Update `relevance_score`.

**3d. Find contacts**

This is the most valuable and most fragile step. Strategy by company type:

| company_type | Target role | Method |
|---|---|---|
| `TECH` | CTO / Engineering Manager / Tech Lead | LinkedIn People tab via MCP |
| `TECH_ADJACENT` | IT Director / Infrastructure Manager / CIO | LinkedIn People tab via MCP |
| Either | Technical recruiter (fallback) | LinkedIn People tab via MCP |

MCP flow for LinkedIn contact discovery:
1. Navigate to company LinkedIn page via MCP
2. Click "People" tab
3. Search within company for "CTO" / "infrastructure" / "engineering"
4. Extract top 3 results: name, role, LinkedIn URL
5. For each result: check if public email visible on their profile
6. Pass results to LLM to pick the best contact given company type

Write each found contact to `contacts` table. Set `primary_contact_id` on
`companies` for the highest-confidence contact.

**3e. Save and advance status**

```
contact found    → status = TO_CONTACT
no contact       → status = NO_CONTACT_FOUND (try guess-emails later)
fetch failed     → status = NEEDS_REVIEW
3 failures same step → status = FAILED (excluded from future runs)
```

**Concurrency:** Run enrichment for N companies in parallel (default N=3,
configurable). Each company gets its own goroutine. Rate limiter is shared
across all goroutines via `golang.org/x/time/rate`.

---

### Stage 4 — Application Generation

For each company with `status = TO_CONTACT` and no existing drafts:

**4a. Career page letter** (if `careers_page_url` is set)

A formal but personal application letter, structured for a career page form or
email to a generic address. Inputs: company description, tech stack,
`tech_team_signals`, `profile.json`.

**4b. Cold email per contact**

One email draft per contact in `contacts` table. Angle varies:
- `TECH` + CTO/tech lead → technically specific, reference their stack
- `TECH` + HR → impact and fit angle
- `TECH_ADJACENT` + IT director → internal tooling, adaptability, infra interest

**4c. LinkedIn hook per contact**

≤ 280 chars. Opening line references something specific about their profile or
company. Generated in the same LLM call as the email to save cost.

All drafts written to `drafts` table with `status = draft`. The dashboard
shows them for review and one-click send.

**Provider fallback:** if OpenRouter exhausts retries, the client automatically
retries on the configured fallback provider (Gemini CLI). The calling code in
`generator/drafts.go` sees a single `client.CompleteJSON()` call — the fallback
is transparent. Usage is logged to the correct table either way.

---

## 4. Metrics & Cost Tracking

### Unified through the LLM client

All usage logging — regardless of provider — flows through `client.logUsage()`
in `internal/llm/client.go`. The `CompletionResponse.EstimatedCost` bool
determines which table gets the write:

```
OpenRouter call  →  resp.EstimatedCost = false  →  INSERT INTO llm_usage    (exact tokens + cost)
Gemini CLI call  →  resp.EstimatedCost = true   →  INSERT INTO gemini_usage  (estimated tokens)
```

No separate tracking path. No special-casing in calling code. The provider
abstraction means the pipeline never knows or cares which backend ran.

### `llm_usage` — exact (OpenRouter)

```sql
run_id, step, model, prompt_tokens, completion_tokens, cost_usd, ts
```

OpenRouter returns exact token counts and cost on every response via the
`usage` field and `x-openrouter-cost` header.

### `gemini_usage` — estimated (Gemini CLI)

Since Gemini CLI returns no token counts, estimate from character count:
~4 chars per token is a reasonable approximation for French/English mixed text.

```sql
CREATE TABLE gemini_usage (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id           TEXT,
  step             TEXT,
  prompt_tokens    INTEGER,   -- estimated: prompt_chars / 4
  completion_tokens INTEGER,  -- estimated: response_chars / 4
  ts               TEXT NOT NULL DEFAULT (datetime('now'))
);
```

The dashboard Usage tab queries both tables and renders them side by side:
"OpenRouter: $0.06 exact · Gemini CLI: ~3 200 tokens estimated."

### Pipeline run metrics

The existing `run_log` table covers per-step observability. Add a `pipeline_runs`
summary table for the run-level view:

```sql
CREATE TABLE pipeline_runs (
  run_id         TEXT PRIMARY KEY,
  started_at     TEXT NOT NULL,
  finished_at    TEXT,
  status         TEXT NOT NULL DEFAULT 'running'
                 CHECK(status IN ('running','done','failed','partial')),
  companies_processed INTEGER DEFAULT 0,
  contacts_found      INTEGER DEFAULT 0,
  drafts_generated    INTEGER DEFAULT 0,
  llm_cost_usd        REAL DEFAULT 0,
  error_count         INTEGER DEFAULT 0
);
```

The dashboard sidebar shows the last N runs with these summary counts. Clicking
a run shows the per-step `run_log` grid (already implemented in `index.html`).

---

## 5. Go Project Structure

```
jobhunter/
  cmd/
    jobhunter/
      main.go               ← cobra root command + subcommand registration
  internal/
    tui/
      pipeline_view.go      ← Bubble Tea model for scan / enrich / generate (live per-company progress)
      scheduler_view.go     ← Bubble Tea model for schedule (countdown + last run summary)
      stats_view.go         ← Lip Gloss static render for stats (no event loop needed)
      setup_view.go         ← Huh multi-step form for first-time setup
      common.go             ← shared styles (Lip Gloss), colour palette, helper renderers
    pipeline/
      engine.go             ← Run, Step, pipeline executor, concurrency
      step.go               ← StepFn type, result types
    db/
      db.go                 ← connection, WAL, migrations, schema_migrations table
      companies.go          ← upsert, update, get, filter queries
      contacts.go           ← add, get, set_primary queries
      runs.go               ← run_log, pipeline_runs, llm_usage, gemini_usage
      scrape_cache.go       ← get, set, expire, archive
    llm/
      provider.go           ← Provider interface, CompletionRequest/Response types
      client.go             ← Client: rate limiter, retry, backoff, unified usage logging
      openrouter.go         ← OpenRouterProvider (exact tokens + cost)
      gemini_cli.go         ← GeminiCLIProvider (exec, estimated tokens)
      openai.go             ← OpenAIProvider (future, trivial once interface exists)
    scraper/
      fetcher.go            ← fetch_url: Jina → MCP → NEEDS_REVIEW, cache
      mcp.go                ← Blueprint MCP client (navigate, get content, search)
      jina.go               ← Jina client + quality check
    collector/
      sirene.go             ← Parquet stream + dept/NAF/headcount filter
      recherche.go          ← recherche-entreprises.api.gouv.fr client
      frenchtech.go         ← CSV import (one-time)
    enricher/
      enrich.go             ← run_enrichment(companyID): the main glue
      discover.go           ← URL discovery (website, LinkedIn URL)
      extract.go            ← RawCompanyPage struct + LLM extraction
      contacts.go           ← LinkedIn contact discovery via MCP
      classifier.go         ← llm_score_company, TECH_ADJACENT cap
    generator/
      drafts.go             ← career letter, cold email, LinkedIn hook
      profile.go            ← load profile.json
    guesser/
      guesser.go            ← email pattern candidates + SMTP verification
    api/
      server.go             ← chi router setup, static file serving
      jobs.go               ← /api/jobs handlers
      prospects.go          ← /api/prospects handlers
      runs.go               ← /api/runs, /api/usage, /api/health handlers
      drafts.go             ← /api/drafts handlers (V2)
    scheduler/
      scheduler.go          ← ticker loop, run at configured times, archive job
    errors/
      errors.go             ← JobHunterError types, sentinel errors
  migrations/
    001_contacts.sql        ← reuse as-is from Python version
    002_run_log.sql         ← reuse as-is
    003_llm_usage.sql       ← reuse as-is
    004_scrape_cache.sql    ← reuse as-is
    005_pipeline_runs.sql   ← NEW: pipeline_runs summary table
    006_gemini_usage.sql    ← NEW: gemini CLI estimated usage
    007_drafts.sql          ← NEW: drafts table (was V2 in Python plan)
  static/
    index.html              ← reuse as-is, zero changes needed
  data/
    sirene.parquet          ← already downloaded
    cache/                  ← archived markdown
  profile.json              ← your resume as structured data
  .env
  Taskfile.yml
  go.mod
  go.sum
```

---

## 6. Key Go Libraries

| Need | Library | Notes |
|---|---|---|
| SQLite | `modernc.org/sqlite` | Pure Go, no CGO, works everywhere. Prefer over `mattn/go-sqlite3` |
| HTTP router | `github.com/go-chi/chi/v5` | Lightweight, idiomatic, no magic |
| HTTP client | stdlib `net/http` | Sufficient for Jina + MCP + OpenRouter calls |
| Rate limiter | `golang.org/x/time/rate` | Token bucket, exactly what you need |
| Parquet | `github.com/xitongsys/parquet-go` | Pure Go streaming reads for SIRENE |
| JSON | stdlib `encoding/json` | Sufficient; add `github.com/tidwall/gjson` for quick field extraction |
| Config | `github.com/caarlos0/env/v11` | Struct tags on a single `Config` struct, validated at startup — cleaner than scattered `os.Getenv` calls |
| TUI framework | `github.com/charmbracelet/bubbletea` | Elm-architecture TUI loop — pipeline views, live progress, scheduler screen |
| TUI components | `github.com/charmbracelet/bubbles` | Spinner, progress bar, table, viewport, text input — use these before writing custom components |
| TUI styling | `github.com/charmbracelet/lipgloss` | Colors, borders, layout — replaces inline ANSI codes everywhere |
| TUI forms | `github.com/charmbracelet/huh` | Multi-step setup wizard (`jobhunter setup`) — purpose-built for this use case |
| CLI subcommands | `github.com/spf13/cobra` | Needed now that each subcommand has its own Bubble Tea model; Cobra dispatches to the right one |
| Logging | `go.uber.org/zap` | Structured fields + levels; `zap.String("run_id", id)` makes pipeline logs greppable. Use `zap.NewDevelopment()` locally, `zap.NewProduction()` (JSON) elsewhere |
| DNS (guesser) | stdlib `net` | MX lookup is built in |
| SMTP (guesser + send) | stdlib `net/smtp` | Built in |
| Fuzzy match (V3) | `github.com/lithammer/fuzzysearch` | For company name matching against `companies` table |

---

## 7. The TUI — Charm Stack

### Library breakdown

Charm's ecosystem is a coherent stack, not a single library. Each piece has a
distinct role:

| Library | Role | Used for |
|---|---|---|
| **Bubble Tea** | TUI event loop (Elm architecture) | Any view that updates in real time |
| **Bubbles** | Pre-built components | Spinner, progress bar, table, viewport, text input |
| **Lip Gloss** | Styling (colours, borders, layout) | All terminal styling — replaces ANSI codes |
| **Huh** | Form / wizard library | `setup` command only |
| **Cobra** | Subcommand routing | Dispatches `scan`, `enrich`, etc. to the right Bubble Tea model |

### Command → TUI mapping

Each subcommand gets the right treatment — not everything needs a full event loop:

**`jobhunter scan [city] [depts]`** → Bubble Tea app
Full-screen view. Header: run ID + elapsed time. Body: scrollable list of
companies being processed, each row cycling through `⠋ fetching` → `✓ saved` /
`✗ skipped`. Footer: running totals (found / new / skipped). Quits automatically
when the pipeline engine signals completion.

**`jobhunter enrich [batch]`** → Bubble Tea app
Same structure as scan. Each company row shows its current step:
`⠋ fetching` → `⠋ extracting` → `⠋ finding contacts` → `✓ contact found` /
`~ needs review`. Concurrency is visible — up to 3 rows animating simultaneously.

**`jobhunter generate`** → Bubble Tea app
Simpler: one row per company, spinner while LLM generates, then `✓ 3 drafts` or
`✗ error`. Progress bar at the top showing X/total done.

**`jobhunter stats`** → Lip Gloss only, no event loop
Render a styled table to stdout and exit. No `tea.NewProgram` needed — just
`lipgloss.NewStyle()` and `fmt.Println`. Done in ~30 lines.

**`jobhunter setup`** → Huh form
Multi-step wizard. Name → school → skills → start date → SMTP → API keys.
Each field validates on submit. Writes `.env` on completion. This is exactly
what Huh was designed for.

**`jobhunter schedule`** → Bubble Tea app
Persistent full-screen view. Shows: next scheduled run with live countdown,
last run summary (companies processed, contacts found, cost), a log viewport
scrolling recent activity. `q` to quit.

**`jobhunter dashboard`** → no TUI
Just print a Lip Gloss–styled banner (`http://localhost:8000`) and block on the
HTTP server. The dashboard is the web UI — no terminal UI needed here.

### The zap + Bubble Tea coexistence problem

Zap writes to stdout/stderr. Bubble Tea owns the terminal. If both write
simultaneously, the TUI corrupts. The fix:

During a Bubble Tea session, configure zap to write to a file only:

```go
// Before tea.NewProgram(model):
fileCore := zapcore.NewCore(
    zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
    zapcore.AddSync(logFile),
    zap.DebugLevel,
)
logger = zap.New(fileCore)
```

Surface log lines inside the TUI by sending them as Bubble Tea messages through
a channel. The pipeline engine pushes `LogMsg{Level, Text}` onto a channel; the
Bubble Tea model receives them via `tea.Cmd` and appends them to a `viewport`
component. The user sees live log output inside the TUI without stdout conflicts.

```go
// internal/tui/common.go
type LogMsg struct {
    Level string
    Text  string
}

// Pipeline engine pushes to this channel
// Bubble Tea model polls it via a tea.Cmd
func waitForLog(ch <-chan LogMsg) tea.Cmd {
    return func() tea.Msg {
        return <-ch
    }
}
```

This pattern — a channel bridge between background goroutines and the Bubble Tea
loop — is the standard way to drive TUI updates from async work.

### Lip Gloss colour palette

Define once in `internal/tui/common.go`, use everywhere:

```go
var (
    accent   = lipgloss.Color("#4af0a0")   // matches index.html --accent
    accent2  = lipgloss.Color("#4ab8f0")
    warn     = lipgloss.Color("#f0c44a")
    danger   = lipgloss.Color("#f04a6e")
    dim      = lipgloss.Color("#4a5268")
    surface  = lipgloss.Color("#13161b")

    tagTech     = lipgloss.NewStyle().Foreground(accent).Border(lipgloss.RoundedBorder())
    tagAdjacent = lipgloss.NewStyle().Foreground(accent2).Border(lipgloss.RoundedBorder())
    bold        = lipgloss.NewStyle().Bold(true)
    dimStyle    = lipgloss.NewStyle().Foreground(dim)
)
```

Keeping the palette consistent with `index.html`'s CSS variables means the
terminal and web UIs feel like the same product.

---

## 8. The Pipeline Engine in Detail

This replaces Dagster. Here is how it works:

```go
// engine.go

type Run struct {
    ID        string
    StartedAt time.Time
    Steps     []StepResult
    db        *DB
}

type StepResult struct {
    Name       string
    Status     string // ok | error | skipped | needs_review
    Error      error
    DurationMs int64
}

func (e *Engine) Execute(ctx context.Context, steps []Step) (*Run, error) {
    run := e.newRun()           // INSERT INTO pipeline_runs
    for _, step := range steps {
        result := e.runStep(ctx, run, step)
        run.Steps = append(run.Steps, result)
        if result.Status == "error" && step.StopOnError {
            break
        }
    }
    e.finalizeRun(run)          // UPDATE pipeline_runs SET status, finished_at
    return run, nil
}

func (e *Engine) runStep(ctx context.Context, run *Run, step Step) StepResult {
    start := time.Now()
    e.logStep(run.ID, step.Name, "running", nil, 0)  // INSERT run_log

    ctx, cancel := context.WithTimeout(ctx, step.Timeout)
    defer cancel()

    err := step.Fn(ctx, run)
    duration := time.Since(start).Milliseconds()

    status := "ok"
    if err != nil { status = "error" }

    e.logStep(run.ID, step.Name, status, err, duration)  // UPDATE run_log
    return StepResult{Name: step.Name, Status: status, Error: err, DurationMs: duration}
}
```

**For the enrichment stage**, where you run the same step for N companies in
parallel:

```go
func enrichAllCompanies(ctx context.Context, run *Run) error {
    companies, _ := run.db.GetCompaniesForEnrichment()

    sem := make(chan struct{}, 3) // max 3 concurrent
    var wg sync.WaitGroup
    var mu sync.Mutex
    var errs []error

    for _, company := range companies {
        wg.Add(1)
        go func(c Company) {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()

            err := enrichSingleCompany(ctx, run, c)
            if err != nil {
                mu.Lock()
                errs = append(errs, err)
                mu.Unlock()
            }
        }(company)
    }

    wg.Wait()
    // partial failure is fine — individual errors are in run_log
    return nil
}
```

This gives you: parallel execution, per-company error isolation (one failure
doesn't stop the others), full observability in `run_log`, and resumability
(re-running the pipeline skips companies that already have `status != NEW`).

---

## 9. Build Order

Do these in strict order. Each step produces something runnable or testable
before moving on.

| Step | Package | What | Testable when done |
|---|---|---|---|
| 1 | `internal/errors` | Error types | Import compiles |
| 2 | `internal/db` | Connection, migrations, all query functions | `go run . stats` shows empty DB |
| 3 | `internal/llm` | `Provider` interface + `Client` (rate limiter, retry, usage logging) + `OpenRouterProvider` | `go run . test-llm` calls API and logs to `llm_usage` |
| 3b | `internal/llm/gemini_cli.go` | `GeminiCLIProvider` — same interface, estimated tokens | Fallback works transparently |
| 4 | `internal/tui/common.go` | Lip Gloss palette + `LogMsg` channel bridge | Styles compile, colours match web UI |
| 5 | `internal/tui/stats_view.go` | Lip Gloss stats table (no event loop) | `go run . stats` renders styled output |
| 6 | `internal/collector/sirene.go` | DuckDB shell-out + NAF/headcount filter + import to `companies` | `go run . scan Poitiers 86` populates DB with real data |
| 7 | `internal/enricher/classifier.go` | LLM scoring + TECH / TECH_ADJACENT / NON_TECH cap | Companies get types and scores against real SIRENE rows |
| 8 | `internal/pipeline/engine.go` | Run + Step executor + run_log writes | `go run . run` creates a run in DB |
| 9 | `internal/scraper/jina.go` | Jina fetch + quality check | Fetch any URL, see markdown |
| 10 | `internal/scraper/mcp.go` | Blueprint MCP client | Navigate to LinkedIn, see HTML |
| 11 | `internal/scraper/fetcher.go` | Jina → MCP → NEEDS_REVIEW + cache | Full fetch flow works |
| 12 | `internal/enricher/extract.go` | RawCompanyPage + LLM extraction | Parse a fetched page for a real company from DB |
| 13 | `internal/enricher/contacts.go` | LinkedIn contact discovery via MCP | Find a contact for a real company |
| 14 | `internal/enricher/enrich.go` | **The glue** — full enrichment flow end-to-end | Enrichment runs against real SIRENE-sourced companies |
| 15 | `internal/tui/pipeline_view.go` | Bubble Tea pipeline model (spinner list + log viewport) | `go run . enrich` shows live progress |
| 16 | `internal/api` | chi router + all existing handlers | Dashboard loads, runs tab works |
| **Core done** | | | |
| 17 | `internal/generator/drafts.go` | Career letter + email + LinkedIn hook | Drafts appear in dashboard |
| 18 | `internal/guesser` | Email pattern + SMTP verify | `go run . guess-emails` works |
| 19 | `internal/scheduler` + `tui/scheduler_view.go` | Ticker loop + archive job + schedule screen | `go run . schedule` shows countdown UI |
| 20 | `internal/tui/setup_view.go` | Huh multi-step form | `go run . setup` walks through config wizard |
| **V1 complete** | | | |

Steps 1–5 are infrastructure and TUI foundation. Steps 6–7 seed the DB with
real data immediately — every subsequent step runs against actual companies, not
fixtures. Steps 8–14 build the enrichment pipeline on top of that real data.
Steps 15–20 complete the product.

---

## 10. What to Reuse From the Python Version

| File | Action | What to take |
|---|---|---|
| `migrations/*.sql` | Copy as-is | All four files, unchanged |
| `static/index.html` | Copy as-is | Served by Go's `http.FileServer` |
| `Taskfile.yml` | Adapt | Replace `python jobhunter.py X` with `go run . X` |
| `llm.py` | Read as reference | Retry logic, backoff math, `complete_json` pattern |
| `scraper/fetcher.py` | Read as reference | Quality check logic, cache TTL config |
| `scraper/parsers/careers_page.py` | Read as reference | System prompt, `RawCompanyPage` fields → Go struct |
| `scraper/pipeline.py` | Read as reference | Step wrapper pattern → Go function wrapper |
| `prospector.py` | Read as reference | `_headcount_label` map, SIRENE column names, enrichment prompt |
| `errors.py` | Read as reference | Error type names → Go error types |
| Everything else | Ignore | Rewrite fresh — it's simpler in Go |

The `.env` key names stay identical. `profile.json` stays identical.
The SQL migration files are language-agnostic and are a direct copy.

---

## 11. The One Decision to Make Before Starting

**Parquet reading for SIRENE.**

Option A: `xitongsys/parquet-go`
- Pure Go, no external binary
- API is verbose but works
- Streaming batch reads handle the 2GB file fine
- ~2–3h to get right the first time

Option B: Shell out to DuckDB
```go
cmd := exec.Command("duckdb", "-csv", "-c",
    `SELECT siren, denominationUsuelleEtablissement, ...
     FROM 'data/sirene.parquet'
     WHERE codePostalEtablissement LIKE '86%'
     AND etatAdministratifEtablissement = 'A'`)
out, _ := cmd.Output()
// parse CSV
```
- DuckDB binary is a single 30MB static download
- SQL is much more readable than the Parquet-go API
- Zero library friction, done in 30 minutes
- Adds a binary dependency

**Recommendation:** Option B (DuckDB shell-out) to unblock yourself quickly.
You can always replace it with Option A later. The SIRENE scan is not
performance-critical — it runs once per city, not in the hot path.
