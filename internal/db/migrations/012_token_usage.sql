-- 012_token_usage.sql
CREATE TABLE IF NOT EXISTS token_usage (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id            TEXT,
  task              TEXT,
  model             TEXT NOT NULL,
  provider          TEXT NOT NULL, -- e.g. 'openrouter', 'gemini_api', 'gemini_cli'
  prompt_tokens     INTEGER NOT NULL,
  completion_tokens INTEGER NOT NULL,
  cost_usd          REAL DEFAULT 0,
  is_estimated      BOOLEAN DEFAULT 0,
  ts                TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_token_usage_run_id ON token_usage(run_id);
CREATE INDEX IF NOT EXISTS idx_token_usage_ts ON token_usage(ts);
