#!/usr/bin/env python3
"""
jobhunter.py — Main CLI entry point
"""
import asyncio
import sys
import subprocess
from pathlib import Path
from rich.console import Console
from rich.panel import Panel

console = Console()

LOGO = """
[bold green]
   _       _     _                 _            
  (_) ___ | |__ | |__  _   _ _ __ | |_ ___ _ __ 
  | |/ _ \| '_ \| '_ \| | | | '_ \| __/ _ \ '__|
  | | (_) | |_) | | | | |_| | | | | ||  __/ |   
 _/ |\___/|_.__/|_| |_|\__,_|_| |_|\__\___|_|   
|__/                                              
[/bold green]
[dim]DevOps & Backend internship pipeline · powered by Gemini + Blueprint MCP[/dim]
"""

HELP = """
[bold]Commands:[/bold]

  [bold white]── Job scraping ──[/bold white]
  [cyan]jobhunter.py scrape[/cyan]               Stage 1 — scrape job sites interactively
  [cyan]jobhunter.py enrich[/cyan]               Stage 2 — enrich company leads
  [cyan]jobhunter.py emails[/cyan]               Stage 3 — approve and send cold emails
  [cyan]jobhunter.py guess-emails[/cyan]         Try to guess missing contact emails

  [bold white]── Company prospecting ──[/bold white]
    [cyan]jobhunter.py scan [city] [depts][/cyan]  Scan SIRENE for local tech companies
    [cyan]jobhunter.py target [batch][/cyan]      Score + Enrich best prospects in batch
    [cyan]jobhunter.py download-sirene[/cyan]      Download SIRENE Etablissements Parquet
    [cyan]jobhunter.py download-sirene-ul[/cyan]   Download SIRENE Unites Legales Parquet
    [cyan]jobhunter.py enrich-prospects [batch][/cyan] Enrich prospects via Gemini + LinkedIn
  [cyan]jobhunter.py score-prospects[/cyan]      LLM-score unscored companies in DB
  [cyan]jobhunter.py frenchtech [city][/cyan]    Generate French Tech scrape prompt

  [bold white]── Infrastructure ──[/bold white]
  [cyan]jobhunter.py dashboard[/cyan]            Start local dashboard (localhost:8000)
  [cyan]jobhunter.py schedule[/cyan]             Run on schedule (8:30 + 18:00 daily)
  [cyan]jobhunter.py once[/cyan]                 Run full pipeline once now
  [cyan]jobhunter.py prompts[/cyan]              Generate prompt files only
  [cyan]jobhunter.py stats[/cyan]               Show database stats
  [cyan]jobhunter.py setup[/cyan]               First-time setup
"""


def setup():
    env_path = Path(__file__).parent / ".env"
    if env_path.exists():
        console.print("[yellow].env already exists. Edit it manually.[/yellow]")
        return

    console.print("\n[bold]First-time setup[/bold] — let's configure your profile.\n")
    console.print("[dim]Press Enter to skip any field for now — you can edit .env later.[/dim]\n")

    fields = {
        "YOUR_NAME":           ("Your full name", ""),
        "YOUR_SCHOOL":         ("School / university + year", ""),
        "YOUR_SKILLS":         ("Tech skills (comma-separated)", "Python, Docker, Linux, Git, CI/CD"),
        "INTERNSHIP_DURATION": ("Internship duration (months)", "6"),
        "START_DATE":          ("Available from (e.g. September 2025)", ""),
        "YOUR_INTERESTS":      ("Genuine interests", "infrastructure, distributed systems, backend"),
        "YOUR_EMAIL":          ("Your email address (for sending)", ""),
        "SMTP_HOST":           ("SMTP host", "smtp.gmail.com"),
        "SMTP_PORT":           ("SMTP port", "587"),
        "SMTP_USER":           ("SMTP username (usually your email)", ""),
        "SMTP_PASS":           ("SMTP password / app password", ""),
        "GEMINI_API_KEY":      ("Gemini API key (for LLM classification)", ""),
    }

    lines = ["# JobHunter configuration\n"]
    for key, (label, default) in fields.items():
        val = input(f"  {label} [{default}]: ").strip()
        if not val:
            val = default
        lines.append(f"{key}={val}")

    env_path.write_text("\n".join(lines))
    console.print(f"\n[green]✓ Saved to {env_path}[/green]")
    console.print("[dim]Run 'python jobhunter.py dashboard' to open the dashboard.[/dim]")


def show_stats():
    from jobhunter.db import init_db
    from jobhunter.scraper import print_stats
    init_db()
    print_stats()


async def main():
    cmd = sys.argv[1] if len(sys.argv) > 1 else "help"

    console.print(LOGO)

    if cmd == "scrape":
        from jobhunter.scraper import run_stage1
        await run_stage1()

    elif cmd == "enrich":
        from jobhunter.scraper import run_stage2
        await run_stage2()

    elif cmd == "emails":
        from jobhunter.emailer import run_email_stage
        await run_email_stage()

    elif cmd == "guess-emails":
        from jobhunter.db import get_jobs, init_db
        from jobhunter.guesser import enrich_missing_emails
        init_db()
        jobs = get_jobs(status="TO_CONTACT")
        await enrich_missing_emails(jobs)

    elif cmd == "dashboard":
        console.print("[bold]Starting dashboard at [cyan]http://localhost:8000[/cyan][/bold]")
        console.print("[dim]Press Ctrl+C to stop[/dim]\n")
        try:
            subprocess.run([
                sys.executable, "-m", "uvicorn",
                "jobhunter.api:app", "--reload", "--port", "8000", "--host", "0.0.0.0"
            ])
        except KeyboardInterrupt:
            console.print("\n[yellow]Dashboard stopped.[/yellow]")

    elif cmd == "schedule":
        from jobhunter.scheduler import scheduler_loop
        await scheduler_loop()

    elif cmd == "once":
        from jobhunter.scheduler import run_pipeline_once
        await run_pipeline_once()

    elif cmd == "scan":
        from jobhunter.prospector import cmd_scan
        city = sys.argv[2] if len(sys.argv) > 2 else "Bordeaux"
        depts = sys.argv[3].split(",") if len(sys.argv) > 3 else ["33","47","40"]
        await cmd_scan(city, depts, min_headcount=5)

    elif cmd == "target":
        from jobhunter.prospector import cmd_target
        batch = int(sys.argv[2]) if len(sys.argv) > 2 else 10
        await cmd_target(batch)

    elif cmd == "enrich-prospects":
        from jobhunter.prospector import cmd_enrich
        batch = int(sys.argv[2]) if len(sys.argv) > 2 else 20
        await cmd_enrich(batch)

    elif cmd == "frenchtech":
        from jobhunter.prospector import cmd_frenchtech
        city = sys.argv[2] if len(sys.argv) > 2 else "Bordeaux"
        await cmd_frenchtech(city)

    elif cmd == "score-prospects":
        from jobhunter.prospector import cmd_score_prospects
        await cmd_score_prospects()

    elif cmd == "download-sirene":
        from jobhunter.prospector import download_sirene
        await download_sirene()

    elif cmd == "download-sirene-ul":
        from jobhunter.prospector import download_sirene_ul
        await download_sirene_ul()

    elif cmd == "prompts":
        from jobhunter.scraper import generate_prompts_only
        generate_prompts_only()

    elif cmd == "stats":
        show_stats()

    elif cmd == "setup":
        setup()

    else:
        console.print(HELP)


if __name__ == "__main__":
    asyncio.run(main())
