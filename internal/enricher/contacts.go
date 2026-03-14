package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"strings"
)

type ContactCandidate struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	LinkedinURL string `json:"linkedin_url"`
	Email       string `json:"email"`
}

type ContactResult struct {
	Contacts []ContactCandidate `json:"contacts"`
	Best     *ContactCandidate  `json:"best"`
}

const ContactSelectionPrompt = `You are a technical recruiter. 
Given a list of employees found for a company, pick the BEST contact to reach out to for an internship in DevOps/Backend.

Company Type: %s
Potential Roles: CTO, Engineering Manager, Tech Lead, Technical Recruiter, HR Manager.

Return ONLY a JSON object with:
- contacts: the full list of candidates
- best: the single best candidate object, or null if none are suitable.
`

func (c *Classifier) DiscoverLinkedInContacts(ctx context.Context, linkedinURL string, companyType string, runID string) (ContactResult, error) {
	// MCP navigation to People tab
	peopleURL := linkedinURL
	if !strings.HasSuffix(peopleURL, "/") {
		peopleURL += "/"
	}
	peopleURL += "people/"

	// Search for key roles on the page if MCP supports it, otherwise fetch and extract
	res, err := c.DiscoverURL(ctx, peopleURL) // We'll use a fetcher internally
	if err != nil {
		return ContactResult{}, err
	}

	var result ContactResult
	req := llm.CompletionRequest{
		System: fmt.Sprintf(ContactSelectionPrompt, companyType),
		User:   fmt.Sprintf("Markdown from LinkedIn People tab:\n\n%s", res.ContentMD),
	}

	err = c.llm.CompleteJSON(ctx, req, "select_best_contact", runID, &result)
	return result, err
}

func (c *Classifier) DiscoverURL(ctx context.Context, url string) (scraper.FetchResult, error) {
	// This will be wired up in the main Enricher glue (enrich.go)
	return scraper.FetchResult{}, fmt.Errorf("not implemented - wire through Enricher")
}
