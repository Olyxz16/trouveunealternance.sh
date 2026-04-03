package scraper

import (
	"context"
	"errors"
	"jobhunter/internal/config"
	"jobhunter/internal/db"
	"regexp"
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
	cfg          *config.Config
}

func NewCascadeFetcher(http, browser Fetcher, forceBrowser []string, cache *db.DB, extractor *Extractor, logger *zap.Logger, cfg *config.Config) *CascadeFetcher {
	return &CascadeFetcher{
		http:         http,
		browser:      browser,
		forceBrowser: forceBrowser,
		cache:        cache,
		extractor:    extractor,
		logger:       logger,
		cfg:          cfg,
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

	quality := c.calculateQuality(markdown)

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
	// Cache check first
	if cached, err := c.cache.GetCache(url); err == nil && cached != nil {
		c.logger.Info("cache hit", zap.String("url", url))
		return FetchResult{
			ContentMD: cached.Content,
			Method:    "cache",
			Quality:   cached.Quality,
		}, nil
	}

	// Try browser with scroll if available
	if c.browser != nil {
		c.logger.Info("scrolling and fetching", zap.String("url", url), zap.Int("scrolls", scrolls))

		bf, ok := c.browser.(interface {
			FetchWithScroll(context.Context, string, int) (string, error)
		})
		if ok {
			html, err := bf.FetchWithScroll(ctx, url, scrolls)
			if err == nil {
				markdown, err := c.extractor.Extract(html, url)
				if err == nil {
					quality := c.calculateQuality(markdown)
					if quality > 0 {
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
				}
			}
			c.logger.Debug("browser scroll fetch failed, falling back to HTTP", zap.String("url", url), zap.Error(err))
		}
	}

	// Fallback to HTTP
	return c.Fetch(ctx, url)
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

	if useBrowser && c.browser != nil {
		c.logger.Info("forcing browser for domain", zap.String("url", url))
		res, err := c.tryFetcher(ctx, c.browser, url)
		if err == nil {
			return res, nil
		}
		c.logger.Warn("forced browser fetch failed, falling back to HTTP", zap.String("url", url), zap.Error(err))
	}

	// 3. Try HTTP first
	res, err := c.tryFetcher(ctx, c.http, url)
	if err == nil && res.Quality >= c.cfg.Quality.HTTPMin {
		return res, nil
	}

	// 4. Fallback to Browser
	if err != nil {
		c.logger.Warn("primary fetcher failed, trying fallback", zap.Error(err), zap.String("url", url))
	} else {
		c.logger.Info("low quality primary result, trying fallback", zap.Float64("quality", res.Quality), zap.String("url", url))
	}

	browserRes, browserErr := c.tryFetcher(ctx, c.browser, url)
	if browserErr != nil {
		// Both failed — return HTTP result if it had content, otherwise error
		if err == nil && res.Quality > 0 {
			return res, nil
		}
		return FetchResult{}, browserErr
	}

	// Return whichever is better
	if err == nil && res.Quality > browserRes.Quality {
		return res, nil
	}
	return browserRes, nil
}

func shouldCache(url string) bool {
	// Don't cache search engine results (they change fast)
	if strings.Contains(url, "duckduckgo.com") {
		return false
	}
	return true
}

func (c *CascadeFetcher) calculateQuality(markdown string) float64 {
	if len(markdown) < 100 {
		return 0.1
	}
	lowMD := strings.ToLower(markdown)

	// Anti-bot and error signals
	errorSignals := []string{
		"security check", "recaptcha", "robot check",
		"404 not found", "page not found",
		"access denied", "blocked", "please wait...",
		"checking your browser",
	}
	for _, sig := range errorSignals {
		if strings.Contains(lowMD, sig) {
			return 0.0
		}
	}

	// LinkedIn-specific login wall detection
	if strings.Contains(markdown, "linkedin.com") {
		loginSignals := []string{
			"agree & join linkedin",
			"already on linkedin? sign in",
			"new to linkedin? join now",
			"join linkedin to see who you know",
		}
		for _, sig := range loginSignals {
			if strings.Contains(lowMD, sig) {
				return 0.05 // Very low quality, essentially a failure
			}
		}
	}

	return 1.0
}

// personalProfileRe matches LinkedIn personal profile URLs (/in/slug)
var personalProfileRe = regexp.MustCompile(`linkedin\.com/in/[a-zA-Z0-9\-_%]+`)

// HasPersonalProfiles checks if the markdown contains personal LinkedIn profile links.
// A valid people page MUST have /in/ URLs — if none are found, LinkedIn is likely
// blocking the content (anti-bot, login wall, or empty company page).
func HasPersonalProfiles(markdown string) bool {
	return personalProfileRe.MatchString(markdown)
}

// CountPersonalProfiles returns the number of distinct personal profile URLs found.
func CountPersonalProfiles(markdown string) int {
	matches := personalProfileRe.FindAllString(markdown, -1)
	seen := make(map[string]bool)
	for _, m := range matches {
		seen[m] = true
	}
	return len(seen)
}
