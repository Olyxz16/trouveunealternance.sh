package scraper

import (
	"context"
	"database/sql"
	stdErrors "errors"
	"jobhunter/internal/db"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ... (struct and NewCascadeFetcher stay same)

func (c *CascadeFetcher) tryFetcher(ctx context.Context, f Fetcher, url string) (FetchResult, error) {
	if f == nil {
		return FetchResult{}, stdErrors.New("no fetcher available")
	}

	html, err := f.Fetch(ctx, url)
	if err != nil {
		return FetchResult{}, err
	}

	markdown, err := c.extractor.Extract(html, url)
	if err != nil {
		return FetchResult{}, err
	}

	quality := calculateQuality(markdown)

	res := FetchResult{
		ContentMD: markdown,
		Method:    f.Name(),
		Quality:   quality,
	}

	// Save to cache
	if shouldCache(url) {
		now := time.Now()
		expiresAt := now.Add(24 * time.Hour) // Default 24h
		if strings.Contains(url, "linkedin.com") {
			expiresAt = now.Add(7 * 24 * time.Hour)
		}

		err = c.cache.SetCache(&db.ScrapeCache{
			URL:       url,
			Method:    f.Name(),
			ContentMD: markdown,
			Quality:   quality,
			FetchedAt: now.Format("2006-01-02 15:04:05"),
			ExpiresAt: expiresAt.Format("2006-01-02 15:04:05"),
		})
		if err != nil {
			c.logger.Error("failed to write to cache", zap.Error(err), zap.String("url", url))
		}
	}

	return res, nil
}

type CascadeFetcher struct {
	primary      Fetcher
	fallback     Fetcher
	forceBrowser []string
	cache        *db.DB
	extractor    *Extractor
	logger       *zap.Logger
}

func NewCascadeFetcher(primary Fetcher, fallback Fetcher, forceBrowser []string, cache *db.DB, extractor *Extractor, logger *zap.Logger) *CascadeFetcher {
	return &CascadeFetcher{
		primary:      primary,
		fallback:     fallback,
		forceBrowser: forceBrowser,
		cache:        cache,
		extractor:    extractor,
		logger:       logger,
	}
}

func (c *CascadeFetcher) Fetch(ctx context.Context, url string) (FetchResult, error) {
	// 1. Cache check
	if cached, err := c.cache.GetCache(url); err == nil {
		c.logger.Info("cache hit", zap.String("url", url))
		return FetchResult{
			ContentMD: cached.ContentMD,
			Method:    "cache",
			Quality:   cached.Quality,
		}, nil
	} else if err != nil && err != sql.ErrNoRows {
		c.logger.Warn("cache error", zap.Error(err))
	}

	// 2. Force Browser?
	useBrowser := false
	for _, domain := range c.forceBrowser {
		if strings.Contains(url, domain) {
			useBrowser = true
			break
		}
	}

	if useBrowser {
		if c.fallback != nil {
			c.logger.Info("forcing browser for domain", zap.String("url", url))
			return c.tryFetcher(ctx, c.fallback, url)
		}
		c.logger.Warn("browser required but fetcher not available, trying primary", zap.String("url", url))
	}

	// 3. Try primary
	res, err := c.tryFetcher(ctx, c.primary, url)
	if err == nil && res.Quality >= 0.7 {
		return res, nil
	}

	if err != nil {
		c.logger.Warn("primary fetcher failed, trying fallback", zap.Error(err), zap.String("url", url))
	} else {
		c.logger.Info("primary quality too low, trying fallback", zap.Float64("quality", res.Quality), zap.String("url", url))
	}

	// 4. Try fallback
	if c.fallback != nil {
		return c.tryFetcher(ctx, c.fallback, url)
	}
	return res, err
}

func (c *CascadeFetcher) ScrollAndFetch(ctx context.Context, url string, scrolls int) (FetchResult, error) {
	if c.fallback == nil {
		return c.Fetch(ctx, url) // Fallback to normal fetch
	}

	// Browser must be available for scroll
	browser, ok := c.fallback.(*BrowserFetcher)
	if !ok {
		return c.Fetch(ctx, url)
	}

	c.logger.Info("scrolling and fetching", zap.String("url", url), zap.Int("scrolls", scrolls))

	html, err := browser.FetchWithScroll(ctx, url, scrolls)
	if err != nil {
		return FetchResult{}, err
	}

	markdown, err := c.extractor.Extract(html, url)
	if err != nil {
		return FetchResult{}, err
	}

	quality := calculateQuality(markdown)

	return FetchResult{
		ContentMD: markdown,
		Method:    "browser_scroll",
		Quality:   quality,
	}, nil
}

// shouldCache returns false for search engine result pages that must never
// be cached — their content changes on every request and stale results
// break URL discovery.
func shouldCache(url string) bool {
	noCacheDomains := []string{
		"duckduckgo.com",
		"google.com",
		"bing.com",
		"search.yahoo.com",
	}
	for _, d := range noCacheDomains {
		if strings.Contains(url, d) {
			return false
		}
	}
	return true
}

func calculateQuality(content string) float64 {
	if len(content) == 0 {
		return 0.0
	}
	score := 1.0
	if len(content) < 1000 {
		score -= 0.3
	}
	if strings.Contains(strings.ToLower(content), "login") && len(content) < 2000 {
		score -= 0.2
	}
	if score < 0 {
		score = 0
	}
	return score
}
