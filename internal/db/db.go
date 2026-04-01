package db

import (
	"fmt"
	"jobhunter/internal/config"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type DB struct {
	*gorm.DB
	logger *zap.Logger
}

// NewDB initializes a GORM database connection based on config.
func NewDB(cfg *config.Config, zapLogger *zap.Logger) (*DB, error) {
	if zapLogger == nil {
		zapLogger = zap.NewNop()
	}
	var dialector gorm.Dialector

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

	zapLogger.Info("Connected to database", zap.String("type", dbType))

	return &DB{db, zapLogger}, nil
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
