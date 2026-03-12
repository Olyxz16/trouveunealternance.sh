"""
prospector.py — Pipeline 0: discover local tech companies
"""
import asyncio
import json
import os
from typing import Optional, List, Literal
from pydantic import BaseModel, Field
from llm import client as llm_client
from db import get_conn, upsert_company, update_company, get_companies

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

Score 0-10 based on stack relevance and company profile.
"""

async def llm_score_company(company: dict, run_id: Optional[str] = None) -> Optional[dict]:
    """Score a company using LLM."""
    if not llm_client.api_key:
        return _heuristic_score(company)

    prompt = f"""Company: {company['name']}
NAF: {company.get('naf_code')} - {company.get('naf_label')}
City: {company.get('city')}
Size: {company.get('headcount_range')} employees
Description: {company.get('description') or 'none'}"""

    try:
        result = await llm_client.complete_json(
            system=SCORE_SYSTEM,
            user=prompt,
            schema=CompanyScore,
            run_id=run_id,
            task="score_company"
        )
        return result.model_dump()
    except Exception as e:
        print(f"  ⚠ Scoring error for {company['name']}: {e}")
        return _heuristic_score(company)

def _heuristic_score(company: dict) -> dict:
    # Simplified fallback
    return {
        "relevance_score": 5,
        "company_type": "TECH",
        "has_internal_tech_team": True,
        "tech_team_signals": ["Heuristic match"],
        "reasoning": "Fallback score"
    }

# ... (rest of the file remains similar but uses llm_client and new DB schema)
