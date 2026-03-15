package enricher

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"strings"
)

// PeoplePageData — extracted from LinkedIn People tab.
// Individual people ONLY.
type PeoplePageData struct {
	Contacts []IndividualContact `json:"contacts"`
}

type IndividualContact struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	LinkedinURL string `json:"linkedin_url"` // MUST be /in/ personal profile, never /company/
	Email       string `json:"email"`        // personal work email if publicly visible; empty if not
}

// IndividualProfileData — extracted from a personal LinkedIn /in/ profile.
// Used to enrich a contact found on the People tab.
type IndividualProfileData struct {
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Email       string   `json:"email"`        // if visible on profile
	RecentPosts []string `json:"recent_posts"` // topics/themes of recent activity
	Background  string   `json:"background"`   // 1-2 sentence summary of their profile
	TechStack   []string `json:"tech_stack"`   // technologies mentioned on their profile
}

const PeopleExtractionPrompt = `You are extracting a list of individual employees from a LinkedIn People tab or employee listing.

Return ALL relevant contacts found (up to 5 people). Do not return just one.

STRICT RULES:
- linkedin_url MUST be a personal LinkedIn profile URL starting with https://www.linkedin.com/in/ — never a relative path like /in/... — NEVER a /company/ URL
- email: only include if a personal work email is explicitly visible on the page — do NOT include generic company emails (contact@, info@, careers@, jobs@) — leave empty if unsure
- name and role are required — skip entries where you cannot determine both
- Focus on: CTO, VP Engineering, Engineering Manager, Tech Lead, DevOps Engineer, Infrastructure Manager, IT Director, Technical Recruiter

Return a JSON object with a single field "contacts" containing the list.`

const ContactRankingPrompt = `Given this list of contacts at a %s company, pick the single BEST person to cold-email for a DevOps/backend internship.

Priority order: CTO > Engineering Manager > Tech Lead > Infrastructure Manager > IT Director > Technical Recruiter > HR Manager

Return a JSON object with field "best" containing the chosen contact object (same fields as input), or null if none are suitable.`

const IndividualProfilePrompt = `You are extracting information from a personal LinkedIn profile page.

Return a JSON object with:
- name: full name
- role: current job title and company
- email: personal work email if explicitly visible on the profile — empty string if not visible
- recent_posts: list of up to 3 topics or themes from their recent activity (empty list if none)
- background: 1-2 sentence summary of their professional background relevant to tech
- tech_stack: list of technologies mentioned on their profile`

// ExtractPeopleFromPage extracts all individuals from a People tab markdown.
// Returns up to 5 candidates. Does NOT rank them.
func (c *Classifier) ExtractPeopleFromPage(ctx context.Context, markdown string, runID string) (PeoplePageData, error) {
	var result PeoplePageData
	req := llm.CompletionRequest{
		System: PeopleExtractionPrompt,
		User:   fmt.Sprintf("LinkedIn People tab content:\n\n%s", markdown),
	}
	err := c.llm.CompleteJSON(ctx, req, "extract_people", runID, &result)
	return result, err
}

// RankContacts picks the best contact from a list for a given company type.
func (c *Classifier) RankContacts(ctx context.Context, contacts []IndividualContact, companyType string, runID string) (*IndividualContact, error) {
	if len(contacts) == 0 {
		return nil, nil
	}
	if len(contacts) == 1 {
		return &contacts[0], nil
	}

	type rankResult struct {
		Best *IndividualContact `json:"best"`
	}
	var result rankResult

	contactsJSON, _ := json.Marshal(contacts)
	req := llm.CompletionRequest{
		System: fmt.Sprintf(ContactRankingPrompt, companyType),
		User:   fmt.Sprintf("Contacts:\n%s", string(contactsJSON)),
	}
	err := c.llm.CompleteJSON(ctx, req, "rank_contacts", runID, &result)
	return result.Best, err
}

// EnrichIndividualProfile fetches and extracts data from a personal /in/ profile.
func (c *Classifier) EnrichIndividualProfile(ctx context.Context, fetcher *scraper.CascadeFetcher, contact IndividualContact, runID string) (IndividualProfileData, error) {
	profileURL := normalizeLinkedInURL(contact.LinkedinURL)
	if profileURL == "" {
		return IndividualProfileData{Name: contact.Name, Role: contact.Role}, nil
	}

	res, err := fetcher.Fetch(ctx, profileURL)
	if err != nil {
		// Non-fatal: return what we already know
		return IndividualProfileData{Name: contact.Name, Role: contact.Role}, nil
	}

	var profile IndividualProfileData
	req := llm.CompletionRequest{
		System: IndividualProfilePrompt,
		User:   fmt.Sprintf("LinkedIn profile content:\n\n%s", res.ContentMD),
	}
	err = c.llm.CompleteJSON(ctx, req, "enrich_individual", runID, &profile)
	if err != nil {
		return IndividualProfileData{Name: contact.Name, Role: contact.Role}, nil
	}
	return profile, nil
}

// normalizeLinkedInURL ensures a LinkedIn profile URL is absolute.
// The LLM occasionally returns relative paths (/in/...) or scheme-less URLs.
func normalizeLinkedInURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return "https://www.linkedin.com" + raw
	}
	return "https://www.linkedin.com/in/" + raw
}
