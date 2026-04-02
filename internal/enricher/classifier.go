package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/llm"
	"strings"
)

type CompanyScore struct {
	RelevanceScore      int      `json:"relevance_score"`
	CompanyType         string   `json:"company_type"` // TECH, TECH_ADJACENT, NON_TECH
	HasInternalTechTeam bool     `json:"has_internal_tech_team"`
	TechTeamSignals     []string `json:"tech_team_signals"`
	Reasoning           string   `json:"reasoning"`
}

const ScoreSystemPrompt = `You are evaluating French companies as potential internship hosts for a DevOps/backend student.

Classification:
- TECH: Core product is software, infra, or IT services. IMPORTANT: NAF codes starting with 62 or 63 are ALMOST ALWAYS TECH.
- TECH_ADJACENT: Non-tech business (retail, bank, logistics, industry) but large enough (100+ employees) to have a significant internal IT/infra team.
- NON_TECH: No meaningful internal tech team or tech needs. This includes: public institutions (mairies, EHPADs, lycées, collèges, hôpitaux, centres communaux), small retail, restaurants, small associations, and any entity whose primary mission is not tech-related AND is not large enough to need a dedicated IT team.

Scoring (0-10):
- 10: Perfect fit (DevOps/Cloud product, major tech company).
- 8-9: Very good (Tech services, large tech team in a product company).
- 5-7: Good (Tech_adjacent but with clear tech signals, or smaller IT services).
- 1-4: Poor (Low relevance but some tech).
- 0: Completely irrelevant (NO internal tech, or classified as NON_TECH).

A company classified as TECH or TECH_ADJACENT should NEVER have a 0 score.
If you see NAF 62xx or 63xx, it is TECH by definition.
A company classified as NON_TECH MUST have a score of 0.

IMPORTANT: Even if the Description is empty, you MUST provide an assessment based on the Company Name and NAF code. Use your internal knowledge about the company if the name is recognizable.

Provide your reasoning in the 'reasoning' field, explaining why you gave that score and type.

You MUST return a JSON object with EXACTLY these fields:
- relevance_score: int (0-10)
- company_type: string (TECH, TECH_ADJACENT, or NON_TECH)
- has_internal_tech_team: boolean
- tech_team_signals: list of strings
- reasoning: string
`

type Classifier struct {
	llm *llm.Client
	db  *db.DB
}

func NewClassifier(llmClient *llm.Client, database *db.DB) *Classifier {
	return &Classifier{
		llm: llmClient,
		db:  database,
	}
}

func (c *Classifier) GetDB() *db.DB {
	return c.db
}

func (c *Classifier) ExtractInterestingLinks(ctx context.Context, markdown string, runID string) ([]string, error) {
	var result struct {
		Links []string `json:"links"`
	}
	req := llm.CompletionRequest{
		System: "Extract URLs from the provided text that are likely to contain contact information, about us, or recruitment details. Return ONLY a JSON object with a 'links' field.",
		User:   fmt.Sprintf("Markdown:\n\n%s", markdown),
	}
	err := c.llm.CompleteJSON(ctx, req, "extract_links", runID, &result)
	return result.Links, err
}

const SearchDiscoveryPrompt = `You are extracting the official company website and LinkedIn company page from search engine results.

Company Context:
- Name: %s
- City: %s
- SIREN: %s

STRICT RULES:
- website: the company's own domain. Skip directory sites (societe.com, pappers.fr, etc.).
- linkedin_url: MUST be the company page (linkedin.com/company/...).
- VERIFICATION: If the result is clearly for a different company or a different country (e.g. .br, .in when searching for a French company), leave it empty unless you are 100%% sure it's the right one.
- PREFER results that mention the target city.

Return a JSON object:
{
  "website": "...",
  "linkedin_url": "..."
}`

const PeopleSearchExtractionPrompt = `You are extracting a list of individual professionals and their LinkedIn profile URLs from search engine results.

Company Context:
- Name: %s
- City: %s

STRICT RULES:
- ONLY extract people who explicitly work for THIS company (%s).
- DO NOT invent, guess, or hallucinate names. If a name is not literally present in the search results, do NOT include it.
- SKIP people from other companies even if they appear in search results (e.g. if you see famous CTOs from Palantir or Google, and they don't work for the target company, IGNORE them).
- VERIFY company affiliation: if a person's LinkedIn snippet or search result doesn't clearly mention they work for %s, SKIP them.
- linkedin_url MUST be a full, absolute personal LinkedIn profile URL (https://www.linkedin.com/in/...).
- name and role are required.
- Focus on: CTO, Engineering Manager, HR, Recruitment, CEO, Founder, Tech Lead.
- If no real people are found in the search results, return an empty contacts list.

Return a JSON object with a single field "contacts" containing the list.`

func (c *Classifier) ExtractPeopleFromSearchResults(ctx context.Context, markdown string, comp db.Company, runID string) (PeoplePageData, error) {
	var result PeoplePageData
	req := llm.CompletionRequest{
		System: fmt.Sprintf(PeopleSearchExtractionPrompt, comp.Name, comp.City, comp.Name, comp.Name),
		User:   fmt.Sprintf("Search results (Markdown):\n\n%s", markdown),
	}
	err := c.llm.CompleteJSON(ctx, req, "extract_people_from_search", runID, &result)
	return result, err
}

func (c *Classifier) ExtractURLsFromSearch(ctx context.Context, searchResultMD string, comp db.Company, runID string) (string, string, error) {
	type searchResult struct {
		Website     string `json:"website"`
		LinkedinURL string `json:"linkedin_url"`
	}
	var res searchResult
	req := llm.CompletionRequest{
		System: fmt.Sprintf(SearchDiscoveryPrompt, comp.Name, comp.City, comp.Siren),
		User:   fmt.Sprintf("Search results (Markdown):\n\n%s", searchResultMD),
	}
	err := c.llm.CompleteJSON(ctx, req, "extract_urls_from_search", runID, &res)
	return res.Website, res.LinkedinURL, err
}
func (c *Classifier) ScoreCompany(ctx context.Context, comp db.Company, runID string) (CompanyScore, error) {
	currentInfo := ""
	if comp.CompanyType != "" && comp.CompanyType != "UNKNOWN" {
		currentInfo = fmt.Sprintf("\nHeuristic Classification: %s", comp.CompanyType)
	}

	prompt := fmt.Sprintf(`Company: %s
NAF: %s - %s
City: %s
Size: %s employees%s`,
		comp.Name,
		comp.NAFCode,
		comp.NAFLabel,
		comp.City,
		comp.HeadcountRange,
		currentInfo,
	)

	var score CompanyScore
	req := llm.CompletionRequest{
		System: ScoreSystemPrompt,
		User:   prompt,
	}

	err := c.llm.CompleteJSON(ctx, req, "score_company", runID, &score)
	if err != nil {
		return CompanyScore{}, err
	}

	// Update DB with score and reasoning
	err = c.db.UpdateCompany(comp.ID, map[string]interface{}{
		"relevance_score":        score.RelevanceScore,
		"company_type":           score.CompanyType,
		"has_internal_tech_team": score.HasInternalTechTeam,
		"tech_team_signals":      strings.Join(score.TechTeamSignals, ", "),
		"notes":                  fmt.Sprintf("%s | %s", comp.Notes, score.Reasoning),
	})

	return score, err
}
