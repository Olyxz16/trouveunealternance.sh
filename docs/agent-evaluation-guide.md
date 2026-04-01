# Agent Evaluation Guide

This document explains how an autonomous agent should use the evaluation suite to measure the impact of its changes and work independently.

## Why This Exists

You are an agent tasked with improving the enrichment pipeline. You cannot rely on subjective judgment — you need hard numbers to know if your changes help or hurt. The `eval` command gives you that.

## Quick Start

```bash
task eval          # evaluate the last 20 enriched companies
task eval-all      # evaluate every company in the database
```

## The Two Benchmarks

### Company Benchmark (0-100)
Measures whether basic company URLs were found:
- **Website found** — 50 pts (must be a real domain, not a directory like societe.com)
- **LinkedIn URL found** — 50 pts (must be a valid `linkedin.com/company/` URL)

This is a gate check. If a company scores 100 here, its online presence was found. Below 100 means something is missing.

### Contact Benchmark (0-100, PRIMARY KPI)
This is the metric you should optimize for. It measures whether useful contacts were found:

| Criterion | Points |
|-----------|--------|
| At least 1 contact found | 15 |
| Primary contact identified | 15 |
| Primary contact has valid LinkedIn (`/in/` URL) | 20 |
| Primary contact has valid personal email | 25 |
| Contact has a real role/title | 10 |
| Contact confidence is "probable" or better | 15 |

### Penalties (deducted from contact score, can go negative)

| Issue | Penalty |
|-------|---------|
| Hallucinated contact saved | -20 each |
| Placeholder name (John Doe, etc.) | -20 each |
| **User listed as a contact** | **-50** |
| Generic email as personal contact (contact@, info@, etc.) | -15 each |
| Invalid LinkedIn URL (e.g. `/company/` instead of `/in/`) | -10 each |
| Directory site passed as website | -10 |

### Score Tiers

| Score | Meaning |
|-------|---------|
| 80-100 | Excellent — ready for outreach |
| 60-79 | Good — minor gaps |
| 40-59 | Partial — needs manual review |
| 20-39 | Poor — mostly failed |
| 0-19 | Failed |
| < 0 | Critical — hallucinations or contamination |

## How an Agent Should Work

### Step 1: Establish a Baseline

Before making any changes, run the evaluation on the current state:

```bash
task eval
```

Note the key numbers:
- **Contact Benchmark Average** — this is your north star
- **Valid Rate** — % of companies with a contact scoring >= 60
- **Hallucination Rate** — % of contacts that are hallucinated
- **Total Penalties** — count of all penalty triggers

Save the JSON report for later comparison:

```bash
go run . eval --output data/eval
```

The report file will be at `data/eval/eval-<timestamp>-<runid>.json`.

### Step 2: Make Your Changes

Implement your improvement. Commit incrementally as you work.

### Step 3: Re-run Enrichment

If your changes affect how enrichment works, you need fresh data to evaluate. Use a small batch:

```bash
# Backup current DB first
cp data/jobs.db data/jobs.db.bak.before-<your-change>

# Reset and re-scan (or just wipe and re-enrich scored companies)
task reset
# or, if you only want to re-enrich:
task wipe
task scan
task enrich   # runs on BATCH=20 by default
```

The `BATCH` variable is set to 20 in the Taskfile. Keep experiments small to save cost and time.

### Step 4: Evaluate Again

```bash
task eval
```

Compare the new numbers against your baseline.

### Step 5: Decide

| Outcome | Action |
|---------|--------|
| Contact score improved, hallucinations stable or down | Keep the change |
| Contact score improved but hallucinations increased | Investigate — the gain may be illusory |
| Contact score dropped | Revert the change |
| Contact score unchanged | The change is neutral — keep if it improves other metrics (speed, cost) |

## CLI Flags

| Flag | Description |
|------|-------------|
| `--batch, -b` | Number of companies to evaluate (default: 20) |
| `--all, -a` | Evaluate all companies in the database |
| `--json` | Output only the JSON report to stdout (useful for piping) |
| `--output` | Directory to save the JSON report (default: `data/eval`) |

## JSON Report Structure

The JSON report contains everything needed for automated comparison:

```json
{
  "run_id": "uuid",
  "timestamp": "2026-04-01T...",
  "metadata": {
    "llm_primary_provider": "openrouter",
    "llm_primary_model": "google/gemini-2.0-flash-lite:free",
    "llm_fallback_provider": "gemini_api",
    "gemini_api_enabled": true,
    "gemini_model": "gemini-2.0-flash-lite",
    "browser_enabled": true,
    "batch_size": 20,
    "duration_seconds": 342.5,
    "commit_hash": "abc1234"
  },
  "aggregate": {
    "company_benchmark": { "average_score": 72.5, "pass_rate": 0.65 },
    "contact_benchmark": { "average_score": 45.0, "valid_rate": 0.30 },
    "hallucination_rate": 0.08,
    "total_penalties": 5,
    "penalty_breakdown": {
      "hallucinated_contact": 2,
      "generic_email": 1
    }
  },
  "companies": [
    {
      "id": 1,
      "name": "Acme Corp",
      "company_score": 100,
      "contact_score": 85,
      "contacts_count": 2,
      "contact_details": ["Jane D. (CTO) — jane@acme.fr"],
      "penalties": [],
      "status": "TO_CONTACT"
    }
  ]
}
```

## Comparing Reports Programmatically

Use `--json` to compare two runs in a script:

```bash
# Run before changes
go run . eval --json > /tmp/before.json

# ... make changes, re-enrich ...

# Run after changes
go run . eval --json > /tmp/after.json

# Compare key metrics
jq '.aggregate.contact_benchmark.average_score' /tmp/before.json /tmp/after.json
jq '.aggregate.hallucination_rate' /tmp/before.json /tmp/after.json
```

## Important Notes

- **The commit hash is recorded** in every report. A report is only valid for the exact code state it was generated from. If you change the code, old reports are invalidated.
- **Always backup the database** before wiping or re-enriching. Use `cp data/jobs.db data/jobs.db.bak.<description>`.
- **Keep batches small** (BATCH=20) during experiments. You can always run `eval-all` later on the full dataset.
- **The user-as-contact penalty is -50** — this is the harshest penalty because it means the system is returning the student's own profile as a company contact, which is a critical failure. Check your `profile.json` name field.
- **Negative scores are possible** — a company can have more penalties than points. This is intentional and signals a serious problem.

## Common Scenarios

### "I changed the URL discovery logic"
1. Baseline: `task eval`
2. Change code
3. Wipe DB, rescan, re-enrich: `task reset && task enrich`
4. Evaluate: `task eval`
5. Compare company benchmark (website + linkedin scores)

### "I changed the contact extraction prompt"
1. Baseline: `task eval`
2. Change code
3. Wipe DB, rescan, re-enrich: `task reset && task enrich`
4. Evaluate: `task eval`
5. Compare contact benchmark and hallucination rate

### "I added a new fallback mechanism"
1. Baseline: `task eval`
2. Change code
3. Re-enrich only the companies that previously failed (status `NO_CONTACT_FOUND`)
4. Evaluate: `task eval`
5. Check if valid_rate improved without increasing hallucinations
