package db

import (
	"time"
)

func (db *DB) CreatePipelineRun(runID string) error {
	run := &PipelineRun{
		ID:        runID,
		Status:    "running",
		StartedAt: time.Now(),
	}
	return db.Create(run).Error
}

func (db *DB) FinalizePipelineRun(runID string, status string) error {
	now := time.Now()
	return db.Model(&PipelineRun{}).Where("id = ?", runID).Updates(map[string]interface{}{
		"status":   status,
		"ended_at": &now,
	}).Error
}

func (db *DB) LogStep(l *RunLog) error {
	if l.StartedAt.IsZero() {
		l.StartedAt = time.Now()
	}
	return db.Create(l).Error
}

func (db *DB) InsertTokenUsage(u *TokenUsage) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	return db.Create(u).Error
}

func (db *DB) GetRuns(limit int) ([]PipelineRun, error) {
	var runs []PipelineRun
	err := db.Order("started_at desc").Limit(limit).Find(&runs).Error
	return runs, err
}

func (db *DB) GetRunDetails(runID string) ([]RunLog, error) {
	var logs []RunLog
	err := db.Where("run_id = ?", runID).Order("started_at asc").Find(&logs).Error
	return logs, err
}

type UsageStats struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalCost        float64 `json:"total_cost"`
}

func (db *DB) GetUsageToday() (UsageStats, error) {
	var s UsageStats
	today := time.Now().Format("2006-01-02")
	
	db.Model(&TokenUsage{}).Where("date(created_at) = ?", today).Count(&s.Requests)
	db.Model(&TokenUsage{}).Where("date(created_at) = ?", today).Select("COALESCE(SUM(prompt_tokens), 0)").Scan(&s.PromptTokens)
	db.Model(&TokenUsage{}).Where("date(created_at) = ?", today).Select("COALESCE(SUM(completion_tokens), 0)").Scan(&s.CompletionTokens)
	db.Model(&TokenUsage{}).Where("date(created_at) = ?", today).Select("COALESCE(SUM(cost_usd), 0)").Scan(&s.TotalCost)
	
	return s, nil
}

type DayUsage struct {
	Day  string  `json:"day"`
	Cost float64 `json:"cost"`
}

func (db *DB) GetUsageHistory(days int) ([]DayUsage, error) {
	var results []struct {
		Day  string
		Cost float64
	}
	
	// SQLite specific date grouping, might need adjustment for Postgres
	db.Model(&TokenUsage{}).
		Select("date(created_at) as day, SUM(cost_usd) as cost").
		Group("day").
		Order("day desc").
		Limit(days).
		Scan(&results)

	history := make([]DayUsage, len(results))
	for i, r := range results {
		history[i] = DayUsage{Day: r.Day, Cost: r.Cost}
	}
	return history, nil
}

type HealthStats struct {
	HTTPHealthy      int64 `json:"http_healthy"`
	HTTPTotal        int64 `json:"http_total"`
	Browser24h       int64 `json:"browser_24h"`
	NeedsReviewCount int64 `json:"needs_review_count"`
}

func (db *DB) GetScrapingHealth() (HealthStats, error) {
	var s HealthStats
	db.Model(&ScrapeCache{}).Where("method='http' AND quality >= 0.7").Count(&s.HTTPHealthy)
	db.Model(&ScrapeCache{}).Where("method='http'").Count(&s.HTTPTotal)
	db.Model(&ScrapeCache{}).Where("method='browser' AND created_at >= ?", time.Now().Add(-24*time.Hour)).Count(&s.Browser24h)
	db.Model(&RunLog{}).Where("status='needs_review' AND started_at >= ?", time.Now().Add(-24*time.Hour)).Count(&s.NeedsReviewCount)
	return s, nil
}
