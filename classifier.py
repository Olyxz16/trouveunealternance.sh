"""
classifier.py — LLM-powered relevance scoring and tech stack extraction
Separates "thinking" from "scraping" for speed and re-classifiability.
"""
import json
import httpx
import os
from typing import Optional
from dotenv import load_dotenv

load_dotenv()

GEMINI_API_KEY = os.getenv("GEMINI_API_KEY", "")
MODEL = "gemini-2.0-flash"

SYSTEM_PROMPT = """You are a job listing classifier for a computer science student
looking for internships in DevOps or backend development in France.

You will receive a raw job listing and must return ONLY valid JSON — no markdown, no explanation.

Classification rules:
- type = "DIRECT" if it is explicitly an internship (stage, alternance) in DevOps, backend, SRE, platform, or infrastructure
- type = "COMPANY_LEAD" if it is a CDI/CDD/freelance with a clearly DevOps or backend technical stack
  (the company likely has a team that could take an intern even if they didn't post for one)
- type = "SKIP" if it's unrelated, frontend-only, management, sales, data science without DevOps overlap, etc.

Relevance score (0-10):
- 10: perfect match (DevOps/backend internship, good stack, good company)
- 7-9: strong match
- 4-6: partial match (interesting company or stack but not ideal)
- 1-3: weak signal
- 0: SKIP

Return this exact JSON structure:
{
  "type": "DIRECT" | "COMPANY_LEAD" | "SKIP",
  "relevance_score": 0-10,
  "tech_stack": ["tag1", "tag2"],  // extracted keywords: Docker, Kubernetes, Python, Go, CI/CD, etc.
  "contract_type": "stage" | "CDI" | "CDD" | "alternance" | "freelance" | "unknown",
  "description_summary": "2-sentence English summary of the role",
  "reasoning": "1 sentence explaining your classification"
}"""


async def classify_listing(
    title: str,
    company: str,
    raw_description: str,
    location: str = "",
) -> Optional[dict]:
    """Call Gemini to classify and enrich a raw listing."""
    if not GEMINI_API_KEY:
        # Fallback: basic keyword heuristic if no API key
        return _heuristic_classify(title, raw_description)

    prompt = f"""Title: {title}
Company: {company}
Location: {location}
Description:
{raw_description[:3000]}"""

    try:
        async with httpx.AsyncClient(timeout=30) as client:
            resp = await client.post(
                f"https://generativelanguage.googleapis.com/v1beta/models/{MODEL}:generateContent?key={GEMINI_API_KEY}",
                json={
                    "system_instruction": {"parts": [{"text": SYSTEM_PROMPT}]},
                    "contents": [{"parts": [{"text": prompt}]}],
                    "generationConfig": {"temperature": 0.1, "maxOutputTokens": 512},
                }
            )
            resp.raise_for_status()
            text = resp.json()["candidates"][0]["content"]["parts"][0]["text"]
            # Strip any accidental markdown fences
            text = text.strip().lstrip("```json").lstrip("```").rstrip("```").strip()
            return json.loads(text)
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


if __name__ == "__main__":
    import asyncio

    async def test():
        result = await classify_listing(
            title="Ingénieur DevOps - Stage",
            company="Acme Corp",
            raw_description="""
            Nous recherchons un stagiaire DevOps pour rejoindre notre équipe infrastructure.
            Vous travaillerez sur Kubernetes, Terraform, et nos pipelines CI/CD GitLab.
            Stack: Python, Docker, AWS, Prometheus.
            Durée: 6 mois. Début: septembre 2025.
            """,
            location="Paris"
        )
        print(json.dumps(result, indent=2))

    asyncio.run(test())
