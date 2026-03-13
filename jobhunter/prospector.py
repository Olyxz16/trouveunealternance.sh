"""
prospector.py — Pipeline 0: discover local tech companies
Sources: SIRENE (data.gouv.fr), La French Tech, LinkedIn
"""
import asyncio
import json
import os
import sys
import httpx
import subprocess
import tempfile
from pathlib import Path
from typing import Optional, List, Literal
from pydantic import BaseModel, Field
from jobhunter.llm import client as llm_client
from jobhunter.db import get_conn, upsert_company, update_company, get_companies, init_db
from jobhunter.scraper.pipeline import pipeline_step
from jobhunter.errors import Ok, Err
from rich.console import Console
from rich.table import Table
from rich.progress import track

console = Console()

# SIRENE StockEtablissement — official monthly Parquet from data.gouv.fr
SIRENE_PARQUET_URL = "https://www.data.gouv.fr/fr/datasets/r/a29c1297-1f92-4e2a-8f6b-8c902ce96c5f"

# Pure TECH NAF codes (62xx, 63xx)
TECH_NAF_PREFIXES = ["62", "63"]

NAF_LABELS = {
    "6201Z": "Programmation informatique",
    "6202A": "Conseil en systèmes et logiciels informatiques",
    "6202B": "Tierce maintenance de systèmes et d'applications informatiques",
    "6203Z": "Gestion d'installations informatiques",
    "6209Z": "Autres activités informatiques",
    "6311Z": "Traitement de données, hébergement et activités connexes",
    "6312Z": "Portails Internet",
}

class CompanyScore(BaseModel):
    relevance_score: int = Field(ge=0, le=10)
    company_type: Literal["TECH", "TECH_ADJACENT", "NON_TECH"]
    has_internal_tech_team: bool
    tech_team_signals: List[str]
    reasoning: str

SCORE_SYSTEM = """You are evaluating French companies as potential internship hosts for a DevOps/backend student.

Classification:
- TECH: Product is software/infra.
- TECH_ADJACENT: Non-tech business (retail, bank, logistics) but large enough (100+ emp) to have internal IT/infra.
- NON_TECH: No meaningful tech needs.

For TECH_ADJACENT, look for signals: digital transformation, tech blog, job postings for devs despite non-tech core business.
Score 0-10 based on stack relevance and company profile.
"""

@pipeline_step("score_company")
async def llm_score_company(company: dict, run_id: Optional[str] = None, company_id: Optional[int] = None):
    if not llm_client.api_key:
        return _heuristic_score(company)

    prompt = f"""Company: {company['name']}
NAF: {company.get('naf_code')} - {company.get('naf_label')}
City: {company.get('city')}
Size: {company.get('headcount_range')} employees
Description: {company.get('description') or 'none'}"""

    result = await llm_client.complete_json(
        system=SCORE_SYSTEM,
        user=prompt,
        schema=CompanyScore,
        run_id=run_id,
        task="score_company"
    )
    return result.model_dump()

def _heuristic_score(company: dict) -> dict:
    return {
        "relevance_score": 5,
        "company_type": "TECH",
        "has_internal_tech_team": True,
        "tech_team_signals": ["Heuristic match"],
        "reasoning": "Fallback score"
    }

# ── SIRENE SCAN ───────────────────────────────────────────────────────────────

@pipeline_step("scan_sirene")
async def cmd_scan(city: str, departments: list[str], min_headcount: int = 5, run_id: Optional[str] = None):
    init_db()
    console.rule(f"[bold]Scanning SIRENE for {city}[/bold]")
    
    companies = scan_sirene_local(departments=departments, min_headcount=min_headcount)
    
    new_count = 0
    for c in companies:
        # SIRENE Pre-filter (Stage 1 of classification)
        naf = c.get("naf_code", "").replace(".", "")
        hc_label = c.get("headcount_range", "")
        
        # Heuristic for headcount (e.g. "100-199" -> 100)
        hc_val = 0
        try: hc_val = int(hc_label.split("-")[0])
        except: pass

        if any(naf.startswith(p) for p in TECH_NAF_PREFIXES):
            c["company_type"] = "TECH"
            c["status"] = "NEW"
        elif hc_val >= 100:
            # Candidate for TECH_ADJACENT, needs LLM check
            c["company_type"] = "UNKNOWN"
            c["status"] = "NEW"
        else:
            # Too small and not tech NAF
            continue

        _, is_new = upsert_company(c)
        if is_new: new_count += 1
    
    console.print(f"[green]✓ Found {len(companies)} candidates, {new_count} new added to DB.[/green]")
    await cmd_score(only_new=True, run_id=run_id)
    return {"total": len(companies), "new": new_count}

def scan_sirene_local(
    departments: list[str],
    min_headcount: int = 5,
    data_dir: Path = None,
) -> list[dict]:
    try:
        import pyarrow.parquet as pq
    except ImportError:
        console.print("[red]pyarrow not installed — run: pip install pyarrow requests[/red]")
        return []

    if data_dir is None:
        data_dir = Path(__file__).parent.parent / "data"
    
    local_path = data_dir / "sirene.parquet"
    if not local_path.exists():
        console.print("[yellow]No local sirene.parquet found. Run 'task download-sirene' first.[/yellow]")
        return []

    headcount_map = {
        "03": 6, "11": 10, "12": 20, "21": 50, "22": 100,
        "31": 200, "32": 250, "41": 500, "42": 1000,
        "51": 2000, "52": 5000, "53": 10000,
    }
    valid_headcount = [k for k, v in headcount_map.items() if v >= min_headcount]
    headcount_set = set(valid_headcount)

    console.print(f"  Reading {local_path.name}...")

    try:
        pf = pq.ParquetFile(local_path)
        companies = []
        dept_set = set(departments)

        needed_cols = [
            "siren", "siret", "denominationUsuelleEtablissement", "enseigne1Etablissement",
            "activitePrincipaleEtablissement", "etatAdministratifEtablissement",
            "trancheEffectifsEtablissement", "codePostalEtablissement",
            "libelleCommuneEtablissement",
        ]
        available = pf.schema_arrow.names
        cols = [c for c in needed_cols if c in available]

        for batch in pf.iter_batches(batch_size=100_000, columns=cols):
            tbl = batch.to_pydict()
            n = len(tbl["siren"])
            for i in range(n):
                if (tbl["etatAdministratifEtablissement"][i] or "") != "A": continue
                cp = (tbl["codePostalEtablissement"][i] or "")
                if cp[:2] not in dept_set: continue
                
                hc = (tbl["trancheEffectifsEtablissement"][i] or "")
                if hc and hc not in headcount_set: continue
                
                naf = (tbl["activitePrincipaleEtablissement"][i] or "")
                name = ((tbl["denominationUsuelleEtablissement"][i] or "") or 
                        (tbl["enseigne1Etablissement"][i] or "") or
                        f"Company {tbl['siren'][i]}").strip()

                companies.append({
                    "name": name,
                    "siren": tbl["siren"][i],
                    "siret": tbl["siret"][i],
                    "naf_code": naf,
                    "naf_label": NAF_LABELS.get(naf.replace(".", ""), ""),
                    "city": tbl.get("libelleCommuneEtablissement", [None]*n)[i],
                    "department": cp[:2],
                    "headcount_range": _headcount_label(hc),
                    "source": "sirene",
                })
        return companies
    except Exception as e:
        console.print(f"[red]Parquet read error: {e}[/red]")
        return []

def _headcount_label(code: str) -> str:
    labels = {
        "NN": "0", "00": "0", "01": "1-2", "02": "3-5", "03": "6-9",
        "11": "10-19", "12": "20-49", "21": "50-99", "22": "100-199",
        "31": "200-249", "32": "250-499", "41": "500-999", "42": "1000-1999",
        "51": "2000-4999", "52": "5000-9999", "53": "10000+",
    }
    return labels.get(str(code).strip(), code or "?")

# ── ENRICHMENT ────────────────────────────────────────────────────────────────

def build_enrichment_prompt(companies: list[dict]) -> str:
    company_list = "\n".join(
        f'- ID {c["id"]}: {c["name"]} ({c.get("city","")}) — {c.get("naf_label","")}, {c.get("headcount_range","?")} employees'
        for c in companies
    )
    return f"""You are helping me build a list of tech companies to research for a DevOps/backend internship.

## Companies to research:
{company_list}

## For each company, do the following:
1. Search "[company name] site:linkedin.com" to find their LinkedIn company page
2. Extract tech stack hints, recent posts, and find 1 contact (prefer tech lead, CTO, or HR/talent).
3. Identify if it is TECH, TECH_ADJACENT or NON_TECH.
4. Rate their internship-friendliness 0-10.

## For each company, print a JSON line starting with ENRICHED:
{{
  "id": <id from list>,
  "website": "url or null",
  "linkedin_url": "company linkedin url or null",
  "headcount_exact": null_or_number,
  "tech_stack": "comma-separated list",
  "description": "brief description",
  "company_type": "TECH | TECH_ADJACENT | NON_TECH",
  "has_internal_tech_team": true/false,
  "tech_team_signals": ["signal 1", "signal 2"],
  "contact_name": "name or null",
  "contact_role": "role or null",
  "contact_linkedin": "profile url or null",
  "contact_email": "email if publicly visible",
  "relevance_score": 0-10,
  "status": "TO_CONTACT" if score >= 5 else "PASS"
}}

Print ENRICHMENT_DONE when finished."""

def _parse_enrichment_output(output: str):
    from jobhunter.db import add_contact
    count = 0
    for line in output.splitlines():
        if "ENRICHED:" in line:
            try:
                json_str = line[line.find("ENRICHED:") + 9:].strip()
                data = json.loads(json_str)
                cid = data.pop("id", None)
                if cid:
                    contact_data = {
                        "name": data.pop("contact_name", None),
                        "role": data.pop("contact_role", None),
                        "email": data.pop("contact_email", None),
                        "linkedin_url": data.pop("contact_linkedin", None),
                        "source": "linkedin",
                        "confidence": "probable",
                        "is_primary": True
                    }
                    if data.get("tech_team_signals"):
                        data["tech_team_signals"] = ", ".join(data["tech_team_signals"])
                    
                    update_company(cid, data)
                    if contact_data["name"] or contact_data["email"]:
                        add_contact(cid, contact_data)
                    count += 1
            except Exception as e:
                console.print(f"  [yellow]Parse error: {e}[/yellow]")
    return count

# ── COMMANDS ──────────────────────────────────────────────────────────────────

async def download_sirene():
    import requests
    data_dir = Path(__file__).parent.parent / "data"
    data_dir.mkdir(exist_ok=True)
    dest = data_dir / "sirene.parquet"
    if dest.exists():
        console.print(f"[yellow]SIRENE file already exists.[/yellow]")
        return
    console.print(f"[yellow]Downloading SIRENE Parquet (~2GB)...[/yellow]")
    with requests.get(SIRENE_PARQUET_URL, stream=True, timeout=600) as r:
        r.raise_for_status()
        total = int(r.headers.get("content-length", 0))
        downloaded = 0
        with open(dest, "wb") as f:
            for chunk in r.iter_content(chunk_size=1024 * 1024):
                f.write(chunk)
                downloaded += len(chunk)
                if total:
                    print(f"\r  {downloaded/total*100:.1f}% ({downloaded//1024//1024}MB)", end="", flush=True)
    console.print(f"\n[green]✓ Saved to {dest}[/green]")

async def cmd_score(only_new: bool = False, run_id: Optional[str] = None):
    companies = get_companies(status="NEW" if only_new else None)
    unscored = [c for c in companies if not c.get("relevance_score")]
    if not unscored: return
    console.print(f"Scoring {len(unscored)} companies...")
    for company in track(unscored, description="Scoring..."):
        res = await llm_score_company(company, run_id=run_id, company_id=company["id"])
        if isinstance(res, Ok):
            data = res.value
            update_company(company["id"], {
                "relevance_score": data["relevance_score"],
                "company_type": data["company_type"],
                "has_internal_tech_team": data["has_internal_tech_team"],
                "tech_team_signals": ", ".join(data["tech_team_signals"]),
                "notes": (company.get("notes") or "") + f" | {data['reasoning']}"
            })

async def cmd_target(batch_size: int = 10):
    """Target prospects: score them if needed, then enrich in batch."""
    from uuid import uuid4
    run_id = str(uuid4())
    init_db()
    await cmd_score(only_new=True, run_id=run_id)
    await cmd_enrich(batch_size=batch_size, run_id=run_id)

@pipeline_step("enrich_prospect")
async def _enrich_single_company(company: dict, run_id: Optional[str] = None):
    prompt = build_enrichment_prompt([company])
    
    with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False) as f:
        f.write(prompt)
        prompt_path = f.name

    try:
        cmd = f'gemini < "{prompt_path}"'
        process = subprocess.Popen(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        
        full_output = []
        for line in iter(process.stdout.readline, ""):
            print(line, end="")
            full_output.append(line)
        
        process.wait()
        count = _parse_enrichment_output("".join(full_output))
        return {"success": count > 0}
    finally:
        if os.path.exists(prompt_path): os.unlink(prompt_path)

async def cmd_enrich(batch_size: int = 10, run_id: Optional[str] = None):
    """Enrich prospects: research LinkedIn/Web for contacts."""
    all_eligible = [
        c for c in get_companies(min_score=5) 
        if c["status"] in ("NEW", "ENRICHING", "TO_ENRICH") 
        and not c.get("primary_contact_id")
    ]
    
    companies = all_eligible[:batch_size]
    if not companies:
        console.print("[yellow]No scored companies found for enrichment.[/yellow]")
        return

    console.rule(f"[bold]Enriching {len(companies)} companies (one agent per company)[/bold]")
    
    for i, company in enumerate(companies):
        console.print(f"\n[bold cyan]▶ [{i+1}/{len(companies)}] Researching: {company['name']}[/bold cyan]")
        await _enrich_single_company(company, run_id=run_id, company_id=company["id"])
        await asyncio.sleep(1)

async def cmd_frenchtech(city: str):
    prompt = f"Scrape French Tech companies in {city}..."
    path = Path(__file__).parent.parent / "data" / f"frenchtech_{city.lower()}.txt"
    path.write_text(prompt)
    console.print(f"[green]✓ Prompt saved to {path}[/green]")
