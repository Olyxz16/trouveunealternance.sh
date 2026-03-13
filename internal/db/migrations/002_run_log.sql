-- 002_run_log.sql
CREATE TABLE IF NOT EXISTS run_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id      TEXT NOT NULL,
  company_id  INTEGER REFERENCES companies(id),
  job_id      INTEGER REFERENCES jobs(id),
  step        TEXT NOT NULL,
  status      TEXT NOT NULL CHECK(status IN ('ok','error','skipped','needs_review')),
  error_type  TEXT,
  error_msg   TEXT,
  duration_ms INTEGER,
  ts          TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_run_log_run_id     ON run_log(run_id);
CREATE INDEX IF NOT EXISTS idx_run_log_company_id ON run_log(company_id);
CREATE INDEX IF NOT EXISTS idx_run_log_status     ON run_log(status);
