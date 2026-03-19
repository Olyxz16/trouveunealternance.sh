package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

// cookieData matches the JSON structure for persistence.
type cookieData struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
}

// BrowserFetcher is a persistent chromedp session.
// Create once at startup, reuse for all fetches, Close() at shutdown.
type BrowserFetcher struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
	cookies     string // path to browser_session.json
	logger      *zap.Logger
}

// NewBrowserFetcher starts a chromedp browser and loads saved cookies.
// display: empty string uses the current DISPLAY env var (dev/macOS).
//
//	":99" uses Xvfb on headless server.
//
// headless: false for the login command, true for pipeline runs.
// binaryPath: empty = auto-detect chromium/chrome.
func NewBrowserFetcher(cookiesPath, display string, headless bool, binaryPath string, logger *zap.Logger) (*BrowserFetcher, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-infobars", true),
		chromedp.Flag("no-sandbox", true),             // required in Docker/server
		chromedp.Flag("disable-setuid-sandbox", true), // required in Docker/server
		chromedp.Flag("disable-dev-shm-usage", true),  // prevents crashes on low-memory servers
		chromedp.WindowSize(1920, 1080),
		// Use a slightly more common/modern user agent
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"),
	)

	if binaryPath != "" {
		opts = append(opts, chromedp.ExecPath(binaryPath))
	}

	if display != "" {
		opts = append(opts, chromedp.Env("DISPLAY="+display))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)

	// Warm up with timeout — start the browser process now
	warmupCtx, warmupCancel := context.WithTimeout(ctx, 30*time.Second)
	defer warmupCancel()

	if err := chromedp.Run(warmupCtx); err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("failed to start browser (check if Chrome/Chromium is installed): %w", err)
	}

	bf := &BrowserFetcher{
		allocCtx:    allocCtx,
		allocCancel: allocCancel,
		ctx:         ctx,
		cancel:      cancel,
		cookies:     cookiesPath,
		logger:      logger,
	}

	// Load saved cookies if they exist
	if err := bf.loadCookies(); err != nil {
		logger.Warn("no saved cookies found, starting fresh session", zap.Error(err))
	} else {
		logger.Info("browser session loaded", zap.String("cookies", cookiesPath))
	}

	return bf, nil
}

func (f *BrowserFetcher) Name() string { return "browser" }

// Fetch navigates to url, waits for the page to load, and returns the full HTML.
func (f *BrowserFetcher) Fetch(ctx context.Context, url string) (string, error) {
	var html string

	f.logger.Info("browser fetching", zap.String("url", url))

	// Deriving a new context from the allocator context
	runCtx, cancel := chromedp.NewContext(f.allocCtx)
	defer cancel()

	// Apply timeout from provided ctx if it has one, or default 60s
	deadline, ok := ctx.Deadline()
	var timeoutCtx context.Context
	var timeoutCancel context.CancelFunc
	if ok {
		timeoutCtx, timeoutCancel = context.WithDeadline(runCtx, deadline)
	} else {
		timeoutCtx, timeoutCancel = context.WithTimeout(runCtx, 60*time.Second)
	}
	defer timeoutCancel()

	err := chromedp.Run(timeoutCtx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// Optional cookie clicker
			_ = chromedp.Click(`button[data-control-name="ga-cookie.accept_all"]`, chromedp.ByQuery).Do(ctx)
			return nil
		}),
		chromedp.Sleep(2000*time.Millisecond),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	if err != nil {
		return "", fmt.Errorf("browser fetch failed for %s: %w", url, err)
	}

	f.logger.Info("browser fetch success", zap.String("url", url), zap.Int("html_len", len(html)))

	// Save cookies after each successful fetch to keep session fresh
	if err := f.saveCookies(); err != nil {
		f.logger.Warn("failed to save cookies", zap.Error(err))
	}

	return html, nil
}

// Scroll scrolls the page down to trigger lazy loading.
func (f *BrowserFetcher) Scroll(ctx context.Context, times int) error {
	runCtx, cancel := chromedp.NewContext(f.allocCtx)
	defer cancel()

	deadline, ok := ctx.Deadline()
	var timeoutCtx context.Context
	var timeoutCancel context.CancelFunc
	if ok {
		timeoutCtx, timeoutCancel = context.WithDeadline(runCtx, deadline)
	} else {
		timeoutCtx, timeoutCancel = context.WithTimeout(runCtx, 60*time.Second)
	}
	defer timeoutCancel()

	for i := 0; i < times; i++ {
		err := chromedp.Run(timeoutCtx,
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
			chromedp.Sleep(1000*time.Millisecond),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// FetchWithScroll navigates to url, scrolls down multiple times, and returns the full HTML.
// This is essential for sites like LinkedIn that lazy-load content.
func (f *BrowserFetcher) FetchWithScroll(ctx context.Context, url string, scrolls int) (string, error) {
	var html string

	f.logger.Info("browser fetching with scroll", zap.String("url", url), zap.Int("scrolls", scrolls))

	runCtx, cancel := chromedp.NewContext(f.allocCtx)
	defer cancel()

	deadline, ok := ctx.Deadline()
	var timeoutCtx context.Context
	var timeoutCancel context.CancelFunc
	if ok {
		timeoutCtx, timeoutCancel = context.WithDeadline(runCtx, deadline)
	} else {
		timeoutCtx, timeoutCancel = context.WithTimeout(runCtx, 150*time.Second)
	}
	defer timeoutCancel()

	actions := []chromedp.Action{
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_ = chromedp.Click(`button[data-control-name="ga-cookie.accept_all"]`, chromedp.ByQuery).Do(ctx)
			return nil
		}),
		chromedp.Sleep(3000 * time.Millisecond),
		// Random mouse movement
		chromedp.MouseEvent("mouseMoved", 150, 150),
	}

	// Add scroll actions with more variance
	for i := 0; i < scrolls; i++ {
		actions = append(actions,
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight * (0.4 + Math.random()*0.5))`, nil),
			chromedp.Sleep(time.Duration(2000+i*500)*time.Millisecond),
			chromedp.MouseEvent("mouseMoved", 200+float64(i)*20, 200+float64(i)*20),
		)
	}

	// Final extraction
	actions = append(actions, chromedp.OuterHTML("html", &html, chromedp.ByQuery))

	err := chromedp.Run(timeoutCtx, actions...)
	if err != nil {
		return "", fmt.Errorf("browser fetch with scroll failed for %s: %w", url, err)
	}

	f.logger.Info("browser fetch with scroll success", zap.String("url", url), zap.Int("html_len", len(html)))

	if err := f.saveCookies(); err != nil {
		f.logger.Warn("failed to save cookies", zap.Error(err))
	}

	return html, nil
}

// Close shuts down the browser gracefully and saves cookies one final time.
func (f *BrowserFetcher) Close() {
	_ = f.saveCookies()
	f.cancel()
	f.allocCancel()
}

// HasCookie returns true if a cookie with the given name and domain exists
// in the current browser session. Used by the login command to detect
// successful authentication without fetching a page.
func (f *BrowserFetcher) HasCookie(name, domain string) (bool, error) {
	var found bool
	err := chromedp.Run(f.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		cookies, err := storage.GetCookies().Do(ctx)
		if err != nil {
			return err
		}
		for _, c := range cookies {
			if c.Name == name && strings.Contains(c.Domain, strings.TrimPrefix(domain, ".")) {
				found = true
				return nil
			}
		}
		return nil
	}))
	return found, err
}

// loadCookies reads the session file and injects cookies into the browser.
func (f *BrowserFetcher) loadCookies() error {
	data, err := os.ReadFile(f.cookies)
	if err != nil {
		return err
	}

	var cookies []cookieData
	if err := json.Unmarshal(data, &cookies); err != nil {
		return fmt.Errorf("invalid cookies file: %w", err)
	}

	return chromedp.Run(f.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, c := range cookies {
			expr := cdp.TimeSinceEpoch(time.Unix(int64(c.Expires), 0))
			if err := network.SetCookie(c.Name, c.Value).
				WithDomain(c.Domain).
				WithPath(c.Path).
				WithHTTPOnly(c.HTTPOnly).
				WithSecure(c.Secure).
				WithExpires(&expr).
				Do(ctx); err != nil {
				f.logger.Warn("failed to set cookie",
					zap.String("name", c.Name),
					zap.Error(err))
			}
		}
		return nil
	}))
}

// saveCookies reads all cookies from the browser and writes them to the session file.
func (f *BrowserFetcher) saveCookies() error {
	var cookies []*network.Cookie
	if err := chromedp.Run(f.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		cookies, err = storage.GetCookies().Do(ctx)
		return err
	})); err != nil {
		return err
	}

	var out []cookieData
	for _, c := range cookies {
		out = append(out, cookieData{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  float64(c.Expires),
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
		})
	}

	dir := filepath.Dir(f.cookies)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(f.cookies, data, 0600) // 0600 = owner read/write only
}
