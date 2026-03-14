package enricher

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/llm"
	"log"
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
- NON_TECH: No meaningful internal tech team or tech needs.

Scoring (0-10):
- 10: Perfect fit (DevOps/Cloud product, major tech company).
- 8-9: Very good (Tech services, large tech team in a product company).
- 5-7: Good (Tech_adjacent but with clear tech signals, or smaller IT services).
- 1-4: Poor (Low relevance but some tech).
- 0: Completely irrelevant (NO internal tech).

A company classified as TECH or TECH_ADJACENT should NEVER have a 0 score.
If you see NAF 62xx or 63xx, it is TECH by definition.

IMPORTANT: Even if the Description is empty, you MUST provide an assessment based on the Company Name and NAF code. Do not ask for more information. Use your internal knowledge about the company if the name is recognizable.

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

func (c *Classifier) ScoreCompany(ctx context.Context, comp db.Company, runID string) (CompanyScore, error) {
	currentInfo := ""
	if comp.CompanyType != "" && comp.CompanyType != "UNKNOWN" {
		currentInfo = fmt.Sprintf("\nHeuristic Classification: %s", comp.CompanyType)
	}

	prompt := fmt.Sprintf(`Company: %s
NAF: %s - %s
City: %s
Size: %s employees
Description: %s%s`,
		comp.Name,
		comp.NAFCode.String,
		comp.NAFLabel.String,
		comp.City.String,
		comp.HeadcountRange.String,
		comp.Description.String,
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

	// Normalize CompanyType to match DB constraint
	score.CompanyType = strings.ToUpper(strings.ReplaceAll(score.CompanyType, "-", "_"))
	
	// Validate against allowed values to avoid DB error
	switch score.CompanyType {
	case "TECH", "TECH_ADJACENT", "NON_TECH":
		// OK
	default:
		log.Printf("Warning: LLM returned invalid company_type '%s', defaulting to UNKNOWN", score.CompanyType)
		score.CompanyType = "UNKNOWN"
	}

	// Apply caps and defaults from PLAN.md
	if score.CompanyType == "TECH_ADJACENT" && score.RelevanceScore > 7 {
		score.RelevanceScore = 7
	}

	// Update DB
	updates := map[string]interface{}{
		"relevance_score":         score.RelevanceScore,
		"company_type":            score.CompanyType,
		"has_internal_tech_team":  score.HasInternalTechTeam,
		"tech_team_signals":      strings.Join(score.TechTeamSignals, ", "),
		"notes":                   fmt.Sprintf("%s | %s", comp.Notes.String, score.Reasoning),
		"status":                  "NEW",
	}

	if score.CompanyType == "NON_TECH" {
		updates["status"] = "NOT_TECH"
	}

	err = c.db.UpdateCompany(comp.ID, updates)
	if err != nil {
		return score, fmt.Errorf("failed to update company in DB: %w", err)
	}

	return score, nil
}
