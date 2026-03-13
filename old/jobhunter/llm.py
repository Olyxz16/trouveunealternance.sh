"""
llm.py — OpenRouter client with rate limiting, retries, and usage tracking.
"""
import os
import json
import asyncio
import time
import random
import httpx
from typing import Optional, Any, Type, TypeVar
from pydantic import BaseModel, ValidationError
from dotenv import load_dotenv
from jobhunter.errors import RateLimitError, ModelError, ParseError

load_dotenv()

T = TypeVar("T", bound=BaseModel)

class LLMClient:
    def __init__(
        self,
        api_key: Optional[str] = None,
        default_model: str = "google/gemini-2.5-flash-lite",
        rpm_limit: int = 60,
        max_tokens: int = 2048,
        base_url: str = "https://openrouter.ai/api/v1",
    ):
        self.api_key = api_key or os.getenv("OPENROUTER_API_KEY")
        self.default_model = os.getenv("OPENROUTER_MODEL", default_model)
        self.rpm_limit = int(os.getenv("OPENROUTER_RPM", rpm_limit))
        self.max_tokens = int(os.getenv("OPENROUTER_MAX_TOKENS", max_tokens))
        self.base_url = base_url
        
        self.semaphore = asyncio.Semaphore(10)  # Concurrent requests
        self._last_request_time = 0.0
        self._request_interval = 60.0 / self.rpm_limit if self.rpm_limit > 0 else 0.0

    async def _wait_for_rate_limit(self):
        if self._request_interval <= 0:
            return
        
        now = time.time()
        elapsed = now - self._last_request_time
        wait_time = self._request_interval - elapsed
        if wait_time > 0:
            await asyncio.sleep(wait_time)
        self._last_request_time = time.time()

    async def complete(
        self, 
        system: str, 
        user: str, 
        model: Optional[str] = None,
        run_id: Optional[str] = None,
        task: Optional[str] = None,
    ) -> str:
        model = model or self.default_model
        
        messages = [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ]
        
        return await self._request_with_retry(model, messages, run_id, task)

    async def complete_json(
        self, 
        system: str, 
        user: str, 
        schema: Type[T],
        model: Optional[str] = None,
        run_id: Optional[str] = None,
        task: Optional[str] = None,
    ) -> T:
        model = model or self.default_model
        
        # OpenRouter supports json_object for many models
        # We append schema info to the prompt to be safe
        system_with_schema = f"{system}\n\nReturn ONLY a JSON object matching this schema: {schema.model_json_schema()}"
        
        messages = [
            {"role": "system", "content": system_with_schema},
            {"role": "user", "content": user},
        ]
        
        retries = 1
        last_error = None
        
        for attempt in range(retries + 1):
            try:
                if attempt > 0:
                    # On second attempt, add the error to the prompt
                    messages.append({"role": "assistant", "content": last_raw_response})
                    messages.append({"role": "user", "content": f"The previous response failed validation: {last_error}. Please fix and return only valid JSON."})

                raw_response = await self._request_with_retry(
                    model, 
                    messages, 
                    run_id, 
                    task, 
                    json_mode=True
                )
                last_raw_response = raw_response
                
                # Strip markdown fences if present
                clean_json = raw_response.strip().lstrip("```json").lstrip("```").rstrip("```").strip()
                data = json.loads(clean_json)
                return schema.model_validate(data)
                
            except (ValidationError, json.JSONDecodeError) as e:
                last_error = str(e)
                if attempt == retries:
                    raise ParseError(raw_response, schema.__name__)
        
        raise ParseError("Unknown error in complete_json", schema.__name__)

    async def _request_with_retry(
        self, 
        model: str, 
        messages: list, 
        run_id: Optional[str],
        task: Optional[str],
        json_mode: bool = False
    ) -> str:
        max_retries = 4
        backoff = 2.0
        
        async with self.semaphore:
            for attempt in range(max_retries + 1):
                try:
                    await self._wait_for_rate_limit()
                    
                    headers = {
                        "Authorization": f"Bearer {self.api_key}",
                        "Content-Type": "application/json",
                        "HTTP-Referer": "https://github.com/jobhunter", # Required by OpenRouter
                        "X-Title": "JobHunter",
                    }
                    
                    payload = {
                        "model": model,
                        "messages": messages,
                        "max_tokens": self.max_tokens,
                    }
                    if json_mode:
                        payload["response_format"] = {"type": "json_object"}

                    async with httpx.AsyncClient(timeout=60.0) as client:
                        resp = await client.post(
                            f"{self.base_url}/chat/completions",
                            headers=headers,
                            json=payload
                        )
                        
                        if resp.status_code == 429:
                            retry_after = float(resp.headers.get("Retry-After", backoff))
                            if attempt == max_retries:
                                raise RateLimitError(retry_after, model)
                            
                            wait = retry_after * (1 + 0.2 * (random.random() * 2 - 1)) # jitter
                            await asyncio.sleep(wait)
                            backoff *= 2
                            continue
                        
                        if resp.status_code >= 500:
                            if attempt == 2: # Max 2 retries for 5xx
                                raise ModelError(model, resp.status_code)
                            await asyncio.sleep(backoff)
                            backoff *= 2
                            continue
                        
                        resp.raise_for_status()
                        
                        result = resp.json()
                        content = result["choices"][0]["message"]["content"]
                        usage = result.get("usage", {})
                        cost = result.get("x-openrouter-cost", 0.0) # Might not always be present
                        
                        # Log usage to DB
                        await self._log_usage(
                            run_id=run_id,
                            step=task,
                            model=model,
                            prompt_tokens=usage.get("prompt_tokens", 0),
                            completion_tokens=usage.get("completion_tokens", 0),
                            cost_usd=cost
                        )
                        
                        return content

                except httpx.HTTPStatusError as e:
                    if e.response.status_code not in [429, 500, 502, 503, 504]:
                        raise ModelError(model, e.response.status_code)
                    if attempt == max_retries:
                        raise e
                except Exception as e:
                    if attempt == max_retries:
                        raise e
                    await asyncio.sleep(backoff)
                    backoff *= 2

    async def _log_usage(self, run_id, step, model, prompt_tokens, completion_tokens, cost_usd):
        # This will be implemented when db.py is ready with the llm_usage table.
        # For now, we just print or use a placeholder.
        from jobhunter.db import get_conn
        try:
            with get_conn() as conn:
                conn.execute(
                    "INSERT INTO llm_usage (run_id, step, model, prompt_tokens, completion_tokens, cost_usd) VALUES (?,?,?,?,?,?)",
                    (run_id, step, model, prompt_tokens, completion_tokens, cost_usd)
                )
        except Exception:
            # Table might not exist yet
            pass

# Singleton instance
client = LLMClient()
