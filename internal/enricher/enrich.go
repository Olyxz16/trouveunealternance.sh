package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/collector"
	"jobhunter/internal/db"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"log"
	"strings"
)

type Enricher struct {
	db         *db.DB
	fetcher    *scraper.CascadeFetcher
	classifier *Classifier
	recherche  *collector.RechercheClient
	geminiAPI  *llm.GeminiAPIProvider // nil if not configured
}

func NewEnricher(
	database *db.DB,
	fetcher *scraper.CascadeFetcher,
	classifier *Classifier,
	geminiAPI *llm.GeminiAPIProvider,
) *Enricher {
	return &Enricher{
		db:         database,
		fetcher:    fetcher,
		classifier: classifier,
		recherche:  collector.NewRechercheClient(),
		geminiAPI:  geminiAPI,
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

	// Stage 0b. Fetch website from Recherche API if missing
	website := comp.Website.String
	if website == "" && comp.Siren.Valid {
		info, err := e.recherche.GetCompanyInfo(ctx, comp.Siren.String)
		if err == nil && info.Website != "" {
			website = info.Website
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"website": website,
			})
			log.Printf("Recherche API: found website %s for %s", website, comp.Name)
		}
	}

	fmt.Printf("▶ Enriching %s...\n", comp.Name)

	// 1. Discover URLs if missing
	linkedin := comp.LinkedinURL.String
	if website == "" || linkedin == "" {
		disc := NewURLDiscoverer(e.fetcher, e.geminiAPI, e.classifier)
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
		log.Printf("DEBUG [%s]: No target URL (LinkedIn or Website) found after discovery", comp.Name)
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
			"status": "NO_CONTACT_FOUND",
		})
		return nil
	}

	res, err := e.fetcher.Fetch(ctx, targetURL)
	if err == nil {
		log.Printf("DEBUG [%s]: Fetched %s: quality %.2f via %s", comp.Name, targetURL, res.Quality, res.Method)
	}

	if err != nil || res.Quality < 0.3 {
		if website != "" && targetURL != website {
			log.Printf("DEBUG [%s]: low quality fetch for %s (%.2f), retrying with website %s",
				comp.Name, targetURL, res.Quality, website)
			res, err = e.fetcher.Fetch(ctx, website)
			if err == nil {
				log.Printf("DEBUG [%s]: Website retry result: quality %.2f via %s", comp.Name, res.Quality, res.Method)
			}
		}
		if err != nil || res.Quality < 0.3 {
			log.Printf("DEBUG [%s]: Fetch failed or quality too low (%.2f) for both LinkedIn and Website", comp.Name, res.Quality)
			// guessed URL was wrong — clear it if we wrote it
			if strings.Contains(targetURL, "linkedin.com") &&
				targetURL != comp.LinkedinURL.String {
				_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
					"linkedin_url": "",
				})
			}
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"status": "NO_CONTACT_FOUND",
			})
			return nil
		}
	}

	// 3. Extract company-level info
	log.Printf("DEBUG [%s]: Extracting company info from content (length: %d)", comp.Name, len(res.ContentMD))
	info, err := e.classifier.ExtractCompanyInfo(ctx, res.ContentMD, runID)
	if err != nil {
		log.Printf("DEBUG [%s]: Company info extraction failed: %v", comp.Name, err)
		return fmt.Errorf("company extraction failed: %w", err)
	}
	log.Printf("DEBUG [%s]: Company info extracted: Type=%s, TechStack=%v", comp.Name, info.CompanyType, info.TechStack)

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
		log.Printf("DEBUG [%s]: No LinkedIn URL available, cannot search for individuals", comp.Name)
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
		return nil
	}

	peopleURL := strings.TrimSuffix(linkedin, "/") + "/people/"
	log.Printf("DEBUG [%s]: Fetching people from %s (with scroll)", comp.Name, peopleURL)
	peopleRes, err := e.fetcher.ScrollAndFetch(ctx, peopleURL, 3)
	if err != nil {
		log.Printf("DEBUG [%s]: People fetch failed: %v", comp.Name, err)
		// Fallback to website if LinkedIn failed
		if website != "" {
			log.Printf("DEBUG [%s]: Retrying contact search on website %s", comp.Name, website)
			peopleRes, err = e.fetcher.Fetch(ctx, website)
		}
	}

	if err != nil || peopleRes.Quality < 0.2 {
		log.Printf("DEBUG [%s]: No reliable contact page found (err: %v)", comp.Name, err)
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
		return nil
	}
	log.Printf("DEBUG [%s]: Contact page fetched: quality %.2f, length %d via %s", comp.Name, peopleRes.Quality, len(peopleRes.ContentMD), peopleRes.Method)

	people, err := e.classifier.ExtractPeopleFromPage(ctx, peopleRes.ContentMD, runID)
	log.Printf("DEBUG [%s]: ExtractPeopleFromPage result: count=%d, err=%v", comp.Name, len(people.Contacts), err)
	
	// Filter out obviously fake/hallucinated contacts if they came from a low-quality source
	// (Placeholder names like "John Doe" or generic ones)
	
	if err != nil || len(people.Contacts) == 0 {
		log.Printf("DEBUG [%s]: No contacts found on page, trying external search", comp.Name)
		disc := NewURLDiscoverer(e.fetcher, e.geminiAPI, e.classifier)
		extContacts, err := disc.SearchPeopleOnLinkedIn(ctx, *comp, []string{"CTO", "Directeur Technique", "DevOps", "Recrutement", "RH", "Engineering Manager"})
		if err == nil && len(extContacts) > 0 {
			people.Contacts = extContacts
		} else {
			log.Printf("DEBUG [%s]: No contacts found even with external search", comp.Name)
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
			return nil
		}
	}

	log.Printf("Found %d candidates for %s", len(people.Contacts), comp.Name)

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
	if best == nil {
		log.Printf("No suitable contact found after ranking for %s", comp.Name)
	} else {
		log.Printf("Best contact for %s: %s (%s)", comp.Name, best.Name, best.Role)
	}

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
