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

# SIRENE official monthly Parquet from data.gouv.fr
SIRENE_ETAB_URL = "https://www.data.gouv.fr/fr/datasets/r/a29c1297-1f92-4e2a-8f6b-8c902ce96c5f"
SIRENE_UL_URL = "https://www.data.gouv.fr/fr/datasets/r/6fb0299d-00eb-40da-90ae-cd90ca9cc9fb"

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

# ── SIRENE DOWNLOADS ─────────────────────────────────────────────────────────

async def download_sirene_ul():
    await _download_file(SIRENE_UL_URL, "sirene_unites_legales.parquet")

async def download_sirene():
    await _download_file(SIRENE_ETAB_URL, "sirene_etablissements.parquet")

async def _download_file(url: str, filename: str):
    import requests
    data_dir = Path(__file__).parent.parent / "data"
    data_dir.mkdir(exist_ok=True)
    dest = data_dir / filename
    if dest.exists():
        console.print(f"[yellow]{filename} already exists.[/yellow]")
        return
    console.print(f"[yellow]Downloading {filename} (~2GB)...[/yellow]")
    with requests.get(url, stream=True, timeout=600) as r:
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

# ── SCORING ──────────────────────────────────────────────────────────────────

@pipeline_step("score_company")
async def llm_score_company(company: dict, run_id: Optional[str] = None, company_id: Optional[int] = None):
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

# ── SIRENE SCAN ───────────────────────────────────────────────────────────────

@pipeline_step("scan_sirene")
async def cmd_scan(city: str, departments: list[str], min_headcount: int = 5, run_id: Optional[str] = None):
    init_db()
    console.rule(f"[bold]Scanning SIRENE for {city}[/bold]")
    
    companies = scan_sirene_local(departments=departments, min_headcount=min_headcount)
    
    new_count = 0
    for c in companies:
        naf = c.get("naf_code", "").replace(".", "")
        hc_label = c.get("headcount_range", "")
        
        # Heuristic for headcount
        hc_val = 0
        try: hc_val = int(hc_label.split("-")[0])
        except: pass

        if any(naf.startswith(p) for p in TECH_NAF_PREFIXES):
            c["company_type"] = "TECH"
            c["status"] = "NEW"
        elif hc_val >= 100:
            c["company_type"] = "UNKNOWN"
            c["status"] = "NEW"
        else:
            continue

        _, is_new = upsert_company(c)
        if is_new: new_count += 1
    
    console.print(f"[green]✓ Found {len(companies)} candidates, {new_count} new added to DB.[/green]")
    return {"total": len(companies), "new": new_count}

def scan_sirene_local(
    departments: list[str],
    min_headcount: int = 5,
    data_dir: Path = None,
) -> list[dict]:
    if data_dir is None:
        data_dir = Path(__file__).parent.parent / "data"
    
    etab_path = data_dir / "sirene_etablissements.parquet"
    ul_path = data_dir / "sirene_unites_legales.parquet"

    if not etab_path.exists() or not ul_path.exists():
        console.print("[yellow]Missing SIRENE files. Run 'task download-sirene' and 'task download-sirene-ul' first.[/yellow]")
        return []

    dept_list = ", ".join([f"'{d}'" for d in departments])
    
    headcount_map = {
        "03": 6, "11": 10, "12": 20, "21": 50, "22": 100,
        "31": 200, "32": 250, "41": 500, "42": 1000,
        "51": 2000, "52": 5000, "53": 10000,
    }
    headcount_codes = [f"'{c}'" for c, v in headcount_map.items() if v >= min_headcount]
    hc_filter = ", ".join(headcount_codes)

    console.print(f"  Joining {etab_path.name} and {ul_path.name} via DuckDB...")

    query = f"""
    SELECT 
        e.siren, 
        e.siret, 
        COALESCE(u.denominationUniteLegale, e.denominationUsuelleEtablissement, e.enseigne1Etablissement, 'Company ' || e.siren) as name,
        u.denominationUniteLegale as legal_name,
        u.sigleUniteLegale as acronym,
        e.activitePrincipaleEtablissement as naf_code,
        e.trancheEffectifsEtablissement as headcount_code,
        e.codePostalEtablissement as zip,
        e.libelleCommuneEtablissement as city,
        e.numeroVoieEtablissement as address_num,
        e.typeVoieEtablissement as address_type,
        e.libelleVoieEtablissement as address_street
    FROM read_parquet('{etab_path}') e
    JOIN read_parquet('{ul_path}') u ON e.siren = u.siren
    WHERE e.etatAdministratifEtablissement = 'A'
    AND SUBSTR(e.codePostalEtablissement, 1, 2) IN ({dept_list})
    AND e.trancheEffectifsEtablissement IN ({hc_filter})
    """

    import subprocess
    import csv
    import io

    try:
        cmd = ["duckdb", "-csv", "-c", query]
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        
        companies = []
        reader = csv.DictReader(io.StringIO(result.stdout))
        for row in reader:
            naf = row["naf_code"]
            addr = f"{row['address_num']} {row['address_type']} {row['address_street']}".strip()
            
            companies.append({
                "name": row["name"],
                "legal_name": row["legal_name"],
                "acronym": row["acronym"],
                "siren": row["siren"],
                "siret": row["siret"],
                "naf_code": naf,
                "naf_label": NAF_LABELS.get(naf.replace(".", ""), ""),
                "city": row["city"],
                "department": row["zip"][:2],
                "address": addr,
                "headcount_range": _headcount_label(row["headcount_code"]),
                "source": "sirene",
            })
        return companies
    except Exception as e:
        console.print(f"[red]DuckDB join error: {e}[/red]")
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
    return f"""You are helping me research tech companies for a DevOps/backend internship.
    
Use your browser tools to:
1. Search for the company's LinkedIn company page.
2. Find their official website.
3. Extract their tech stack (Docker, K8s, Cloud providers, languages).
4. Find a primary technical contact (CTO, Engineering Manager, Tech Lead) or Recruiter.
5. Identify if they are TECH, TECH_ADJACENT, or NON_TECH.

IMPORTANT: If it's a one-person business or freelancer, classify as NON_TECH and status PASS.

## Companies to research:
{company_list}

## For each company, print a JSON line starting with ENRICHED:
{{
  "id": <id from list>,
  "official_name": "the full, correct name of the company",
  "website": "url",
  "linkedin_url": "url",
  "description": "short summary",
  "tech_stack": "comma-separated list",
  "company_type": "TECH | TECH_ADJACENT | NON_TECH",
  "has_internal_tech_team": true/false,
  "tech_team_signals": ["signal 1", "signal 2"],
  "contact_name": "name or null",
  "contact_role": "role or null",
  "contact_linkedin": "profile url or null",
  "contact_email": "email if public",
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
                    # Update company with LLM findings
                    update_company(cid, {
                        "name": data.get("official_name") or data.get("name"),
                        "website": data.get("website"),
                        "linkedin_url": data.get("linkedin_url"),
                        "description": data.get("description"),
                        "tech_stack": data.get("tech_stack"),
                        "company_type": data.get("company_type"),
                        "has_internal_tech_team": data.get("has_internal_tech_team"),
                        "tech_team_signals": ", ".join(data.get("tech_team_signals", [])),
                        "status": data.get("status")
                    })
                    
                    if data.get("contact_name"):
                        add_contact(cid, {
                            "name": data.get("contact_name"),
                            "role": data.get("contact_role"),
                            "email": data.get("contact_email"),
                            "linkedin_url": data.get("contact_linkedin"),
                            "source": "linkedin",
                            "confidence": "probable",
                            "is_primary": True
                        })
                    count += 1
            except Exception as e:
                console.print(f"  [yellow]Parse error: {e}[/yellow]")
    return count

@pipeline_step("enrich_prospect")
async def _enrich_single_company(company: dict, run_id: Optional[str] = None):
    prompt = build_enrichment_prompt([company])
    
    with tempfile.NamedTemporaryFile(mode='w', suffix='.txt', delete=False) as f:
        f.write(prompt)
        prompt_path = f.name

    try:
        # We use gemini CLI for this because it has the browser tools
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
    # Only enrich scored companies that aren't NON_TECH
    all_eligible = [
        c for c in get_companies(min_score=1) 
        if c["status"] == "NEW" 
        and c["company_type"] != "NON_TECH"
        and not c.get("primary_contact_id")
    ]
    
    companies = all_eligible[:batch_size]
    if not companies:
        console.print("[yellow]No eligible companies found for enrichment.[/yellow]")
        return

    console.rule(f"[bold]Enriching {len(companies)} companies[/bold]")
    
    for i, company in enumerate(companies):
        console.print(f"\n[bold cyan]▶ [{i+1}/{len(companies)}] Researching: {company['name']}[/bold cyan]")
        await _enrich_single_company(company, run_id=run_id)
        await asyncio.sleep(1)

async def cmd_score_prospects(run_id: Optional[str] = None):
    companies = get_companies(status="NEW")
    unscored = [c for c in companies if not c.get("relevance_score")]
    if not unscored:
        console.print("[yellow]No unscored companies found.[/yellow]")
        return
    
    console.rule(f"[bold]Scoring {len(unscored)} companies[/bold]")
    for company in track(unscored, description="Scoring..."):
        res = await llm_score_company(company, run_id=run_id, company_id=company["id"])
        # Update DB
        update_company(company["id"], {
            "relevance_score": res["relevance_score"],
            "company_type": res["company_type"],
            "has_internal_tech_team": 1 if res["has_internal_tech_team"] else 0,
            "tech_team_signals": ", ".join(res["tech_team_signals"]),
            "notes": (company.get("notes") or "") + f" | {res['reasoning']}"
        })
