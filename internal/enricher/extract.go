package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/llm"
)

type RawCompanyPage struct {
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	City                  string   `json:"city"`
	Headcount             string   `json:"headcount"`
	TechStack             []string `json:"tech_stack"`
	GithubOrg             string   `json:"github_org"`
	EngineeringBlogURL    string   `json:"engineering_blog_url"`
	OpenSourceMentioned   bool     `json:"open_source_mentioned"`
	InfraKeywords         []string `json:"infrastructure_keywords"`
	
	ContactName           string   `json:"contact_name"`
	ContactRole           string   `json:"contact_role"`
	ContactLinkedin       string   `json:"contact_linkedin"`
	ContactEmail          string   `json:"contact_email"`
	
	CompanyType           string   `json:"company_type"` // TECH | TECH_ADJACENT | NON_TECH
	HasInternalTechTeam   bool     `json:"has_internal_tech_team"`
	TechTeamSignals       []string `json:"tech_team_signals"`
}

const ExtractionSystemPrompt = `You are a technical recruiter and OSINT expert. 
Your task is to extract structured company information from the provided markdown content of a company's career page or LinkedIn profile.

Extraction focus:
1. Identify if the company is a 'TECH' company (product is software/infra), 'TECH_ADJACENT' (retail, logistics, bank with a large internal tech team), or 'NON_TECH'.
2. Look for signals of an internal engineering team: job postings for DevOps/SRE/Backend, mentions of an engineering culture, a tech blog, or specific tech stacks.
3. Find a primary technical contact (CTO, VP Eng, Engineering Manager) or a technical recruiter.

Return only a valid JSON object matching the requested schema.
`

func (c *Classifier) ExtractCompanyInfo(ctx context.Context, markdown string, runID string) (RawCompanyPage, error) {
	var info RawCompanyPage
	req := llm.CompletionRequest{
		System: ExtractionSystemPrompt,
		User:   fmt.Sprintf("Content to extract:\n\n%s", markdown),
	}

	err := c.llm.CompleteJSON(ctx, req, "extract_company_info", runID, &info)
	if err != nil {
		return RawCompanyPage{}, err
	}

	return info, nil
}
