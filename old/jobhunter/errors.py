from typing import Optional

class JobHunterError(Exception):
    """Base class for all JobHunter exceptions."""
    pass

# --- Scraping Errors ---

class ScrapingError(JobHunterError):
    """Base class for scraping-related errors."""
    def __init__(self, url: str, message: str):
        self.url = url
        super().__init__(f"Scraping error at {url}: {message}")

class JinaError(ScrapingError):
    """Error when Jina fails to fetch or parse."""
    def __init__(self, url: str, status_code: int, response_preview: str):
        self.status_code = status_code
        self.response_preview = response_preview
        super().__init__(url, f"Jina failed with status {status_code}: {response_preview[:100]}")

class MCPError(ScrapingError):
    """Error when MCP browser automation fails."""
    def __init__(self, url: str, reason: str):
        self.reason = reason
        super().__init__(url, f"MCP failed: {reason}")

class EmptyContentError(ScrapingError):
    """Error when a fetcher returns no usable content."""
    def __init__(self, url: str, method: str):
        self.method = method
        super().__init__(url, f"Empty content returned by {method}")

# --- LLM Errors ---

class LLMError(JobHunterError):
    """Base class for LLM-related errors."""
    pass

class RateLimitError(LLMError):
    """Error when LLM provider rate limit is hit."""
    def __init__(self, retry_after: float, model: str):
        self.retry_after = retry_after
        self.model = model
        super().__init__(f"Rate limit hit for {model}. Retry after {retry_after}s")

class ParseError(LLMError):
    """Error when LLM response doesn't match expected schema."""
    def __init__(self, raw_response: str, expected_schema: str):
        self.raw_response = raw_response
        self.expected_schema = expected_schema
        super().__init__(f"Failed to parse LLM response into {expected_schema}")

class ModelError(LLMError):
    """Error when model returns a non-OK status (e.g., 500, 402)."""
    def __init__(self, model: str, status_code: int, message: str = ""):
        self.model = model
        self.status_code = status_code
        super().__init__(f"Model {model} returned error status {status_code}: {message}")

# --- Pipeline/Process Errors ---

class EnrichmentError(JobHunterError):
    """Error during the company enrichment stage."""
    def __init__(self, company_id: int, step: str, message: str):
        self.company_id = company_id
        self.step = step
        super().__init__(f"Enrichment error for company {company_id} at step '{step}': {message}")

class DatabaseError(JobHunterError):
    """Generic database error."""
    pass

# --- Result Type ---

class Result:
    """A simple Result type inspired by Rust's Result."""
    def __init__(self, value=None, error: Optional[JobHunterError] = None):
        self.value = value
        self.error = error

    @property
    def ok(self) -> bool:
        return self.error is None

    @classmethod
    def Ok(cls, value):
        return cls(value=value)

    @classmethod
    def Err(cls, error: JobHunterError):
        return cls(error=error)

    def __repr__(self):
        if self.ok:
            return f"Ok({self.value})"
        return f"Err({self.error})"

def Ok(value) -> Result:
    return Result.Ok(value)

def Err(error: JobHunterError) -> Result:
    return Result.Err(error)
