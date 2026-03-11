"""
guesser.py — Email pattern guesser + SMTP verification
For companies where LinkedIn didn't expose an email.
"""
import asyncio
import smtplib
import socket
import dns.resolver
import re
from typing import Optional
from rich.console import Console

console = Console()

# Try to import dnspython; install hint if missing
try:
    import dns.resolver
    HAS_DNS = True
except ImportError:
    HAS_DNS = False
    console.print("[yellow]⚠ dnspython not installed. Run: pip install dnspython --break-system-packages[/yellow]")


def generate_candidates(first: str, last: str, domain: str) -> list[str]:
    """Generate common email pattern candidates for a person at a domain."""
    first = first.lower().strip()
    last = last.lower().strip()

    # Handle compound last names (de Vries → devries)
    first_clean = re.sub(r"[^a-z]", "", first)
    last_clean = re.sub(r"[^a-z]", "", last)
    f = first_clean[0] if first_clean else ""
    l = last_clean[0] if last_clean else ""

    patterns = [
        f"{first_clean}.{last_clean}@{domain}",
        f"{first_clean}{last_clean}@{domain}",
        f"{f}{last_clean}@{domain}",
        f"{first_clean}_{last_clean}@{domain}",
        f"{first_clean}@{domain}",
        f"{last_clean}@{domain}",
        f"{f}.{last_clean}@{domain}",
        f"{first_clean}.{l}@{domain}",
    ]
    # Remove duplicates while preserving order
    seen = set()
    return [p for p in patterns if not (p in seen or seen.add(p))]


def extract_domain(company_url: str) -> Optional[str]:
    """Extract bare domain from a URL."""
    import re
    match = re.search(r"(?:https?://)?(?:www\.)?([^/\s]+\.[a-z]{2,})", company_url.lower())
    return match.group(1) if match else None


async def verify_email_smtp(email: str, timeout: int = 5) -> tuple[bool, str]:
    """
    Attempt SMTP RCPT TO verification.
    Returns (is_valid, reason).
    Note: Many mail servers reject VRFY/RCPT checks — treat "maybe" as useful signal.
    """
    if not HAS_DNS:
        return False, "dnspython not installed"

    domain = email.split("@")[-1]

    try:
        # Get MX records
        records = dns.resolver.resolve(domain, "MX")
        mx_host = str(sorted(records, key=lambda r: r.preference)[0].exchange).rstrip(".")
    except Exception as e:
        return False, f"No MX record: {e}"

    try:
        with smtplib.SMTP(timeout=timeout) as smtp:
            smtp.connect(mx_host, 25)
            smtp.helo("verify.example.com")
            smtp.mail("verify@example.com")
            code, _ = smtp.rcpt(email)
            if code == 250:
                return True, "SMTP 250 OK"
            elif code == 550:
                return False, "SMTP 550 — mailbox does not exist"
            else:
                return None, f"SMTP {code} — inconclusive"
    except (smtplib.SMTPConnectError, socket.timeout, ConnectionRefusedError) as e:
        return None, f"SMTP unreachable: {e}"
    except Exception as e:
        return None, f"Error: {e}"


async def guess_and_verify(
    first_name: str,
    last_name: str,
    domain: str,
    max_to_verify: int = 4,
) -> list[dict]:
    """
    Generate email candidates and verify them.
    Returns list of {email, status, reason} sorted by confidence.
    """
    candidates = generate_candidates(first_name, last_name, domain)
    results = []

    console.print(f"  Testing {min(len(candidates), max_to_verify)} patterns for {first_name} {last_name} @{domain}...")

    for email in candidates[:max_to_verify]:
        valid, reason = await verify_email_smtp(email)
        results.append({"email": email, "valid": valid, "reason": reason})
        icon = "✓" if valid else ("?" if valid is None else "✗")
        console.print(f"    {icon} {email} — {reason}")
        await asyncio.sleep(0.5)  # be polite to mail servers

    # Sort: True first, then None (inconclusive), then False
    order = {True: 0, None: 1, False: 2}
    results.sort(key=lambda r: order.get(r["valid"], 2))
    return results


async def enrich_missing_emails(db_jobs: list[dict]) -> None:
    """
    For all TO_CONTACT rows without a contact_email,
    attempt to guess and verify one.
    """
    from db import update_job, log_activity

    candidates_for_guess = [
        j for j in db_jobs
        if j["status"] == "TO_CONTACT"
        and not j.get("contact_email")
        and j.get("contact_name")
        and j.get("apply_url")
    ]

    if not candidates_for_guess:
        console.print("[dim]No jobs need email guessing.[/dim]")
        return

    console.print(f"\n[bold]Email guesser — {len(candidates_for_guess)} contacts to process[/bold]")

    for job in candidates_for_guess:
        name_parts = (job["contact_name"] or "").split()
        if len(name_parts) < 2:
            console.print(f"  [dim]Skipping {job['company']} — can't split '{job['contact_name']}' into first/last[/dim]")
            continue

        # Try to get domain from careers page or apply URL
        domain_source = job.get("careers_page_url") or job.get("apply_url", "")
        domain = extract_domain(domain_source)
        if not domain:
            console.print(f"  [dim]Skipping {job['company']} — couldn't extract domain from URL[/dim]")
            continue

        console.print(f"\n  [cyan]{job['company']}[/cyan] — {job['contact_name']} (@{domain})")

        results = await guess_and_verify(
            first_name=name_parts[0],
            last_name=" ".join(name_parts[1:]),
            domain=domain,
        )

        # Take the best candidate (first verified, or first inconclusive)
        best = next((r for r in results if r["valid"] is True), None) or \
               next((r for r in results if r["valid"] is None), None)

        if best:
            note = f"Email guessed ({best['reason']})"
            update_job(job["id"], {
                "contact_email": best["email"],
                "notes": (job.get("notes") or "") + f" | {note}"
            })
            log_activity(job["id"], "EMAIL_GUESSED", f"{best['email']} — {best['reason']}")
            console.print(f"  [green]✓ Best guess: {best['email']}[/green]")
        else:
            console.print(f"  [red]✗ No valid email found[/red]")


if __name__ == "__main__":
    # Quick test
    async def test():
        results = await guess_and_verify("Jean", "Dupont", "example.com")
        for r in results:
            print(r)
    asyncio.run(test())
