package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/collector"
	"jobhunter/internal/config"
	"jobhunter/internal/db"
	"jobhunter/internal/llm"
	"jobhunter/internal/pipeline"
	"jobhunter/internal/scraper"
	"strings"

	"go.uber.org/zap"
)

type Enricher struct {
	db              *db.DB
	cfg             *config.Config
	fetcher         *scraper.CascadeFetcher
	classifier      *Classifier
	recherche       *collector.RechercheClient
	geminiAPI       *llm.GeminiAPIProvider // nil if not configured
	reporter        pipeline.Reporter
	logger          *zap.Logger
	userLinkedInURL string
}

func NewEnricher(
	database *db.DB,
	cfg *config.Config,
	fetcher *scraper.CascadeFetcher,
	classifier *Classifier,
	geminiAPI *llm.GeminiAPIProvider,
	logger *zap.Logger,
	userLinkedInURL string,
) *Enricher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Enricher{
		db:              database,
		cfg:             cfg,
		fetcher:         fetcher,
		classifier:      classifier,
		recherche:       collector.NewRechercheClient(),
		geminiAPI:       geminiAPI,
		reporter:        pipeline.NilReporter{},
		logger:          logger,
		userLinkedInURL: normalizeLinkedIn(userLinkedInURL),
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
		e.logger.Info("Resolving generic name", zap.String("siren", comp.Siren))
		info, err := e.recherche.GetCompanyInfo(ctx, comp.Siren)
		if err == nil {
			e.logger.Info("Resolved name", zap.String("siren", comp.Siren), zap.String("name", info.Name))
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
			e.logger.Info("Recherche API found website", zap.String("company", comp.Name), zap.String("website", website))
		}
	}

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
		disc.SetLogger(e.logger)
		disc.SetReporter(e.reporter)
		disc.SetSkipDDG(e.cfg.Enrichment.Discovery.SkipDDGSearch)
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
		e.logger.Debug("No target URL found after discovery", zap.String("company", comp.Name))
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
		e.logger.Debug("Fetched target URL",
			zap.String("company", comp.Name),
			zap.String("url", targetURL),
			zap.Float64("quality", res.Quality),
			zap.String("method", res.Method))
	}

	if err != nil || res.Quality < e.cfg.Quality.BrowserMin {
		if website != "" && targetURL != website {
			e.logger.Debug("Low quality fetch, retrying with website",
				zap.String("company", comp.Name),
				zap.String("url", targetURL),
				zap.Float64("quality", res.Quality),
				zap.String("website", website))

			e.reporter.Update(pipeline.ProgressUpdate{
				ID:      int(comp.ID),
				Name:    comp.Name,
				Step:    "Retry Fetch",
				Status:  pipeline.StatusRunning,
				Message: website,
			})
			res, err = e.fetcher.Fetch(ctx, website)
			if err == nil {
				e.logger.Debug("Website retry result",
					zap.String("company", comp.Name),
					zap.Float64("quality", res.Quality),
					zap.String("method", res.Method))
			}
		}
		if err != nil || res.Quality < e.cfg.Quality.BrowserMin {
			e.logger.Debug("Fetch failed or quality too low for both LinkedIn and Website",
				zap.String("company", comp.Name),
				zap.Float64("quality", res.Quality))

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
	e.logger.Debug("Extracting company info", zap.String("company", comp.Name), zap.Int("content_len", len(res.ContentMD)))
	info, err := e.classifier.ExtractCompanyInfo(ctx, res.ContentMD, runID)
	if err != nil {
		e.logger.Error("Company info extraction failed", zap.String("company", comp.Name), zap.Error(err))
		return fmt.Errorf("company extraction failed: %w", err)
	}
	e.logger.Debug("Company info extracted",
		zap.String("company", comp.Name),
		zap.String("type", info.CompanyType),
		zap.Strings("tech_stack", info.TechStack))

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
		e.logger.Debug("No LinkedIn URL available, cannot search for individuals", zap.String("company", comp.Name))
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
	e.logger.Debug("Fetching people from LinkedIn", zap.String("company", comp.Name), zap.String("url", peopleURL))
	peopleRes, err := e.fetcher.ScrollAndFetch(ctx, peopleURL, 3)

	// Fail-fast: check if LinkedIn is blocking the people page (no /in/ profile URLs)
	if err == nil && peopleRes.Quality >= e.cfg.Quality.DiscoveryMin {
		profileCount := scraper.CountPersonalProfiles(peopleRes.ContentMD)
		if profileCount == 0 {
			e.logger.Warn("LinkedIn anti-bot detected: people page has no personal profile URLs",
				zap.String("company", comp.Name),
				zap.Int("content_len", len(peopleRes.ContentMD)))
			e.reporter.Log(pipeline.LogMsg{
				Level: "WARN",
				Text:  fmt.Sprintf("[%s] LinkedIn anti-bot: people page contains no personal profiles. Skipping enrichment.", comp.Name),
			})
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "ENRICHMENT_BLOCKED"})
			return nil
		}
		e.logger.Debug("LinkedIn people page OK", zap.String("company", comp.Name), zap.Int("profile_count", profileCount))

		p, err := e.classifier.ExtractPeopleFromPage(ctx, peopleRes.ContentMD, runID)
		if err != nil {
			e.logger.Error("LinkedIn people extraction failed", zap.String("company", comp.Name), zap.Error(err))
		} else {
			people.Contacts = append(people.Contacts, p.Contacts...)
		}
	}

	if err != nil || len(people.Contacts) == 0 {
		if err != nil {
			e.logger.Debug("People fetch failed", zap.String("company", comp.Name), zap.Error(err))
		}

		// Fallback to website if LinkedIn failed or no contacts found
		if website != "" {
			e.reporter.Update(pipeline.ProgressUpdate{
				ID:     int(comp.ID),
				Name:   comp.Name,
				Step:   "Website Contact Search",
				Status: pipeline.StatusRunning,
			})
			e.logger.Debug("Retrying contact search on website", zap.String("company", comp.Name), zap.String("website", website))

			// EXPLORATION PHASE: find interesting links first
			e.logger.Debug("Exploring website for interesting links", zap.String("company", comp.Name))
			mainRes, err := e.fetcher.Fetch(ctx, website)
			if err == nil {
				links, _ := e.classifier.ExtractInterestingLinks(ctx, mainRes.ContentMD, runID)
				e.logger.Debug("Interesting links found", zap.String("company", comp.Name), zap.Strings("links", links))

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

					e.logger.Debug("Visiting explored link", zap.String("company", comp.Name), zap.String("url", target))
					subRes, err := e.fetcher.Fetch(ctx, target)
					if err == nil && subRes.Quality >= e.cfg.Quality.EnrichMin {
						// Extract people from this page
						p, err := e.classifier.ExtractPeopleFromPage(ctx, subRes.ContentMD, runID)
						if err != nil {
							e.logger.Error("Website link people extraction failed", zap.String("company", comp.Name), zap.String("url", target), zap.Error(err))
						} else if len(p.Contacts) > 0 {
							e.logger.Info("Found contacts on website link", zap.String("company", comp.Name), zap.String("url", target), zap.Int("count", len(p.Contacts)))
							people.Contacts = append(people.Contacts, p.Contacts...)
						}
					}
				}
			}

			// Final fallback to main page if no contacts found yet
			if len(people.Contacts) == 0 && mainRes.Quality >= e.cfg.Quality.DiscoveryMin {
				p, err := e.classifier.ExtractPeopleFromPage(ctx, mainRes.ContentMD, runID)
				if err != nil {
					e.logger.Error("Website main page people extraction failed", zap.String("company", comp.Name), zap.Error(err))
				} else {
					people.Contacts = append(people.Contacts, p.Contacts...)
				}
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

	e.logger.Info("Found candidates", zap.String("company", comp.Name), zap.Int("count", len(people.Contacts)))

	// Filter out hallucinated and self-contacts for enrichment and ranking
	var realCandidates []IndividualContact
	for _, c := range people.Contacts {
		if c.Confidence == "hallucinated" {
			continue
		}
		if e.isSelf(c) {
			e.logger.Warn("Filtered out self-contact", zap.String("company", comp.Name), zap.String("name", c.Name), zap.String("linkedin", c.LinkedinURL))
			continue
		}
		realCandidates = append(realCandidates, c)
	}

	// If filtering removed all contacts, try external search
	if len(realCandidates) == 0 {
		e.reporter.Update(pipeline.ProgressUpdate{
			ID:     int(comp.ID),
			Name:   comp.Name,
			Step:   "External Search",
			Status: pipeline.StatusRunning,
		})
		e.logger.Debug("No contacts after filtering, trying external search", zap.String("company", comp.Name))
		disc := NewURLDiscoverer(e.fetcher, e.geminiAPI, e.classifier)
		disc.SetLogger(e.logger)
		disc.SetReporter(e.reporter)
		disc.SetSkipDDG(e.cfg.Enrichment.Discovery.SkipDDGSearch)
		extContacts, err := disc.SearchPeopleOnLinkedIn(ctx, *comp, []string{"CTO", "Directeur Technique", "DevOps", "Recrutement", "RH", "Engineering Manager"})
		if err == nil && len(extContacts) > 0 {
			for i := range extContacts {
				if isHallucinated(extContacts[i]) {
					extContacts[i].Confidence = "hallucinated"
				} else {
					extContacts[i].Confidence = "probable"
				}
				if e.isSelf(extContacts[i]) {
					e.logger.Warn("Filtered out self-contact from external search", zap.String("company", comp.Name), zap.String("name", extContacts[i].Name))
					continue
				}
				realCandidates = append(realCandidates, extContacts[i])
			}
		}
	}

	if len(realCandidates) == 0 {
		e.logger.Debug("No contacts found even with external search", zap.String("company", comp.Name))

		// Check if LinkedIn showed very few profiles — this means LinkedIn is limiting
		// results for this company, not that contacts genuinely don't exist
		if peopleRes.ContentMD != "" {
			profileCount := scraper.CountPersonalProfiles(peopleRes.ContentMD)
			if profileCount <= 2 {
				e.logger.Warn("LinkedIn limiting results: very few profiles visible",
					zap.String("company", comp.Name),
					zap.Int("profile_count", profileCount))
				e.reporter.Log(pipeline.LogMsg{
					Level: "WARN",
					Text:  fmt.Sprintf("[%s] LinkedIn limiting results: only %d profile(s) visible. Marking as blocked.", comp.Name, profileCount),
				})
				_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "ENRICHMENT_BLOCKED"})
				return nil
			}
		}

		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{"status": "NO_CONTACT_FOUND"})
		return nil
	}

	// 5. Enrich top candidates from their individual /in/ profiles (configurable)
	e.reporter.Update(pipeline.ProgressUpdate{
		ID:      int(comp.ID),
		Name:    comp.Name,
		Step:    "Enriching Contacts",
		Status:  pipeline.StatusRunning,
		Message: fmt.Sprintf("0/%d", min(3, len(realCandidates))),
	})
	maxProfiles := e.cfg.GetMaxProfilesToEnrich(comp.RelevanceScore)
	enriched := make([]IndividualContact, 0, len(people.Contacts))

	// We only enrich 'real' candidates
	enrichedCount := 0
	candidatesToEnrich := realCandidates
	if maxProfiles < len(candidatesToEnrich) {
		candidatesToEnrich = candidatesToEnrich[:maxProfiles]
	}

	if e.cfg.Enrichment.Methods.BatchEnrichment && len(candidatesToEnrich) > 1 {
		// Batch enrichment: single LLM call for all profiles
		batchProfiles, err := e.classifier.EnrichProfilesBatch(ctx, e.fetcher, candidatesToEnrich, runID)
		if err == nil {
			for i, profile := range batchProfiles {
				candidate := candidatesToEnrich[i]
				if profile.Email != "" && candidate.Email == "" {
					candidate.Email = profile.Email
				}
				enriched = append(enriched, candidate)
				enrichedCount++
			}
		} else {
			// Fallback to individual enrichment on batch failure
			for _, candidate := range candidatesToEnrich {
				profile, _ := e.classifier.EnrichIndividualProfile(ctx, e.fetcher, candidate, runID)
				if profile.Email != "" && candidate.Email == "" {
					candidate.Email = profile.Email
				}
				enriched = append(enriched, candidate)
				enrichedCount++
			}
		}
	} else {
		// Individual enrichment (original method)
		for _, candidate := range candidatesToEnrich {
			profile, _ := e.classifier.EnrichIndividualProfile(ctx, e.fetcher, candidate, runID)
			if profile.Email != "" && candidate.Email == "" {
				candidate.Email = profile.Email
			}
			enriched = append(enriched, candidate)
			enrichedCount++
		}
	}

	// Add remaining candidates without enrichment
	for i := len(candidatesToEnrich); i < len(realCandidates); i++ {
		enriched = append(enriched, realCandidates[i])
	}

	e.reporter.Update(pipeline.ProgressUpdate{
		ID:      int(comp.ID),
		Name:    comp.Name,
		Step:    "Enriching Contacts",
		Status:  pipeline.StatusRunning,
		Message: fmt.Sprintf("%d/%d", enrichedCount, min(3, len(realCandidates))),
	})

	// Do NOT save hallucinated contacts — they pollute the database

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
		if e.cfg.Enrichment.Methods.BatchRanking {
			best, _ = e.classifier.RankContactsBatch(ctx, toRank, info.CompanyType, runID)
		} else {
			best, _ = e.classifier.RankContacts(ctx, toRank, info.CompanyType, runID)
		}
	}

	if best == nil {
		e.logger.Warn("No suitable contact found after ranking", zap.String("company", comp.Name))
	} else {
		e.logger.Info("Best contact identified", zap.String("company", comp.Name), zap.String("contact", best.Name), zap.String("role", best.Role))
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
		isPrimary := false
		if best != nil {
			candidateName := strings.ToLower(strings.TrimSpace(candidate.Name))
			bestName := strings.ToLower(strings.TrimSpace(best.Name))
			isPrimary = candidateName == bestName
			if !isPrimary {
				// Fuzzy match: check if one contains the other
				isPrimary = strings.Contains(candidateName, bestName) || strings.Contains(bestName, candidateName)
			}
		}

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
			e.logger.Error("Failed to save contact", zap.String("company", comp.Name), zap.String("contact", candidate.Name), zap.Error(err))
		} else if isPrimary {
			primaryContactID = contactID
		}
	}

	if best != nil && primaryContactID == 0 {
		e.logger.Warn("Primary contact name mismatch during save",
			zap.String("company", comp.Name),
			zap.String("best_name", best.Name),
			zap.Int("enriched_count", len(enriched)))
		for _, c := range enriched {
			e.logger.Warn("  candidate", zap.String("name", c.Name))
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

func isHallucinated(c IndividualContact) bool {
	hallucinatedFullNames := []string{
		"john doe", "jane smith", "jane doe", "john smith",
		"mike smith", "test user", "unknown", "n/a",
	}
	name := strings.ToLower(strings.TrimSpace(c.Name))
	li := strings.ToLower(strings.TrimSpace(c.LinkedinURL))

	for _, h := range hallucinatedFullNames {
		if name == h {
			return true
		}
	}

	if li == "n/a" || li == "none" || li == "null" {
		return true
	}

	if strings.Contains(li, "/company/") {
		return true
	}

	parts := strings.Fields(name)
	if len(parts) < 2 {
		return true
	}

	for _, part := range parts {
		if len(part) < 2 {
			return true
		}
		if part == "x" || part == "xx" || part == "xxx" {
			return true
		}
	}

	if strings.Contains(name, "contact") || strings.Contains(name, "rh ") ||
		strings.Contains(name, "directeur") || strings.Contains(name, "manager") ||
		strings.Contains(name, "cto") || strings.Contains(name, "responsable") {
		if len(parts) < 2 || strings.EqualFold(parts[0], "le") || strings.EqualFold(parts[0], "la") {
			return true
		}
	}

	return false
}

func (e *Enricher) isSelf(c IndividualContact) bool {
	if e.userLinkedInURL == "" {
		return false
	}
	return normalizeLinkedIn(c.LinkedinURL) == e.userLinkedInURL
}

func normalizeLinkedIn(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	u = strings.TrimRight(u, "/")
	if idx := strings.Index(u, "?"); idx >= 0 {
		u = u[:idx]
	}
	return u
}
