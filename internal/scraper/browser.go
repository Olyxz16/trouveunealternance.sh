package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/config"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

// stealthScript is injected into every page to hide automation fingerprints.
const stealthScript = `
// Remove navigator.webdriver flag
Object.defineProperty(navigator, 'webdriver', { get: () => false });

// Add window.chrome object (missing in headless Chrome)
window.chrome = {
  runtime: {},
  loadTimes: function() {},
  connection: { effectiveType: '4g', rtt: 50, downlink: 10, saveData: false }
};

// Spoof navigator.plugins to look like a real browser
const fakePlugins = [
  { name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer' },
  { name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai' },
  { name: 'Native Client', filename: 'internal-nacl-plugin' }
];
Object.defineProperty(navigator, 'plugins', {
  get: () => fakePlugins,
  enumerable: true,
  configurable: true,
  length: fakePlugins.length
});

// Spoof navigator.languages
Object.defineProperty(navigator, 'languages', {
  get: () => ['en-US', 'en', 'fr-FR', 'fr']
});

// Spoof navigator.hardwareConcurrency
Object.defineProperty(navigator, 'hardwareConcurrency', {
  get: () => 8
});

// Spoof navigator.maxTouchPoints
Object.defineProperty(navigator, 'maxTouchPoints', {
  get: () => 0
});

// Override permissions query to hide automation
const originalQuery = window.navigator.permissions.query;
window.navigator.permissions.query = (parameters) => {
  if (parameters.name === 'notifications') {
    return Promise.resolve({ state: Notification.permission });
  }
  return originalQuery(parameters);
};

// Spoof WebGL vendor and renderer
const getParameter = WebGLRenderingContext.prototype.getParameter;
WebGLRenderingContext.prototype.getParameter = function(parameter) {
  if (parameter === 37445) return 'Google Inc. (Intel)';
  if (parameter === 37446) return 'ANGLE (Intel, Intel(R) UHD Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)';
  return getParameter.call(this, parameter);
};

// Remove headless indicators
delete navigator.__proto__.webdriver;
`

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
type BrowserFetcher struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
	cookies     string
	logger      *zap.Logger
	cfg         *config.Config
}

func NewBrowserFetcher(cookiesPath, display string, headless bool, binaryPath string, logger *zap.Logger, cfg *config.Config) (*BrowserFetcher, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-infobars", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-setuid-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WindowSize(1920, 1080),
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

	bf := &BrowserFetcher{
		allocCtx:    allocCtx,
		allocCancel: allocCancel,
		ctx:         ctx,
		cancel:      cancel,
		cookies:     cookiesPath,
		logger:      logger,
		cfg:         cfg,
	}

	if err := bf.loadCookies(); err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("failed to load cookies", zap.Error(err))
		}
	} else {
		logger.Info("browser session loaded", zap.String("cookies", cookiesPath))
	}

	return bf, nil
}

func (f *BrowserFetcher) Name() string { return "browser" }

// applyStealth injects stealth scripts to hide automation fingerprints.
func (f *BrowserFetcher) applyStealth(ctx context.Context) error {
	return chromedp.Run(ctx,
		chromedp.Evaluate(stealthScript, nil),
	)
}

func (f *BrowserFetcher) Navigate(ctx context.Context, url string) error {
	f.logger.Info("browser navigating (persistent)", zap.String("url", url))
	return chromedp.Run(f.ctx, chromedp.Navigate(url))
}

func (f *BrowserFetcher) Fetch(ctx context.Context, url string) (string, error) {
	var html string

	f.logger.Info("browser fetching", zap.String("url", url))

	runCtx, cancel := chromedp.NewContext(f.ctx)
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

	err := chromedp.Run(timeoutCtx,
		network.Enable(),
		emulation.SetUserAgentOverride(f.cfg.Scraping.UserAgent),
		chromedp.Navigate(url),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// Apply stealth scripts to hide automation
			if err := f.applyStealth(ctx); err != nil {
				f.logger.Debug("stealth script failed", zap.Error(err))
			}

			// Check if we were redirected to a login wall
			var currentURL string
			if err := chromedp.Location(&currentURL).Do(ctx); err == nil {
				if strings.Contains(currentURL, "linkedin.com/authwall") ||
					strings.Contains(currentURL, "linkedin.com/login") ||
					strings.Contains(currentURL, "checkpoint/challenges") {
					f.logger.Warn("LinkedIn session invalid or blocked (authwall)", zap.String("url", currentURL))
					return fmt.Errorf("linkedin authwall detected: %s", currentURL)
				}
			}

			selectors := []string{
				`button[data-control-name="ga-cookie.accept_all"]`,
				`button#onetrust-accept-btn-handler`,
				`button.accept-all`,
				`button[aria-label="Accept all"]`,
				`.cookie-banner button`,
			}
			for _, sel := range selectors {
				var nodes []*cdp.Node
				if err := chromedp.Nodes(sel, &nodes, chromedp.AtLeast(0)).Do(ctx); err == nil && len(nodes) > 0 {
					_ = chromedp.Click(sel, chromedp.ByQuery).Do(ctx)
					time.Sleep(f.cfg.Scraping.Delays.CookieClick)
				}
			}
			return nil
		}),
		chromedp.Sleep(f.cfg.Scraping.Delays.BrowserSettle),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	)
	if err != nil {
		return "", fmt.Errorf("browser fetch failed for %s: %w", url, err)
	}

	f.logger.Info("browser fetch success", zap.String("url", url), zap.Int("html_len", len(html)))

	if err := f.saveCookies(); err != nil {
		f.logger.Warn("failed to save cookies", zap.Error(err))
	}

	return html, nil
}

func (f *BrowserFetcher) Scroll(ctx context.Context, times int) error {
	runCtx, cancel := chromedp.NewContext(f.ctx)
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

func (f *BrowserFetcher) FetchWithScroll(ctx context.Context, url string, scrolls int) (string, error) {
	var html string

	f.logger.Info("browser fetching with scroll", zap.String("url", url), zap.Int("scrolls", scrolls))

	runCtx, cancel := chromedp.NewContext(f.ctx)
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
		network.Enable(),
		emulation.SetUserAgentOverride(f.cfg.Scraping.UserAgent),
		chromedp.Navigate(url),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// Apply stealth scripts to hide automation
			if err := f.applyStealth(ctx); err != nil {
				f.logger.Debug("stealth script failed", zap.Error(err))
			}

			// Check if we were redirected to a login wall
			var currentURL string
			if err := chromedp.Location(&currentURL).Do(ctx); err == nil {
				if strings.Contains(currentURL, "linkedin.com/authwall") ||
					strings.Contains(currentURL, "linkedin.com/login") ||
					strings.Contains(currentURL, "checkpoint/challenges") {
					f.logger.Warn("LinkedIn session invalid or blocked (authwall)", zap.String("url", currentURL))
					return fmt.Errorf("linkedin authwall detected: %s", currentURL)
				}
			}

			selectors := []string{
				`button[data-control-name="ga-cookie.accept_all"]`,
				`button#onetrust-accept-btn-handler`,
				`button.accept-all`,
				`button[aria-label="Accept all"]`,
				`.cookie-banner button`,
			}
			for _, sel := range selectors {
				var nodes []*cdp.Node
				if err := chromedp.Nodes(sel, &nodes, chromedp.AtLeast(0)).Do(ctx); err == nil && len(nodes) > 0 {
					_ = chromedp.Click(sel, chromedp.ByQuery).Do(ctx)
					time.Sleep(f.cfg.Scraping.Delays.CookieClick)
				}
			}
			return nil
		}),
		chromedp.Sleep(f.cfg.Scraping.Delays.BrowserSettle),
		chromedp.MouseEvent("mouseMoved", 150, 150),
	}

	for i := 0; i < scrolls; i++ {
		actions = append(actions,
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight * (0.4 + Math.random()*0.5))`, nil),
			chromedp.Sleep(f.cfg.Scraping.Delays.ScrollBase+time.Duration(i)*f.cfg.Scraping.Delays.ScrollVariance),
			chromedp.MouseEvent("mouseMoved", 200+float64(i)*20, 200+float64(i)*20),
		)
	}

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

func (f *BrowserFetcher) Close() {
	_ = f.saveCookies()
	f.cancel()
	f.allocCancel()
}

func (f *BrowserFetcher) SaveCookiesManual() error {
	return f.saveCookies()
}

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
		// Enable network domain to ensure cookies can be set
		if err := network.Enable().Do(ctx); err != nil {
			return err
		}

		// Optional: Navigate to a neutral page on the domain to "warm up"
		// This can help Chrome associate cookies more reliably
		_ = chromedp.Navigate("https://www.linkedin.com/robots.txt").Do(ctx)

		for _, c := range cookies {
			call := network.SetCookie(c.Name, c.Value).
				WithDomain(c.Domain).
				WithPath(c.Path).
				WithHTTPOnly(c.HTTPOnly).
				WithSecure(c.Secure)

			// Only set expires if it's a future date and not a session cookie (0 or -1)
			if c.Expires > 0 {
				expr := cdp.TimeSinceEpoch(time.Unix(int64(c.Expires), 0))
				call = call.WithExpires(&expr)
			}

			if err := call.Do(ctx); err != nil {
				f.logger.Warn("failed to set cookie",
					zap.String("name", c.Name),
					zap.Error(err))
			}
		}
		return nil
	}))
}

func (f *BrowserFetcher) saveCookies() error {
	var cookies []*network.Cookie
	if err := chromedp.Run(f.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		// Use storage.GetCookies to get ALL cookies from the browser.
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

	return os.WriteFile(f.cookies, data, 0600)
}
