"""
scraper/parsers/careers_page.py — Generic company page and LinkedIn extractor.
"""
from typing import List, Optional, Literal
from pydantic import BaseModel, Field
from llm import client as llm_client

class RawCompanyPage(BaseModel):
    name: str = Field(description="Company name")
    description: str = Field(description="Company summary/mission")
    city: Optional[str] = Field(None, description="City where company is based")
    headcount: Optional[str] = Field(None, description="Approximate employee count")
    tech_stack: List[str] = Field(default_factory=list, description="List of technologies mentioned (Docker, K8s, Python, etc.)")
    github_org: Optional[str] = Field(None, description="URL of their GitHub organization")
    engineering_blog_url: Optional[str] = Field(None, description="URL of their technical/engineering blog")
    open_source_mentioned: bool = Field(False, description="Whether they mention contributing to open source")
    infrastructure_keywords: List[str] = Field(default_factory=list, description="Keywords related to infra/platform (AWS, GCP, Terraform, etc.)")
    
    contact_name: Optional[str] = Field(None, description="Name of a potential technical contact")
    contact_role: Optional[str] = Field(None, description="Role of the technical contact")
    contact_linkedin: Optional[str] = Field(None, description="LinkedIn URL of the contact")
    contact_email: Optional[str] = Field(None, description="Publicly visible email of the contact")
    
    company_type: Literal["TECH", "TECH_ADJACENT", "NON_TECH"] = Field(
        description="TECH: core product is software. TECH_ADJACENT: non-tech business with internal tech team. NON_TECH: no meaningful tech needs."
    )
    has_internal_tech_team: bool = Field(description="True if company likely has internal IT/infra/dev team")
    tech_team_signals: List[str] = Field(default_factory=list, description="Evidence found for internal tech team")

SYSTEM_PROMPT = """You are a technical recruiter and OSINT expert. 
Your task is to extract structured company information from the provided markdown content of a company's career page or LinkedIn profile.

Extraction focus:
1. Identify if the company is a 'TECH' company (product is software/infra), 'TECH_ADJACENT' (retail, logistics, bank with a large internal tech team), or 'NON_TECH'.
2. Look for signals of an internal engineering team: job postings for DevOps/SRE/Backend, mentions of an engineering culture, a tech blog, or specific tech stacks.
3. Find a primary technical contact (CTO, VP Eng, Engineering Manager) or a technical recruiter.

Return only a valid JSON object matching the requested schema.
"""

async def parse_careers_page(content_md: str, run_id: Optional[str] = None) -> RawCompanyPage:
    """Extract structured data from page content using LLM."""
    return await llm_client.complete_json(
        system=SYSTEM_PROMPT,
        user=f"Content to extract:\n\n{content_md}",
        schema=RawCompanyPage,
        run_id=run_id,
        task="extract_company_info"
    )
