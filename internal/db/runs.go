package db

import (
	"database/sql"
	"time"
)

type PipelineRun struct {
	RunID              string         `json:"run_id"`
	StartedAt          string         `json:"started_at"`
	FinishedAt         sql.NullString `json:"finished_at"`
	Status             string         `json:"status"`
	CompaniesProcessed int            `json:"companies_processed"`
	ContactsFound      int            `json:"contacts_found"`
	DraftsGenerated    int            `json:"drafts_generated"`
	LLMCostUSD         float64        `json:"llm_cost_usd"`
	ErrorCount         int            `json:"error_count"`
}

type RunLog struct {
	ID         int            `json:"id"`
	RunID      string         `json:"run_id"`
	CompanyID  sql.NullInt64  `json:"company_id"`
	JobID      sql.NullInt64  `json:"job_id"`
	Step       string         `json:"step"`
	Status     string         `json:"status"`
	ErrorType  sql.NullString `json:"error_type"`
	ErrorMsg   sql.NullString `json:"error_msg"`
	DurationMS int64          `json:"duration_ms"`
	TS         string         `json:"ts"`
}

func (db *DB) CreatePipelineRun(runID string) error {
	_, err := db.Exec(`
		INSERT INTO pipeline_runs (run_id, started_at, status)
		VALUES (?, ?, 'running')
	`, runID, time.Now().Format(time.RFC3339))
	return err
}

func (db *DB) FinalizePipelineRun(runID string, status string) error {
	_, err := db.Exec(`
		UPDATE pipeline_runs 
		SET status = ?, finished_at = ?
		WHERE run_id = ?
	`, status, time.Now().Format(time.RFC3339), runID)
	return err
}

func (db *DB) LogStep(l *RunLog) error {
	_, err := db.Exec(`
		INSERT INTO run_log (run_id, company_id, job_id, step, status, error_type, error_msg, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		l.RunID, l.CompanyID, l.JobID, l.Step, l.Status, l.ErrorType, l.ErrorMsg, l.DurationMS,
	)
	return err
}

func (db *DB) InsertLLMUsage(runID, step, model string, promptTokens, completionTokens int, costUSD float64) error {
	_, err := db.Exec(`
		INSERT INTO llm_usage (run_id, step, model, prompt_tokens, completion_tokens, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?)
	`, runID, step, model, promptTokens, completionTokens, costUSD)
	return err
}

func (db *DB) InsertGeminiUsage(runID, step string, promptTokens, completionTokens int) error {
	_, err := db.Exec(`
		INSERT INTO gemini_usage (run_id, step, prompt_tokens, completion_tokens)
		VALUES (?, ?, ?, ?)
	`, runID, step, promptTokens, completionTokens)
	return err
}
