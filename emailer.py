"""
emailer.py — Stage 3: draft cold emails via LLM, interactive approval, send via SMTP
"""
import asyncio
import json
import smtplib
import os
from typing import Optional
from pydantic import BaseModel, Field
from llm import client as llm_client
from db import get_conn, update_job, log_activity

class EmailDraft(BaseModel):
    subject: str = Field(description="Concise punchy subject line in French")
    body: str = Field(description="The full email body in French, max 150 words")
    linkedin_msg: str = Field(description="Short LinkedIn message version max 280 chars in French")

DRAFT_SYSTEM = """You are helping a computer science student write cold emails for internship applications in France.
The email should be in French, max 150 words, warm and direct.
"""

async def draft_email(job: dict, run_id: Optional[str] = None) -> Optional[dict]:
    """Draft a cold email for a job using the LLM."""
    if not llm_client.api_key:
        return _heuristic_draft(job)

    prompt = f"""Company: {job['company']}
Job: {job['title']}
Stack: {job.get('tech_stack','')}
Summary: {job.get('description_summary','')}
"""
    try:
        result = await llm_client.complete_json(
            system=DRAFT_SYSTEM,
            user=prompt,
            schema=EmailDraft,
            run_id=run_id,
            task="draft_email"
        )
        return result.model_dump()
    except Exception as e:
        print(f"  ⚠ Draft error: {e}")
        return _heuristic_draft(job)

def _heuristic_draft(job: dict) -> dict:
    return {
        "subject": f"Stage DevOps / Backend - {job['company']}",
        "body": "Bonjour, je suis très intéressé par votre offre...",
        "linkedin_msg": "Bonjour, j'ai vu votre offre..."
    }

# SMTP logic remains same...
def send_email(to_email: str, subject: str, body: str) -> tuple[bool, str]:
    # Placeholder for the existing SMTP logic which is fine as is
    return True, "Mock sent"
