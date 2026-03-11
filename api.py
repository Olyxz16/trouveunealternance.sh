"""
api.py — FastAPI backend for the JobHunter dashboard
Run: uvicorn api:app --reload --port 8000
"""
import asyncio
import json
from pathlib import Path
from typing import Optional
from datetime import datetime

from fastapi import FastAPI, HTTPException, Query
from fastapi.staticfiles import StaticFiles
from fastapi.responses import HTMLResponse, StreamingResponse, FileResponse
from pydantic import BaseModel

from db import (
    init_db, get_jobs, get_job, update_job, get_stats, get_recent_activity, log_activity,
    get_companies, get_company, update_company, upsert_company, get_prospect_cities,
)

app = FastAPI(title="JobHunter API")

# SSE event queue for real-time dashboard updates
_event_queue: asyncio.Queue = asyncio.Queue()


@app.on_event("startup")
def startup():
    init_db()


# ── JOBS ──────────────────────────────────────────────────────────────────────

@app.get("/api/jobs")
def list_jobs(
    status: Optional[str] = Query(None),
    type_: Optional[str] = Query(None, alias="type"),
    search: Optional[str] = Query(None),
):
    return get_jobs(status=status, type_=type_, search=search)


@app.get("/api/jobs/{job_id}")
def get_job_detail(job_id: int):
    job = get_job(job_id)
    if not job:
        raise HTTPException(404, "Job not found")
    return job


class JobUpdate(BaseModel):
    status: Optional[str] = None
    notes: Optional[str] = None
    contact_email: Optional[str] = None
    contact_name: Optional[str] = None
    contact_role: Optional[str] = None
    contact_linkedin: Optional[str] = None
    careers_page_url: Optional[str] = None


@app.patch("/api/jobs/{job_id}")
def patch_job(job_id: int, body: JobUpdate):
    job = get_job(job_id)
    if not job:
        raise HTTPException(404, "Job not found")

    fields = {k: v for k, v in body.dict().items() if v is not None}
    if not fields:
        raise HTTPException(400, "No fields to update")

    update_job(job_id, fields)
    if "status" in fields:
        log_activity(job_id, "STATUS_CHANGE", fields["status"])
        asyncio.create_task(_notify_sse({"type": "status_change", "job_id": job_id, "status": fields["status"]}))
    return {"ok": True, "updated": fields}


# ── STATS ─────────────────────────────────────────────────────────────────────

@app.get("/api/stats")
def stats():
    return get_stats()


@app.get("/api/activity")
def activity(limit: int = 30):
    return get_recent_activity(limit)


# ── EMAIL DRAFT ───────────────────────────────────────────────────────────────

@app.post("/api/jobs/{job_id}/draft")
async def generate_draft(job_id: int):
    from emailer import draft_email
    job = get_job(job_id)
    if not job:
        raise HTTPException(404, "Job not found")

    draft = await draft_email(job)
    if not draft:
        raise HTTPException(500, "Draft generation failed")

    update_job(job_id, {"email_draft": json.dumps(draft)})
    log_activity(job_id, "DRAFT_GENERATED")
    return draft


@app.post("/api/jobs/{job_id}/send")
async def send_email_endpoint(job_id: int, draft: dict):
    from emailer import send_email
    job = get_job(job_id)
    if not job:
        raise HTTPException(404, "Job not found")
    if not job.get("contact_email"):
        raise HTTPException(400, "No contact email for this job")

    ok, msg = send_email(job["contact_email"], draft["subject"], draft["body"])
    if ok:
        update_job(job_id, {"status": "CONTACTED"})
        log_activity(job_id, "EMAIL_SENT", job["contact_email"])
        return {"ok": True, "message": msg}
    raise HTTPException(500, msg)


# ── PIPELINE CONTROL ──────────────────────────────────────────────────────────

@app.post("/api/pipeline/stage1")
async def trigger_stage1():
    """Trigger Stage 1 scraping (non-blocking)."""
    asyncio.create_task(_run_stage1_bg())
    return {"ok": True, "message": "Stage 1 started"}


@app.post("/api/pipeline/stage2")
async def trigger_stage2():
    asyncio.create_task(_run_stage2_bg())
    return {"ok": True, "message": "Stage 2 started"}


async def _run_stage1_bg():
    from scraper import run_stage1
    await _notify_sse({"type": "pipeline", "stage": "1", "status": "started"})
    try:
        await run_stage1()
        await _notify_sse({"type": "pipeline", "stage": "1", "status": "done"})
    except Exception as e:
        await _notify_sse({"type": "pipeline", "stage": "1", "status": "error", "error": str(e)})


async def _run_stage2_bg():
    from scraper import run_stage2
    await _notify_sse({"type": "pipeline", "stage": "2", "status": "started"})
    try:
        await run_stage2()
        await _notify_sse({"type": "pipeline", "stage": "2", "status": "done"})
    except Exception as e:
        await _notify_sse({"type": "pipeline", "stage": "2", "status": "error", "error": str(e)})


# ── PROSPECTS ─────────────────────────────────────────────────────────────────

@app.get("/api/prospects")
def list_prospects(
    status: Optional[str] = Query(None),
    city: Optional[str] = Query(None),
    search: Optional[str] = Query(None),
    min_score: int = Query(0),
):
    return get_companies(status=status, city=city, search=search, min_score=min_score)


@app.get("/api/prospects/cities")
def prospect_cities():
    return get_prospect_cities()


@app.get("/api/prospects/{company_id}")
def get_prospect_detail(company_id: int):
    c = get_company(company_id)
    if not c:
        raise HTTPException(404, "Company not found")
    return c


class CompanyUpdate(BaseModel):
    status: Optional[str] = None
    notes: Optional[str] = None
    contact_name: Optional[str] = None
    contact_role: Optional[str] = None
    contact_email: Optional[str] = None
    contact_linkedin: Optional[str] = None
    website: Optional[str] = None
    linkedin_url: Optional[str] = None
    careers_page_url: Optional[str] = None
    tech_stack: Optional[str] = None
    relevance_score: Optional[int] = None


@app.patch("/api/prospects/{company_id}")
def patch_prospect(company_id: int, body: CompanyUpdate):
    c = get_company(company_id)
    if not c:
        raise HTTPException(404, "Company not found")
    fields = {k: v for k, v in body.dict().items() if v is not None}
    if not fields:
        raise HTTPException(400, "No fields to update")
    update_company(company_id, fields)
    if "status" in fields:
        asyncio.create_task(_notify_sse({"type": "prospect_update", "company_id": company_id}))
    return {"ok": True, "updated": fields}


@app.post("/api/prospects/{company_id}/draft")
async def draft_prospect_email(company_id: int):
    """Draft a cold email for a prospect company (no job listing needed)."""
    from emailer import draft_prospect_email as _draft
    c = get_company(company_id)
    if not c:
        raise HTTPException(404, "Company not found")
    draft = await _draft(c)
    if not draft:
        raise HTTPException(500, "Draft generation failed")
    update_company(company_id, {"email_draft": json.dumps(draft)})
    return draft


@app.post("/api/prospects/{company_id}/send")
async def send_prospect_email(company_id: int, draft: dict):
    from emailer import send_email
    c = get_company(company_id)
    if not c:
        raise HTTPException(404, "Company not found")
    if not c.get("contact_email"):
        raise HTTPException(400, "No contact email")
    ok, msg = send_email(c["contact_email"], draft["subject"], draft["body"])
    if ok:
        update_company(company_id, {"status": "CONTACTED"})
        return {"ok": True}
    raise HTTPException(500, msg)


@app.post("/api/pipeline/scan")
async def trigger_scan(city: str = "Bordeaux", departments: str = "33,47,40"):
    dept_list = [d.strip() for d in departments.split(",")]
    asyncio.create_task(_run_scan_bg(city, dept_list))
    return {"ok": True, "message": f"Scan started for {city}"}


@app.post("/api/pipeline/enrich-prospects")
async def trigger_enrich_prospects():
    asyncio.create_task(_run_prospect_enrich_bg())
    return {"ok": True, "message": "Prospect enrichment started"}


async def _run_scan_bg(city: str, departments: list[str]):
    from prospector import cmd_scan
    await _notify_sse({"type": "pipeline", "stage": "scan", "status": "started"})
    try:
        await cmd_scan(city, departments, min_headcount=5)
        await _notify_sse({"type": "pipeline", "stage": "scan", "status": "done"})
    except Exception as e:
        await _notify_sse({"type": "pipeline", "stage": "scan", "status": "error", "error": str(e)})


async def _run_prospect_enrich_bg():
    from prospector import cmd_enrich
    await _notify_sse({"type": "pipeline", "stage": "enrich_prospects", "status": "started"})
    try:
        await cmd_enrich()
        await _notify_sse({"type": "pipeline", "stage": "enrich_prospects", "status": "done"})
    except Exception as e:
        await _notify_sse({"type": "pipeline", "stage": "enrich_prospects", "status": "error", "error": str(e)})


@app.get("/api/export/prospects")
def export_prospects_tsv():
    companies = get_companies()
    if not companies:
        raise HTTPException(404, "No prospects")
    headers = list(companies[0].keys())
    lines = ["\t".join(headers)]
    for c in companies:
        lines.append("\t".join(str(c.get(h, "") or "") for h in headers))
    return StreamingResponse(
        iter(["\n".join(lines)]),
        media_type="text/tab-separated-values",
        headers={"Content-Disposition": "attachment; filename=prospects.tsv"}
    )


# ── SSE ───────────────────────────────────────────────────────────────────────

async def _notify_sse(data: dict):
    await _event_queue.put(data)


@app.get("/api/events")
async def sse_stream():
    """Server-sent events for real-time dashboard updates."""
    async def generator():
        yield "data: {\"type\": \"connected\"}\n\n"
        while True:
            try:
                event = await asyncio.wait_for(_event_queue.get(), timeout=30)
                yield f"data: {json.dumps(event)}\n\n"
            except asyncio.TimeoutError:
                yield "data: {\"type\": \"ping\"}\n\n"
    return StreamingResponse(generator(), media_type="text/event-stream")


# ── EXPORT ────────────────────────────────────────────────────────────────────

@app.get("/api/export/tsv")
def export_tsv():
    jobs = get_jobs()
    if not jobs:
        raise HTTPException(404, "No jobs to export")
    headers = list(jobs[0].keys())
    lines = ["\t".join(headers)]
    for job in jobs:
        lines.append("\t".join(str(job.get(h, "") or "") for h in headers))
    content = "\n".join(lines)
    return StreamingResponse(
        iter([content]),
        media_type="text/tab-separated-values",
        headers={"Content-Disposition": "attachment; filename=jobs.tsv"}
    )


# ── SERVE DASHBOARD ───────────────────────────────────────────────────────────

DASHBOARD_PATH = Path(__file__).parent / "static" / "index.html"


@app.get("/", response_class=HTMLResponse)
def serve_dashboard():
    if DASHBOARD_PATH.exists():
        return HTMLResponse(DASHBOARD_PATH.read_text())
    return HTMLResponse("<h1>Dashboard not found. Run build step.</h1>")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run("api:app", host="0.0.0.0", port=8000, reload=True)
