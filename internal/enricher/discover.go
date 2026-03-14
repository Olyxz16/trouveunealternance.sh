package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/scraper"
	"net/url"
	"strings"
)

type URLDiscoverer struct {
	fetcher *scraper.CascadeFetcher
}

func NewURLDiscoverer(fetcher *scraper.CascadeFetcher) *URLDiscoverer {
	return &URLDiscoverer{fetcher: fetcher}
}

func (d *URLDiscoverer) DiscoverURLs(ctx context.Context, comp db.Company) (string, string, error) {
	// 1. DuckDuckGo search for LinkedIn
	// Use Name + City + "linkedin" for better precision
	query := fmt.Sprintf("%s %s linkedin company", comp.Name, comp.City.String)
	searchURL := fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))
	
	res, err := d.fetcher.Fetch(ctx, searchURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to search for LinkedIn: %w", err)
	}

	// Very simple heuristic: find the first linkedin.com/company link
	linkedinURL := d.extractLinkedInURL(res.ContentMD)

	// If not found, try a broader search or name only as fallback
	if linkedinURL == "" {
		query = fmt.Sprintf("%s linkedin company", comp.Name)
		searchURL = fmt.Sprintf("https://duckduckgo.com/html/?q=%s", url.QueryEscape(query))
		res, err = d.fetcher.Fetch(ctx, searchURL)
		if err == nil {
			linkedinURL = d.extractLinkedInURL(res.ContentMD)
		}
	}

	return "", linkedinURL, nil
}

func (d *URLDiscoverer) extractLinkedInURL(markdown string) string {
	if strings.Contains(markdown, "linkedin.com/company/") {
		parts := strings.Split(markdown, "linkedin.com/company/")
		if len(parts) > 1 {
			slug := strings.Split(parts[1], ")")[0]
			slug = strings.Split(slug, " ")[0]
			slug = strings.Split(slug, "/")[0] // Handle trailing slashes
			slug = strings.Split(slug, "\"")[0]
			slug = strings.Split(slug, "?")[0] // Strip query params
			return "https://www.linkedin.com/company/" + strings.TrimSpace(slug)
		}
	}
	return ""
}
