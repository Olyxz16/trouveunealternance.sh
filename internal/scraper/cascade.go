package scraper

import (
	"context"
	"errors"
	"jobhunter/internal/db"
	"strings"

	"go.uber.org/zap"
)

type CascadeFetcher struct {
	http         Fetcher
	browser      Fetcher
	forceBrowser []string
	cache        *db.DB
	extractor    *Extractor
	logger       *zap.Logger
}

func NewCascadeFetcher(http, browser Fetcher, forceBrowser []string, cache *db.DB, extractor *Extractor, logger *zap.Logger) *CascadeFetcher {
	return &CascadeFetcher{
		http:         http,
		browser:      browser,
		forceBrowser: forceBrowser,
		cache:        cache,
		extractor:    extractor,
		logger:       logger,
	}
}

func (c *CascadeFetcher) tryFetcher(ctx context.Context, f Fetcher, url string) (FetchResult, error) {
	if f == nil {
		return FetchResult{}, errors.New("no fetcher available")
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
		err = c.cache.SetCache(&db.ScrapeCache{
			URL:     url,
			Method:  f.Name(),
			Content: markdown,
			Quality: quality,
		})
		if err != nil {
			c.logger.Error("failed to write to cache", zap.Error(err), zap.String("url", url))
		}
	}

	return res, nil
}

func (c *CascadeFetcher) ScrollAndFetch(ctx context.Context, url string, scrolls int) (FetchResult, error) {
	// For now, DDG and others that need scrolling will use the browser directly
	// bypassing the normal cascade because they need specific browser interactions
	if c.browser == nil {
		return c.Fetch(ctx, url)
	}

	// Cache check first
	if cached, err := c.cache.GetCache(url); err == nil && cached != nil {
		c.logger.Info("cache hit", zap.String("url", url))
		return FetchResult{
			ContentMD: cached.Content,
			Method:    "cache",
			Quality:   cached.Quality,
		}, nil
	}

	c.logger.Info("scrolling and fetching", zap.String("url", url), zap.Int("scrolls", scrolls))
	
	bf, ok := c.browser.(interface {
		FetchWithScroll(context.Context, string, int) (string, error)
	})
	if !ok {
		return c.tryFetcher(ctx, c.browser, url)
	}

	html, err := bf.FetchWithScroll(ctx, url, scrolls)
	if err != nil {
		return FetchResult{}, err
	}

	markdown, err := c.extractor.Extract(html, url)
	if err != nil {
		return FetchResult{}, err
	}

	quality := calculateQuality(markdown)
	
	// Save to cache
	if shouldCache(url) {
		_ = c.cache.SetCache(&db.ScrapeCache{
			URL:     url,
			Method:  "browser",
			Content: markdown,
			Quality: quality,
		})
	}

	return FetchResult{
		ContentMD: markdown,
		Method:    "browser",
		Quality:   quality,
	}, nil
}

func (c *CascadeFetcher) Fetch(ctx context.Context, url string) (FetchResult, error) {
	// 1. Cache check
	if cached, err := c.cache.GetCache(url); err == nil && cached != nil {
		c.logger.Info("cache hit", zap.String("url", url))
		return FetchResult{
			ContentMD: cached.Content,
			Method:    "cache",
			Quality:   cached.Quality,
		}, nil
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
		c.logger.Info("forcing browser for domain", zap.String("url", url))
		return c.tryFetcher(ctx, c.browser, url)
	}

	// 3. Try HTTP first
	res, err := c.tryFetcher(ctx, c.http, url)
	if err == nil && res.Quality >= 0.7 {
		return res, nil
	}

	// 4. Fallback to Browser
	if err != nil {
		c.logger.Warn("primary fetcher failed, trying fallback", zap.Error(err), zap.String("url", url))
	} else {
		c.logger.Info("low quality primary result, trying fallback", zap.Float64("quality", res.Quality), zap.String("url", url))
	}

	return c.tryFetcher(ctx, c.browser, url)
}

func shouldCache(url string) bool {
	// Don't cache search engine results (they change fast)
	if strings.Contains(url, "duckduckgo.com") {
		return false
	}
	return true
}

func calculateQuality(markdown string) float64 {
	if len(markdown) < 100 {
		return 0.1
	}
	if strings.Contains(markdown, "Security Check") || strings.Contains(markdown, "reCAPTCHA") {
		return 0.0
	}
	if strings.Contains(markdown, "404 Not Found") || strings.Contains(markdown, "Page Not Found") {
		return 0.0
	}
	return 1.0 // Basic heuristic
}
