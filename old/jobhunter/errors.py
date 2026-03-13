"""
errors.py — Exception hierarchy and Result type for JobHunter
"""
from dataclasses import dataclass
from typing import TypeVar, Generic, Union, Optional, Any

T = TypeVar("T")

class JobHunterError(Exception):
    """Base exception for all JobHunter errors"""
    pass

# Scraping Errors
class ScrapingError(JobHunterError):
    """Base for scraping-related failures"""
    pass

class JinaError(ScrapingError):
    def __init__(self, url: str, status_code: int, response_preview: str):
        self.url = url
        self.status_code = status_code
        self.response_preview = response_preview
        super().__init__(f"Jina failed for {url} (status {status_code}): {response_preview[:100]}")

class MCPError(ScrapingError):
    def __init__(self, url: str, reason: str):
        self.url = url
        self.reason = reason
        super().__init__(f"MCP failed for {url}: {reason}")

class EmptyContentError(ScrapingError):
    def __init__(self, url: str, method: str):
        self.url = url
        self.method = method
        super().__init__(f"No content found for {url} via {method}")

# LLM Errors
class LLMError(JobHunterError):
    """Base for LLM-related failures"""
    pass

class RateLimitError(LLMError):
    def __init__(self, retry_after: float, model: str):
        self.retry_after = retry_after
        self.model = model
        super().__init__(f"Rate limit hit for {model}. Retry after {retry_after}s")

class ParseError(LLMError):
    def __init__(self, raw_response: str, expected_schema: str):
        self.raw_response = raw_response
        self.expected_schema = expected_schema
        super().__init__(f"Failed to parse LLM response into schema {expected_schema}")

class ModelError(LLMError):
    def __init__(self, model: str, status_code: int):
        self.model = model
        self.status_code = status_code
        super().__init__(f"Model {model} returned error status {status_code}")

# Other Errors
class EnrichmentError(JobHunterError):
    def __init__(self, company_id: int, step: str, reason: str = ""):
        self.company_id = company_id
        self.step = step
        super().__init__(f"Enrichment failed for company {company_id} at step '{step}': {reason}")

class DatabaseError(JobHunterError):
    pass


@dataclass
class Ok(Generic[T]):
    value: T
    def is_ok(self) -> bool: return True
    def is_err(self) -> bool: return False
    def unwrap(self) -> T: return self.value

@dataclass
class Err:
    error: JobHunterError
    def is_ok(self) -> bool: return False
    def is_err(self) -> bool: return True
    def unwrap(self) -> Any: raise self.error

Result = Union[Ok[T], Err]
