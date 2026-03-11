"""
scraper.py — Stage 1: generate Gemini CLI prompts and parse results into SQLite
Run interactively: python scraper.py run
Or generate prompt files only: python scraper.py prompts
"""
import asyncio
import json
import subprocess
import sys
from pathlib import Path
from datetime import datetime
from rich.console import Console
from rich.table import Table
from rich.progress import track

from db import init_db, upsert_job, log_activity, get_stats
from classifier import classify_listing

console = Console()

# ── CONFIG ────────────────────────────────────────────────────────────────────

SITES = [
    "Welcome to the Jungle (wttj.co)",
    "LinkedIn Jobs (linkedin.com/jobs)",
    "Indeed France (indeed.fr)",
    "Lesjeudis (lesjeudis.com)",
]

QUERIES = [
    ("DevOps", "stage"),
    ("Backend", "stage"),
    ("SRE", "stage"),
    ("Platform engineer", "stage"),
    ("DevOps", "CDI"),
    ("Backend développeur", "CDI"),
    ("Python backend", "CDI"),
    ("Golang", "CDI"),
    ("Infrastructure", "CDI"),
]

OUTPUT_FILE = Path(__file__).parent / "data" / "jobs.tsv"
PROMPTS_DIR = Path(__file__).parent / "data" / "prompts"

# ── PROMPT BUILDER ────────────────────────────────────────────────────────────

def build_stage1_prompt(site: str) -> str:
    queries_str = "\n".join(f'{i+1}. "{q[0]}" + {q[1]}' for i, q in enumerate(QUERIES))
    return f"""You are helping me find internships in DevOps and backend development in France.
I am logged into {site} in the browser.

## Your task
Search for job listings using these queries one by one:
{queries_str}

For each listing you open, classify it:
- DIRECT       → explicitly an internship (stage/alternance) in DevOps, backend, SRE, or platform engineering
- COMPANY_LEAD → CDI/CDD with a clearly DevOps or backend technical stack (Python, Go, Docker, K8s, etc.)
- SKIP         → anything else — do not save it

## For each non-SKIP listing, extract these fields as a JSON object:
{{
  "source_site": "{site}",
  "type": "DIRECT or COMPANY_LEAD",
  "title": "exact job title",
  "company": "company name",
  "location": "city",
  "contract_type": "stage / CDI / CDD / alternance",
  "raw_description": "paste the full job description text here",
  "apply_url": "direct URL to the listing"
}}

## Output rules
- After processing each listing, print the JSON object on a single line starting with: LISTING:
- After each query is done, print: QUERY_DONE: <query number> — <count> listings found
- When all queries for this site are done, print: SITE_DONE: {site}

## Interaction rules
- Process one listing at a time, open it, extract data, print the LISTING: line, then move to the next
- If you hit a CAPTCHA, login wall, or can't load a page → print: BLOCKED: <reason> and ask me to help
- If a listing is ambiguous → print: AMBIGUOUS: <title> at <company> — <your question> and wait for my input
- Do not navigate away from a listing until you have printed its LISTING: line
- For COMPANY_LEAD rows, make sure you capture enough of the description to identify their tech stack

Ready? Start with query 1 on {site}."""


def build_stage2_prompt(companies: list[dict]) -> str:
    company_list = "\n".join(
        f'- ID {c["id"]}: {c["company"]} (posted: {c["title"]}, stack: {c.get("tech_stack","")})'
        for c in companies
    )
    return f"""You are helping me find contact information for companies that may take a DevOps/backend intern,
even though they only posted CDI/CDD roles.

## Companies to enrich:
{company_list}

## For each company, follow these steps in order:

1. Search "[company name] careers" or "[company name] recrutement"
   - If they have a spontaneous application form or email → note careers_page_url

2. Go to their LinkedIn company page → People tab
   - Small company (< 50 people): find CTO or tech lead
   - Mid-size: find HR, talent acquisition, or technical recruiter
   - Large: find someone in engineering HR or a tech lead in the relevant team

3. If an email is publicly visible anywhere → note it. NEVER guess or invent one.

## For each company, print a JSON object on a line starting with ENRICHED:
{{
  "id": <company id from above>,
  "careers_page_url": "url or null",
  "contact_name": "name or null",
  "contact_role": "role or null",
  "contact_email": "email or null",
  "contact_linkedin": "linkedin profile url or null",
  "notes": "anything useful"
}}

## Rules
- Process one company at a time
- If LinkedIn needs interaction (login prompt, see-more) → print: NEED_HELP: <company> and wait for me
- If no contact found after all steps → set all contact fields to null and note what you tried
- When all companies are done → print: ENRICHMENT_DONE
"""

# ── LIVE PARSER ───────────────────────────────────────────────────────────────

async def parse_and_save_listing(raw_json: dict) -> tuple[bool, str]:
    """Classify a raw listing and save it to DB. Returns (saved, reason)."""
    classification = await classify_listing(
        title=raw_json.get("title", ""),
        company=raw_json.get("company", ""),
        raw_description=raw_json.get("raw_description", ""),
        location=raw_json.get("location", ""),
    )

    if not classification or classification["type"] == "SKIP":
        return False, f"Classified as SKIP (score {classification.get('relevance_score', 0) if classification else '?'})"

    if classification["relevance_score"] < 3:
        return False, f"Low relevance score: {classification['relevance_score']}"

    job_data = {
        "source_site": raw_json.get("source_site"),
        "type": classification["type"],
        "title": raw_json.get("title"),
        "company": raw_json.get("company"),
        "location": raw_json.get("location"),
        "contract_type": classification.get("contract_type") or raw_json.get("contract_type"),
        "tech_stack": ", ".join(classification.get("tech_stack", [])),
        "description_summary": classification.get("description_summary"),
        "apply_url": raw_json.get("apply_url"),
        "relevance_score": classification.get("relevance_score", 5),
        "status": "TO_APPLY" if classification["type"] == "DIRECT" else "TO_ENRICH",
        "notes": classification.get("reasoning"),
    }

    job_id, is_new = upsert_job(job_data)
    if is_new:
        return True, f"Saved as {classification['type']} (score {classification['relevance_score']})"
    return False, "Duplicate — already in DB"


def parse_gemini_output(output: str) -> list[dict]:
    """Extract LISTING: JSON lines from Gemini CLI output."""
    listings = []
    for line in output.splitlines():
        line = line.strip()
        if line.startswith("LISTING:"):
            try:
                raw = line[len("LISTING:"):].strip()
                listings.append(json.loads(raw))
            except json.JSONDecodeError as e:
                console.print(f"  [yellow]⚠ Could not parse listing JSON: {e}[/yellow]")
    return listings


def parse_enrichment_output(output: str) -> list[dict]:
    """Extract ENRICHED: JSON lines from Gemini CLI output."""
    results = []
    for line in output.splitlines():
        line = line.strip()
        if line.startswith("ENRICHED:"):
            try:
                raw = line[len("ENRICHED:"):].strip()
                results.append(json.loads(raw))
            except json.JSONDecodeError as e:
                console.print(f"  [yellow]⚠ Could not parse enrichment JSON: {e}[/yellow]")
    return results


# ── INTERACTIVE RUNNER ────────────────────────────────────────────────────────

def run_gemini_interactive(prompt: str, label: str) -> str:
    """
    Writes the prompt to a temp file and launches Gemini CLI.
    In a real run, Gemini CLI is interactive — the user watches and can intervene.
    We capture output via tee so we can parse it afterward.
    """
    import tempfile, os

    prompt_file = Path(__file__).parent / "data" / f"prompt_{label}.txt"
    prompt_file.parent.mkdir(exist_ok=True)
    prompt_file.write_text(prompt)

    log_file = Path(__file__).parent / "logs" / f"{label}_{datetime.now().strftime('%Y%m%d_%H%M%S')}.log"
    log_file.parent.mkdir(exist_ok=True)

    console.print(f"\n[bold cyan]▶ Launching Gemini CLI for: {label}[/bold cyan]")
    console.print(f"  Prompt saved to: {prompt_file}")
    console.print(f"  Log will be saved to: {log_file}")
    console.print(f"\n[dim]Press Ctrl+C to stop this site and move to the next.[/dim]\n")

    # Launch gemini CLI with the prompt piped in, tee output to log
    cmd = f'gemini < "{prompt_file}" 2>&1 | tee "{log_file}"'

    try:
        result = subprocess.run(cmd, shell=True, text=True, capture_output=False)
        return log_file.read_text() if log_file.exists() else ""
    except KeyboardInterrupt:
        console.print(f"\n[yellow]⏸ Interrupted. Parsing what we have so far...[/yellow]")
        return log_file.read_text() if log_file.exists() else ""


# ── MAIN COMMANDS ─────────────────────────────────────────────────────────────

async def run_stage1():
    """Run Stage 1: scrape all sites."""
    init_db()
    total_saved = 0
    total_skipped = 0

    for site in SITES:
        console.rule(f"[bold]Stage 1 — {site}[/bold]")
        prompt = build_stage1_prompt(site)

        output = run_gemini_interactive(prompt, label=site.split("(")[0].strip().replace(" ", "_"))

        # Parse listings from output
        listings = parse_gemini_output(output)
        console.print(f"\n  Found [bold]{len(listings)}[/bold] raw listings to classify...")

        for listing in listings:
            saved, reason = await parse_and_save_listing(listing)
            icon = "✓" if saved else "·"
            color = "green" if saved else "dim"
            console.print(f"  [{color}]{icon} {listing.get('company','?')} — {listing.get('title','?')}: {reason}[/{color}]")
            if saved:
                total_saved += 1
            else:
                total_skipped += 1

        notify(f"Site done: {site}", f"{total_saved} saved so far")

    console.print(f"\n[bold green]✓ Stage 1 complete![/bold green]")
    console.print(f"  Saved: {total_saved} | Skipped: {total_skipped}")
    print_stats()


async def run_stage2():
    """Run Stage 2: enrich COMPANY_LEAD rows."""
    from db import get_jobs, update_job, log_activity

    companies = get_jobs(status="TO_ENRICH", type_="COMPANY_LEAD")
    if not companies:
        console.print("[yellow]No TO_ENRICH companies found. Run Stage 1 first.[/yellow]")
        return

    console.rule(f"[bold]Stage 2 — Enriching {len(companies)} companies[/bold]")
    prompt = build_stage2_prompt(companies)

    output = run_gemini_interactive(prompt, label="enrichment")

    results = parse_enrichment_output(output)
    console.print(f"\n  Parsed [bold]{len(results)}[/bold] enrichment results...")

    for result in results:
        job_id = result.pop("id", None)
        if not job_id:
            continue

        has_contact = any(result.get(f) for f in ["contact_name", "contact_email", "contact_linkedin"])
        result["status"] = "TO_CONTACT" if has_contact else "NO_CONTACT_FOUND"

        update_job(job_id, result)
        log_activity(job_id, "ENRICHED", f"Contact: {result.get('contact_name', 'none found')}")

        icon = "✓" if has_contact else "✗"
        color = "green" if has_contact else "red"
        console.print(f"  [{color}]{icon} ID {job_id}: {result.get('contact_name', 'No contact found')}[/{color}]")

    notify("Stage 2 done", f"{len(results)} companies enriched")
    print_stats()


def print_stats():
    stats = get_stats()
    table = Table(title="Database Stats", show_header=True)
    table.add_column("Metric", style="cyan")
    table.add_column("Value", style="bold")
    table.add_row("Total listings", str(stats["total"]))
    table.add_row("New today", str(stats["new_today"]))
    for status, count in stats.get("by_status", {}).items():
        table.add_row(f"  {status}", str(count))
    console.print(table)


def notify(title: str, message: str):
    import platform
    console.print(f"\n🔔 [bold]{title}[/bold]: {message}")
    system = platform.system()
    if system == "Darwin":
        subprocess.run([
            "osascript", "-e",
            f'display notification "{message}" with title "{title}" sound name "Glass"'
        ], capture_output=True)
    elif system == "Linux":
        subprocess.run(["notify-send", title, message], capture_output=True)


def generate_prompts_only():
    """Write all prompts to files without running Gemini."""
    PROMPTS_DIR.mkdir(parents=True, exist_ok=True)

    for site in SITES:
        safe_name = site.split("(")[0].strip().replace(" ", "_")
        path = PROMPTS_DIR / f"stage1_{safe_name}.txt"
        path.write_text(build_stage1_prompt(site))
        console.print(f"  ✓ {path}")

    console.print(f"\n[green]✓ {len(SITES)} Stage 1 prompts saved to {PROMPTS_DIR}[/green]")
    console.print("[dim]Paste them manually into Gemini CLI if you prefer manual control.[/dim]")


# ── ENTRY POINT ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else "help"

    if cmd == "run":
        asyncio.run(run_stage1())
    elif cmd == "enrich":
        asyncio.run(run_stage2())
    elif cmd == "prompts":
        generate_prompts_only()
    elif cmd == "stats":
        init_db()
        print_stats()
    else:
        console.print("""
[bold]scraper.py[/bold] — JobHunter Stage 1 & 2

Usage:
  python scraper.py run       Run Stage 1 (scrape all sites interactively)
  python scraper.py enrich    Run Stage 2 (enrich COMPANY_LEAD rows)
  python scraper.py prompts   Generate prompt files only (no Gemini CLI)
  python scraper.py stats     Show database stats
        """)
