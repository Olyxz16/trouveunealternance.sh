# JobHunter (trouveunealternance.py)

Automated internship/apprenticeship search engine that identifies, scores, and enriches company data to streamline outreach.

## 🎯 Goal
The project aims to build a high-accuracy, stable, and cost-effective system to:
1.  **Discover** local tech companies using official SIRENE data.
2.  **Qualify** leads using LLMs to score their relevance.
3.  **Enrich** company profiles with websites, LinkedIn pages, and key recruitment contacts.
4.  **Automate** the initial touchpoint by guessing emails and drafting personalized hooks.

## 🏗️ Architecture

### Core Components
- **Collector (`internal/collector`):** Ingests official French government data (SIRENE) from Parquet files.
- **Enricher (`internal/enricher`):** 
    - **Classifier:** Uses LLMs to score companies based on their description/activity.
    - **Enricher:** Discovers URLs (Website, LinkedIn) and extracts contact information.
- **Scraper (`internal/scraper`):** 
    - **Cascade Fetcher:** A robust multi-layer fetching strategy: Cache ➡️ HTTP ➡️ Browser (Headless Chrome).
    - **Markdown Extractor:** Converts web pages to clean markdown for LLM consumption.
- **LLM Layer (`internal/llm`):** Unified provider system supporting **Gemini API/CLI** and **OpenRouter**. Includes fallback logic and usage tracking.
- **Pipeline (`internal/pipeline`):** An execution engine that runs sequential steps, logs progress to the DB, and manages timeouts.
- **Database (`internal/db`):** SQLite backend with a robust migration system (`internal/db/migrations`).

### Interfaces
- **TUI (`internal/tui`):** A modern terminal interface built with `Bubbletea` to monitor pipeline execution in real-time.
- **Dashboard (`internal/api`):** A local web server (port 8080) to visualize gathered data and statistics.
- **CLI (`cmd/`):** Cobra-based command-line interface for all operations.

## 🚀 Workflow

1.  **`scan`**: Filters SIRENE Parquet files by department and headcount to find candidate companies.
2.  **`score`**: LLM reviews candidates and assigns a relevance score (0-10).
3.  **`enrich`**: Performs deep research on scored companies:
    - Finds official website and LinkedIn profiles.
    - Scrapes career pages for recruitment info.
    - Identifies key contacts (HR, Tech Leads).
4.  **`guess-emails`**: Generates probable email addresses for identified contacts using common patterns.
5.  **`generate`**: Creates tailored outreach drafts (emails/LinkedIn hooks) based on company data.

## 🛠️ Tech Stack
- **Language:** Go 1.25
- **Persistence:** SQLite
- **UI:** Bubbletea (TUI), Go-Chi (Web)
- **Browser:** Chromedp (Headless Chrome)
- **Data:** Apache Parquet (via `modernc.org/sqlite` / specialized loaders)
- **AI:** Google Gemini, OpenRouter (GPT-4o/Claude)

## 📁 Project Structure
```text
├── cmd/                # CLI Command definitions (scan, score, enrich, etc.)
├── internal/
│   ├── collector/      # SIRENE data ingestion
│   ├── enricher/       # Scoring and data discovery logic
│   ├── scraper/        # HTTP/Browser fetching and content extraction
│   ├── llm/            # AI provider integration
│   ├── db/             # Persistence and migrations
│   ├── pipeline/       # Execution engine
│   ├── tui/            # Terminal UI components
│   └── api/            # Web dashboard server
├── data/               # SQLite DB, Parquet files, and cache
└── Taskfile.yml        # Task automation (shortcuts for common commands)
```

## 📈 Current Status & Roadmap

### Recently Implemented
- **Robust Scraper:** Multi-stage cascade fetcher with caching and browser-based search fallback (DuckDuckGo).
- **Integrated TUI:** Modern progress monitoring for scan, score, and enrich pipelines.
- **LLM Usage Tracking:** Real-time monitoring of token usage and API costs in SQLite.
- **URL Discovery:** AI-driven URL discovery using Gemini Search Grounding when available.

### Next Steps (Priority)
- [ ] **Enrichment Engine:** Further improve lead discovery quality using more advanced prompt engineering.

## 📊 Performance Metrics
- **Accuracy:** Prioritized above all. Results are verified through multi-source cross-referencing.
- **Stability:** High. Uses caching and robust error handling to survive network/API failures.
- **Cost:** Optimized by using Gemini (often free/low cost) and local data processing.
- **Speed:** Managed by asynchronous processing and parallel batching (where safe).
