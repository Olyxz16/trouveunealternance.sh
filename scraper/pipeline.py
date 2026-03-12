"""
scraper/pipeline.py — Pipeline orchestration, @pipeline_step decorator, and run logging.
"""
import functools
import time
import uuid
import traceback
from datetime import datetime
from typing import Callable, Any, Optional, TypeVar, Generic
from db import get_conn, log_activity
from errors import JobHunterError, Ok, Err, Result

T = TypeVar("T")

def pipeline_step(step_name: str):
    """
    Decorator to wrap a pipeline function.
    Catches exceptions, logs to run_log, and returns a Result.
    """
    def decorator(func: Callable[..., Any]):
        @functools.wraps(func)
        async def wrapper(*args, **kwargs):
            run_id = kwargs.get("run_id")
            company_id = kwargs.get("company_id")
            job_id = kwargs.get("job_id")
            
            start_time = time.time()
            try:
                result_value = await func(*args, **kwargs)
                duration_ms = int((time.time() - start_time) * 1000)
                
                _log_step(
                    run_id=run_id,
                    company_id=company_id,
                    job_id=job_id,
                    step=step_name,
                    status="ok",
                    duration_ms=duration_ms
                )
                return Ok(result_value)
                
            except Exception as e:
                duration_ms = int((time.time() - start_time) * 1000)
                error_type = type(e).__name__
                error_msg = str(e)
                
                _log_step(
                    run_id=run_id,
                    company_id=company_id,
                    job_id=job_id,
                    step=step_name,
                    status="error",
                    error_type=error_type,
                    error_msg=error_msg,
                    duration_ms=duration_ms
                )
                
                if isinstance(e, JobHunterError):
                    return Err(e)
                return Err(JobHunterError(f"Unexpected error in {step_name}: {error_msg}"))
        
        return wrapper
    return decorator

def _log_step(run_id, company_id, job_id, step, status, error_type=None, error_msg=None, duration_ms=None):
    with get_conn() as conn:
        conn.execute("""
            INSERT INTO run_log (run_id, company_id, job_id, step, status, error_type, error_msg, duration_ms)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        """, (run_id, company_id, job_id, step, status, error_type, error_msg, duration_ms))

class Pipeline:
    def __init__(self):
        self.run_id = str(uuid.uuid4())

    async def run_enrichment(self, company_id: int):
        """
        Full enrichment pipeline for a single company.
        """
        print(f"  [Pipeline] Starting enrichment for company {company_id} (Run: {self.run_id})")
        
        # 1. Fetch website/LinkedIn
        # 2. Extract info
        # 3. Save contacts
        # ... this will be expanded as we integrate fetcher and parsers
        pass

    async def run_daily(self):
        """
        Main entry point for daily automated runs.
        """
        # 1. Scrape new jobs (Stage 1)
        # 2. Enrich new companies (Stage 2)
        pass
