CREATE TABLE IF NOT EXISTS pipeline_runs (
  run_id         TEXT PRIMARY KEY,
  started_at     TEXT NOT NULL,
  finished_at    TEXT,
  status         TEXT NOT NULL DEFAULT 'running'
                 CHECK(status IN ('running','done','failed','partial')),
  companies_processed INTEGER DEFAULT 0,
  contacts_found      INTEGER DEFAULT 0,
  drafts_generated    INTEGER DEFAULT 0,
  llm_cost_usd        REAL DEFAULT 0,
  error_count         INTEGER DEFAULT 0
);
