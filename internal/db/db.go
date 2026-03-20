package db

import (
	"fmt"
	"jobhunter/internal/config"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type DB struct {
	*gorm.DB
}

// NewDB initializes a GORM database connection based on config.
func NewDB(cfg *config.Config) (*DB, error) {
	var dialector gorm.Dialector

	// For now, we assume DBType is either "sqlite" (default) or "postgres".
	// We'll add DBType to config later.
	dbType := os.Getenv("DB_TYPE")
	if dbType == "" {
		dbType = "sqlite"
	}

	switch dbType {
	case "postgres":
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return nil, fmt.Errorf("DATABASE_URL must be set for postgres")
		}
		dialector = postgres.Open(dsn)
	default:
		// Ensure directory exists
		dir := filepath.Dir(cfg.DBPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create db directory: %w", err)
		}
		dialector = sqlite.Open(cfg.DBPath)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	log.Printf("Connected to %s database", dbType)

	return &DB{db}, nil
}

// Migrate performs the auto-migration for all models.
func (db *DB) Migrate() error {
	return db.AutoMigrate(
		&Company{},
		&Contact{},
		&Job{},
		&Draft{},
		&PipelineRun{},
		&RunLog{},
		&ScrapeCache{},
		&TokenUsage{},
	)
}

// Stats holds database statistics.
type Stats struct {
	TotalJobs          int64
	NewJobsToday       int64
	TotalProspects     int64
	NewProspectsToday  int64
	ProspectsByStatus  map[string]int64
}

func (db *DB) GetStats() (Stats, error) {
	var s Stats
	s.ProspectsByStatus = make(map[string]int64)

	db.Model(&Job{}).Count(&s.TotalJobs)
	db.Model(&Job{}).Where("date_found = ?", time.Now().Format("2006-01-02")).Count(&s.NewJobsToday)

	db.Model(&Company{}).Count(&s.TotalProspects)
	db.Model(&Company{}).Where("date_found = ?", time.Now().Format("2006-01-02")).Count(&s.NewProspectsToday)

	// Status breakdown
	type statusCount struct {
		Status string
		Count  int64
	}
	var results []statusCount
	db.Model(&Company{}).Select("status, count(*) as count").Group("status").Scan(&results)
	for _, res := range results {
		s.ProspectsByStatus[res.Status] = res.Count
	}

	return s, nil
}

func ToNullString(s string) string {
	return s
}
