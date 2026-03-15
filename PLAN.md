# JobHunter — Technical Roadmap (Go)

> Living reference document. Reflects the actual Go codebase.
> Python POC is archived in `old/`. All decisions below apply to the Go rewrite.

---

## Architecture Overview

| Concern | Decision |
|---|---|
| Language | Go 1.25 |
| CLI | Cobra (`cmd/` flat package) |
| Database | SQLite via `modernc.org/sqlite` (pure Go, no CGO), WAL mode |
| Migrations | Embedded SQL files in `internal/db/migrations/`, applied at startup |
| LLM | `Provider` interface — OpenRouter primary, Gemini CLI fallback |
| Scraping | `Fetcher` interface — `HTTPFetcher` primary, `MCPFetcher` fallback via `CascadeFetcher` |
| MCP browser | Local HTTP bridge (`cmd bridge`) wrapping chromedp — runs your real Chrome session |
| HTML extraction | Trafilatura → Readability → raw, then html-to-markdown |
| Pipeline observability | `pipeline_runs` + `run_log` tables, written by `internal/pipeline/engine.go` |
| Config | `github.com/caarlos0/env/v11` struct tags + `godotenv` for `.env` loading |
| Logging | `go.uber.org/zap` — structured fields, redirected to file during TUI sessions |
| TUI | Charm stack: Bubble Tea + Bubbles + Lip Gloss + Huh |
| Web dashboard | Single-file `static/index.html` served by chi (not yet built) |

---

## Current File Structure

```
jobhunter/
  main.go
  cmd/
    root.go               ← cobra root, config + DB init in PersistentPreRun
    scan.go               ← scan + score subcommands
    enrich.go             ← enrich subcommand
    stats.go              ← stats subcommand
    bridge.go             ← bridge subcommand (starts local MCP HTTP server)
  internal/
    config/
      config.go           ← typed Config struct via caarlos0/env
    errors/
      errors.go           ← JobHunterError hierarchy (scraping, LLM, enrichment, DB)
    db/
      db.go               ← connection, WAL, embedded migration runner
      companies.go        ← UpsertCompany, UpdateCompany, GetCompany, GetCompaniesForEnrichment
      contacts.go         ← AddContact, GetContacts
      runs.go             ← CreatePipelineRun, FinalizePipelineRun, LogStep, InsertLLMUsage, InsertGeminiUsage
      scrape_cache.go     ← GetCache, SetCache
      migrations/
        000_base.sql      ← companies, jobs, activity_log base schema
        001_contacts.sql  ← contacts table + data migration from legacy columns
        002_run_log.sql   ← run_log table
        003_llm_usage.sql ← llm_usage table
        004_scrape_cache.sql ← scrape_cache table
        005_pipeline_runs.sql ← pipeline_runs summary table
        006_gemini_usage.sql  ← gemini_usage estimated table
        007_drafts.sql    ← drafts table
    llm/
      provider.go         ← Provider interface, CompletionRequest, CompletionResponse
      client.go           ← Client: rate limiter, retry+backoff, unified logUsage, CompleteJSON
      openrouter.go       ← OpenRouterProvider (exact tokens + cost from headers)
      gemini_cli.go       ← GeminiCLIProvider (exec -p flag, estimated tokens)
    pipeline/
      engine.go           ← Engine, Run, Step, Execute, runStep, run_log writes
    scraper/
      fetcher.go          ← Fetcher interface, FetchResult type
      http.go             ← HTTPFetcher (net/http, browser-like headers)
      mcp.go              ← MCPFetcher (calls local bridge on /fetch)
      mcp_server.go       ← MCPServer (local HTTP server wrapping chromedp)
      cascade.go          ← CascadeFetcher: cache, forceMCP domains, primary→fallback
      extractor.go        ← Trafilatura → Readability → raw HTML cascade + quality check
      preprocessors.go    ← preprocess() dispatcher, preprocessLinkedIn()
      markdown.go         ← ToMarkdown() wrapping html-to-markdown/v2
    collector/
      sirene.go           ← DuckDB shell-out, single parquet file, NAF/headcount filter
      recherche.go        ← recherche-entreprises.api.gouv.fr HTTP client
    enricher/
      classifier.go       ← Classifier: ScoreCompany (LLM scoring + DB update)
      extract.go          ← RawCompanyPage struct, ExtractCompanyInfo (LLM extraction)
      contacts.go         ← DiscoverLinkedInContactsWithMD (LLM contact selection)
      discover.go         ← URLDiscoverer: DuckDuckGo search → LinkedIn URL extraction
      enrich.go           ← Enricher: EnrichCompany (full glue: name fix → URLs → fetch → extract → contacts)
    tui/
      common.go           ← Lip Gloss palette, LogMsg type, WaitForLog Cmd
      stats_view.go       ← RenderStats() — Lip Gloss static table, no event loop
  static/                 ← not yet created
  data/
    sirene.parquet        ← SIRENE StockEtablissement (single file, current)
    cache/                ← archived markdown (future)
  go.mod
  go.sum
  Taskfile.yml
  .env
```

---

## What Is Built ✅

**`internal/errors`** — full error hierarchy.

**`internal/db`** — WAL connection, embedded migration runner, all query
functions. Migrations 000–007 cover all tables.

**`internal/llm`** — `Provider` interface with `OpenRouterProvider` (exact
tokens + cost) and `GeminiCLIProvider` (headless `-p` flag, estimated tokens).
`Client` has token-bucket rate limiter, exponential backoff, JSON parse retry,
unified `logUsage` routing to `llm_usage` or `gemini_usage`.

**`internal/pipeline/engine.go`** — sequential step executor, creates and
finalizes `pipeline_runs`, writes each step result to `run_log`.

**`internal/scraper`** — complete cascade:
- `HTTPFetcher` with browser-like headers
- `MCPFetcher` calling local bridge at `MCP_HOST/fetch`
- `MCPServer` — local HTTP bridge wrapping `chromedp` for real Chrome sessions,
  started by `go run . bridge`. This is how LinkedIn and bot-protected pages are
  fetched with session persistence.
- `CascadeFetcher` — cache → forceMCP domains → HTTP → MCP fallback
- `Extractor` — Trafilatura → Readability → raw, quality gated at 500 chars
- `markdown.go` — html-to-markdown/v2

**`internal/collector`** — `SireneCollector` (DuckDB shell-out, single Parquet)
and `RechercheClient` (free French company search API).

**`internal/enricher`** — full end-to-end glue: `ScoreCompany`, `ExtractCompanyInfo`,
`DiscoverLinkedInContactsWithMD`, `DiscoverURLs`, `EnrichCompany`.

**`internal/tui`** — Lip Gloss palette and static stats view.

**`cmd/`** — `scan`, `score`, `enrich`, `stats`, `bridge` all functional.

---

## Known Gaps & Bugs to Fix First

### Gap 1 — SIRENE name resolution (`Company X` rows) 🔴

**Root cause:** `sirene.go` falls back to `"Company " + siren` when both
`denominationUsuelleEtablissement` and `enseigne` fields are blank. This affects
a large portion of the DB and breaks enrichment (LLM searches for the wrong name).

**Fix — requires three changes:**

1. Download `StockUniteLegale` Parquet (~200MB, same data.gouv.fr dataset):
```yaml
download-sirene-ul:
  cmds:
    - curl -L -o data/sirene_unites_legales.parquet <url>
```

2. Update `sirene.go` DuckDB query to JOIN both files:
```sql
SELECT
    e.siren, e.siret,
    COALESCE(
        NULLIF(TRIM(e.enseigne1Etablissement), ''),
        NULLIF(TRIM(e.denominationUsuelleEtablissement), ''),
        NULLIF(TRIM(ul.denominationUniteLegale), ''),
        NULLIF(TRIM(ul.sigleUniteLegale), '')
    ) AS name_raw,
    ul.denominationUniteLegale AS legal_name,
    ul.sigleUniteLegale        AS acronym,
    ...
FROM 'data/sirene_etablissements.parquet' e
JOIN 'data/sirene_unites_legales.parquet' ul ON e.siren = ul.siren
WHERE e.etatAdministratifEtablissement = 'A'
  AND ul.etatAdministratifUniteLegale  = 'A'
```

3. Add `internal/collector/names.go`:
```go
// Strip "SAS", "SARL", "SASU", "SA", "SNC", "SCI", "EURL", "SELARL" suffixes
func cleanCompanyName(raw string) string

// lowercase + remove accents + collapse whitespace — used for dedup and fuzzy match
func normalizeName(s string) string
```

4. Write `internal/db/migrations/008_company_names.sql`:
```sql
ALTER TABLE companies ADD COLUMN legal_name      TEXT;
ALTER TABLE companies ADD COLUMN acronym         TEXT;
ALTER TABLE companies ADD COLUMN name_normalized TEXT;
CREATE INDEX IF NOT EXISTS idx_companies_name_norm ON companies(name_normalized);
```

5. Add `LegalName`, `Acronym`, `NameNormalized` to `db.Company` and update
`UpsertCompany`. Add `SIRENE_UL_PARQUET_PATH` to `config.go`.

---

### Gap 2 — `scrape_cache` method CHECK is wrong 🔴

`004_scrape_cache.sql` has `CHECK(method IN ('jina','mcp','manual','cache'))`.
The codebase uses `'http'`. Cache writes silently fail.

**Fix — write `009_fix_scrape_cache_method.sql`:**
```sql
CREATE TABLE scrape_cache_new (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  url         TEXT NOT NULL UNIQUE,
  method      TEXT NOT NULL CHECK(method IN ('http','mcp','manual','cache')),
  content_md  TEXT NOT NULL,
  quality     REAL NOT NULL DEFAULT 1.0,
  fetched_at  TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at  TEXT NOT NULL
);
INSERT INTO scrape_cache_new SELECT * FROM scrape_cache;
DROP TABLE scrape_cache;
ALTER TABLE scrape_cache_new RENAME TO scrape_cache;
CREATE INDEX IF NOT EXISTS idx_scrape_cache_url        ON scrape_cache(url);
CREATE INDEX IF NOT EXISTS idx_scrape_cache_expires_at ON scrape_cache(expires_at);
```

---

### Gap 3 — Invalid company statuses 🔴

`enrich.go` writes `status = 'ENRICHED'` and the plan requires `'FAILED'` and
`'NEEDS_REVIEW'`, but the CHECK constraint in `000_base.sql` only allows
`NEW, ENRICHING, TO_CONTACT, CONTACTED, REPLIED, NOT_TECH, PASS`.

**Fix — write `010_company_status.sql`** to drop the constraint and enforce
valid statuses at the Go layer. Valid set going forward:
`NEW`, `ENRICHING`, `ENRICHED`, `TO_CONTACT`, `NO_CONTACT_FOUND`, `CONTACTED`,
`REPLIED`, `NOT_TECH`, `NEEDS_REVIEW`, `FAILED`, `PASS`.

```sql
-- Recreate without CHECK to allow the full status set
CREATE TABLE companies_new AS SELECT * FROM companies;
DROP TABLE companies;
ALTER TABLE companies_new RENAME TO companies;
CREATE INDEX IF NOT EXISTS idx_companies_status ON companies(status);
CREATE INDEX IF NOT EXISTS idx_companies_city   ON companies(city);
CREATE INDEX IF NOT EXISTS idx_companies_score  ON companies(relevance_score DESC);
```

---

### Gap 4 — `enrich.go` final status logic 🟡

When enrichment runs but finds no contact, the final status is set to
`'ENRICHED'` (only if still `'NEW'`). It should be `'NO_CONTACT_FOUND'`:

```go
// Replace the final block in EnrichCompany:
if comp.Status == "NEW" {
    _ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
        "status": "NO_CONTACT_FOUND",
    })
}
```

---

### Gap 5 — Dead code in `contacts.go` 🟢

`DiscoverURL()` in `internal/enricher/contacts.go` returns
`fmt.Errorf("not implemented")`. Fetching is correctly done via `Enricher.fetcher`
in `enrich.go`. Remove this stub to avoid confusion.

---

## What Remains to Build

### Step 15 — `internal/tui/pipeline_view.go`

Bubble Tea model for `scan` and `enrich` commands. Replace raw `fmt.Printf`
progress output with a live full-screen view.

Layout: header (run ID + elapsed time), scrollable list of company rows
(`⠋ scoring…` → `✓ TECH (7)` / `✗ error`), footer with aggregate counts.

Wire via `LogMsg` channel already in `common.go`:
```go
logCh := make(chan tui.LogMsg, 100)
// enricher pushes to logCh
// cmd/enrich.go: tea.NewProgram(tui.NewPipelineModel(logCh)).Run()
```

The `scan` and `enrich` commands need updating to launch the model.

---

### Step 16 — `internal/api/` + `static/index.html` + `cmd/dashboard.go`

Chi router serving the web dashboard.

```
internal/api/
  server.go      ← chi setup, embed static/index.html, /api/events SSE
  prospects.go   ← GET /api/prospects, GET /api/prospects/{id}, PATCH /api/prospects/{id}
                   GET /api/prospects/{id}/contacts
  runs.go        ← GET /api/runs, GET /api/runs/{id}
                   GET /api/usage/today, GET /api/usage/history
                   GET /api/health
  drafts.go      ← GET /api/drafts, PATCH /api/drafts/{id}, POST /api/drafts/{id}/send
```

`static/index.html` — port the existing Python dashboard design. The HTML/JS
is already fully designed; it just needs to be served by Go. All API endpoints
it calls are defined above.

`cmd/dashboard.go` — starts the chi server on `:8080`.

---

### Step 17 — `internal/generator/` + `cmd/generate.go`

Draft generation for companies with `status = TO_CONTACT`.

```
internal/generator/
  profile.go   ← LoadProfile() reads profile.json
  drafts.go    ← GenerateDrafts(companyID, runID): writes career_page + cold_email + linkedin_hook to drafts table
```

One LLM call per contact generating both email and LinkedIn hook together to
save tokens. Angle matrix:

| company_type | contact role | angle |
|---|---|---|
| TECH | CTO / tech lead | technically specific, reference their stack |
| TECH | HR | impact and team fit |
| TECH_ADJACENT | IT director / CIO | internal tooling, adaptability, infra |

`cmd/generate.go` — batch generation with configurable `--batch` flag.

---

### Step 18 — `internal/guesser/` + `cmd/guess-emails.go`

Email pattern guessing for `status = NO_CONTACT_FOUND` companies.

```
internal/guesser/
  guesser.go  ← GenerateCandidates(first, last, domain), VerifyEmailSMTP(), EnrichMissingEmails()
```

Uses stdlib `net` for MX lookups, `net/smtp` for RCPT TO verification.
Treat results as `'guessed'` confidence, not verified.

---

### Step 19 — `internal/scheduler/` + `internal/tui/scheduler_view.go` + `cmd/schedule.go`

Scheduled daily runs and nightly cache archival.

```
internal/scheduler/
  scheduler.go  ← ticker loop at configured times, run full pipeline, archive scrape_cache rows >30 days
```

`tui/scheduler_view.go` — Bubble Tea screen showing next run countdown + last
run summary + live log viewport.

Nightly archive job: move `scrape_cache` rows older than 30 days to
`data/cache/{YYYY-MM}/{domain}/{hash}.md` and delete from SQLite.

---

### Step 20 — `internal/tui/setup_view.go` + `cmd/setup.go`

Huh multi-step wizard for first-time configuration. Walks through: name →
school → skills → start date → SMTP credentials → API keys. Writes `.env` on
completion.

---

## Database Migrations — Full List

| File | Status | Contents |
|---|---|---|
| `000_base.sql` | ✅ | companies, jobs, activity_log |
| `001_contacts.sql` | ✅ | contacts table, legacy data migration |
| `002_run_log.sql` | ✅ | run_log table |
| `003_llm_usage.sql` | ✅ | llm_usage (exact) |
| `004_scrape_cache.sql` | ✅ applied, ⚠️ wrong CHECK | scrape_cache — fix in 009 |
| `005_pipeline_runs.sql` | ✅ | pipeline_runs summary |
| `006_gemini_usage.sql` | ✅ | gemini_usage (estimated) |
| `007_drafts.sql` | ✅ | drafts table |
| `008_company_names.sql` | ❌ write first | legal_name, acronym, name_normalized |
| `009_fix_scrape_cache_method.sql` | ❌ write first | fix 'jina'→'http' CHECK |
| `010_company_status.sql` | ❌ write first | drop status CHECK, allow full set |

Migrations 008, 009, 010 are blockers for clean operation. Write them before
continuing with steps 15+.

---

## Config Reference (`.env`)

```env
DB_PATH=data/jobs.db

# SIRENE (add second line after gap 1 is fixed)
SIRENE_PARQUET_PATH=data/sirene.parquet
SIRENE_UL_PARQUET_PATH=data/sirene_unites_legales.parquet

# LLM
LLM_PRIMARY=openrouter
LLM_FALLBACK=gemini_cli
OPENROUTER_API_KEY=sk-or-...
OPENROUTER_MODEL=google/gemini-2.5-flash-lite
OPENROUTER_RPM=60
GEMINI_CLI_PATH=gemini

# Scraping
MCP_HOST=http://localhost:3000
FORCE_MCP_DOMAINS=linkedin.com

# Email (Step 18+)
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USER=you@gmail.com
SMTP_PASS=your_app_password
YOUR_EMAIL=you@example.com
```

---

## Key Libraries (go.mod)

| Need | Library | In use |
|---|---|---|
| SQLite | `modernc.org/sqlite` | ✅ |
| Config | `github.com/caarlos0/env/v11` | ✅ |
| .env | `github.com/joho/godotenv` | ✅ |
| Logging | `go.uber.org/zap` | ✅ |
| CLI | `github.com/spf13/cobra` | ✅ |
| Rate limiter | `golang.org/x/time/rate` | ✅ |
| UUID | `github.com/google/uuid` | ✅ |
| HTML extraction | `github.com/markusmobius/go-trafilatura` | ✅ |
| HTML extraction | `github.com/go-shiori/go-readability` | ✅ |
| HTML → Markdown | `github.com/JohannesKaufmann/html-to-markdown/v2` | ✅ |
| Browser automation | `github.com/chromedp/chromedp` | ✅ |
| MCP protocol | `github.com/metoro-io/mcp-golang` | ✅ |
| TUI | `github.com/charmbracelet/bubbletea` | ✅ |
| TUI components | `github.com/charmbracelet/bubbles` | ✅ |
| TUI styling | `github.com/charmbracelet/lipgloss` | ✅ |
| TUI forms | `github.com/charmbracelet/huh` | in go.mod, step 20 |
| HTTP router | `github.com/go-chi/chi/v5` | ❌ add for step 16 |
| Fuzzy match | `github.com/lithammer/fuzzysearch` | ❌ add for V3 |

---

## V3 — Job Board Scraping (Future)

Add job board scrapers after V1 is complete. Each source is a plugin:

```
internal/scraper/parsers/
  wttj.go        ← Welcome to the Jungle
  indeed.go      ← Indeed France
  lesjeudis.go   ← Lesjeudis
```

LinkedIn Jobs via MCP (session required). All others: HTTP primary, MCP fallback.

Company linking: fuzzy match scraped company names against `companies.name_normalized`
using `lithammer/fuzzysearch` at threshold 85. Match increments `relevance_score`
(capped at 10). Dashboard "hot leads" filter: contact found + recent matched job.
