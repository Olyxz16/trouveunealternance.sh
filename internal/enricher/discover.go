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
	fetcher    *scraper.CascadeFetcher
	geminiAPI  *llm.GeminiAPIProvider // nil if not configured — falls back to DDG
	classifier *Classifier
}

func NewURLDiscoverer(fetcher *scraper.CascadeFetcher, geminiAPI *llm.GeminiAPIProvider, classifier *Classifier) *URLDiscoverer {
	return &URLDiscoverer{fetcher: fetcher, geminiAPI: geminiAPI, classifier: classifier}
}

const discoverySystemPrompt = `You are finding the online presence of French companies.
Given a company name, SIREN, city and NAF code, find:
1. Their official website — the primary domain they own and operate
2. Their LinkedIn company page URL

RULES:
- website must be the company's own domain — NEVER return directory sites (societe.com, pappers.fr, etc.)
- linkedin_url must be a linkedin.com/company/ URL
- Provide your BEST GUESS if you are not 100% sure, but mark it as empty if you have no idea.
- If it's a public institution, look for their official .fr or .gouv.fr domain.

Return ONLY a JSON object:
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

	// Use ScrollAndFetch to ensure browser is used and page is loaded
	res, err := d.fetcher.ScrollAndFetch(ctx, searchURL, 1)
	if err != nil {
		return "", "", fmt.Errorf("DDG search failed: %w", err)
	}

	website, linkedin, err := d.classifier.ExtractURLsFromSearch(ctx, res.ContentMD, "discovery_ddg")
	if err == nil && (website != "" || linkedin != "") {
		return website, linkedin, nil
	}

	// Try more general query if first one failed
	query = fmt.Sprintf("%s linkedin company", comp.Name)
	searchURL = fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))
	res, err = d.fetcher.ScrollAndFetch(ctx, searchURL, 1)
	if err == nil {
		w, l, err := d.classifier.ExtractURLsFromSearch(ctx, res.ContentMD, "discovery_ddg_retry")
		if err == nil {
			if website == "" {
				website = w
			}
			if linkedin == "" {
				linkedin = l
			}
		}
	}

	// Last resort: guess slug from company name if linkedin is still missing
	if linkedin == "" {
		linkedin = guessLinkedInSlug(comp.Name)
	}

	return website, linkedin, nil
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

func (d *URLDiscoverer) SearchPeopleOnLinkedIn(ctx context.Context, comp db.Company, titles []string) ([]IndividualContact, error) {
	// 1. Try Gemini Search Grounding first if available
	if d.geminiAPI != nil {
		people, err := d.discoverPeopleWithGemini(ctx, comp, titles)
		if err == nil && len(people) > 0 {
			log.Printf("DEBUG [%s]: Gemini people discovery success: found %d contacts", comp.Name, len(people))
			return people, nil
		}
		log.Printf("DEBUG [%s]: Gemini people discovery failed or empty (err: %v). Falling back to DDG...", comp.Name, err)
	}

	// 2. Fallback to DDG search
	// titles like "CTO", "HR", "DevOps"
	query := fmt.Sprintf("site:linkedin.com/in/ %s %s (%s)", comp.Name, comp.City.String, strings.Join(titles, " OR "))
	searchURL := fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	res, err := d.fetcher.Fetch(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	// Use the classifier to extract individual profiles from search result markdown
	people, err := d.classifier.ExtractPeopleFromSearchResults(ctx, res.ContentMD, "linkedin_search_people")
	if err != nil {
		return nil, err
	}

	return people.Contacts, nil
}

func (d *URLDiscoverer) discoverPeopleWithGemini(ctx context.Context, comp db.Company, titles []string) ([]IndividualContact, error) {
	prompt := fmt.Sprintf(
		"Find up to 5 real recruitment contacts (CTO, HR, Manager) at %s in %s. \n"+
			"For each person, provide their name, current role, and LinkedIn profile URL. \n\n"+
			"CRITICAL: You MUST return the data as a JSON object with a 'contacts' field. \n"+
			"JSON Format Example: {\"contacts\": [{\"name\": \"...\", \"role\": \"...\", \"linkedin_url\": \"...\"}]}\n\n"+
			"Do NOT include any text before or after the JSON.",
		comp.Name,
		comp.City.String,
	)

	resp, err := d.geminiAPI.CompleteWithSearch(ctx, llm.CompletionRequest{
		System: "You are a professional recruitment assistant. You only output valid JSON.",
		User:   prompt,
	})
	if err != nil {
		return nil, err
	}

	clean := extractJSONFromText(resp.Content)
	if clean == "" {
		log.Printf("DEBUG [%s]: No JSON found in gemini people response. Attempting to convert text to JSON...", comp.Name)
		// Fallback: ask LLM to convert the text response to JSON
		fixPrompt := fmt.Sprintf("Convert the following text into a JSON object with a 'contacts' field containing people's name, role, and linkedin_url. If no people are found, return {\"contacts\": []}. \n\nText:\n%s", resp.Content)
		
		var fixResult struct {
			Contacts []IndividualContact `json:"contacts"`
		}
		err = d.classifier.llm.CompleteJSON(ctx, llm.CompletionRequest{
			System: "You are a data converter. Output ONLY valid JSON.",
			User:   fixPrompt,
		}, "fix_people_json", "discovery_fix", &fixResult)
		
		if err == nil {
			return fixResult.Contacts, nil
		}
		
		return nil, fmt.Errorf("failed to fix people JSON: %w", err)
	}

	var result struct {
		Contacts []IndividualContact `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(clean), &result); err != nil {
		return nil, fmt.Errorf("failed to parse discovery JSON: %w", err)
	}

	return result.Contacts, nil
}

// extractJSONFromText finds the first {...} block in a text response.
// Used when JSON mode cannot be enabled (e.g. search grounding active).
func extractJSONFromText(content string) string {
	// Try to find markdown code block first
	if strings.Contains(content, "```json") {
		parts := strings.Split(content, "```json")
		if len(parts) > 1 {
			inner := strings.Split(parts[1], "```")[0]
			return strings.TrimSpace(inner)
		}
	}

	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	return strings.TrimSpace(content[start : end+1])
}
