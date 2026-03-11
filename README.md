# JobHunter 🎯

Automated DevOps & backend internship pipeline using **Gemini CLI + Blueprint MCP**.  
Two-track approach: direct internship listings + company leads for cold outreach.

---

## Architecture

```
jobhunter/
├── jobhunter.py      # Main CLI entry point
├── db.py             # SQLite layer (WAL mode, dedup, activity log)
├── classifier.py     # LLM pre-filter: score + tech stack extraction
├── scraper.py        # Stage 1 & 2: Gemini prompts + output parsing
├── emailer.py        # Stage 3: draft emails + interactive approval + SMTP send
├── guesser.py        # Email pattern guesser + SMTP verification
├── scheduler.py      # Daily cron runner with diff detection
├── api.py            # FastAPI backend + SSE for live dashboard
├── static/
│   └── index.html    # Local web dashboard (single file)
├── data/
│   ├── jobs.db       # SQLite database (auto-created)
│   └── prompts/      # Generated Gemini CLI prompt files
├── emails/           # Drafted cold emails (.md files)
├── logs/             # Run logs
└── .env              # Your config (create with: python jobhunter.py setup)
```

---

## Setup

### 1. Prerequisites
```bash
# Gemini CLI
npm install -g @google/generative-ai-cli

# Blueprint MCP (for Firefox)
npm install -g @railsblueprint/blueprint-mcp
# Install Firefox extension from addons.mozilla.org (search "Blueprint MCP")
# Click the extension icon → Start Server

# Python deps
pip install -r requirements.txt
```

### 2. First-time config
```bash
python jobhunter.py setup
```
This walks you through creating your `.env` file with your profile, SMTP credentials, and Gemini API key.

Or create `.env` manually:
```env
YOUR_NAME=Your Name
YOUR_SCHOOL=ENSEIRB-MATMECA, 2nd year
YOUR_SKILLS=Python, Docker, Linux, CI/CD, Git, Kubernetes basics
INTERNSHIP_DURATION=6
START_DATE=September 2025
YOUR_INTERESTS=infrastructure, distributed systems, backend architecture
YOUR_EMAIL=you@example.com

SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USER=you@gmail.com
SMTP_PASS=your_app_password   # Gmail: Settings → Security → App passwords

GEMINI_API_KEY=your_key_here  # Optional but improves classification accuracy
```

---

## Usage

### Interactive pipeline (recommended)

```bash
# Stage 1 — scrape all sites alongside you in Firefox
python jobhunter.py scrape

# Stage 2 — enrich company leads with contact info
python jobhunter.py enrich

# Guess missing emails for contacts
python jobhunter.py guess-emails

# Stage 3 — review drafts and send (one by one, you approve each)
python jobhunter.py emails
```

### Dashboard
```bash
python jobhunter.py dashboard
# → open http://localhost:8000
```

Dashboard features:
- Live stats (total, today, by status)
- Filter by status + type + search
- Click any row to see details + contact info
- Generate email draft from listing
- Update status with one click
- Trigger Stage 1/2 from the UI
- Export everything to TSV

### Scheduled daily runs
```bash
python jobhunter.py schedule   # Runs at 8:30 and 18:00, notifies you of new listings
python jobhunter.py once       # Run full pipeline right now
```

### Generate prompts only (manual mode)
```bash
python jobhunter.py prompts
# → saves prompt .txt files to data/prompts/
# Paste them into Gemini CLI manually if you prefer
```

---

## Status Flow

| Status | Meaning | Next action |
|---|---|---|
| `TO_APPLY` | Direct internship found | Apply via the listing URL |
| `TO_ENRICH` | Company lead, needs contact research | Auto: Stage 2 |
| `TO_CONTACT` | Contact found, draft ready | Stage 3: approve + send |
| `NO_CONTACT_FOUND` | Stage 2 found nothing | Try `guess-emails` or manual search |
| `CONTACTED` | Email / LinkedIn message sent | Wait for reply |
| `REPLIED` | They replied! | Follow up |
| `PASS` | Not relevant | — |

---

## Tips

- **Blueprint MCP uses your real Firefox session** — you're already logged into LinkedIn, WTTJ, etc. Step in for CAPTCHAs and Gemini will continue.
- **Gemini will pause and ask** when a listing is ambiguous — this is intentional. Just answer in the CLI.
- Re-run `scrape` weekly — duplicates are automatically ignored via `INSERT OR IGNORE`.
- For Gmail SMTP, generate an [App Password](https://myaccount.google.com/apppasswords) — don't use your main password.
- The `guess-emails` command tries SMTP verification but many servers block this — treat results as "probable" not "confirmed".
