package enricher

type ContactCandidate struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	LinkedinURL string `json:"linkedin_url"`
	Email       string `json:"email"`
}

type ContactResult struct {
	Contacts []ContactCandidate `json:"contacts"`
	Best     *ContactCandidate  `json:"best"`
}

const ContactSelectionPrompt = `You are a technical recruiter. 
Given a list of employees found for a company, pick the BEST contact to reach out to for an internship in DevOps/Backend.

Company Type: %s
Potential Roles: CTO, Engineering Manager, Tech Lead, Technical Recruiter, HR Manager.

Return ONLY a JSON object with:
- contacts: the full list of candidates
- best: the single best candidate object, or null if none are suitable.
`
