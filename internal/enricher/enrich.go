package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/collector"
	"jobhunter/internal/db"
	"jobhunter/internal/llm"
	"jobhunter/internal/pipeline"
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
	reporter   pipeline.Reporter
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
		reporter:   pipeline.NilReporter{},
	}
}

func (e *Enricher) SetReporter(r pipeline.Reporter) {
	if r == nil {
		e.reporter = pipeline.NilReporter{}
	} else {
		e.reporter = r
	}
}

func (e *Enricher) EnrichCompany(ctx context.Context, compID uint, runID string) error {
	comp, err := e.db.GetCompany(compID)
	if err != nil {
		return err
	}

	e.reporter.Update(pipeline.ProgressUpdate{
		ID:     int(comp.ID),
		Name:   comp.Name,
		Step:   "Initializing",
		Status: pipeline.StatusRunning,
	})

	// 0. Resolve generic name if possible (Stage 0)
	if (strings.HasPrefix(comp.Name, "Company ") || comp.Name == "") && comp.Siren != "" {
		e.reporter.Update(pipeline.ProgressUpdate{
			ID:     int(comp.ID),
			Name:   comp.Name,
			Step:   "Resolving Name",
			Status: pipeline.StatusRunning,
		})
		log.Printf("Resolving generic name for SIREN %s via Recherche API...", comp.Siren)
		info, err := e.recherche.GetCompanyInfo(ctx, comp.Siren)
		if err == nil {
			log.Printf("Resolved: %s", info.Name)
			comp.Name = info.Name
			if comp.Website == "" && info.Website != "" {
				comp.Website = info.Website
			}
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"name":    comp.Name,
				"website": comp.Website,
			})
		}
	}

	// Stage 0b. Fetch website from Recherche API if missing
	website := comp.Website
	if website == "" && comp.Siren != "" {
		info, err := e.recherche.GetCompanyInfo(ctx, comp.Siren)
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
	linkedin := comp.LinkedinURL
	if website == "" || linkedin == "" {
		e.reporter.Update(pipeline.ProgressUpdate{
			ID:     int(comp.ID),
			Name:   comp.Name,
			Step:   "URL Discovery",
			Status: pipeline.StatusRunning,
		})
		disc := NewURLDiscoverer(e.fetcher, e.geminiAPI, e.classifier)
		disc.SetReporter(e.reporter)
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

	e.reporter.Update(pipeline.ProgressUpdate{
		ID:      int(comp.ID),
		Name:    comp.Name,
		Step:    "Fetching Page",
		Status:  pipeline.StatusRunning,
		Message: targetURL,
	})
	res, err := e.fetcher.Fetch(ctx, targetURL)
	if err == nil {
		log.Printf("DEBUG [%s]: Fetched %s: quality %.2f via %s", comp.Name, targetURL, res.Quality, res.Method)
	}

	if err != nil || res.Quality < 0.3 {
		if website != "" && targetURL != website {
			log.Printf("DEBUG [%s]: low quality fetch for %s (%.2f), retrying with website %s",
				comp.Name, targetURL, res.Quality, website)
			e.reporter.Update(pipeline.ProgressUpdate{
				ID:      int(comp.ID),
				Name:    comp.Name,
				Step:    "Retry Fetch",
				Status:  pipeline.StatusRunning,
				Message: website,
			})
			res, err = e.fetcher.Fetch(ctx, website)
			if err == nil {
				log.Printf("DEBUG [%s]: Website retry result: quality %.2f via %s", comp.Name, res.Quality, res.Method)
			}
		}
		if err != nil || res.Quality < 0.3 {
			log.Printf("DEBUG [%s]: Fetch failed or quality too low (%.2f) for both LinkedIn and Website", comp.Name, res.Quality)

			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"status": "NO_CONTACT_FOUND",
			})
			return nil
		}
	}

	// 3. Extract company-level info
	e.reporter.Update(pipeline.ProgressUpdate{
		ID:     int(comp.ID),
		Name:   comp.Name,
		Step:   "Extracting Info",
		Status: pipeline.StatusRunning,
	})
	log.Printf("DEBUG [%s]: Extracting company info from content (length: %d)", comp.Name, len(res.ContentMD))
	info, err := e.classifier.ExtractCompanyInfo(ctx, res.ContentMD, runID)
	if err != nil {
		log.Printf("DEBUG [%s]: Company info extraction failed: %v", comp.Name, err)
		return fmt.Errorf("company extraction failed: %w", err)
	}
	log.Printf("DEBUG [%s]: Company info extracted: Type=%s, TechStack=%v", comp.Name, info.CompanyType, info.TechStack)

	updates := map[string]interface{}{
		"tech_stack":             strings.Join(info.TechStack, ", "),
		"website":                firstNonEmpty(info.Website, website),
		"linkedin_url":           firstNonEmpty(info.LinkedinURL, linkedin),
		"careers_page_url":       info.CareersPageURL,
		"company_email":          info.CompanyEmail,
		"has_internal_tech_team": info.HasInternalTechTeam,
		"tech_team_signals":      strings.Join(info.TechTeamSignals, ", "),
	}
	if info.LinkedinURL != "" && linkedin == "" {
		linkedin = info.LinkedinURL
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

	e.reporter.Update(pipeline.ProgressUpdate{
		ID:     int(comp.ID),
		Name:   comp.Name,
		Step:   "Searching People",
		Status: pipeline.StatusRunning,
	})
	var people PeoplePageData
	peopleURL := strings.TrimSuffix(linkedin, "/") + "/people/"
	log.Printf("DEBUG [%s]: Fetching people from %s (with scroll)", comp.Name, peopleURL)
	peopleRes, err := e.fetcher.ScrollAndFetch(ctx, peopleURL, 3)
	if err == nil && peopleRes.Quality >= 0.2 {
		p, err := e.classifier.ExtractPeopleFromPage(ctx, peopleRes.ContentMD, runID)
		if err == nil {
			people.Contacts = append(people.Contacts, p.Contacts...)
		}
	}

	if err != nil || len(people.Contacts) == 0 {
		if err != nil {
			log.Printf("DEBUG [%s]: People fetch failed or no contacts: %v", comp.Name, err)
		}
		// Fallback to website if LinkedIn failed or no contacts found
		if website != "" {
			e.reporter.Update(pipeline.ProgressUpdate{
				ID:     int(comp.ID),
				Name:   comp.Name,
				Step:   "Website Contact Search",
				Status: pipeline.StatusRunning,
			})
			log.Printf("DEBUG [%s]: Retrying contact search on website %s", comp.Name, website)
			
			// EXPLORATION PHASE: find interesting links first
			log.Printf("DEBUG [%s]: Exploring website for interesting links...", comp.Name)
			mainRes, err := e.fetcher.Fetch(ctx, website)
			if err == nil {
				links, _ := e.classifier.ExtractInterestingLinks(ctx, mainRes.ContentMD, runID)
				log.Printf("DEBUG [%s]: Interesting links found: %v", comp.Name, links)
				
				// Try these links in order of importance
				for _, link := range links {
					// resolve relative URLs
					target := link
					if !strings.HasPrefix(link, "http") {
						base := strings.TrimSuffix(website, "/")
						if !strings.HasPrefix(link, "/") {
							target = base + "/" + link
						} else {
							target = base + link
						}
					}
					
					log.Printf("DEBUG [%s]: Visiting explored link: %s", comp.Name, target)
					subRes, err := e.fetcher.Fetch(ctx, target)
					if err == nil && subRes.Quality >= 0.5 {
						// Extract people from this page
						p, _ := e.classifier.ExtractPeopleFromPage(ctx, subRes.ContentMD, runID)
						if len(p.Contacts) > 0 {
							log.Printf("DEBUG [%s]: Found %d contacts on %s", comp.Name, len(p.Contacts), target)
							people.Contacts = append(people.Contacts, p.Contacts...)
						}
					}
				}
			}
			
			// Final fallback to main page if no contacts found yet
			if len(people.Contacts) == 0 && mainRes.Quality >= 0.2 {
				p, _ := e.classifier.ExtractPeopleFromPage(ctx, mainRes.ContentMD, runID)
				people.Contacts = append(people.Contacts, p.Contacts...)
			}
		}
	}
	
	for i := range people.Contacts {
		if isHallucinated(people.Contacts[i]) {
			people.Contacts[i].Confidence = "hallucinated"
		} else {
			people.Contacts[i].Confidence = "probable"
		}
	}

	// If NO real contacts found on page, try external search (Gemini Search Grounding)
	if len(people.Contacts) == 0 {
		e.reporter.Update(pipeline.ProgressUpdate{
			ID:     int(comp.ID),
			Name:   comp.Name,
			Step:   "External Search",
			Status: pipeline.StatusRunning,
		})
		log.Printf("DEBUG [%s]: No contacts found on page, trying external search", comp.Name)
		disc := NewURLDiscoverer(e.fetcher, e.geminiAPI, e.classifier)
		disc.SetReporter(e.reporter)
		extContacts, err := disc.SearchPeopleOnLinkedIn(ctx, *comp, []string{"CTO", "Directeur Technique", "DevOps", "Recrutement", "RH", "Engineering Manager"})
		if err == nil && len(extContacts) > 0 {
			for i := range extContacts {
				if isHallucinated(extContacts[i]) {
					extContacts[i].Confidence = "hallucinated"
				} else {
					extContacts[i].Confidence = "probable"
				}
			}
			people.Contacts = append(people.Contacts, extContacts...)
		}
	}

	if len(people.Contacts) == 0 {
		log.Printf("DEBUG [%s]: No contacts found even with external search", comp.Name)
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
		return nil
	}

	log.Printf("Found %d candidates for %s", len(people.Contacts), comp.Name)

	// Filter out hallucinated ones for enrichment and ranking to avoid wasting tokens
	var realCandidates []IndividualContact
	for _, c := range people.Contacts {
		if c.Confidence != "hallucinated" {
			realCandidates = append(realCandidates, c)
		}
	}

	// 5. Enrich top candidates from their individual /in/ profiles (max 3)
	e.reporter.Update(pipeline.ProgressUpdate{
		ID:      int(comp.ID),
		Name:    comp.Name,
		Step:    "Enriching Contacts",
		Status:  pipeline.StatusRunning,
		Message: fmt.Sprintf("0/%d", min(3, len(realCandidates))),
	})
	maxProfiles := 3
	enriched := make([]IndividualContact, 0, len(people.Contacts))
	
	// We only enrich 'real' candidates
	enrichedCount := 0
	for i, candidate := range realCandidates {
		if i >= maxProfiles {
			enriched = append(enriched, candidate)
			continue
		}
		profile, _ := e.classifier.EnrichIndividualProfile(ctx, e.fetcher, candidate, runID)
		if profile.Email != "" && candidate.Email == "" {
			candidate.Email = profile.Email
		}
		enriched = append(enriched, candidate)
		enrichedCount++
		e.reporter.Update(pipeline.ProgressUpdate{
			ID:      int(comp.ID),
			Name:    comp.Name,
			Step:    "Enriching Contacts",
			Status:  pipeline.StatusRunning,
			Message: fmt.Sprintf("%d/%d", enrichedCount, min(3, len(realCandidates))),
		})
	}

	// Add the hallucinated ones to 'enriched' list so they get saved too
	for _, c := range people.Contacts {
		if c.Confidence == "hallucinated" {
			enriched = append(enriched, c)
		}
	}

	// 6. Rank to find best contact (only from enriched real candidates)
	e.reporter.Update(pipeline.ProgressUpdate{
		ID:     int(comp.ID),
		Name:   comp.Name,
		Step:   "Ranking Contacts",
		Status: pipeline.StatusRunning,
	})
	var best *IndividualContact
	if len(realCandidates) > 0 {
		// Only rank the ones that were in realCandidates
		toRank := make([]IndividualContact, 0)
		for _, c := range enriched {
			if c.Confidence != "hallucinated" {
				toRank = append(toRank, c)
			}
		}
		best, _ = e.classifier.RankContacts(ctx, toRank, info.CompanyType, runID)
	}

	if best == nil {
		log.Printf("No suitable contact found after ranking for %s", comp.Name)
	} else {
		log.Printf("Best contact for %s: %s (%s)", comp.Name, best.Name, best.Role)
	}

	// 7. Save ALL contacts, mark best as primary
	e.reporter.Update(pipeline.ProgressUpdate{
		ID:     int(comp.ID),
		Name:   comp.Name,
		Step:   "Saving Results",
		Status: pipeline.StatusRunning,
	})
	var primaryContactID uint
	for _, candidate := range enriched {
		isPrimary := best != nil && candidate.Name == best.Name
		
		conf := candidate.Confidence
		if conf == "" {
			conf = "probable"
		}

		contactID, err := e.db.AddContact(&db.Contact{
			CompanyID:   comp.ID,
			Name:        candidate.Name,
			Role:        candidate.Role,
			LinkedinURL: candidate.LinkedinURL,
			Email:       candidate.Email,
			Source:      "linkedin",
			Confidence:  conf,
		}, isPrimary)
		
		if err != nil {
			log.Printf("ERROR [%s]: Failed to save contact %s: %v", comp.Name, candidate.Name, err)
		} else if isPrimary {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// TODO: Improve hallucination detection logic to be more robust than hardcoded names.
func isHallucinated(c IndividualContact) bool {
	hallucinatedFullNames := []string{
		"john doe", "jane smith",
	}
	name := strings.ToLower(c.Name)
	li := strings.ToLower(c.LinkedinURL)
	
	for _, h := range hallucinatedFullNames {
		if name == h {
			return true
		}
	}

	// Only flag as hallucinated if it really looks like a placeholder
	if li == "" || li == "n/a" || li == "none" {
		return true
	}

	return false
}
