package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/llm"
)

// CompanyPageData — extracted from company LinkedIn page or website.
// Company-level data ONLY. No individual people.
type CompanyPageData struct {
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	City                string   `json:"city"`
	Headcount           string   `json:"headcount"`
	TechStack           []string `json:"tech_stack"`
	Website             string   `json:"website"`
	CareersPageURL      string   `json:"careers_page_url"`
	CompanyEmail        string   `json:"company_email"` // generic: careers@, contact@, info@
	GithubOrg           string   `json:"github_org"`
	EngineeringBlogURL  string   `json:"engineering_blog_url"`
	OpenSourceMentioned bool     `json:"open_source_mentioned"`
	InfraKeywords       []string `json:"infrastructure_keywords"`
	CompanyType         string   `json:"company_type"`
	HasInternalTechTeam bool     `json:"has_internal_tech_team"`
	TechTeamSignals     []string `json:"tech_team_signals"`
}

const CompanyExtractionPrompt = `You are extracting COMPANY-LEVEL information from a company's LinkedIn page or website.

CRITICAL: Do NOT extract individual people's contact details. That is a separate step.

Rules:
- website: the company's public website URL (not LinkedIn)
- careers_page_url: direct URL to their jobs/careers page if visible
- company_email: a generic company contact address (careers@, jobs@, contact@, info@) if visible — NOT an individual's personal email
- Do NOT include any /in/ LinkedIn profile URLs — only company-level data belongs here
- company_type: TECH if core product is software/infra, TECH_ADJACENT if non-tech business with internal tech team, NON_TECH otherwise

Return ONLY a valid JSON object.`

func (c *Classifier) ExtractCompanyInfo(ctx context.Context, markdown string, runID string) (CompanyPageData, error) {
	var info CompanyPageData
	req := llm.CompletionRequest{
		System: CompanyExtractionPrompt,
		User:   fmt.Sprintf("Content to extract:\n\n%s", markdown),
	}

	err := c.llm.CompleteJSON(ctx, req, "extract_company_info", runID, &info)
	if err != nil {
		return CompanyPageData{}, err
	}

	return info, nil
}
