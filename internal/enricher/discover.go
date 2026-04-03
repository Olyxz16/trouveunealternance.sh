package enricher

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/errors"
	"jobhunter/internal/llm"
	"jobhunter/internal/pipeline"
	"jobhunter/internal/scraper"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

var (
	linkedinCompanyRe = regexp.MustCompile(
		`https?://(?:www\.)?linkedin\.com/company/([a-zA-Z0-9\-_%]+)`)
	nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)
)

type URLDiscoverer struct {
	fetcher          *scraper.CascadeFetcher
	geminiAPI        *llm.GeminiAPIProvider // nil if not configured
	classifier       *Classifier
	reporter         pipeline.Reporter
	logger           *zap.Logger
	skipDDG          bool
	useBrowserSearch bool
}

func NewURLDiscoverer(fetcher *scraper.CascadeFetcher, geminiAPI *llm.GeminiAPIProvider, classifier *Classifier) *URLDiscoverer {
	return &URLDiscoverer{
		fetcher:          fetcher,
		geminiAPI:        geminiAPI,
		classifier:       classifier,
		reporter:         pipeline.NilReporter{},
		logger:           zap.NewNop(),
		skipDDG:          false,
		useBrowserSearch: false,
	}
}

func (d *URLDiscoverer) SetSkipDDG(skip bool) {
	d.skipDDG = skip
}

func (d *URLDiscoverer) SetUseBrowserSearch(use bool) {
	d.useBrowserSearch = use
}

func (d *URLDiscoverer) SetLogger(l *zap.Logger) {
	if l != nil {
		d.logger = l
	}
}

func (d *URLDiscoverer) SetReporter(r pipeline.Reporter) {
	if r == nil {
		d.reporter = pipeline.NilReporter{}
	} else {
		d.reporter = r
	}
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
// Tries LLM knowledge first, then Gemini search grounding, then browser-based search.
func (d *URLDiscoverer) DiscoverURLs(ctx context.Context, comp db.Company) (string, string, error) {
	// 1. Try LLM direct knowledge (no search needed for known companies)
	website, linkedin, err := d.discoverWithLLM(ctx, comp)
	if err == nil && (website != "" || linkedin != "") {
		d.logger.Debug("LLM discovery success",
			zap.String("company", comp.Name),
			zap.String("website", website),
			zap.String("linkedin", linkedin))
		return website, linkedin, nil
	}

	// 2. Try Gemini search grounding if available
	if d.geminiAPI != nil {
		website, linkedin, err := d.discoverWithGemini(ctx, comp)
		if err == nil && (website != "" || linkedin != "") {
			d.logger.Debug("Gemini discovery success",
				zap.String("company", comp.Name),
				zap.String("website", website),
				zap.String("linkedin", linkedin))
			return website, linkedin, nil
		}

		msg := "Gemini search grounding failed"
		if err != nil {
			msg = fmt.Sprintf("Gemini search grounding failed: %v", err)
		}
		d.logger.Warn("Gemini discovery failed", zap.String("company", comp.Name), zap.Error(err))
		d.reporter.Log(pipeline.LogMsg{Level: "WARN", Text: fmt.Sprintf("[%s] %s.", comp.Name, msg)})
	}

	// 3. Browser-based search (DuckDuckGo via browser, no rate limits)
	if d.useBrowserSearch {
		d.reporter.Update(pipeline.ProgressUpdate{
			ID:     int(comp.ID),
			Name:   comp.Name,
			Step:   "URL Discovery (Browser Search)",
			Status: pipeline.StatusRunning,
		})
		w, l, err := d.discoverWithBrowserSearch(ctx, comp)
		if err == nil && (w != "" || l != "") {
			d.logger.Debug("Browser search discovery success",
				zap.String("company", comp.Name),
				zap.String("website", w),
				zap.String("linkedin", l))
			return w, l, nil
		}
		d.logger.Debug("Browser search discovery failed or empty",
			zap.String("company", comp.Name),
			zap.Error(err))
	}

	d.logger.Debug("All discovery methods exhausted", zap.String("company", comp.Name))
	return "", "", fmt.Errorf("discovery exhausted for company %s", comp.Name)
}

func (d *URLDiscoverer) discoverWithLLM(ctx context.Context, comp db.Company) (string, string, error) {
	prompt := fmt.Sprintf(
		"Company: %s\nSIREN: %s\nCity: %s\nNAF: %s\n\nWhat is their official website and LinkedIn company page?",
		comp.Name,
		comp.Siren,
		comp.City,
		comp.NAFCode,
	)

	var result struct {
		Website     string `json:"website"`
		LinkedinURL string `json:"linkedin_url"`
	}
	req := llm.CompletionRequest{
		System: discoverySystemPrompt,
		User:   prompt,
	}

	err := d.classifier.llm.CompleteJSON(ctx, req, "discovery_llm", "", &result)
	if err != nil {
		return "", "", err
	}

	return result.Website, result.LinkedinURL, nil
}

func (d *URLDiscoverer) discoverWithGemini(ctx context.Context, comp db.Company) (string, string, error) {
	var lastErr error
	backoff := 5 * time.Second
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			d.logger.Debug("Retrying Gemini discovery",
				zap.String("company", comp.Name),
				zap.Int("attempt", attempt+1),
				zap.Duration("backoff", backoff))
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}

		website, linkedin, err := d.tryGeminiSearch(ctx, comp)
		if err == nil && (website != "" || linkedin != "") {
			return website, linkedin, nil
		}

		if err != nil {
			lastErr = err
			// Check if it's a rate limit error or model error that warrants a retry
			shouldRetry := false
			if _, ok := err.(*errors.RateLimitError); ok {
				shouldRetry = true
			} else if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate") {
				shouldRetry = true
			} else if strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "503") {
				shouldRetry = true
			}

			if shouldRetry {
				continue
			}
			// For other errors (e.g. 404, 400), don't retry
			return "", "", err
		}

		// If err == nil but no results, we stop (don't retry a successful empty response)
		return "", "", nil
	}
	return "", "", lastErr
}

func (d *URLDiscoverer) tryGeminiSearch(ctx context.Context, comp db.Company) (string, string, error) {
	prompt := fmt.Sprintf(
		"Company: %s\nSIREN: %s\nCity: %s\nNAF: %s\n\nFind their official website and LinkedIn company page.",
		comp.Name,
		comp.Siren,
		comp.City,
		comp.NAFCode,
	)

	resp, err := d.geminiAPI.CompleteWithSearch(ctx, llm.CompletionRequest{
		System: discoverySystemPrompt,
		User:   prompt,
	})
	if err == nil && d.classifier != nil {
		// Log usage if we have access to the DB via classifier
		d.logGeminiUsage(resp, "discovery_gemini")
	}
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

// discoverWithBrowserSearch uses the browser to search DuckDuckGo for company URLs.
// This is the primary search method when Gemini is unavailable — no rate limits.
func (d *URLDiscoverer) discoverWithBrowserSearch(ctx context.Context, comp db.Company) (string, string, error) {
	var website, linkedin string

	// Search 1: LinkedIn company page
	query := fmt.Sprintf("%s %s linkedin company", comp.Name, comp.City)
	searchURL := fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	d.logger.Info("Browser search for LinkedIn", zap.String("company", comp.Name), zap.String("query", query))
	res, err := d.fetcher.ScrollAndFetch(ctx, searchURL, 1)
	if err == nil {
		w, l, err := d.classifier.ExtractURLsFromSearch(ctx, res.ContentMD, comp, "discovery_browser_linkedin")
		if err == nil {
			if l != "" {
				linkedin = l
			}
			if w != "" {
				website = w
			}
		}
	}

	// Search 2: Official website (if we don't have one yet)
	if website == "" {
		query = fmt.Sprintf("%s %s official website", comp.Name, comp.City)
		searchURL = fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))

		d.logger.Info("Browser search for website", zap.String("company", comp.Name), zap.String("query", query))
		res, err := d.fetcher.ScrollAndFetch(ctx, searchURL, 1)
		if err == nil {
			w, _, err := d.classifier.ExtractURLsFromSearch(ctx, res.ContentMD, comp, "discovery_browser_website")
			if err == nil && w != "" {
				website = w
			}
		}
	}

	// Search 3: General company search (if still missing URLs)
	if website == "" || linkedin == "" {
		query = fmt.Sprintf("%s %s", comp.Name, comp.City)
		searchURL = fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))

		d.logger.Info("Browser search general", zap.String("company", comp.Name), zap.String("query", query))
		res, err := d.fetcher.ScrollAndFetch(ctx, searchURL, 1)
		if err == nil {
			w, l, err := d.classifier.ExtractURLsFromSearch(ctx, res.ContentMD, comp, "discovery_browser_general")
			if err == nil {
				if website == "" && w != "" {
					website = w
				}
				if linkedin == "" && l != "" {
					linkedin = l
				}
			}
		}
	}

	// Last resort: guess LinkedIn slug
	if linkedin == "" {
		linkedin = guessLinkedInSlug(comp.Name)
	}

	if website == "" && linkedin == "" {
		return "", "", fmt.Errorf("browser search found no URLs for %s", comp.Name)
	}

	return website, linkedin, nil
}

// DEPRECATED: Use discoverWithBrowserSearch instead.
// Kept for backward compatibility but no longer called in the main flow.
func (d *URLDiscoverer) discoverWithDDG(ctx context.Context, comp db.Company) (string, string, error) {
	query := fmt.Sprintf("%s %s linkedin company", comp.Name, comp.City)
	searchURL := fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	d.logger.Info("DDG search", zap.String("company", comp.Name), zap.String("url", searchURL))
	res, err := d.fetcher.ScrollAndFetch(ctx, searchURL, 1)
	if err != nil {
		d.logger.Warn("DDG search failed", zap.String("company", comp.Name), zap.Error(err))
		return "", "", fmt.Errorf("DDG search failed: %w", err)
	}

	d.logger.Debug("DDG search result", zap.String("company", comp.Name), zap.Int("content_len", len(res.ContentMD)))

	website, linkedin, err := d.classifier.ExtractURLsFromSearch(ctx, res.ContentMD, comp, "discovery_ddg")
	d.logger.Debug("DDG extraction result",
		zap.String("company", comp.Name),
		zap.String("website", website),
		zap.String("linkedin", linkedin),
		zap.Error(err))
	if err == nil && (website != "" || linkedin != "") {
		return website, linkedin, nil
	}

	// Try more general query if first one failed
	query = fmt.Sprintf("%s linkedin company", comp.Name)
	searchURL = fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))
	d.logger.Info("DDG retry search", zap.String("company", comp.Name), zap.String("url", searchURL))
	res, err = d.fetcher.ScrollAndFetch(ctx, searchURL, 1)
	if err == nil {
		d.logger.Debug("DDG retry result", zap.String("company", comp.Name), zap.Int("content_len", len(res.ContentMD)))
		w, l, err := d.classifier.ExtractURLsFromSearch(ctx, res.ContentMD, comp, "discovery_ddg_retry")
		d.logger.Debug("DDG retry extraction",
			zap.String("company", comp.Name),
			zap.String("website", w),
			zap.String("linkedin", l),
			zap.Error(err))
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
	d.logger.Debug("Searching for people on LinkedIn", zap.String("company", comp.Name))

	// 1. Try LLM direct knowledge first
	people, err := d.discoverPeopleWithLLM(ctx, comp, titles)
	if err == nil && len(people) > 0 {
		d.logger.Debug("LLM people discovery success", zap.String("company", comp.Name), zap.Int("count", len(people)))
		return people, nil
	}

	// 2. Try Gemini Search Grounding if available
	if d.geminiAPI != nil {
		people, err := d.discoverPeopleWithGemini(ctx, comp, titles)
		if err == nil && len(people) > 0 {
			d.logger.Debug("Gemini people discovery success", zap.String("company", comp.Name), zap.Int("count", len(people)))
			return people, nil
		}
		d.logger.Debug("Gemini people discovery failed or empty", zap.String("company", comp.Name), zap.Error(err))
	}

	// 3. Browser-based people search (DuckDuckGo via browser, no rate limits)
	if d.useBrowserSearch {
		people, err := d.discoverPeopleWithBrowser(ctx, comp, titles)
		if err == nil && len(people) > 0 {
			d.logger.Debug("Browser people discovery success", zap.String("company", comp.Name), zap.Int("count", len(people)))
			return people, nil
		}
		d.logger.Debug("Browser people discovery failed or empty", zap.String("company", comp.Name), zap.Error(err))
	}

	// DEPRECATED: DDG fallback kept for backward compatibility but no longer called
	// if !d.skipDDG {
	//     return d.discoverPeopleWithDDG(ctx, comp, titles)
	// }

	return nil, nil
}

// discoverPeopleWithBrowser searches via Google to find individual LinkedIn profiles.
// Uses 3 targeted queries for different role categories.
func (d *URLDiscoverer) discoverPeopleWithBrowser(ctx context.Context, comp db.Company, titles []string) ([]IndividualContact, error) {
	// Group titles into 3 search categories for broader coverage
	techTitles := []string{"CTO", "Directeur Technique", "Tech Lead", "DevOps", "Engineering Manager", "Responsable technique", "DSI"}
	hrTitles := []string{"RH", "Recrutement", "HR Manager", "Talent Acquisition", "Responsable RH", "Chargé de recrutement"}
	mgmtTitles := []string{"CEO", "Directeur Général", "Founder", "Président", "Gérant", "Fondateur"}

	queryGroups := [][]string{techTitles, hrTitles, mgmtTitles}

	var allContacts []IndividualContact
	seenURLs := make(map[string]bool)

	for i, group := range queryGroups {
		query := fmt.Sprintf("%s site:linkedin.com/in/ (%s)", comp.Name, strings.Join(group, " OR "))
		searchURL := fmt.Sprintf("https://www.google.com/search?q=%s", url.QueryEscape(query))

		d.logger.Info("Google people search", zap.String("company", comp.Name), zap.Int("query", i+1), zap.String("query_text", query))

		searchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		res, err := d.fetcher.ScrollAndFetch(searchCtx, searchURL, 0)
		cancel()

		if err != nil {
			d.logger.Debug("Google people search failed", zap.String("company", comp.Name), zap.Int("query", i+1), zap.Error(err))
			continue
		}

		people := extractPeopleFromSearchResults(res.ContentMD, comp.Name)
		d.logger.Info("Google people search extracted",
			zap.String("company", comp.Name),
			zap.Int("query", i+1),
			zap.Int("contacts_found", len(people)),
			zap.Int("markdown_len", len(res.ContentMD)))

		for _, c := range people {
			if c.LinkedinURL != "" && !seenURLs[c.LinkedinURL] {
				allContacts = append(allContacts, c)
				seenURLs[c.LinkedinURL] = true
			}
		}

		if len(allContacts) >= 5 {
			break
		}
	}

	return allContacts, nil
}

// extractPeopleFromSearchResults extracts LinkedIn profiles from search engine results (markdown format).
// Works with both Bing and Google markdown structures.
// Bing structure: [Name - Role](https://www.linkedin.com/in/slug)
// Google structure: ### Name - Role ...\n\nLinkedIn · Name
func extractPeopleFromSearchResults(markdown, companyName string) []IndividualContact {
	var contacts []IndividualContact
	seenNames := make(map[string]bool)

	// Try Bing-style extraction first: [Name - Role](https://www.linkedin.com/in/slug)
	bingLinkRe := regexp.MustCompile(`\[([^\]]+)\]\(https://(?:[a-z]{2}\.)?linkedin\.com/in/([^)]+)\)`)
	bingMatches := bingLinkRe.FindAllStringSubmatch(markdown, -1)

	for _, m := range bingMatches {
		linkText := m[1]
		slug := m[2]

		// Clean slug
		if idx := strings.Index(slug, "?"); idx != -1 {
			slug = slug[:idx]
		}
		if idx := strings.Index(slug, "#"); idx != -1 {
			slug = slug[:idx]
		}

		linkedinURL := "https://www.linkedin.com/in/" + slug

		// Verify company name appears near this link
		linkFullPattern := regexp.MustCompile(regexp.QuoteMeta(m[0]))
		loc := linkFullPattern.FindStringIndex(markdown)
		if loc == nil {
			continue
		}

		start := loc[0] - 200
		if start < 0 {
			start = 0
		}
		end := loc[1] + 500
		if end > len(markdown) {
			end = len(markdown)
		}
		context := markdown[start:end]

		keyWords := extractCompanyKeyWords(strings.ToLower(companyName))
		matchCount := 0
		for _, word := range keyWords {
			if strings.Contains(strings.ToLower(context), word) {
				matchCount++
			}
		}

		if matchCount < 2 && !strings.Contains(strings.ToLower(context), strings.ToLower(companyName)) {
			continue
		}

		linkText = strings.TrimSpace(linkText)
		if linkText == "" {
			continue
		}

		name := linkText
		role := ""

		for _, sep := range []string{" - ", " · ", " | "} {
			if idx := strings.Index(linkText, sep); idx > 0 {
				name = strings.TrimSpace(linkText[:idx])
				role = strings.TrimSpace(linkText[idx+len(sep):])
				break
			}
		}

		name = strings.TrimRight(name, ". ")
		role = strings.TrimRight(role, ". ")

		nameParts := strings.Fields(name)
		if len(nameParts) < 2 {
			continue
		}

		nameKey := strings.ToLower(name)
		if seenNames[nameKey] {
			continue
		}
		seenNames[nameKey] = true

		contacts = append(contacts, IndividualContact{
			Name:        name,
			Role:        role,
			LinkedinURL: linkedinURL,
		})

		if len(contacts) >= 5 {
			break
		}
	}

	if len(contacts) > 0 {
		return contacts
	}

	// Fall back to Google-style extraction: ### Name - Role ...\n\nLinkedIn · Name
	headingRe := regexp.MustCompile(`^### (.+)$`)
	lines := strings.Split(markdown, "\n")

	for i, line := range lines {
		matches := headingRe.FindStringSubmatch(strings.TrimSpace(line))
		if len(matches) < 2 {
			continue
		}

		headingText := matches[1]
		headingText = strings.TrimRight(headingText, ". ")

		// Check for LinkedIn nearby
		isLinkedIn := false
		for j := i + 1; j < len(lines) && j < i+6; j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "LinkedIn") {
				isLinkedIn = true
				break
			}
		}
		if !isLinkedIn {
			continue
		}

		// Check company name in context
		contextStart := i
		contextEnd := i + 10
		if contextEnd > len(lines) {
			contextEnd = len(lines)
		}
		context := strings.Join(lines[contextStart:contextEnd], " ")
		contextLower := strings.ToLower(context)
		companyLower := strings.ToLower(companyName)

		keyWords := extractCompanyKeyWords(companyLower)
		matchCount := 0
		for _, word := range keyWords {
			if strings.Contains(contextLower, word) {
				matchCount++
			}
		}

		if matchCount < 2 && !strings.Contains(contextLower, companyLower) {
			continue
		}

		// Parse name and role
		name := headingText
		role := ""

		for _, sep := range []string{" - ", " · ", " | "} {
			if idx := strings.Index(headingText, sep); idx > 0 {
				name = strings.TrimSpace(headingText[:idx])
				role = strings.TrimSpace(headingText[idx+len(sep):])
				break
			}
		}

		name = strings.TrimRight(name, ". ")
		role = strings.TrimRight(role, ". ")

		nameParts := strings.Fields(name)
		if len(nameParts) < 2 {
			continue
		}

		nameKey := strings.ToLower(name)
		if seenNames[nameKey] {
			continue
		}
		seenNames[nameKey] = true

		linkedinURL := constructLinkedInURL(name)

		contacts = append(contacts, IndividualContact{
			Name:        name,
			Role:        role,
			LinkedinURL: linkedinURL,
		})

		if len(contacts) >= 5 {
			break
		}
	}

	return contacts
}

// extractCompanyKeyWords extracts key words from a company name for flexible matching.
// Removes common suffixes and short words.
func extractCompanyKeyWords(companyName string) []string {
	// Remove common suffixes that don't help with matching
	suffixes := []string{
		" france", " s.a.", " s.a.s", " s.a.r.l", " sarl", " sas", " sa",
		" ltd", " inc", " corp", " llc", " gmbh", " ag",
	}
	cleaned := companyName
	for _, suffix := range suffixes {
		cleaned = strings.ReplaceAll(cleaned, suffix, "")
	}

	words := strings.Fields(cleaned)
	var keyWords []string
	for _, w := range words {
		if len(w) >= 3 {
			keyWords = append(keyWords, w)
		}
	}
	return keyWords
}

// constructLinkedInURL constructs a best-effort LinkedIn URL from a person's name.
func constructLinkedInURL(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	// Remove special characters, keep letters and numbers
	slug = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return ""
	}
	return "https://www.linkedin.com/in/" + slug
}

// DEPRECATED: Use discoverPeopleWithBrowser instead.
// Kept for backward compatibility but no longer called in the main flow.
func (d *URLDiscoverer) discoverPeopleWithDDG(ctx context.Context, comp db.Company, titles []string) ([]IndividualContact, error) {
	var allContacts []IndividualContact
	if d.skipDDG {
		return allContacts, nil
	}
	queries := []string{
		fmt.Sprintf("site:linkedin.com/in/ %s Poitiers (%s)", comp.Name, strings.Join(titles, " OR ")),
		fmt.Sprintf("site:linkedin.com/in/ %s (%s)", comp.Name, strings.Join(titles, " OR ")),
	}

	seenURLs := make(map[string]bool)

	for _, query := range queries {
		searchURL := fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))
		searchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		res, err := d.fetcher.ScrollAndFetch(searchCtx, searchURL, 0)
		cancel()

		if err != nil {
			continue
		}

		people, err := d.classifier.ExtractPeopleFromSearchResults(ctx, res.ContentMD, comp, "linkedin_search_people_deprecated")
		if err == nil {
			for _, c := range people.Contacts {
				if c.LinkedinURL != "" && !seenURLs[c.LinkedinURL] {
					allContacts = append(allContacts, c)
					seenURLs[c.LinkedinURL] = true
				}
			}
		}
	}

	return allContacts, nil
}

func (d *URLDiscoverer) discoverPeopleWithLLM(ctx context.Context, comp db.Company, titles []string) ([]IndividualContact, error) {
	prompt := fmt.Sprintf(
		"Find real, publicly known contacts at '%s' in '%s', France.\n\n"+
			"Target roles: %s\n\n"+
			"Return their full name, job title, and LinkedIn profile URL (https://www.linkedin.com/in/...)."+
			"If you find a personal work email, include it as well.\n\n"+
			"CRITICAL: DO NOT invent names. Only return people who actually exist and work at this company. "+
			"If you don't know any real people, return an empty contacts list.\n\n"+
			"Return ONLY a JSON object: \n"+
			"{\"contacts\": [{\"name\": \"...\", \"role\": \"...\", \"linkedin_url\": \"...\", \"email\": \"...\"}]}",
		comp.Name,
		comp.City,
		strings.Join(titles, ", "),
	)

	var result struct {
		Contacts []IndividualContact `json:"contacts"`
	}
	req := llm.CompletionRequest{
		System: "You are a helpful recruitment research assistant. You provide data in JSON format.",
		User:   prompt,
	}

	err := d.classifier.llm.CompleteJSON(ctx, req, "people_discovery_llm", "", &result)
	if err != nil {
		return nil, err
	}

	return result.Contacts, nil
}

func (d *URLDiscoverer) discoverPeopleWithGemini(ctx context.Context, comp db.Company, titles []string) ([]IndividualContact, error) {
	city := comp.City
	if city == "" {
		return nil, fmt.Errorf("missing city for company %s", comp.Name)
	}

	prompt := fmt.Sprintf(
		"I need to find recruitment contacts, technical managers, or founders at the company '%s' near '%s', France.\n\n"+
			"Search for real people and return their details. \n"+
			"Include their full name, job title, and absolute LinkedIn profile URL (https://www.linkedin.com/in/...)."+
			"If you find a personal work email, include it as well.\n\n"+
			"CRITICAL: DO NOT invent or halluc names. Only return people you can actually find through search. "+
			"If you cannot find real people, return an empty contacts list.\n\n"+
			"Return ONLY a JSON object: \n"+
			"{\"contacts\": [{\"name\": \"...\", \"role\": \"...\", \"linkedin_url\": \"...\", \"email\": \"...\"}]}",
		comp.Name,
		city,
	)

	resp, err := d.geminiAPI.CompleteWithSearch(ctx, llm.CompletionRequest{
		System: "You are a helpful recruitment research assistant. You provide data in JSON format.",
		User:   prompt,
	})
	if err == nil && d.classifier != nil {
		d.logGeminiUsage(resp, "people_discovery_gemini")
	}
	if err != nil {
		return nil, err
	}

	d.logger.Debug("Gemini People Discovery raw response", zap.String("company", comp.Name), zap.String("content", resp.Content))

	clean := extractJSONFromText(resp.Content)
	if clean == "" {
		d.logger.Debug("No JSON found in gemini response", zap.String("company", comp.Name), zap.String("raw_content", resp.Content))

		// Attempt fallback conversion
		fixPrompt := fmt.Sprintf("Extract the people information from the following text into a JSON object with a 'contacts' field. Each contact MUST have name, role, and linkedin_url. If a linkedin_url is missing for a person, DO NOT include that person. \n\nText:\n%s", resp.Content)
		var fixResult struct {
			Contacts []IndividualContact `json:"contacts"`
		}
		err = d.classifier.llm.CompleteJSON(ctx, llm.CompletionRequest{
			System: "Output ONLY JSON.",
			User:   fixPrompt,
		}, "fix_people_json", "discovery_fix", &fixResult)

		if err == nil {
			return fixResult.Contacts, nil
		}
		return nil, fmt.Errorf("no JSON found and fallback failed: %w", err)
	}

	var result struct {
		Contacts []IndividualContact `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(clean), &result); err != nil {
		return nil, fmt.Errorf("failed to parse discovery JSON: %w", err)
	}

	return result.Contacts, nil
}

func (d *URLDiscoverer) logGeminiUsage(resp llm.CompletionResponse, task string) {
	if d.classifier == nil || d.classifier.GetDB() == nil {
		return
	}
	usage := &db.TokenUsage{
		Task:             task,
		Model:            d.geminiAPI.Model,
		Provider:         "gemini_api",
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
		CostUSD:          0,
		IsEstimated:      false,
	}
	_ = d.classifier.GetDB().InsertTokenUsage(usage)
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
