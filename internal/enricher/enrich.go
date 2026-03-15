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
}

func NewEnricher(database *db.DB, fetcher *scraper.CascadeFetcher, classifier *Classifier) *Enricher {
	return &Enricher{
		db:         database,
		fetcher:    fetcher,
		classifier: classifier,
		recherche:  collector.NewRechercheClient(),
	}
}

const ResearchSystemPrompt = `You are a technical recruiter and OSINT expert.
Your task is to research a French company to find information for a DevOps/backend internship application.

Use your browser tools to:
1. Search for the company's LinkedIn company page.
2. Find their official website.
3. Extract their tech stack (Docker, K8s, Cloud providers, languages).
4. Find a primary technical contact (CTO, Engineering Manager, Tech Lead) or Recruiter.
5. Identify if they are TECH, TECH_ADJACENT, or NON_TECH.

IMPORTANT: If the company is a one-person business, a freelancer, or an individual (auto-entrepreneur), classify it as NON_TECH and set status to 'PASS'. We only want companies with an internal tech team.

Return ONLY a JSON object with:
{
  "official_name": "the full, correct name of the company",
  "website": "url",
  "linkedin_url": "url",
  "description": "short summary",
  "tech_stack": "comma-separated list",
  "company_type": "TECH | TECH_ADJACENT | NON_TECH",
  "has_internal_tech_team": true/false,
  "tech_team_signals": ["signal 1", "signal 2"],
  "contact_name": "name or null",
  "contact_role": "role or null",
  "contact_linkedin": "profile url or null",
  "contact_email": "email if public",
  "is_freelancer": true/false,
  "status_override": "ENRICHED | PASS"
}
`

func (e *Enricher) EnrichCompany(ctx context.Context, compID int, runID string) error {
	comp, err := e.db.GetCompany(compID)
	if err != nil {
		return err
	}

	// 0. Fix generic name if possible
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
		} else {
			log.Printf("Warning: Name resolution failed for %s: %v", comp.Siren.String, err)
		}
	}

	fmt.Printf("  🔍 Researching %s (%s)...\n", comp.Name, comp.City.String)

	// 1. Send research task to LLM
	var researchResult struct {
		OfficialName        string   `json:"official_name"`
		Website             string   `json:"website"`
		LinkedinURL         string   `json:"linkedin_url"`
		Description         string   `json:"description"`
		TechStack           string   `json:"tech_stack"`
		CompanyType         string   `json:"company_type"`
		HasInternalTechTeam bool     `json:"has_internal_tech_team"`
		TechTeamSignals     []string `json:"tech_team_signals"`
		ContactName         string   `json:"contact_name"`
		ContactRole         string   `json:"contact_role"`
		ContactLinkedin     string   `json:"contact_linkedin"`
		ContactEmail        string   `json:"contact_email"`
		IsFreelancer        bool     `json:"is_freelancer"`
		StatusOverride      string   `json:"status_override"`
	}

	prompt := fmt.Sprintf("Research company: %s in %s. SIREN: %s. NAF: %s", 
		comp.Name, comp.City.String, comp.Siren.String, comp.NAFLabel.String)
	req := llm.CompletionRequest{
		System: ResearchSystemPrompt,
		User:   prompt,
	}

	err = e.classifier.llm.CompleteJSON(ctx, req, "research_company", runID, &researchResult)
	if err != nil {
		return fmt.Errorf("research failed: %w", err)
	}

	// 2. Update Company
	if researchResult.OfficialName != "" {
		comp.Name = researchResult.OfficialName
	}

	status := "ENRICHED"
	if researchResult.IsFreelancer || researchResult.StatusOverride == "PASS" {
		status = "PASS"
	}

	updates := map[string]interface{}{
		"name":                   comp.Name,
		"company_type":           researchResult.CompanyType,
		"has_internal_tech_team": researchResult.HasInternalTechTeam,
		"tech_team_signals":      strings.Join(researchResult.TechTeamSignals, ", "),
		"status":                 status,
	}

	if researchResult.Website != "" {
		updates["website"] = researchResult.Website
	}
	if researchResult.LinkedinURL != "" {
		updates["linkedin_url"] = researchResult.LinkedinURL
	}
	if researchResult.Description != "" {
		updates["description"] = researchResult.Description
	}
	if researchResult.TechStack != "" {
		updates["tech_stack"] = researchResult.TechStack
	}

	_ = e.db.UpdateCompany(comp.ID, updates)

	// 3. Save contact if found
	if researchResult.ContactName != "" {
		contactID, err := e.db.AddContact(&db.Contact{
			CompanyID:   comp.ID,
			Name:        db.ToNullString(researchResult.ContactName),
			Role:        db.ToNullString(researchResult.ContactRole),
			LinkedinURL: db.ToNullString(researchResult.ContactLinkedin),
			Email:       db.ToNullString(researchResult.ContactEmail),
			Source:      db.ToNullString("linkedin"),
			Confidence:  db.ToNullString("probable"),
		}, true)
		if err == nil {
			_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
				"primary_contact_id": contactID,
				"status":            "TO_CONTACT",
			})
		}
	} else if status == "ENRICHED" {
		// If research succeeded but no contact was found, set specific status
		_ = e.db.UpdateCompany(comp.ID, map[string]interface{}{
			"status": "NO_CONTACT_FOUND",
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
