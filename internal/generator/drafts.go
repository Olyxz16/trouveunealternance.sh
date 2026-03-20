package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/llm"
	"strings"
)

type Draft struct {
	Type    string `json:"type"` // "email" | "linkedin"
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body"`
}

type DraftSet struct {
	Email    Draft `json:"email"`
	Linkedin Draft `json:"linkedin"`
}

type Generator struct {
	db  *db.DB
	llm *llm.Client
}

func NewGenerator(database *db.DB, llmClient *llm.Client) *Generator {
	return &Generator{db: database, llm: llmClient}
}

const DraftGenerationPrompt = `You are a career coach helping a student apply for a DevOps/backend internship.
Your task is to generate a highly personalized cold email and LinkedIn connection request.

Company Context:
- Name: %s
- Type: %s
- Tech Stack: %s

Recipient:
- Name: %s
- Role: %s

My Profile:
- Name: %s
- School: %s
- Skills: %s
- Projects: %s
- Availability: %s for %s

Angle Matrix:
- TECH company + CTO/Lead: Be technically specific, reference their stack.
- TECH company + HR: Focus on impact, team-fit, and growth.
- TECH_ADJACENT + IT Director: Emphasize internal tooling, adaptability, and infra interest.

Return a JSON object with:
{
  "email": { "subject": "...", "body": "..." },
  "linkedin": { "body": "..." }
}
`

func (g *Generator) GenerateForContact(ctx context.Context, profile Profile, companyID uint, contactID uint, runID string) (DraftSet, error) {
	comp, err := g.db.GetCompany(companyID)
	if err != nil {
		return DraftSet{}, err
	}

	contacts, err := g.db.GetContacts(companyID)
	if err != nil {
		return DraftSet{}, err
	}

	var targetContact *db.Contact
	for _, c := range contacts {
		if c.ID == contactID {
			targetContact = &c
			break
		}
	}
	if targetContact == nil {
		return DraftSet{}, fmt.Errorf("contact %d not found", contactID)
	}

	// TODO: Handle marshaling error properly when drafting logic is finalized.
	projectsJSON, _ := json.Marshal(profile.Projects)

	req := llm.CompletionRequest{
		System: fmt.Sprintf(DraftGenerationPrompt,
			comp.Name, comp.CompanyType, comp.TechStack,
			targetContact.Name, targetContact.Role,
			profile.Name, profile.School, strings.Join(profile.Skills, ", "),
			string(projectsJSON), profile.Availability, profile.Duration,
		),
		User: "Generate the drafts now.",
	}


	var result DraftSet
	err = g.llm.CompleteJSON(ctx, req, "generate_drafts", runID, &result)
	if err != nil {
		return DraftSet{}, err
	}

	return result, nil
}
