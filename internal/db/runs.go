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

func (db *DB) GetRuns(limit int) ([]PipelineRun, error) {
	rows, err := db.Query(`
		SELECT run_id, started_at, finished_at, status, companies_processed, contacts_found, drafts_generated, llm_cost_usd, error_count 
		FROM pipeline_runs ORDER BY started_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []PipelineRun
	for rows.Next() {
		var r PipelineRun
		err := rows.Scan(&r.RunID, &r.StartedAt, &r.FinishedAt, &r.Status, &r.CompaniesProcessed, &r.ContactsFound, &r.DraftsGenerated, &r.LLMCostUSD, &r.ErrorCount)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func (db *DB) GetRunDetails(runID string) ([]RunLog, error) {
	rows, err := db.Query(`
		SELECT id, run_id, company_id, job_id, step, status, error_type, error_msg, duration_ms, ts 
		FROM run_log WHERE run_id = ? ORDER BY ts ASC
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []RunLog
	for rows.Next() {
		var l RunLog
		err := rows.Scan(&l.ID, &l.RunID, &l.CompanyID, &l.JobID, &l.Step, &l.Status, &l.ErrorType, &l.ErrorMsg, &l.DurationMS, &l.TS)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

type UsageStats struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalCost        float64 `json:"total_cost"`
}

func (db *DB) GetUsageToday() (UsageStats, error) {
	var s UsageStats
	// Combine llm_usage and gemini_usage (estimated)
	err := db.QueryRow(`
		SELECT COUNT(*), SUM(prompt_tokens), SUM(completion_tokens), SUM(cost_usd) 
		FROM llm_usage WHERE ts >= date('now')
	`).Scan(&s.Requests, &s.PromptTokens, &s.CompletionTokens, &s.TotalCost)
	
	// Add gemini estimates
	var gReq, gPrompt, gComp int
	_ = db.QueryRow(`
		SELECT COUNT(*), SUM(prompt_tokens), SUM(completion_tokens) 
		FROM gemini_usage WHERE ts >= date('now')
	`).Scan(&gReq, &gPrompt, &gComp)
	
	s.Requests += gReq
	s.PromptTokens += gPrompt
	s.CompletionTokens += gComp
	
	return s, err
}

type DayUsage struct {
	Day  string  `json:"day"`
	Cost float64 `json:"cost"`
}

func (db *DB) GetUsageHistory(days int) ([]DayUsage, error) {
	rows, err := db.Query(`
		SELECT date(ts) as day, SUM(cost_usd) as total_cost 
		FROM llm_usage GROUP BY day ORDER BY day DESC LIMIT ?
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []DayUsage
	for rows.Next() {
		var d DayUsage
		rows.Scan(&d.Day, &d.Cost)
		history = append(history, d)
	}
	return history, nil
}

type HealthStats struct {
	JinaHealthy      int `json:"jina_healthy"`
	JinaTotal        int `json:"jina_total"`
	MCP24h           int `json:"mcp_24h"`
	NeedsReviewCount int `json:"needs_review_count"`
}

func (db *DB) GetScrapingHealth() (HealthStats, error) {
	var s HealthStats
	_ = db.QueryRow("SELECT COUNT(*) FROM scrape_cache WHERE method='jina' AND quality >= 0.7").Scan(&s.JinaHealthy)
	_ = db.QueryRow("SELECT COUNT(*) FROM scrape_cache WHERE method='jina'").Scan(&s.JinaTotal)
	_ = db.QueryRow("SELECT COUNT(*) FROM run_log WHERE step='enrich' AND status='needs_review' AND ts >= datetime('now', '-24 hours')").Scan(&s.NeedsReviewCount)
	// For MCP, we now use 'browser'
	_ = db.QueryRow("SELECT COUNT(*) FROM run_log WHERE step LIKE '%fetch%' AND status='ok' AND ts >= datetime('now', '-24 hours')").Scan(&s.MCP24h)
	
	return s, nil
}
