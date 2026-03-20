package db

import (
	"time"

	"gorm.io/gorm/clause"
)

func (db *DB) GetCache(url string) (*ScrapeCache, error) {
	var s ScrapeCache
	// Check for non-expired cache. We'll use a 7-day default if not specified.
	err := db.Where("url = ? AND created_at > ?", url, time.Now().Add(-7*24*time.Hour)).First(&s).Error
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (db *DB) SetCache(s *ScrapeCache) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	return db.Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Create(s).Error
}
