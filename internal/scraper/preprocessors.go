package scraper

import (
	"strings"
)

func preprocess(html, url string) string {
	switch {
	case strings.Contains(url, "linkedin.com"):
		return preprocessLinkedIn(html)
	default:
		return html
	}
}

func preprocessLinkedIn(html string) string {
	// Simple cleanup: remove some obviously noisy parts if they are in the HTML
	// In a real browser-rendered HTML from LinkedIn, these might be present.
	
	// We could use a proper HTML parser here, but for now we'll do some basic
	// string-based stripping of known noisy tags if they are very large.
	
	// Actually, trafilatura/readability are better at this.
	// LinkedIn-specific: often has large JSON-LD or script tags with tracking.
	
	return html
}
