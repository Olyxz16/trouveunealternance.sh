package scraper

import (
	"regexp"
	"strings"
)

// LinkedInPerson represents a person extracted from a LinkedIn page.
type LinkedInPerson struct {
	Name        string
	Role        string
	LinkedinURL string
}

// ExtractPeopleFromLinkedInHTML extracts people data from LinkedIn HTML content.
// It looks for patterns like profile links with names and titles.
func ExtractPeopleFromLinkedInHTML(html string) []LinkedInPerson {
	var people []LinkedInPerson
	seen := make(map[string]bool)

	// Pattern 1: [Name](linkedin.com/in/...) followed by role text nearby
	// Matches: [Guillaume Texier](https://www.linkedin.com/in/guillaume-texier-1205031b...)
	nameURLRe := regexp.MustCompile(`\[([A-ZÀ-Ž][a-zà-ž]+(?:\s+[A-ZÀ-Ž][a-zà-ž'-]+)+)\]\(https://www\.linkedin\.com/in/([a-zA-Z0-9\-_%]+)`)

	matches := nameURLRe.FindAllStringSubmatch(html, -1)
	for _, m := range matches {
		name := m[1]
		slug := m[2]
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true

		// Find role/title near this match in the HTML
		role := extractRoleNearMatch(html, m[0])

		people = append(people, LinkedInPerson{
			Name:        name,
			Role:        role,
			LinkedinURL: "https://www.linkedin.com/in/" + slug,
		})
	}

	// Pattern 2: "Relation de Xe niveau et plus" + Title text (no name, just title)
	// This is for the "Les connaissez-vous?" section
	// We skip this pattern since we don't have names

	return people
}

// extractRoleNearMatch looks for a job title near the matched name in the HTML.
func extractRoleNearMatch(html, match string) string {
	idx := strings.Index(html, match)
	if idx == -1 {
		return ""
	}

	// Look at the next 500 characters after the match
	end := idx + len(match) + 500
	if end > len(html) {
		end = len(html)
	}
	after := html[idx+len(match) : end]

	// Clean up URL artifacts and relation markers
	cleaned := after
	// Remove URL query params and closing parens
	cleaned = regexp.MustCompile(`\?[^\n)]*\)?`).ReplaceAllString(cleaned, "")
	// Remove relation level text (various forms)
	cleaned = regexp.MustCompile(`Relation de [0-9]+e niveau(?: et plus)?`).ReplaceAllString(cleaned, "")
	// Remove standalone "3e", "2e", "1er" level fragments (leftover from relation text)
	cleaned = regexp.MustCompile(`(?:^|[\s·])[0-9]+e(?:\s|$)`).ReplaceAllString(cleaned, " ")
	cleaned = strings.ReplaceAll(cleaned, "Utilisateur LinkedIn", "")
	cleaned = strings.ReplaceAll(cleaned, "Anciens élèves", "")
	// Clean whitespace and non-breaking spaces
	cleaned = strings.ReplaceAll(cleaned, "\u00a0", " ")
	cleaned = strings.ReplaceAll(cleaned, "&nbsp;", " ")
	cleaned = strings.TrimSpace(cleaned)
	// Remove leading separators and leftover fragments
	cleaned = strings.TrimLeft(cleaned, "·- \n\r\t")
	cleaned = strings.TrimSpace(cleaned)

	// Now look for title patterns in the cleaned text
	// Pattern: "Title - Company" or "Title at Company" or "Title chez Company"
	titlePatterns := []string{" - ", " at ", " chez "}
	for _, pattern := range titlePatterns {
		if pIdx := strings.Index(cleaned, pattern); pIdx != -1 {
			title := strings.TrimSpace(cleaned[:pIdx])
			if len(title) > 2 && len(title) < 100 {
				return title
			}
		}
	}

	// If no pattern found, return first line if it looks like a title
	lines := strings.SplitN(cleaned, "\n", 2)
	if len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		if len(first) > 2 && len(first) < 100 {
			return first
		}
	}

	return ""
}
