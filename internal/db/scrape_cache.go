package db

type ScrapeCache struct {
	ID        int     `json:"id"`
	URL       string  `json:"url"`
	Method    string  `json:"method"`
	ContentMD string  `json:"content_md"`
	Quality   float64 `json:"quality"`
	FetchedAt string  `json:"fetched_at"`
	ExpiresAt string  `json:"expires_at"`
}

func (db *DB) GetCache(url string) (*ScrapeCache, error) {
	var s ScrapeCache
	err := db.QueryRow(`
		SELECT * FROM scrape_cache 
		WHERE url = ? AND expires_at > datetime('now')
	`, url).Scan(
		&s.ID, &s.URL, &s.Method, &s.ContentMD, &s.Quality, &s.FetchedAt, &s.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (db *DB) SetCache(s *ScrapeCache) error {
	_, err := db.Exec(`
		INSERT INTO scrape_cache (url, method, content_md, quality, fetched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET
			method=excluded.method,
			content_md=excluded.content_md,
			quality=excluded.quality,
			fetched_at=excluded.fetched_at,
			expires_at=excluded.expires_at
	`, s.URL, s.Method, s.ContentMD, s.Quality, s.FetchedAt, s.ExpiresAt)
	return err
}
