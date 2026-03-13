CREATE TABLE IF NOT EXISTS gemini_usage (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id           TEXT,
  step             TEXT,
  prompt_tokens    INTEGER,   -- estimated: prompt_chars / 4
  completion_tokens INTEGER,  -- estimated: response_chars / 4
  ts               TEXT NOT NULL DEFAULT (datetime('now'))
);
