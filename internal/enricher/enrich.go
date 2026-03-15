package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/collector"
	"jobhunter/internal/db"
	"jobhunter/internal/scraper"
	"log"
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

	// 0. Resolve generic name if possible (Stage 0)
	if (strings.HasPrefix(comp.Name, "Company ") || comp.Name == "") && comp.Siren.Valid {
		log.Printf("Resolving generic name for SIREN %s via Recherche API...", comp.Siren.String)
		info, err := e.recherche.GetCompanyInfo(ctx, comp.Siren.String)
		if err == nil {
			log.Printf("Resolved: %s", info.Name)
			comp.Name = info.Name
			if comp.Website.String == "" && info.Website != "" {
				comp.Website = db.ToNullString(info.Website)
			}
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"name":    comp.Name,
				"website": comp.Website,
			})
		}
	}

	fmt.Printf("▶ Enriching %s...\n", comp.Name)

	// 1. Discover URLs if missing
	website := comp.Website.String
	linkedin := comp.LinkedinURL.String
	if website == "" || linkedin == "" {
		disc := NewURLDiscoverer(e.fetcher)
		w, l, err := disc.DiscoverURLs(ctx, *comp)
		if err == nil {
			if website == "" {
				website = w
			}
			if linkedin == "" {
				linkedin = l
			}
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"website":      website,
				"linkedin_url": linkedin,
			})
		}
	}

	// 2. Fetch main page (LinkedIn or Website)
	targetURL := linkedin
	if targetURL == "" {
		targetURL = website
	}
	if targetURL == "" {
		return fmt.Errorf("no URL found for company %s", comp.Name)
	}

	res, err := e.fetcher.Fetch(ctx, targetURL)
	if err != nil {
		return fmt.Errorf("fetch failed for %s: %w", targetURL, err)
	}

	// 3. Extract company-level info
	info, err := e.classifier.ExtractCompanyInfo(ctx, res.ContentMD, runID)
	if err != nil {
		return fmt.Errorf("company extraction failed: %w", err)
	}

	updates := map[string]interface{}{
		"description":            info.Description,
		"tech_stack":             strings.Join(info.TechStack, ", "),
		"website":                firstNonEmpty(info.Website, website),
		"careers_page_url":       info.CareersPageURL,
		"company_email":          info.CompanyEmail,
		"has_internal_tech_team": info.HasInternalTechTeam,
		"tech_team_signals":      strings.Join(info.TechTeamSignals, ", "),
	}
	if comp.CompanyType == "UNKNOWN" {
		updates["company_type"] = info.CompanyType
	}
	_ = e.db.UpdateCompany(comp.ID, updates)

	// 4. Discover individuals from LinkedIn People tab
	if linkedin == "" {
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
		return nil
	}

	peopleURL := strings.TrimSuffix(linkedin, "/") + "/people/"
	peopleRes, err := e.fetcher.Fetch(ctx, peopleURL)
	if err != nil {
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
		return nil
	}

	people, err := e.classifier.ExtractPeopleFromPage(ctx, peopleRes.ContentMD, runID)
	if err != nil || len(people.Contacts) == 0 {
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
		return nil
	}

	// 5. Enrich top candidates from their individual /in/ profiles (max 3)
	maxProfiles := 3
	enriched := make([]IndividualContact, 0, len(people.Contacts))
	for i, candidate := range people.Contacts {
		if i >= maxProfiles {
			// Save remaining candidates without profile enrichment
			enriched = append(enriched, candidate)
			continue
		}
		profile, _ := e.classifier.EnrichIndividualProfile(ctx, e.fetcher, candidate, runID)
		// Merge profile data back into candidate
		if profile.Email != "" && candidate.Email == "" {
			candidate.Email = profile.Email
		}
		// You can also use profile.RecentPosts or profile.Background if we add a place for them
		enriched = append(enriched, candidate)
	}

	// 6. Rank to find best contact
	best, _ := e.classifier.RankContacts(ctx, enriched, info.CompanyType, runID)

	// 7. Save ALL contacts, mark best as primary
	var primaryContactID int
	for _, candidate := range enriched {
		isPrimary := best != nil && candidate.Name == best.Name
		contactID, err := e.db.AddContact(&db.Contact{
			CompanyID:   comp.ID,
			Name:        db.ToNullString(candidate.Name),
			Role:        db.ToNullString(candidate.Role),
			LinkedinURL: db.ToNullString(candidate.LinkedinURL),
			Email:       db.ToNullString(candidate.Email),
			Source:      db.ToNullString("linkedin"),
			Confidence:  db.ToNullString("probable"),
		}, isPrimary)
		if err == nil && isPrimary {
			primaryContactID = contactID
		}
	}

	status := "NO_CONTACT_FOUND"
	if primaryContactID > 0 {
		status = "TO_CONTACT"
	}
	_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": status})

	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
