# JobHunter — Gemini API Integration & URL Discovery Rework

## Context

The current URL discovery pipeline fails silently for many companies, showing
`✗ Failed` in the TUI when the real problem is simply that no URL was found.
The fix is to use Gemini 2.5 Flash with Google Search grounding via the official
Gemini API as the primary discovery method, with DuckDuckGo as fallback.

This approach matches the quality of the old gemini-cli + blueprint-mcp setup
but through a stable API instead of a CLI process.

---

## Prerequisites — Get a Gemini API Key

1. Go to `https://aistudio.google.com/apikey`
2. Click "Create API key" — no credit card, no billing required
3. Add to `.env`:

```env
GEMINI_API_KEY=AIza...
GEMINI_API_MODEL=gemini-2.5-flash
```

**Financial risk: zero.** Without explicitly enabling billing in Google Cloud
Console, requests return `429` when the free tier is exhausted and you are never
charged. The free tier is 500 requests/day, 10 RPM, 1M input tokens/day. At
current scale (hundreds of companies, a few calls each) this is sufficient.
Speed is not a concern so RPM limiting is acceptable.

---

## Changes Required

### 1. `internal/config/config.go`

Add two new fields:

```go
type Config struct {
    // ... existing fields unchanged ...
    GeminiAPIKey   string `env:"GEMINI_API_KEY"   envDefault:""`
    GeminiAPIModel string `env:"GEMINI_API_MODEL" envDefault:"gemini-2.5-flash"`
}
```

---

### 2. `internal/llm/gemini_api.go` — new file

Create this file from scratch. It implements the `Provider` interface and adds
a `CompleteWithSearch` method that enables Google Search grounding.

**Important:** JSON mode (`responseMimeType: application/json`) and search
grounding cannot be used simultaneously — the Gemini API does not support both
at once. When `CompleteWithSearch` is called, JSON must be parsed manually from
the text response using the existing `extractJSON` helper. See the discovery
prompt design in section 4 for how to handle this.

```go
package llm

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "jobhunter/internal/errors"
    "net/http"
    "time"
)

const geminiAPIBase = "https://generativelanguage.googleapis.com/v1beta/models"

type GeminiAPIProvider struct {
    APIKey     string
    Model      string
    HTTPClient *http.Client
}

func NewGeminiAPIProvider(apiKey, model string) *GeminiAPIProvider {
    if model == "" {
        model = "gemini-2.5-flash"
    }
    return &GeminiAPIProvider{
        APIKey: apiKey,
        Model:  model,
        HTTPClient: &http.Client{
            Timeout: 120 * time.Second, // generous — search grounding takes time
        },
    }
}

func (p *GeminiAPIProvider) Name() string {
    return "gemini_api"
}

// --- request/response structs ---

type geminiRequest struct {
    Contents          []geminiContent        `json:"contents"`
    SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
    Tools             []geminiTool           `json:"tools,omitempty"`
    GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiContent struct {
    Role  string       `json:"role,omitempty"`
    Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
    Text string `json:"text"`
}

type geminiTool struct {
    GoogleSearch *struct{} `json:"google_search,omitempty"`
}

type geminiGenerationConfig struct {
    ResponseMIMEType string `json:"responseMimeType,omitempty"`
}

type geminiResponse struct {
    Candidates []struct {
        Content geminiContent `json:"content"`
    } `json:"candidates"`
    UsageMetadata struct {
        PromptTokenCount     int `json:"promptTokenCount"`
        CandidatesTokenCount int `json:"candidatesTokenCount"`
    } `json:"usageMetadata"`
    Error *struct {
        Code    int    `json:"code"`
        Message string `json:"message"`
    } `json:"error"`
}

// Complete implements the Provider interface. No search grounding.
func (p *GeminiAPIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
    return p.complete(ctx, req, false)
}

// CompleteWithSearch enables Google Search grounding. The model will search
// the web as part of generating its response.
// NOTE: JSON mode is disabled when search is enabled — parse JSON from text.
func (p *GeminiAPIProvider) CompleteWithSearch(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
    return p.complete(ctx, req, true)
}

func (p *GeminiAPIProvider) complete(ctx context.Context, req CompletionRequest, withSearch bool) (CompletionResponse, error) {
    payload := geminiRequest{
        Contents: []geminiContent{
            {Role: "user", Parts: []geminiPart{{Text: req.User}}},
        },
        GenerationConfig: geminiGenerationConfig{},
    }

    if req.System != "" {
        payload.SystemInstruction = &geminiContent{
            Parts: []geminiPart{{Text: req.System}},
        }
    }

    if req.JSONMode && !withSearch {
        payload.GenerationConfig.ResponseMIMEType = "application/json"
    }

    if withSearch {
        payload.Tools = []geminiTool{
            {GoogleSearch: &struct{}{}},
        }
    }

    body, err := json.Marshal(payload)
    if err != nil {
        return CompletionResponse{}, err
    }

    url := fmt.Sprintf("%s/%s:generateContent?key=%s",
        geminiAPIBase, p.Model, p.APIKey)

    httpReq, err := http.NewRequestWithContext(ctx, "POST", url,
        bytes.NewReader(body))
    if err != nil {
        return CompletionResponse{}, err
    }
    httpReq.Header.Set("Content-Type", "application/json")

    resp, err := p.HTTPClient.Do(httpReq)
    if err != nil {
        return CompletionResponse{}, err
    }
    defer resp.Body.Close()

    raw, err := io.ReadAll(resp.Body)
    if err != nil {
        return CompletionResponse{}, err
    }

    if resp.StatusCode == 429 {
        return CompletionResponse{}, errors.NewRateLimitError(60, p.Model)
    }
    if resp.StatusCode != 200 {
        return CompletionResponse{}, errors.NewModelError(p.Model, resp.StatusCode)
    }

    var gemResp geminiResponse
    if err := json.Unmarshal(raw, &gemResp); err != nil {
        return CompletionResponse{}, fmt.Errorf("failed to parse Gemini response: %w", err)
    }

    if gemResp.Error != nil {
        return CompletionResponse{}, fmt.Errorf("gemini API error %d: %s",
            gemResp.Error.Code, gemResp.Error.Message)
    }

    if len(gemResp.Candidates) == 0 ||
        len(gemResp.Candidates[0].Content.Parts) == 0 {
        return CompletionResponse{}, fmt.Errorf("empty response from Gemini API")
    }

    content := gemResp.Candidates[0].Content.Parts[0].Text

    return CompletionResponse{
        Content:          content,
        PromptTokens:     gemResp.UsageMetadata.PromptTokenCount,
        CompletionTokens: gemResp.UsageMetadata.CandidatesTokenCount,
        CostUSD:          0,
        EstimatedCost:    false,
    }, nil
}
```

---

### 3. `internal/enricher/discover.go` — full rewrite

Replace the entire file. Key changes:
- `URLDiscoverer` now accepts an optional `*llm.GeminiAPIProvider`
- `DiscoverURLs` tries Gemini search first, falls back to DuckDuckGo
- `extractLinkedInURL` replaced with a compiled regex (fixes brittle parsing)
- LinkedIn slug guessing added as last resort
- Both website and LinkedIn URL returned from a single Gemini call

```go
package enricher

import (
    "context"
    "encoding/json"
    "fmt"
    "jobhunter/internal/db"
    "jobhunter/internal/llm"
    "jobhunter/internal/scraper"
    "net/url"
    "regexp"
    "strings"
)

var (
    linkedinCompanyRe = regexp.MustCompile(
        `https?://(?:www\.)?linkedin\.com/company/([a-zA-Z0-9\-_%]+)`)
    nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)
)

type URLDiscoverer struct {
    fetcher   *scraper.CascadeFetcher
    geminiAPI *llm.GeminiAPIProvider // nil if not configured — falls back to DDG
}

func NewURLDiscoverer(fetcher *scraper.CascadeFetcher, geminiAPI *llm.GeminiAPIProvider) *URLDiscoverer {
    return &URLDiscoverer{fetcher: fetcher, geminiAPI: geminiAPI}
}

const discoverySystemPrompt = `You are finding the online presence of French companies.
Given a company name, SIREN, city and NAF code, find:
1. Their official website — the primary domain they own and operate
2. Their LinkedIn company page URL

RULES:
- website must be the company's own domain — NEVER return URLs from:
  societe.com, pappers.fr, manageo.fr, infogreffe.fr, verif.com,
  linkedin.com, facebook.com, twitter.com, indeed.fr, welcometothejungle.com
- linkedin_url must be a linkedin.com/company/ URL — never a /in/ personal profile
- If you are not confident about a URL, return empty string for that field
- Use your search capability to find accurate results
- For public sector organisations (hospitals, mairies, CCAS) the website is
  often a .fr government domain — include it if found

Return ONLY a JSON object, no explanation:
{
  "website": "https://...",
  "linkedin_url": "https://www.linkedin.com/company/..."
}`

// DiscoverURLs returns (website, linkedinURL, error).
// Tries Gemini search grounding first, falls back to DuckDuckGo.
func (d *URLDiscoverer) DiscoverURLs(ctx context.Context, comp db.Company) (string, string, error) {
    if d.geminiAPI != nil {
        website, linkedin, err := d.discoverWithGemini(ctx, comp)
        if err == nil && (website != "" || linkedin != "") {
            return website, linkedin, nil
        }
        // fall through to DuckDuckGo on error or empty result
    }
    return d.discoverWithDDG(ctx, comp)
}

func (d *URLDiscoverer) discoverWithGemini(ctx context.Context, comp db.Company) (string, string, error) {
    prompt := fmt.Sprintf(
        "Company: %s\nSIREN: %s\nCity: %s\nNAF: %s\n\nFind their official website and LinkedIn company page.",
        comp.Name,
        comp.Siren.String,
        comp.City.String,
        comp.NAFCode.String,
    )

    resp, err := d.geminiAPI.CompleteWithSearch(ctx, llm.CompletionRequest{
        System: discoverySystemPrompt,
        User:   prompt,
    })
    if err != nil {
        return "", "", fmt.Errorf("gemini search failed: %w", err)
    }

    // JSON mode is off when search grounding is active — extract manually
    clean := extractJSONFromText(resp.Content)
    if clean == "" {
        return "", "", fmt.Errorf("no JSON found in gemini response")
    }

    var result struct {
        Website     string `json:"website"`
        LinkedinURL string `json:"linkedin_url"`
    }
    if err := json.Unmarshal([]byte(clean), &result); err != nil {
        return "", "", fmt.Errorf("failed to parse discovery JSON: %w", err)
    }

    return result.Website, result.LinkedinURL, nil
}

func (d *URLDiscoverer) discoverWithDDG(ctx context.Context, comp db.Company) (string, string, error) {
    query := fmt.Sprintf("%s %s linkedin company", comp.Name, comp.City.String)
    searchURL := fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))

    res, err := d.fetcher.Fetch(ctx, searchURL)
    if err != nil {
        return "", "", fmt.Errorf("DDG search failed: %w", err)
    }

    linkedinURL := d.extractLinkedInURL(res.ContentMD)

    if linkedinURL == "" {
        query = fmt.Sprintf("%s linkedin company", comp.Name)
        searchURL = fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))
        res, err = d.fetcher.Fetch(ctx, searchURL)
        if err == nil {
            linkedinURL = d.extractLinkedInURL(res.ContentMD)
        }
    }

    // Last resort: guess slug from company name
    if linkedinURL == "" {
        linkedinURL = guessLinkedInSlug(comp.Name)
    }

    return "", linkedinURL, nil
}

// extractLinkedInURL uses a regex instead of fragile string splitting.
func (d *URLDiscoverer) extractLinkedInURL(markdown string) string {
    match := linkedinCompanyRe.FindStringSubmatch(markdown)
    if len(match) < 2 {
        return ""
    }
    slug := strings.TrimRight(match[1], "/")
    return "https://www.linkedin.com/company/" + slug
}

// guessLinkedInSlug constructs a best-effort LinkedIn URL from the company
// name. Result is unverified — callers must validate fetch quality.
func guessLinkedInSlug(name string) string {
    slug := strings.ToLower(name)
    slug = nonAlphanumRe.ReplaceAllString(slug, "-")
    slug = strings.Trim(slug, "-")
    if slug == "" {
        return ""
    }
    return "https://www.linkedin.com/company/" + slug
}

// extractJSONFromText finds the first {...} block in a text response.
// Used when JSON mode cannot be enabled (e.g. search grounding active).
func extractJSONFromText(content string) string {
    start := strings.Index(content, "{")
    end := strings.LastIndex(content, "}")
    if start == -1 || end == -1 || end <= start {
        return ""
    }
    return strings.TrimSpace(content[start : end+1])
}
```

---

### 4. `internal/enricher/enrich.go` — three targeted changes

**Change A — fix `no URL found` to be a non-error:**

```go
// BEFORE
if targetURL == "" {
    return fmt.Errorf("no URL found for company %s", comp.Name)
}

// AFTER
if targetURL == "" {
    _ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
        "status": "NO_CONTACT_FOUND",
    })
    return nil
}
```

**Change B — add quality gate after fetch:**

A guessed LinkedIn slug that 404s returns low-quality content. Fall back to
the website if available:

```go
// BEFORE
res, err := e.fetcher.Fetch(ctx, targetURL)
if err != nil {
    return fmt.Errorf("fetch failed for %s: %w", targetURL, err)
}

// AFTER
res, err := e.fetcher.Fetch(ctx, targetURL)
if err != nil || res.Quality < 0.3 {
    if website != "" && targetURL != website {
        log.Printf("low quality fetch for %s (%.2f), retrying with website",
            targetURL, res.Quality)
        res, err = e.fetcher.Fetch(ctx, website)
    }
    if err != nil || res.Quality < 0.3 {
        // guessed URL was wrong — clear it if we wrote it
        if strings.Contains(targetURL, "linkedin.com") &&
            targetURL != comp.LinkedinURL.String {
            _ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
                "linkedin_url": "",
            })
        }
        _ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
            "status": "NO_CONTACT_FOUND",
        })
        return nil
    }
}
```

**Change C — pass geminiAPI into URLDiscoverer:**

```go
// Update NewEnricher signature:
type Enricher struct {
    db         *db.DB
    fetcher    *scraper.CascadeFetcher
    classifier *Classifier
    recherche  *collector.RechercheClient
    geminiAPI  *llm.GeminiAPIProvider // nil if not configured
}

func NewEnricher(
    database *db.DB,
    fetcher *scraper.CascadeFetcher,
    classifier *Classifier,
    geminiAPI *llm.GeminiAPIProvider,
) *Enricher {
    return &Enricher{
        db:         database,
        fetcher:    fetcher,
        classifier: classifier,
        recherche:  collector.NewRechercheClient(),
        geminiAPI:  geminiAPI,
    }
}

// Inside EnrichCompany, update the URLDiscoverer instantiation:
disc := NewURLDiscoverer(e.fetcher, e.geminiAPI)
```

---

### 5. `cmd/enrich.go` — wire up GeminiAPIProvider

```go
// After existing LLM setup, before cascade fetcher:
var geminiAPI *llm.GeminiAPIProvider
if cfg.GeminiAPIKey != "" {
    geminiAPI = llm.NewGeminiAPIProvider(cfg.GeminiAPIKey, cfg.GeminiAPIModel)
    logger.Info("Gemini API search grounding enabled for URL discovery")
} else {
    logger.Warn("GEMINI_API_KEY not set — falling back to DuckDuckGo for discovery")
}

// Update NewEnricher call:
enr := enricher.NewEnricher(database, cascade, classifier, geminiAPI)
```

---

## Behaviour After These Changes

```
Company has SIREN + Gemini API key configured:
  → Single Gemini call with Google Search grounding
  → Returns website + LinkedIn URL simultaneously
  → Falls back to DuckDuckGo if Gemini fails or returns empty

Company has no URL after all discovery attempts:
  → Status set to NO_CONTACT_FOUND (silent, not ✗ Failed)
  → No error counted in pipeline run stats

Guessed LinkedIn slug returns 404 / low quality:
  → Falls back to website if available
  → Otherwise NO_CONTACT_FOUND, bad URL not persisted to DB

GEMINI_API_KEY not set:
  → DuckDuckGo path unchanged, works exactly as before
```

## Fallback Chain Summary

```
1. Gemini 2.5 Flash + Google Search  (best quality, free tier 500 req/day)
   ↓ on 429 / error / empty result
2. DuckDuckGo browser fetch + regex extraction
   ↓ no LinkedIn found
3. LinkedIn slug guess from company name
   ↓ low quality fetch (< 0.3)
4. Website as fetch target instead
   ↓ still no good content
5. NO_CONTACT_FOUND — silent, not an error
```

## Rate Limit Handling

The existing `Client` retry logic handles 429 from OpenRouter. The
`GeminiAPIProvider` returns `errors.NewRateLimitError` on 429, which the same
retry logic in `client.go` will catch if routed through the `Client`. However
`CompleteWithSearch` is called directly from `URLDiscoverer`, bypassing
`client.go`. Add simple retry handling directly in `discoverWithGemini`:

```go
func (d *URLDiscoverer) discoverWithGemini(ctx context.Context, comp db.Company) (string, string, error) {
    var lastErr error
    for attempt := 0; attempt < 3; attempt++ {
        if attempt > 0 {
            time.Sleep(time.Duration(attempt*30) * time.Second)
        }
        website, linkedin, err := d.tryGeminiSearch(ctx, comp)
        if err == nil {
            return website, linkedin, nil
        }
        if _, ok := err.(*errors.RateLimitError); ok {
            lastErr = err
            continue // retry after backoff
        }
        return "", "", err // non-rate-limit error, don't retry
    }
    return "", "", lastErr
}
```

Split the existing body into a private `tryGeminiSearch` method and call it
from this wrapper.
