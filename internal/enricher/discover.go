package enricher

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/errors"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"
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
			log.Printf("DEBUG [%s]: Gemini discovery success: website=%s, linkedin=%s", comp.Name, website, linkedin)
			return website, linkedin, nil
		}
		log.Printf("DEBUG [%s]: Gemini discovery failed or empty (err: %v). Falling back to DDG...", comp.Name, err)
	}
	w, l, err := d.discoverWithDDG(ctx, comp)
	log.Printf("DEBUG [%s]: DDG discovery result: website=%s, linkedin=%s", comp.Name, w, l)
	return w, l, err
}

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

func (d *URLDiscoverer) tryGeminiSearch(ctx context.Context, comp db.Company) (string, string, error) {
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
		return "", "", err
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
