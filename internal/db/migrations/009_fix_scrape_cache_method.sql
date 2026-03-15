CREATE TABLE scrape_cache_new (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  url         TEXT NOT NULL UNIQUE,
  method      TEXT NOT NULL CHECK(method IN ('http','mcp','manual','cache','jina')),
  content_md  TEXT NOT NULL,
  quality     REAL NOT NULL DEFAULT 1.0,
  fetched_at  TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at  TEXT NOT NULL
);
INSERT INTO scrape_cache_new SELECT * FROM scrape_cache;
DROP TABLE scrape_cache;
ALTER TABLE scrape_cache_new RENAME TO scrape_cache;
CREATE INDEX IF NOT EXISTS idx_scrape_cache_url        ON scrape_cache(url);
CREATE INDEX IF NOT EXISTS idx_scrape_cache_expires_at ON scrape_cache(expires_at);
