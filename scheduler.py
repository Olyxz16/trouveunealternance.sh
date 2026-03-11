"""
scheduler.py — Daily cron-like re-runs with new listing detection
Run as: python scheduler.py start   (blocking, runs forever)
Or:     python scheduler.py once    (single run now)
"""
import asyncio
import sys
import subprocess
import platform
from datetime import datetime, time as dt_time
from pathlib import Path
from rich.console import Console

console = Console()

# ── CONFIG ────────────────────────────────────────────────────────────────────

# Run times (24h format, local time)
SCHEDULE = [
    dt_time(8, 30),   # Morning run
    dt_time(18, 0),   # Evening run
]

LOG_DIR = Path(__file__).parent / "logs"
LOG_DIR.mkdir(exist_ok=True)

# ── NOTIFICATION ──────────────────────────────────────────────────────────────

def notify(title: str, message: str):
    console.print(f"\n🔔 [bold]{title}[/bold]: {message}")
    system = platform.system()
    try:
        if system == "Darwin":
            subprocess.run([
                "osascript", "-e",
                f'display notification "{message}" with title "{title}" sound name "Glass"'
            ], capture_output=True, timeout=5)
        elif system == "Linux":
            subprocess.run(["notify-send", title, message], capture_output=True, timeout=5)
    except Exception:
        pass  # Notification failure shouldn't stop the pipeline

# ── DIFF DETECTION ────────────────────────────────────────────────────────────

def get_current_counts() -> dict:
    """Snapshot current DB counts per status."""
    from db import get_stats, init_db
    init_db()
    stats = get_stats()
    return {
        "total": stats["total"],
        "new_today": stats["new_today"],
        "by_status": stats.get("by_status", {}),
    }


def summarize_diff(before: dict, after: dict) -> str:
    """Generate a human-readable diff summary."""
    new_listings = after["total"] - before["total"]
    new_today = after["new_today"]

    parts = []
    if new_listings > 0:
        parts.append(f"{new_listings} new listing{'s' if new_listings != 1 else ''}")

    # Check for new TO_APPLY (direct internships)
    before_apply = before["by_status"].get("TO_APPLY", 0)
    after_apply  = after["by_status"].get("TO_APPLY", 0)
    new_direct = after_apply - before_apply
    if new_direct > 0:
        parts.append(f"{new_direct} direct internship{'s' if new_direct != 1 else ''} 🎯")

    # Check for new COMPANY_LEAD
    before_lead = before["by_status"].get("TO_ENRICH", 0) + before["by_status"].get("TO_CONTACT", 0)
    after_lead  = after["by_status"].get("TO_ENRICH", 0) + after["by_status"].get("TO_CONTACT", 0)
    new_leads = after_lead - before_lead
    if new_leads > 0:
        parts.append(f"{new_leads} company lead{'s' if new_leads != 1 else ''}")

    if not parts:
        return "No new listings found"
    return " · ".join(parts)

# ── PIPELINE RUNNER ───────────────────────────────────────────────────────────

async def run_pipeline_once(run_enrichment: bool = True):
    """Run Stage 1 (all sites) + optional Stage 2, return summary."""
    from scraper import run_stage1, run_stage2

    log_path = LOG_DIR / f"run_{datetime.now().strftime('%Y%m%d_%H%M%S')}.log"
    console.print(f"\n{'='*60}")
    console.print(f"  JobHunter run — {datetime.now().strftime('%Y-%m-%d %H:%M')}")
    console.print(f"{'='*60}")

    before = get_current_counts()
    notify("JobHunter", f"Starting scrape run at {datetime.now().strftime('%H:%M')}")

    try:
        await run_stage1()
    except Exception as e:
        console.print(f"[red]✗ Stage 1 failed: {e}[/red]")

    if run_enrichment:
        try:
            await run_stage2()
        except Exception as e:
            console.print(f"[red]✗ Stage 2 failed: {e}[/red]")

    after = get_current_counts()
    summary = summarize_diff(before, after)

    console.print(f"\n[bold green]✓ Run complete:[/bold green] {summary}")
    notify("✅ JobHunter done", summary)

    # Log summary
    with open(log_path, "a") as f:
        f.write(f"\n[{datetime.now().isoformat()}] {summary}\n")

    return summary


# ── SCHEDULER LOOP ────────────────────────────────────────────────────────────

async def scheduler_loop():
    """Run forever, triggering at each time in SCHEDULE."""
    console.print("[bold]JobHunter Scheduler started[/bold]")
    schedule_str = " and ".join(t.strftime("%H:%M") for t in SCHEDULE)
    console.print(f"Will run at: {schedule_str} every day")
    console.print("[dim]Press Ctrl+C to stop[/dim]\n")

    last_run_date = None

    while True:
        now = datetime.now()
        current_time = now.time().replace(second=0, microsecond=0)

        for scheduled_time in SCHEDULE:
            # Check if it's time to run (within 1 minute window)
            delta = abs(
                (datetime.combine(now.date(), current_time) -
                 datetime.combine(now.date(), scheduled_time)).total_seconds()
            )
            if delta < 60 and last_run_date != (now.date(), scheduled_time):
                last_run_date = (now.date(), scheduled_time)
                await run_pipeline_once()

        # Sleep 50s between checks
        await asyncio.sleep(50)


# ── ENTRY POINT ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else "help"

    if cmd == "start":
        try:
            asyncio.run(scheduler_loop())
        except KeyboardInterrupt:
            console.print("\n[yellow]Scheduler stopped.[/yellow]")

    elif cmd == "once":
        asyncio.run(run_pipeline_once())

    elif cmd == "once-scrape":
        # Stage 1 only, no enrichment
        asyncio.run(run_pipeline_once(run_enrichment=False))

    else:
        console.print("""
[bold]scheduler.py[/bold] — JobHunter scheduled runner

Usage:
  python scheduler.py start        Run forever (8:30 + 18:00 daily)
  python scheduler.py once         Run full pipeline once now
  python scheduler.py once-scrape  Run Stage 1 only (no enrichment)
        """)
