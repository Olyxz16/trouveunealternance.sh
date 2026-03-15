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
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)

	if binaryPath != "" {
		opts = append(opts, chromedp.ExecPath(binaryPath))
	}

	if display != "" {
		opts = append(opts, chromedp.Env("DISPLAY="+display))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)

	// Warm up — start the browser process now, not on first fetch
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("failed to start browser: %w", err)
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

	err := chromedp.Run(f.ctx,
		chromedp.Navigate(url),
		// Wait for body to be present — basic JS rendering complete
		chromedp.WaitReady("body", chromedp.ByQuery),
		// Small pause for dynamic content (React, Vue etc.)
		chromedp.Sleep(1500*time.Millisecond),
		// Get full rendered HTML
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	if err != nil {
		return "", fmt.Errorf("browser fetch failed for %s: %w", url, err)
	}

	// Save cookies after each successful fetch to keep session fresh
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
