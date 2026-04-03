package db

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// QueueDB wraps a PostgreSQL connection for the job queue system.
type QueueDB struct {
	*gorm.DB
}

// NewQueueDB creates a new PostgreSQL connection for the job queue.
func NewQueueDB(dsn string) (*QueueDB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying DB: %w", err)
	}

	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	qdb := &QueueDB{DB: db}

	if err := qdb.autoMigrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate queue schema: %w", err)
	}

	if err := qdb.initRateLimits(); err != nil {
		return nil, fmt.Errorf("failed to init rate limits: %w", err)
	}

	return qdb, nil
}

func (q *QueueDB) autoMigrate() error {
	return q.AutoMigrate(
		&Company{},
		&Contact{},
		&QueueJob{},
		&RateLimit{},
		&CompanyCooldown{},
		&ScrapeCache{},
		&TokenUsage{},
		&LLMResponseCache{},
	)
}

// initRateLimits ensures rate limit rows exist with default values.
func (q *QueueDB) initRateLimits() error {
	defaults := []RateLimit{
		{ID: "global", Tokens: 1.0, MaxTokens: 1.0, RefillRate: 0.5, DailyLimit: 500, DailyReset: time.Now().Add(24 * time.Hour)},
		{ID: "openrouter", Tokens: 1.0, MaxTokens: 1.0, RefillRate: 0.5, DailyLimit: 500, DailyReset: time.Now().Add(24 * time.Hour)},
		{ID: "gemini_api", Tokens: 1.0, MaxTokens: 1.0, RefillRate: 0.2, DailyLimit: 1000, DailyReset: time.Now().Add(24 * time.Hour)},
	}

	for _, rl := range defaults {
		var existing RateLimit
		if err := q.Where("id = ?", rl.ID).First(&existing).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				if err := q.Create(&rl).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// AcquireRateLimitToken atomically acquires a token from the rate limiter.
// Returns true if a token was acquired, false if rate limited.
func (q *QueueDB) AcquireRateLimitToken(limitID string) (bool, error) {
	// First, refill tokens based on elapsed time
	err := q.Exec(`
		UPDATE rate_limits 
		SET tokens = LEAST(max_tokens, tokens + refill_rate * EXTRACT(EPOCH FROM (NOW() - last_refill))),
		    last_refill = NOW()
		WHERE id = ?
	`, limitID).Error
	if err != nil {
		return false, err
	}

	// Reset daily counter if needed
	err = q.Exec(`
		UPDATE rate_limits 
		SET daily_used = 0, daily_reset = NOW() + INTERVAL '1 day'
		WHERE id = ? AND daily_reset <= NOW()
	`, limitID).Error
	if err != nil {
		return false, err
	}

	// Try to acquire a token atomically
	result := q.Exec(`
		UPDATE rate_limits 
		SET tokens = tokens - 1,
		    daily_used = daily_used + 1
		WHERE id = ? 
		  AND tokens >= 1
		  AND (daily_limit = 0 OR daily_used < daily_limit)
		RETURNING id
	`, limitID)

	if result.Error != nil {
		return false, result.Error
	}

	return result.RowsAffected > 0, nil
}

// GetRateLimitStatus returns current rate limit status.
func (q *QueueDB) GetRateLimitStatus(limitID string) (*RateLimit, error) {
	var rl RateLimit
	err := q.Where("id = ?", limitID).First(&rl).Error
	if err != nil {
		return nil, err
	}
	return &rl, nil
}

// GetAllRateLimitStatus returns status for all rate limits.
func (q *QueueDB) GetAllRateLimitStatus() ([]RateLimit, error) {
	var rls []RateLimit
	err := q.Find(&rls).Error
	return rls, err
}

// SetCompanyCooldown sets the next allowed scrape time for a company.
func (q *QueueDB) SetCompanyCooldown(companyID uint, cooldown time.Duration, status string) error {
	now := time.Now()
	nextAllowed := now.Add(cooldown)

	cd := CompanyCooldown{
		CompanyID:     companyID,
		LastScrapedAt: &now,
		NextAllowedAt: &nextAllowed,
		LastStatus:    status,
	}

	return q.Save(&cd).Error
}

// IsCompanyOnCooldown checks if a company is currently on cooldown.
func (q *QueueDB) IsCompanyOnCooldown(companyID uint) (bool, *time.Time, error) {
	var cd CompanyCooldown
	err := q.Where("company_id = ?", companyID).First(&cd).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil, nil
		}
		return false, nil, err
	}

	if cd.NextAllowedAt != nil && time.Now().Before(*cd.NextAllowedAt) {
		return true, cd.NextAllowedAt, nil
	}

	return false, nil, nil
}

// EnqueueJob adds a new job to the queue.
func (q *QueueDB) EnqueueJob(job *QueueJob) error {
	if job.ID == "" {
		job.ID = generateUUID()
	}
	return q.Create(job).Error
}

// DequeueJob atomically picks the next available job for a worker.
func (q *QueueDB) DequeueJob(workerID string) (*QueueJob, error) {
	var job QueueJob

	// Use a transaction with row-level locking
	err := q.Transaction(func(tx *gorm.DB) error {
		// Find the next pending job that's ready to run
		now := time.Now()
		err := tx.Where("status = ? AND (next_run_at IS NULL OR next_run_at <= ?) AND attempts < max_attempts",
			"pending", now).
			Order("priority DESC, created_at ASC").
			Limit(1).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			First(&job).Error
		if err != nil {
			return err
		}

		// Mark as running
		startedAt := time.Now()
		return tx.Model(&job).Updates(map[string]interface{}{
			"status":     "running",
			"worker_id":  workerID,
			"started_at": startedAt,
			"attempts":   gorm.Expr("attempts + 1"),
		}).Error
	})

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // No jobs available
		}
		return nil, err
	}

	return &job, nil
}

// CompleteJob marks a job as completed.
func (q *QueueDB) CompleteJob(jobID string) error {
	now := time.Now()
	return q.Model(&QueueJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"status":       "completed",
		"completed_at": now,
	}).Error
}

// FailJob marks a job as failed with retry logic.
func (q *QueueDB) FailJob(jobID string, errMsg string) error {
	var job QueueJob
	if err := q.Where("id = ?", jobID).First(&job).Error; err != nil {
		return err
	}

	if job.Attempts >= job.MaxAttempts {
		// Max retries exceeded
		now := time.Now()
		return q.Model(&QueueJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
			"status":       "failed",
			"error":        errMsg,
			"completed_at": now,
		}).Error
	}

	// Schedule retry with exponential backoff: 2^attempt * 5 minutes
	backoffMinutes := (1 << job.Attempts) * 5
	nextRunAt := time.Now().Add(time.Duration(backoffMinutes) * time.Minute)

	return q.Model(&QueueJob{}).Where("id = ?", jobID).Updates(map[string]interface{}{
		"status":      "pending",
		"error":       errMsg,
		"next_run_at": nextRunAt,
	}).Error
}

// GetJobQueueStats returns queue statistics.
func (q *QueueDB) GetJobQueueStats() (map[string]int64, error) {
	var results []struct {
		Status string
		Count  int64
	}

	err := q.Model(&QueueJob{}).Select("status, COUNT(*) as count").Group("status").Find(&results).Error
	if err != nil {
		return nil, err
	}

	stats := make(map[string]int64)
	for _, r := range results {
		stats[r.Status] = r.Count
	}

	return stats, nil
}

// ListJobs returns jobs filtered by status and type.
func (q *QueueDB) ListJobs(status string, jobType string, limit int) ([]QueueJob, error) {
	var jobs []QueueJob
	query := q.DB.Order("priority DESC, created_at DESC").Limit(limit)

	if status != "" {
		query = query.Where("status = ?", status)
	}
	if jobType != "" {
		query = query.Where("type = ?", jobType)
	}

	err := query.Find(&jobs).Error
	return jobs, err
}

// CancelJob cancels a pending or running job.
func (q *QueueDB) CancelJob(jobID string) error {
	return q.Model(&QueueJob{}).Where("id = ? AND status IN (?, ?)", jobID, "pending", "running").
		Update("status", "cancelled").Error
}

// RetryJob resets a failed job for re-processing.
func (q *QueueDB) RetryJob(jobID string) error {
	return q.Model(&QueueJob{}).Where("id = ? AND status = ?", jobID, "failed").
		Updates(map[string]interface{}{
			"status":      "pending",
			"error":       "",
			"next_run_at": nil,
			"attempts":    0,
		}).Error
}

// GetActiveWorkers returns list of workers with recent activity.
func (q *QueueDB) GetActiveWorkers(since time.Duration) ([]string, error) {
	var workerIDs []string
	cutoff := time.Now().Add(-since)

	err := q.Model(&QueueJob{}).
		Distinct("worker_id").
		Where("status = ? AND started_at > ?", "running", cutoff).
		Pluck("worker_id", &workerIDs).Error

	return workerIDs, err
}

func generateUUID() string {
	return uuid.New().String()
}
