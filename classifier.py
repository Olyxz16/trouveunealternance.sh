"""
classifier.py — LLM-powered relevance scoring and tech stack extraction
"""
import json
from typing import Optional, List, Literal
from pydantic import BaseModel, Field
from llm import client as llm_client

class JobClassification(BaseModel):
    type: Literal["DIRECT", "COMPANY_LEAD", "SKIP"]
    relevance_score: int = Field(ge=0, le=10)
    tech_stack: List[str]
    contract_type: Literal["stage", "CDI", "CDD", "alternance", "freelance", "unknown"]
    description_summary: str
    reasoning: str

SYSTEM_PROMPT = """You are a job listing classifier for a computer science student
looking for internships in DevOps or backend development in France.

Classification rules:
- type = "DIRECT" if it is explicitly an internship (stage, alternance) in DevOps, backend, SRE, platform, or infrastructure
- type = "COMPANY_LEAD" if it is a CDI/CDD/freelance with a clearly DevOps or backend technical stack
- type = "SKIP" if it's unrelated, frontend-only, management, sales, data science without DevOps overlap, etc.

Relevance score (0-10):
- 10: perfect match (DevOps/backend internship, good stack, good company)
- 7-9: strong match
- 4-6: partial match
- 1-3: weak signal
- 0: SKIP
"""

async def classify_listing(
    title: str,
    company: str,
    raw_description: str,
    location: str = "",
    run_id: Optional[str] = None
) -> Optional[dict]:
    """Call LLM to classify and enrich a raw listing."""
    if not llm_client.api_key:
        return _heuristic_classify(title, raw_description)

    prompt = f"Title: {title}\nCompany: {company}\nLocation: {location}\nDescription:\n{raw_description[:3000]}"

    try:
        result = await llm_client.complete_json(
            system=SYSTEM_PROMPT,
            user=prompt,
            schema=JobClassification,
            run_id=run_id,
            task="classify_job"
        )
        return result.model_dump()
    except Exception as e:
        print(f"  ⚠ Classifier error for '{title}': {e}")
        return _heuristic_classify(title, raw_description)


def _heuristic_classify(title: str, description: str) -> dict:
    """Simple keyword fallback when no API key is available."""
    text = (title + " " + description).lower()

    devops_keywords = ["devops", "docker", "kubernetes", "k8s", "terraform", "ansible",
                       "ci/cd", "pipeline", "infrastructure", "sre", "platform", "cloud"]
    backend_keywords = ["backend", "python", "golang", "go ", "java", "rust", "api",
                        "microservices", "postgresql", "redis", "kafka"]
    internship_keywords = ["stage", "intern", "alternance", "apprenti"]
    skip_keywords = ["frontend", "react", "angular", "vue", "ios", "android", "marketing",
                     "sales", "commercial", "rh ", "finance", "comptable"]

    is_skip = any(k in text for k in skip_keywords)
    is_internship = any(k in text for k in internship_keywords)
    is_devops = any(k in text for k in devops_keywords)
    is_backend = any(k in text for k in backend_keywords)

    if is_skip or (not is_devops and not is_backend):
        return {"type": "SKIP", "relevance_score": 0, "tech_stack": [],
                "contract_type": "unknown", "description_summary": "", "reasoning": "No match"}

    tags = [k for k in devops_keywords + backend_keywords if k.strip() in text]
    score = 8 if is_internship else 5
    type_ = "DIRECT" if is_internship else "COMPANY_LEAD"

    return {
        "type": type_,
        "relevance_score": score,
        "tech_stack": list(set(tags))[:8],
        "contract_type": "stage" if is_internship else "CDI",
        "description_summary": f"{title} at {text[:100]}...",
        "reasoning": "Heuristic match"
    }
