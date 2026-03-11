"""
emailer.py — Stage 3: draft cold emails via LLM, interactive approval, send via SMTP
"""
import asyncio
import json
import smtplib
import os
import sys
from email.mime.text import MIMEText
from email.mime.multipart import MIMEMultipart
from pathlib import Path
from datetime import datetime
from dotenv import load_dotenv
from rich.console import Console
from rich.panel import Panel
from rich.prompt import Prompt, Confirm
import httpx

load_dotenv()

console = Console()

# ── CONFIG ────────────────────────────────────────────────────────────────────

PROFILE = {
    "name":      os.getenv("YOUR_NAME", "Your Name"),
    "school":    os.getenv("YOUR_SCHOOL", "Your School, Year X"),
    "skills":    os.getenv("YOUR_SKILLS", "Python, Docker, Linux, CI/CD, Git"),
    "duration":  os.getenv("INTERNSHIP_DURATION", "6"),
    "start":     os.getenv("START_DATE", "September 2025"),
    "interests": os.getenv("YOUR_INTERESTS", "infrastructure, distributed systems, backend"),
    "email":     os.getenv("YOUR_EMAIL", ""),
}

SMTP_HOST  = os.getenv("SMTP_HOST", "smtp.gmail.com")
SMTP_PORT  = int(os.getenv("SMTP_PORT", "587"))
SMTP_USER  = os.getenv("SMTP_USER", "")
SMTP_PASS  = os.getenv("SMTP_PASS", "")

GEMINI_API_KEY = os.getenv("GEMINI_API_KEY", "")
MODEL = "gemini-2.0-flash"
EMAILS_DIR = Path(__file__).parent / "emails"
EMAILS_DIR.mkdir(exist_ok=True)

# ── LLM EMAIL DRAFTER ─────────────────────────────────────────────────────────

DRAFT_SYSTEM = """You are helping a computer science student write cold emails for internship applications in France.
Return ONLY valid JSON — no markdown, no explanation.

The email should be in French, max 150 words, warm and direct (not a formal cover letter).
It must:
- Open with a specific reference to the job they actually posted (shows research)
- Mention one specific thing about their tech stack or product that genuinely interests the student
- Ask if they'd consider an intern alongside their current hiring
- End with a clear CTA: 20-min call or to send CV

Return this JSON:
{
  "subject": "concise punchy subject line in French",
  "body": "the full email body in French",
  "linkedin_msg": "short LinkedIn message version max 280 chars in French"
}"""


async def draft_prospect_email(company: dict) -> Optional[dict]:
    """Draft a cold email for a prospect company (pure cold outreach, no job listing)."""
    prompt = f"""Company: {company['name']}
What they do: {company.get('description') or company.get('naf_label') or 'tech company'}
City: {company.get('city')}
Size: {company.get('headcount_range') or str(company.get('headcount_exact') or '?')} employees
Tech stack: {company.get('tech_stack') or 'unknown'}
Website: {company.get('website') or 'unknown'}
Contact: {company.get('contact_name') or 'unknown'} ({company.get('contact_role') or 'unknown'})

Student profile:
- Name: {PROFILE['name']}
- School: {PROFILE['school']}
- Skills: {PROFILE['skills']}
- Duration: {PROFILE['duration']} months starting {PROFILE['start']}
- Interests: {PROFILE['interests']}

This company has NOT posted any internship — write a pure cold outreach email
asking if they'd consider taking an intern. Be specific about their company."""

    if not GEMINI_API_KEY:
        first = company.get("contact_name", "").split()[0] if company.get("contact_name") else ""
        stack_hint = company.get("tech_stack", "").split(",")[0].strip() if company.get("tech_stack") else ""
        return {
            "subject": f"Candidature spontanée – Stage DevOps/Backend · {PROFILE['name']}",
            "body": f"""Bonjour{' ' + first if first else ''},

J'ai découvert {company['name']} en recherchant des entreprises tech à {company.get('city','')}{(" — votre travail sur " + stack_hint + " m'intéresse particulièrement") if stack_hint else ""}.

Je suis étudiant en {PROFILE['school']} et je recherche un stage de {PROFILE['duration']} mois à partir de {PROFILE['start']} en DevOps ou développement backend.

Seriez-vous ouverts à accueillir un stagiaire ? Je serais ravi d'échanger 20 minutes pour vous présenter mon profil.

Cordialement,
{PROFILE['name']}""",
            "linkedin_msg": f"Bonjour, j'ai découvert {company['name']} et votre activité m'intéresse vraiment. Je cherche un stage {PROFILE['duration']} mois en DevOps/backend — seriez-vous ouverts ? 🙏"
        }

    try:
        async with httpx.AsyncClient(timeout=30) as client:
            resp = await client.post(
                f"https://generativelanguage.googleapis.com/v1beta/models/{MODEL}:generateContent?key={GEMINI_API_KEY}",
                json={
                    "system_instruction": {"parts": [{"text": DRAFT_SYSTEM}]},
                    "contents": [{"parts": [{"text": prompt}]}],
                    "generationConfig": {"temperature": 0.7, "maxOutputTokens": 1024},
                }
            )
            resp.raise_for_status()
            text = resp.json()["candidates"][0]["content"]["parts"][0]["text"]
            text = text.strip().lstrip("```json").lstrip("```").rstrip("```").strip()
            return json.loads(text)
    except Exception as e:
        console.print(f"  [red]✗ Prospect draft error: {e}[/red]")
        return None


async def draft_email(job: dict) -> Optional[dict]:
    """Draft a cold email for a job using the Gemini API."""
    prompt = f"""Company: {job['company']}
Job they posted: {job['title']} ({job.get('contract_type','')})
Tech stack: {job.get('tech_stack','')}
Role summary: {job.get('description_summary','')}
Contact: {job.get('contact_name','')} ({job.get('contact_role','')})

Student profile:
- Name: {PROFILE['name']}
- School: {PROFILE['school']}
- Skills: {PROFILE['skills']}
- Duration: {PROFILE['duration']} months starting {PROFILE['start']}
- Interests: {PROFILE['interests']}

Write the cold email."""

    if not GEMINI_API_KEY:
        # Return a template draft if no API key
        return {
            "subject": f"Candidature spontanée – Stage DevOps/Backend · {PROFILE['name']}",
            "body": f"""Bonjour {job.get('contact_name', '').split()[0] if job.get('contact_name') else ''},

J'ai remarqué votre offre de {job['title']} chez {job['company']} — le travail sur {(job.get('tech_stack','').split(',')[0] or 'votre stack').strip()} m'a particulièrement intéressé.

Je suis étudiant en {PROFILE['school']} et je recherche un stage de {PROFILE['duration']} mois à partir de {PROFILE['start']} en DevOps ou développement backend.

Seriez-vous ouverts à accueillir un stagiaire en parallèle de votre recrutement actuel ? Je serais ravi d'échanger 20 minutes pour vous présenter mon profil.

Cordialement,
{PROFILE['name']}""",
            "linkedin_msg": f"Bonjour, j'ai vu votre offre {job['title']} chez {job['company']}. Je cherche un stage de {PROFILE['duration']} mois en DevOps/backend — seriez-vous ouverts à un stagiaire ? 🙏"
        }

    try:
        async with httpx.AsyncClient(timeout=30) as client:
            resp = await client.post(
                f"https://generativelanguage.googleapis.com/v1beta/models/{MODEL}:generateContent?key={GEMINI_API_KEY}",
                json={
                    "system_instruction": {"parts": [{"text": DRAFT_SYSTEM}]},
                    "contents": [{"parts": [{"text": prompt}]}],
                    "generationConfig": {"temperature": 0.7, "maxOutputTokens": 1024},
                }
            )
            resp.raise_for_status()
            text = resp.json()["candidates"][0]["content"]["parts"][0]["text"]
            text = text.strip().lstrip("```json").lstrip("```").rstrip("```").strip()
            return json.loads(text)
    except Exception as e:
        console.print(f"  [red]✗ Draft error: {e}[/red]")
        return None


# ── SMTP SENDER ───────────────────────────────────────────────────────────────

def send_email(to_email: str, subject: str, body: str) -> tuple[bool, str]:
    """Send an email via SMTP. Returns (success, message)."""
    if not SMTP_USER or not SMTP_PASS:
        return False, "SMTP credentials not configured in .env"
    if not PROFILE["email"]:
        return False, "YOUR_EMAIL not set in .env"

    msg = MIMEMultipart("alternative")
    msg["Subject"] = subject
    msg["From"] = f"{PROFILE['name']} <{PROFILE['email']}>"
    msg["To"] = to_email
    msg.attach(MIMEText(body, "plain", "utf-8"))

    try:
        with smtplib.SMTP(SMTP_HOST, SMTP_PORT) as server:
            server.ehlo()
            server.starttls()
            server.login(SMTP_USER, SMTP_PASS)
            server.sendmail(PROFILE["email"], to_email, msg.as_string())
        return True, "Sent successfully"
    except smtplib.SMTPAuthenticationError:
        return False, "SMTP auth failed — check credentials in .env"
    except Exception as e:
        return False, str(e)


def save_draft(job: dict, draft: dict):
    """Save draft to file and update DB."""
    from db import update_job, log_activity
    safe_name = re.sub(r"[^a-z0-9_-]", "_", job["company"].lower())
    path = EMAILS_DIR / f"{job['id']}_{safe_name}.md"
    path.write_text(f"""# {draft['subject']}

**To:** {job.get('contact_name','')} ({job.get('contact_role','')})  
**Email:** {job.get('contact_email','—')}  
**LinkedIn:** {job.get('contact_linkedin','—')}  
**Company:** {job['company']}  
**Generated:** {datetime.now().strftime('%Y-%m-%d %H:%M')}

---

## Email

**Subject:** {draft['subject']}

{draft['body']}

---

## LinkedIn Message

{draft['linkedin_msg']}
""")
    update_job(job["id"], {"email_draft": json.dumps(draft)})
    log_activity(job["id"], "DRAFT_SAVED", path.name)
    return path


# ── INTERACTIVE APPROVAL FLOW ─────────────────────────────────────────────────

import re
from typing import Optional


async def process_job_interactive(job: dict) -> str:
    """
    Show a job, draft an email, let user approve/edit/skip/send.
    Returns final action taken.
    """
    from db import update_job, log_activity

    console.print(f"\n{'─'*60}")
    console.print(f"[bold cyan]{job['company']}[/bold cyan] — {job['title']}")
    console.print(f"Contact: [bold]{job.get('contact_name','?')}[/bold] ({job.get('contact_role','?')})")
    if job.get("contact_email"):
        console.print(f"Email:   {job['contact_email']}")
    if job.get("contact_linkedin"):
        console.print(f"LinkedIn: {job['contact_linkedin']}")
    console.print(f"Stack:   [dim]{job.get('tech_stack','—')}[/dim]")

    # Check if draft already exists
    existing_draft = None
    if job.get("email_draft"):
        try:
            existing_draft = json.loads(job["email_draft"])
        except Exception:
            pass

    if existing_draft:
        console.print("\n[dim]Using existing draft.[/dim]")
        draft = existing_draft
    else:
        console.print("\n[dim]Drafting email...[/dim]")
        draft = await draft_email(job)
        if not draft:
            return "ERROR"

    while True:
        # Show draft
        console.print(Panel(
            f"[bold]Subject:[/bold] {draft['subject']}\n\n{draft['body']}",
            title="📧 Email Draft",
            border_style="blue"
        ))
        console.print(Panel(
            draft["linkedin_msg"],
            title="💼 LinkedIn Message",
            border_style="cyan"
        ))

        # Save draft to file
        path = save_draft(job, draft)

        # Actions
        console.print("\n[bold]Actions:[/bold]")
        console.print("  [green]s[/green] Send email now")
        console.print("  [cyan]l[/cyan] Mark as LinkedIn-only (no email)")
        console.print("  [yellow]e[/yellow] Edit (regenerate with notes)")
        console.print("  [dim]p[/dim] Pass (skip this company)")
        console.print("  [dim]q[/dim] Quit and save progress")

        action = Prompt.ask("\nChoice", choices=["s", "l", "e", "p", "q"], default="l")

        if action == "s":
            if not job.get("contact_email"):
                console.print("[red]No email address available. Use LinkedIn instead.[/red]")
                continue
            if Confirm.ask(f"Send to {job['contact_email']}?"):
                ok, msg = send_email(job["contact_email"], draft["subject"], draft["body"])
                if ok:
                    update_job(job["id"], {"status": "CONTACTED"})
                    log_activity(job["id"], "EMAIL_SENT", job["contact_email"])
                    console.print(f"[green]✓ Sent![/green]")
                    return "SENT"
                else:
                    console.print(f"[red]✗ Failed: {msg}[/red]")

        elif action == "l":
            update_job(job["id"], {"status": "CONTACTED"})
            log_activity(job["id"], "LINKEDIN_QUEUED", "User will send LinkedIn message manually")
            console.print(f"[cyan]✓ Marked as contacted (LinkedIn). Draft saved to {path.name}[/cyan]")
            return "LINKEDIN"

        elif action == "e":
            notes = Prompt.ask("Notes for regeneration (e.g. 'more casual', 'mention Kubernetes specifically')")
            original_prompt_addition = f"\n\nAdditional notes: {notes}"
            job_copy = dict(job)
            job_copy["_extra_notes"] = original_prompt_addition
            draft = await draft_email(job_copy)
            if not draft:
                console.print("[red]Regeneration failed, keeping previous draft.[/red]")

        elif action == "p":
            update_job(job["id"], {"status": "PASS"})
            log_activity(job["id"], "PASSED", "Skipped during email review")
            console.print("[dim]Passed.[/dim]")
            return "PASS"

        elif action == "q":
            return "QUIT"


async def run_email_stage():
    """Interactive email approval loop for all TO_CONTACT jobs."""
    from db import get_jobs, init_db
    init_db()

    jobs = get_jobs(status="TO_CONTACT")
    if not jobs:
        console.print("[yellow]No TO_CONTACT jobs found. Run scraper.py enrich first.[/yellow]")
        return

    console.print(f"\n[bold]📧 Email Stage — {len(jobs)} companies to contact[/bold]")
    console.print("[dim]You'll review each draft before anything is sent.[/dim]\n")

    sent = linkedin = passed = 0

    for i, job in enumerate(jobs):
        console.print(f"\n[dim]Company {i+1}/{len(jobs)}[/dim]")
        result = await process_job_interactive(job)

        if result == "SENT":    sent += 1
        elif result == "LINKEDIN": linkedin += 1
        elif result == "PASS":  passed += 1
        elif result == "QUIT":
            console.print("\n[yellow]Stopped. Progress saved.[/yellow]")
            break

    console.print(f"\n[bold green]✓ Email stage done[/bold green]")
    console.print(f"  Sent: {sent} | LinkedIn queued: {linkedin} | Passed: {passed}")


if __name__ == "__main__":
    asyncio.run(run_email_stage())
