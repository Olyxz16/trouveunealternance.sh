-- 004_scrape_cache.sql
CREATE TABLE IF NOT EXISTS scrape_cache (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  url         TEXT NOT NULL UNIQUE,
  method      TEXT NOT NULL CHECK(method IN ('jina','mcp','manual','cache')),
  content_md  TEXT NOT NULL,
  quality     REAL NOT NULL DEFAULT 1.0,
  fetched_at  TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_scrape_cache_url        ON scrape_cache(url);
CREATE INDEX IF NOT EXISTS idx_scrape_cache_expires_at ON scrape_cache(expires_at);
