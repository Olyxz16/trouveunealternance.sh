"""
scraper/fetcher.py — Jina -> MCP fallback with caching.
"""
import httpx
import asyncio
from datetime import datetime, timedelta
from dataclasses import dataclass
from typing import Literal, Optional
from db import get_conn
from errors import JinaError, MCPError, EmptyContentError

@dataclass
class FetchResult:
    url: str
    content_md: str
    method: Literal["jina", "mcp", "cache", "manual"]
    fetched_at: datetime
    quality_score: float  # 0.0–1.0

class Fetcher:
    def __init__(self, mcp_host: str = "http://localhost:3000", cache_ttl_days: int = 1):
        self.mcp_host = mcp_host
        self.cache_ttl_days = cache_ttl_days

    async def fetch_url(self, url: str, force_mcp: bool = False, cache_ttl: Optional[int] = None) -> FetchResult:
        # 1. Check cache
        cached = self._get_from_cache(url)
        if cached and not force_mcp:
            return cached

        # 2. Try Jina (unless forced MCP)
        if not force_mcp:
            try:
                result = await self._fetch_jina(url)
                if self._is_good_quality(result.content_md):
                    self._save_to_cache(result, cache_ttl)
                    return result
            except Exception as e:
                print(f"  ⚠ Jina failed for {url}: {e}. Falling back to MCP...")

        # 3. Try MCP (Disabled for now as it hangs/fails when no server present)
        # try:
        #     result = await self._fetch_mcp(url)
        #     if self._is_good_quality(result.content_md):
        #         self._save_to_cache(result, cache_ttl)
        #         return result
        #     else:
        #         raise EmptyContentError(url, "mcp")
        # except Exception as e:
        #     if isinstance(e, EmptyContentError):
        #         raise e
        #     raise MCPError(url, str(e))
        raise EmptyContentError(url, "jina (mcp disabled)")

    async def _fetch_jina(self, url: str) -> FetchResult:
        jina_url = f"https://r.jina.ai/{url}"
        async with httpx.AsyncClient(timeout=30) as client:
            resp = await client.get(jina_url)
            if resp.status_code != 200:
                raise JinaError(url, resp.status_code, resp.text)
            
            content = resp.text
            return FetchResult(
                url=url,
                content_md=content,
                method="jina",
                fetched_at=datetime.now(),
                quality_score=self._calculate_quality(content)
            )

    async def _fetch_mcp(self, url: str) -> FetchResult:
        """
        Interacts with an MCP server (e.g. proxying to a browser-use or similar).
        Assumes endpoints: /browser/navigate and /browser/snapshot or similar.
        """
        async with httpx.AsyncClient(timeout=60) as client:
            # This is a speculative implementation based on the PLAN.md
            # We assume a simple POST to a browser-capable MCP proxy
            resp = await client.post(
                f"{self.mcp_host}/browser/content",
                json={"url": url},
                timeout=60
            )
            if resp.status_code != 200:
                raise MCPError(url, f"MCP server returned {resp.status_code}: {resp.text}")
            
            data = resp.json()
            content = data.get("markdown", data.get("content", ""))
            return FetchResult(
                url=url,
                content_md=content,
                method="mcp",
                fetched_at=datetime.now(),
                quality_score=self._calculate_quality(content)
            )

    def _is_good_quality(self, content: str) -> bool:
        if len(content) < 800:
            return False
        # Check for common error strings
        error_signals = ["captcha", "blocked", "access denied", "robot check", "please wait..."]
        content_lower = content.lower()
        if any(sig in content_lower for sig in error_signals):
            return False
        return True

    def _calculate_quality(self, content: str) -> float:
        if not content:
            return 0.0
        score = 1.0
        if len(content) < 1000:
            score -= 0.3
        if "login" in content.lower() and len(content) < 2000:
            score -= 0.2
        return max(0.0, score)

    def _get_from_cache(self, url: str) -> Optional[FetchResult]:
        with get_conn() as conn:
            row = conn.execute(
                "SELECT * FROM scrape_cache WHERE url = ? AND expires_at > ?",
                (url, datetime.now().isoformat())
            ).fetchone()
            if row:
                return FetchResult(
                    url=row["url"],
                    content_md=row["content_md"],
                    method="cache",
                    fetched_at=datetime.fromisoformat(row["fetched_at"]),
                    quality_score=row["quality"]
                )
        return None

    def _save_to_cache(self, result: FetchResult, ttl_seconds: Optional[int] = None):
        if ttl_seconds is None:
            ttl_seconds = self.cache_ttl_days * 86400
        
        expires_at = (datetime.now() + timedelta(seconds=ttl_seconds)).isoformat()
        
        with get_conn() as conn:
            conn.execute("""
                INSERT OR REPLACE INTO scrape_cache (url, method, content_md, quality, fetched_at, expires_at)
                VALUES (?, ?, ?, ?, ?, ?)
            """, (
                result.url,
                result.method,
                result.content_md,
                result.quality_score,
                result.fetched_at.isoformat(),
                expires_at
            ))

fetcher = Fetcher()
