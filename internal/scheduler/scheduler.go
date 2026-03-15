package scheduler

import (
	"jobhunter/internal/db"
	"log"
	"time"
)

type Scheduler struct {
	db *db.DB
}

func NewScheduler(database *db.DB) *Scheduler {
	return &Scheduler{db: database}
}

func (s *Scheduler) Run() {
	log.Println("Scheduler started...")
	
	// Daily ticker
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		now := time.Now()
		
		// Run at 8:30 and 18:00
		if (now.Hour() == 8 && now.Minute() == 30) || (now.Hour() == 18 && now.Minute() == 0) {
			log.Println("Triggering scheduled pipeline run...")
			// Trigger pipeline logic here
		}

		// Nightly archive job at 02:00
		if now.Hour() == 2 && now.Minute() == 0 {
			log.Println("Running nightly archive job...")
			s.ArchiveOldCache()
		}

		<-ticker.C
	}
}

func (s *Scheduler) ArchiveOldCache() {
	// Move scrape_cache rows older than 30 days to disk or just delete
	res, err := s.db.Exec("DELETE FROM scrape_cache WHERE fetched_at < datetime('now', '-30 days')")
	if err != nil {
		log.Printf("Archive failed: %v", err)
		return
	}
	count, _ := res.RowsAffected()
	log.Printf("Archived %d cache entries", count)
}
