package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/collector"
	"jobhunter/internal/db"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"strings"
)

type Enricher struct {
	db         *db.DB
	fetcher    *scraper.CascadeFetcher
	classifier *Classifier
	recherche  *collector.RechercheClient
}

func NewEnricher(database *db.DB, fetcher *scraper.CascadeFetcher, classifier *Classifier) *Enricher {
	return &Enricher{
		db:         database,
		fetcher:    fetcher,
		classifier: classifier,
		recherche:  collector.NewRechercheClient(),
	}
}

func (e *Enricher) EnrichCompany(ctx context.Context, compID int, runID string) error {
	comp, err := e.db.GetCompany(compID)
	if err != nil {
		return err
	}

	// 0. Fix generic name if possible
	if strings.HasPrefix(comp.Name, "Company ") && comp.Siren.Valid {
		info, err := e.recherche.GetCompanyInfo(ctx, comp.Siren.String)
		if err == nil {
			comp.Name = info.Name
			if comp.Website.String == "" {
				comp.Website = db.ToNullString(info.Website)
			}
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"name":    comp.Name,
				"website": comp.Website,
			})
		}
	}

	// 1. Discover URLs if missing
	website := comp.Website.String
	linkedin := comp.LinkedinURL.String
	if website == "" || linkedin == "" {
		disc := NewURLDiscoverer(e.fetcher)
		w, l, err := disc.DiscoverURLs(ctx, *comp)
		if err != nil {
			return fmt.Errorf("failed to discover URLs: %w", err)
		}
		
		if website == "" { website = w }
		if linkedin == "" { linkedin = l }
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
			"website":      website,
			"linkedin_url": linkedin,
		})
	}

	// 2. Fetch + extract info (use LinkedIn or website)
	targetURL := linkedin
	if targetURL == "" { targetURL = website }
	
	if targetURL == "" {
		return fmt.Errorf("no URL found for company %s", comp.Name)
	}

	res, err := e.fetcher.Fetch(ctx, targetURL)
	if err != nil {
		return fmt.Errorf("fetch failed for %s: %w", targetURL, err)
	}

	info, err := e.classifier.ExtractCompanyInfo(ctx, res.ContentMD, runID)
	if err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// 3. Update company from info
	updates := map[string]interface{}{
		"description":            info.Description,
		"tech_stack":             strings.Join(info.TechStack, ", "),
		"has_internal_tech_team": info.HasInternalTechTeam,
		"tech_team_signals":      strings.Join(info.TechTeamSignals, ", "),
	}
	
	// Only update company_type if it was UNKNOWN
	if comp.CompanyType == "UNKNOWN" {
		updates["company_type"] = info.CompanyType
	}

	_ = e.db.UpdateCompany(comp.ID, updates)

	// 4. Find contacts via People tab if linkedin available
	if linkedin != "" {
		peopleURL := linkedin
		if !strings.HasSuffix(peopleURL, "/") { peopleURL += "/" }
		peopleURL += "people/"

		peopleRes, err := e.fetcher.Fetch(ctx, peopleURL)
		if err == nil {
			contacts, err := e.classifier.DiscoverLinkedInContactsWithMD(ctx, peopleRes.ContentMD, info.CompanyType, runID)
			if err == nil && contacts.Best != nil {
				// Save contact to DB
				contactID, err := e.db.AddContact(&db.Contact{
					CompanyID:   comp.ID,
					Name:        db.ToNullString(contacts.Best.Name),
					Role:        db.ToNullString(contacts.Best.Role),
					LinkedinURL: db.ToNullString(contacts.Best.LinkedinURL),
					Email:       db.ToNullString(contacts.Best.Email),
					Source:      db.ToNullString("linkedin"),
					Confidence:  db.ToNullString("probable"),
				}, true)
				if err == nil {
					_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
						"primary_contact_id": contactID,
						"status":            "TO_CONTACT",
					})
				}
			}
		}
	}

	if comp.Status == "NEW" {
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
			"status": "ENRICHED",
		})
	}

	return nil
}

// Add a helper to Classifier to avoid circular dependency or messy wiring
func (c *Classifier) DiscoverLinkedInContactsWithMD(ctx context.Context, markdown string, companyType string, runID string) (ContactResult, error) {
	var result ContactResult
	req := llm.CompletionRequest{
		System: fmt.Sprintf(ContactSelectionPrompt, companyType),
		User:   fmt.Sprintf("Markdown from LinkedIn People tab:\n\n%s", markdown),
	}

	err := c.llm.CompleteJSON(ctx, req, "select_best_contact", runID, &result)
	return result, err
}
