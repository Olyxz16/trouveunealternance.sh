"""
prospector.py — Pipeline 0: discover local tech companies
Sources: Pappers API (SIRENE), La French Tech, LinkedIn via Gemini/Blueprint MCP

Run:
  python prospector.py scan --city "Bordeaux" --radius 50
  python prospector.py enrich          # enrich NEW companies with LinkedIn data
  python prospector.py score           # LLM-score all unscored companies
"""
import asyncio
import json
import os
import sys
import httpx
from pathlib import Path
from typing import Optional
from dotenv import load_dotenv
from rich.console import Console
from rich.table import Table
from rich.progress import track

load_dotenv()

console = Console()

GEMINI_API_KEY = os.getenv("GEMINI_API_KEY", "")

# SIRENE StockEtablissement — official monthly Parquet from data.gouv.fr
# Updated every month, no auth, no rate limits, filter locally with PyArrow
# The resource ID is stable; data.gouv.fr redirects to the latest file automatically
SIRENE_PARQUET_URL = "https://www.data.gouv.fr/fr/datasets/r/a29c1297-1f92-4e2a-8f6b-8c902ce96c5f"

TECH_NAF_CODES = [
    "62.01Z",  # Software development
    "62.02A",  # IT consulting
    "62.02B",  # IT maintenance
    "62.03Z",  # IT infrastructure management
    "62.09Z",  # Other IT activities
    "63.11Z",  # Data processing / hosting
    "63.12Z",  # Web portals
    "70.22Z",  # Business consulting
    "71.12B",  # Engineering studies
]

DEFAULT_DEPARTMENTS = ["33", "47", "40", "24", "17"]


# ── SIRENE VIA PYARROW + REMOTE PARQUET ───────────────────────────────────────

def scan_sirene_local(
    departments: list[str],
    naf_codes: list[str] = None,
    min_headcount: int = 5,
    data_dir: Path = None,
) -> list[dict]:
    """
    Filter SIRENE StockEtablissement Parquet with pyarrow.
    Uses local file if data/sirene.parquet exists, otherwise streams from data.gouv.fr.
    """
    try:
        import pyarrow.parquet as pq
        import pyarrow.compute as pc
        import pyarrow as pa
    except ImportError:
        console.print("[red]pyarrow not installed — run: pip install pyarrow requests[/red]")
        return []

    if data_dir is None:
        data_dir = Path(__file__).parent / "data"
    data_dir.mkdir(exist_ok=True)

    local_path = data_dir / "sirene.parquet"

    if not local_path.exists():
        console.print("[yellow]No local sirene.parquet found — downloading now (~2.1GB)…[/yellow]")
        console.print("[dim]This only happens once. Future scans will be instant.[/dim]")
        _download_sirene_sync(local_path)

    if naf_codes is None:
        naf_codes = TECH_NAF_CODES

    # Ensure NAF codes have dots (e.g. 6201Z -> 62.01Z)
    processed_naf = []
    for code in naf_codes:
        if len(code) == 5 and "." not in code:
            processed_naf.append(f"{code[:2]}.{code[2:]}")
        else:
            processed_naf.append(code)
    naf_set = set(processed_naf)

    # Headcount codes that meet the minimum
    all_headcount = {
        "03": 6, "11": 10, "12": 20, "21": 50, "22": 100,
        "31": 200, "32": 250, "41": 500, "42": 1000,
        "51": 2000, "52": 5000, "53": 10000,
    }
    valid_headcount = [k for k, v in all_headcount.items() if v >= min_headcount]

    console.print(f"  Reading {local_path.name} ({local_path.stat().st_size // 1024 // 1024}MB)…")

    try:
        pf = pq.ParquetFile(local_path)
        companies = []

        dept_set       = set(departments)
        headcount_set  = set(valid_headcount)

        needed_cols = [
            "siren", "siret",
            "denominationUsuelleEtablissement",
            "enseigne1Etablissement",
            "activitePrincipaleEtablissement",
            "etatAdministratifEtablissement",
            "trancheEffectifsEtablissement",
            "codePostalEtablissement",
            "libelleCommuneEtablissement",
            "dateCreationEtablissement",
            "numeroVoieEtablissement",
            "typeVoieEtablissement",
            "libelleVoieEtablissement",
        ]
        # Only request columns that actually exist in this file
        available = pf.schema_arrow.names
        cols = [c for c in needed_cols if c in available]

        total = 0
        for batch in pf.iter_batches(batch_size=100_000, columns=cols):
            tbl = batch.to_pydict()
            n = len(tbl["siren"])
            for i in range(n):
                naf  = (tbl["activitePrincipaleEtablissement"][i] or "")
                if naf not in naf_set:
                    continue
                etat = (tbl["etatAdministratifEtablissement"][i] or "")
                if etat != "A":
                    continue
                cp   = (tbl["codePostalEtablissement"][i] or "")
                dept = cp[:2]
                if dept not in dept_set:
                    continue
                hc   = (tbl["trancheEffectifsEtablissement"][i] or "")
                if hc and hc not in headcount_set:
                    continue
                
                # Try multiple name fields
                name = (
                    (tbl["denominationUsuelleEtablissement"][i] or "") or 
                    (tbl["enseigne1Etablissement"][i] or "") or
                    f"Company {tbl['siren'][i]}" # Fallback
                ).strip()

                addr_parts = [
                    tbl.get("numeroVoieEtablissement", [None]*n)[i] or "",
                    tbl.get("typeVoieEtablissement",   [None]*n)[i] or "",
                    tbl.get("libelleVoieEtablissement",[None]*n)[i] or "",
                ]
                companies.append({
                    "name":            name,
                    "siren":           tbl["siren"][i],
                    "siret":           tbl["siret"][i],
                    "naf_code":        naf,
                    "naf_label":       NAF_LABELS.get(naf.replace(".", ""), ""),
                    "city":            tbl.get("libelleCommuneEtablissement", [None]*n)[i],
                    "department":      dept,
                    "address":         " ".join(p for p in addr_parts if p).strip(),
                    "headcount_range": _headcount_label(hc),
                    "creation_year":   (str(tbl.get("dateCreationEtablissement", [None]*n)[i] or ""))[:4] or None,
                    "website":         None,
                    "source":          "sirene",
                    "status":          "NEW",
                })
            total += n

        console.print(f"  [green]✓ {len(companies)} companies found (scanned {total:,} rows)[/green]")
        return companies

    except Exception as e:
        console.print(f"[red]Parquet read error: {e}[/red]")
        return []


def _download_sirene_sync(dest: Path):
    """Blocking download with progress bar."""
    try:
        import requests
    except ImportError:
        console.print("[red]requests not installed — run: pip install requests[/red]")
        return

    with requests.get(SIRENE_PARQUET_URL, stream=True, timeout=600) as r:
        r.raise_for_status()
        total = int(r.headers.get("content-length", 0))
        downloaded = 0
        with open(dest, "wb") as f:
            for chunk in r.iter_content(chunk_size=1024 * 1024):
                f.write(chunk)
                downloaded += len(chunk)
                if total:
                    pct = downloaded / total * 100
                    print(f"\r  {pct:.1f}%  ({downloaded//1024//1024}MB / {total//1024//1024}MB)", end="", flush=True)
    print()
    console.print(f"[green]✓ Saved to {dest}[/green]")


async def download_sirene(data_dir: Path = None):
    """Async wrapper — just calls the sync download (it's a one-off)."""
    if data_dir is None:
        data_dir = Path(__file__).parent / "data"
    data_dir.mkdir(exist_ok=True)
    dest = data_dir / "sirene.parquet"
    if dest.exists():
        console.print(f"[yellow]Already exists ({dest.stat().st_size//1024//1024}MB). Delete to re-download.[/yellow]")
        return
    _download_sirene_sync(dest)


# Common NAF labels (subset)
NAF_LABELS = {
    "6201Z": "Programmation informatique",
    "6202A": "Conseil en systèmes et logiciels informatiques",
    "6202B": "Tierce maintenance de systèmes et d'applications informatiques",
    "6203Z": "Gestion d'installations informatiques",
    "6209Z": "Autres activités informatiques",
    "6311Z": "Traitement de données, hébergement et activités connexes",
    "6312Z": "Portails Internet",
    "7022Z": "Conseil pour les affaires et autres conseils de gestion",
    "7112B": "Ingénierie, études techniques",
}

# Common legal form codes → labels
LEGAL_FORMS = {
    "5710": "SAS", "5720": "SASU", "5498": "SARL", "5485": "EURL",
    "5308": "SA", "1000": "Entrepreneur individuel", "6540": "Association",
}

def _parse_headcount_min(range_str: str) -> int:
    """Parse Pappers headcount code to minimum integer."""
    # Pappers uses INSEE codes: NN=0, 00=0, 01=1-2, 02=3-5, 03=6-9, 11=10-19...
    mapping = {
        "NN": 0, "00": 0, "01": 1, "02": 3, "03": 6,
        "11": 10, "12": 20, "21": 50, "22": 100,
        "31": 200, "32": 250, "41": 500, "42": 1000,
        "51": 2000, "52": 5000, "53": 10000,
    }
    return mapping.get(str(range_str).strip(), 0)


def _headcount_label(code: str) -> str:
    """Convert INSEE headcount code to human-readable range."""
    labels = {
        "NN": "0", "00": "0", "01": "1-2", "02": "3-5", "03": "6-9",
        "11": "10-19", "12": "20-49", "21": "50-99", "22": "100-199",
        "31": "200-249", "32": "250-499", "41": "500-999", "42": "1000-1999",
        "51": "2000-4999", "52": "5000-9999", "53": "10000+",
    }
    return labels.get(str(code).strip(), code or "?")


def _mock_pappers_results(department: str) -> list[dict]:
    """Mock data for testing without an API key."""
    return [
        {"name": "TechCo Bordeaux", "siren": "123456789", "naf_code": "6201Z",
         "naf_label": "Programmation informatique", "city": "Bordeaux",
         "department": department, "headcount_range": "10-19",
         "source": "pappers", "status": "NEW"},
        {"name": "DevAgency SAS", "siren": "987654321", "naf_code": "6202A",
         "naf_label": "Conseil en systèmes informatiques", "city": "Mérignac",
         "department": department, "headcount_range": "20-49",
         "source": "pappers", "status": "NEW"},
        {"name": "CloudStart SARL", "siren": "456789123", "naf_code": "6311Z",
         "naf_label": "Traitement de données", "city": "Bordeaux",
         "department": department, "headcount_range": "5-9",
         "source": "pappers", "status": "NEW"},
    ]


# ── LA FRENCH TECH ────────────────────────────────────────────────────────────

async def fetch_french_tech_companies(city: str) -> list[dict]:
    """
    Scrape La French Tech member directory for a city via Gemini + Blueprint MCP.
    Returns companies to add to the prospect list.
    """
    prompt = f"""Go to https://lafrenchtech.com/fr/la-france-investit/french-tech-next40-120/
and also search "French Tech {city} startups" and "BPI France {city} tech entreprises".

For each tech company you find that is based in or near {city}:
- Extract: company name, website, city, brief description, approximate size if shown
- Only include companies that are clearly tech (software, cloud, DevOps, data, SaaS, infra)
- Skip consulting firms, agencies, and non-tech companies

For each company found, print a JSON line starting with PROSPECT:
{{
  "name": "company name",
  "city": "{city}",
  "website": "url or null",
  "description": "1-2 sentence description",
  "source": "frenchtech",
  "status": "NEW"
}}

Print PROSPECT_SCAN_DONE when finished."""
    return prompt  # Return prompt for Gemini CLI runner


# ── LLM ENRICHMENT via Gemini CLI ─────────────────────────────────────────────

def build_enrichment_prompt(companies: list[dict]) -> str:
    """Build Gemini prompt to enrich a batch of companies via Blueprint MCP."""
    company_list = "\n".join(
        f'- ID {c["id"]}: {c["name"]} ({c.get("city","")}) — {c.get("naf_label","")}, {c.get("headcount_range","?")} employees'
        for c in companies
    )
    return f"""You are helping me build a list of tech companies to cold-email for a DevOps/backend internship.

## Companies to research:
{company_list}

## For each company, do the following:

1. Search "[company name] site:linkedin.com" to find their LinkedIn company page
2. On their LinkedIn page:
   - Confirm or correct the headcount
   - Read their "About" section for tech stack hints
   - Check recent posts or job listings for technology mentions
   - Find 1 contact: prefer tech lead, CTO (small co), or HR/talent (larger co)
     Look in the "People" tab, search "CTO" / "tech lead" / "recrutement"

3. Search "[company name] [city] tech stack" or check their GitHub/website for technology info

4. Rate their internship-friendliness 0-10:
   - 10: actively hiring juniors, clear tech team, DevOps/backend stack
   - 7-9: tech company, right stack, likely has capacity
   - 4-6: tech adjacent, unclear stack, or very small
   - 1-3: wrong domain, too small, or pure services
   - 0: not a tech company at all

## For each company, print a JSON line starting with ENRICHED:
{{
  "id": <id from list>,
  "website": "url or null",
  "linkedin_url": "company linkedin url or null",
  "headcount_exact": null_or_number,
  "tech_stack": "comma-separated: Docker, Python, K8s, etc. — null if unknown",
  "description": "1-2 sentence description of what they actually do",
  "contact_name": "name or null",
  "contact_role": "role or null",
  "contact_linkedin": "profile url or null",
  "contact_email": "email if publicly visible, never guess",
  "careers_page_url": "url or null",
  "relevance_score": 0-10,
  "notes": "anything notable",
  "status": "TO_CONTACT" if score >= 5 else "NOT_TECH" if clearly wrong else "PASS"
}}

## Rules
- Process one company at a time, print ENRICHED: immediately after each one
- If LinkedIn requires interaction → print NEED_HELP: <company name> and wait
- Print ENRICHMENT_DONE when all companies are processed
"""


# ── LLM SCORER ───────────────────────────────────────────────────────────────

SCORE_SYSTEM = """You are evaluating French tech companies as potential internship hosts for a DevOps/backend student.

Return ONLY valid JSON, no markdown.

Score 0-10 based on:
- Stack relevance: Docker, K8s, cloud, Python, Go, microservices = high score
- Company size: 10-200 employees = ideal intern host. Too small (<5) or too large (>500) = lower
- Type: Product company > pure consulting/ESN > unknown
- Signals: hiring engineers, has a GitHub, uses cloud services = positive

{
  "relevance_score": 0-10,
  "is_tech": true/false,
  "inferred_stack": ["tag1", "tag2"],
  "company_type": "product" | "consulting" | "agency" | "ESN" | "unknown",
  "reasoning": "1 sentence"
}"""


async def llm_score_company(company: dict) -> Optional[dict]:
    """Quick LLM scoring for companies that haven't been enriched yet."""
    if not GEMINI_API_KEY:
        return _heuristic_score(company)

    prompt = f"""Company: {company['name']}
NAF code: {company.get('naf_code')} — {company.get('naf_label')}
City: {company.get('city')}
Size: {company.get('headcount_range')} employees
Website: {company.get('website') or 'unknown'}
Description: {company.get('description') or 'none'}"""

    try:
        async with httpx.AsyncClient(timeout=20) as client:
            resp = await client.post(
                f"https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key={GEMINI_API_KEY}",
                json={
                    "system_instruction": {"parts": [{"text": SCORE_SYSTEM}]},
                    "contents": [{"parts": [{"text": prompt}]}],
                    "generationConfig": {"temperature": 0.1, "maxOutputTokens": 256},
                }
            )
            resp.raise_for_status()
            text = resp.json()["candidates"][0]["content"]["parts"][0]["text"]
            text = text.strip().lstrip("```json").lstrip("```").rstrip("```").strip()
            return json.loads(text)
    except Exception as e:
        console.print(f"  [yellow]Score error for {company['name']}: {e}[/yellow]")
        return _heuristic_score(company)


def _heuristic_score(company: dict) -> dict:
    """Keyword-based fallback scorer."""
    naf = (company.get("naf_code") or "").replace(".", "")
    label = (company.get("naf_label") or "").lower()
    desc = (company.get("description") or "").lower()
    text = label + " " + desc

    tech_nafs = ["6201Z","6202A","6202B","6203Z","6209Z","6311Z","6312Z"]
    is_tech = naf in [n.replace(".","") for n in tech_nafs]

    consulting_words = ["conseil", "consulting", "esn", "ssii", "intégration"]
    is_consulting = any(w in text for w in consulting_words)

    score = 7 if is_tech else 3
    if is_consulting: score -= 2
    score = max(0, min(10, score))

    return {
        "relevance_score": score,
        "is_tech": is_tech,
        "inferred_stack": [],
        "company_type": "consulting" if is_consulting else ("product" if is_tech else "unknown"),
        "reasoning": "Heuristic based on NAF code"
    }


# ── MAIN COMMANDS ─────────────────────────────────────────────────────────────

async def cmd_scan(city: str, departments: list[str], min_headcount: int):
    """Scan SIRENE Parquet for tech companies in the given departments."""
    from db import init_db, upsert_company

    init_db()
    total_new = 0
    total_seen = 0

    console.rule(f"[bold]Scanning departments {departments} via SIRENE Parquet[/bold]")
    companies = scan_sirene_local(
        departments=departments,
        naf_codes=TECH_NAF_CODES,
        min_headcount=min_headcount,
    )
    console.print(f"  {len(companies)} companies fetched")

    for c in companies:
        cid, is_new = upsert_company(c)
        if is_new:
            total_new += 1
        else:
            total_seen += 1

    console.print(f"\n[bold green]✓ Scan complete[/bold green]")
    console.print(f"  New companies added: {total_new}")
    console.print(f"  Already in DB: {total_seen}")

    # Auto-score new ones
    await cmd_score(only_new=True)


async def cmd_score(only_new: bool = False):
    """LLM-score companies that don't have a score yet."""
    from db import get_companies, update_company

    # Fetch all companies with status NEW (if only_new) or any company
    companies = get_companies(status="NEW" if only_new else None)
    
    # Unscored companies have relevance_score 0 or NULL
    unscored = [c for c in companies if not c.get("relevance_score")]

    if not unscored:
        console.print("[dim]All companies already scored.[/dim]")
        return

    console.print(f"\n[bold]Scoring {len(unscored)} companies...[/bold]")
    scored = skipped = 0

    for company in track(unscored, description="Scoring..."):
        result = await llm_score_company(company)
        if result:
            updates = {
                "relevance_score": result.get("relevance_score", 0),
                "notes": (company.get("notes") or "") + f" | {result.get('reasoning','')}"
            }
            if result.get("inferred_stack"):
                updates["tech_stack"] = ", ".join(result["inferred_stack"])
            if not result.get("is_tech"):
                updates["status"] = "NOT_TECH"

            update_company(company["id"], updates)
            scored += 1
        else:
            skipped += 1
        await asyncio.sleep(0.1)

    console.print(f"[green]✓ Scored {scored} companies ({skipped} skipped)[/green]")


async def cmd_enrich(batch_size: int = 10):
    """Run Gemini CLI enrichment one by one to avoid stalling."""
    from db import get_companies, update_company
    import subprocess
    import tempfile

    # Prioritize NEW companies, then ENRICHING (previously failed/interrupted)
    all_eligible = [
        c for c in get_companies(min_score=5)
        if c["status"] in ("NEW", "ENRICHING")
        and not c.get("contact_name")
    ]
    
    # Sort: NEW first, then ENRICHING
    all_eligible.sort(key=lambda x: 0 if x["status"] == "NEW" else 1)
    companies = all_eligible[:batch_size]

    if not companies:
        console.print("[yellow]No companies to enrich. Run scan first.[/yellow]")
        return

    console.rule(f"[bold]Enriching {len(companies)} companies (Sequential mode)[/bold]")
    
    for i, company in enumerate(companies):
        console.print(f"[{i+1}/{len(companies)}] Researching [bold]{company['name']}[/bold]...")
        
        # Build prompt for a single company
        prompt = build_enrichment_prompt([company])
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False) as f:
            f.write(prompt)
            prompt_path = f.name

        try:
            # Mark as enriching
            update_company(company["id"], {"status": "ENRICHING"})
            
            # Run gemini for this one company with real-time streaming
            process = subprocess.Popen(
                f'gemini --model gemini-2.5-flash < "{prompt_path}"',
                shell=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1
            )
            
            full_output = []
            console.print(f"  [dim]── Researching {company['name']} ──[/dim]")
            
            # Stream output to console
            if process.stdout:
                for line in iter(process.stdout.readline, ""):
                    clean_line = line.strip()
                    if clean_line:
                        # Print to user (dimmed to separate from UI)
                        console.print(f"    [blue]>[/blue] [dim]{clean_line}[/dim]")
                        full_output.append(line)
            
            process.wait()
            output = "".join(full_output)
            
            # Save individual log for debugging
            log_dir = Path(__file__).parent / "logs"
            log_dir.mkdir(exist_ok=True)
            log_path = log_dir / f"enrich_{company['id']}_{__import__('datetime').datetime.now().strftime('%H%M%S')}.log"
            log_path.write_text(output)

            # Parse result from the captured output
            parsed_count = _parse_enrichment_output(output)
            
            # If nothing was parsed, mark as PASS to avoid looping forever
            if parsed_count == 0:
                console.print(f"  [yellow]⚠ No structured data returned by LLM. Marking as PASS to avoid looping.[/yellow]")
                update_company(company["id"], {"status": "PASS", "notes": (company.get("notes") or "") + " | Enrichment failed to return JSON"})
            else:
                # Check if it worked
                updated = get_companies(search=company['name'])
                if updated and updated[0].get('contact_name'):
                    console.print(f"  [green]✓ Enriched: {updated[0]['contact_name']} ({updated[0]['contact_role']})[/green]")
                
        except Exception as e:
            console.print(f"  [red]Error enriching {company['name']}: {e}[/red]")
        finally:
            if Path(prompt_path).exists():
                os.unlink(prompt_path)

    console.print(f"\n[bold green]✓ Batch enrichment complete[/bold green]")


def _parse_enrichment_output(output: str):
    """Parse ENRICHED: lines from Gemini output and update DB."""
    from db import update_company

    count = 0
    for line in output.splitlines():
        line = line.strip()
        if "ENRICHED:" not in line:
            continue
        try:
            # Extract JSON part
            json_str = line[line.find("ENRICHED:") + len("ENRICHED:"):].strip()
            data = json.loads(json_str)
            cid = data.pop("id", None)
            if not cid:
                continue
            data.pop("status", None)  # recompute below
            has_contact = any(data.get(f) for f in ["contact_name","contact_email","contact_linkedin"])
            data["status"] = "TO_CONTACT" if has_contact else "PASS"
            update_company(cid, data)
            count += 1
            console.print(f"  [green]✓[/green] ID {cid}: {data.get('contact_name','no contact')}")
        except json.JSONDecodeError as e:
            console.print(f"  [yellow]Parse error: {e}[/yellow]")

    console.print(f"\n[green]✓ Enriched {count} companies[/green]")
    return count


async def cmd_frenchtech(city: str):
    """Generate a Gemini prompt to scrape La French Tech for a city."""
    prompt = await fetch_french_tech_companies(city)
    path = Path(__file__).parent / "data" / f"frenchtech_{city.lower()}.txt"
    path.parent.mkdir(exist_ok=True)
    path.write_text(prompt)
    console.print(f"[green]✓ Prompt saved to {path}[/green]")
    console.print("[dim]Run this in Gemini CLI, then paste the output back for parsing.[/dim]")


def cmd_stats():
    from db import get_stats, get_prospect_cities, init_db
    init_db()
    stats = get_stats()

    t = Table(title="Prospect Stats")
    t.add_column("Status", style="cyan")
    t.add_column("Count", style="bold")
    t.add_row("Total companies", str(stats.get("total_prospects", 0)))
    t.add_row("Found today", str(stats.get("new_prospects_today", 0)))
    for s, n in stats.get("prospects_by_status", {}).items():
        t.add_row(f"  {s}", str(n))
    console.print(t)

    cities = get_prospect_cities()
    if cities:
        t2 = Table(title="Top Cities")
        t2.add_column("City")
        t2.add_column("Companies", style="bold")
        for c in cities[:10]:
            t2.add_row(c["city"], str(c["count"]))
        console.print(t2)


async def _debug_auth():
    """Test pyarrow can read the local SIRENE parquet and find SOMETHING."""
    local = Path(__file__).parent / "data" / "sirene.parquet"
    if not local.exists():
        console.print(f"[yellow]No local file. Constructing a test from the mini-parquet...[/yellow]")
        local = Path(__file__).parent / "data" / "test_rg0.parquet"
        if not local.exists():
             console.print("[red]No data files found to test.[/red]")
             return

    try:
        import pyarrow.parquet as pq
        pf = pq.ParquetFile(local)
        console.print(f"[green]✓ {local.name} — {pf.metadata.num_rows:,} rows[/green]")
        
        # Check first 100 rows for name fields
        batch = next(pf.iter_batches(batch_size=100))
        tbl = batch.to_pydict()
        
        names_found = 0
        for i in range(len(tbl["siren"])):
            name = tbl.get("denominationUsuelleEtablissement", [None]*100)[i]
            enseigne = tbl.get("enseigne1Etablissement", [None]*100)[i]
            if name or enseigne:
                console.print(f"Row {i}: Name={name}, Enseigne={enseigne}, NAF={tbl['activitePrincipaleEtablissement'][i]}")
                names_found += 1
        
        console.print(f"Found {names_found} rows with some name/enseigne in first 100.")
            
    except Exception as e:
        console.print(f"[red]✗ {e}[/red]")


# ── ENTRY POINT ───────────────────────────────────────────────────────────────

if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="JobHunter Prospector")
    sub = parser.add_subparsers(dest="cmd")

    p_scan = sub.add_parser("scan", help="Scan Pappers for tech companies")
    p_scan.add_argument("--city",        default="Bordeaux")
    p_scan.add_argument("--departments", nargs="+", default=DEFAULT_DEPARTMENTS)
    p_scan.add_argument("--min-headcount", type=int, default=5)

    p_enrich = sub.add_parser("enrich", help="Enrich companies via Gemini + LinkedIn")
    p_enrich.add_argument("--batch", type=int, default=20)

    p_ft = sub.add_parser("frenchtech", help="Scrape La French Tech for a city")
    p_ft.add_argument("--city", default="Bordeaux")

    sub.add_parser("score", help="LLM-score unscored companies")
    sub.add_parser("stats", help="Show prospect stats")
    sub.add_parser("debug-auth", help="Test SIRENE connectivity")
    sub.add_parser("download-sirene", help="Download SIRENE Parquet locally (~600MB, faster future scans)")

    args = parser.parse_args()

    if args.cmd == "scan":
        asyncio.run(cmd_scan(args.city, args.departments, args.min_headcount))
    elif args.cmd == "enrich":
        asyncio.run(cmd_enrich(args.batch))
    elif args.cmd == "frenchtech":
        asyncio.run(cmd_frenchtech(args.city))
    elif args.cmd == "score":
        asyncio.run(cmd_score())
    elif args.cmd == "stats":
        cmd_stats()
    elif args.cmd == "debug-auth":
        asyncio.run(_debug_auth())
    elif args.cmd == "download-sirene":
        asyncio.run(download_sirene())
    else:
        parser.print_help()
