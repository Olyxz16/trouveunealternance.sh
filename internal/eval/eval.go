package eval

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"jobhunter/internal/db"
)

var (
	directorySites = []string{
		"societe.com", "pappers.fr", "annuaire", "societeinfo.com",
		"verif.com", "societe.com", "entreprise.data.gouv.fr",
		"pagesjaunes.fr", "lefigaro.fr", "bfmtv.com",
	}
	genericEmailPrefixes = []string{
		"contact@", "info@", "noreply@", "no-reply@", "admin@",
		"support@", "hello@", "sales@", "marketing@", "webmaster@",
		"postmaster@", "abuse@", "careers@", "jobs@", "rh@", "recrutement@",
	}
	placeholderNames = []string{
		"john doe", "jane smith", "jane doe", "john smith",
		"mike smith", "test user", "unknown", "n/a", "",
	}
)

type CompanyBreakdown struct {
	WebsiteFound     bool `json:"website_found"`
	WebsiteResolves  bool `json:"website_resolves"`
	LinkedinFound    bool `json:"linkedin_found"`
	CareersPageFound bool `json:"careers_page_found"`
	CareersResolves  bool `json:"careers_resolves"`
}

type CompanyScore struct {
	Breakdown CompanyBreakdown `json:"breakdown"`
	Total     int              `json:"total"`
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type ContactBreakdown struct {
	ContactFound  bool `json:"contact_found"`
	PrimarySet    bool `json:"primary_set"`
	LinkedinValid bool `json:"linkedin_valid"`
	EmailValid    bool `json:"email_valid"`
	RoleValid     bool `json:"role_valid"`
	ConfidenceOK  bool `json:"confidence_ok"`
}

type ContactScore struct {
	Breakdown ContactBreakdown `json:"breakdown"`
	Total     int              `json:"total"`
}

type Penalty struct {
	Type        string `json:"type"`
	ContactName string `json:"contact_name"`
	Details     string `json:"details"`
	Points      int    `json:"points"`
}

type CompanyEvaluation struct {
	ID               uint             `json:"id"`
	Name             string           `json:"name"`
	CompanyScore     int              `json:"company_score"`
	CompanyBreakdown CompanyBreakdown `json:"company_breakdown"`
	ContactScore     int              `json:"contact_score"`
	ContactsCount    int              `json:"contacts_count"`
	ContactDetails   []string         `json:"contact_details"`
	Penalties        []Penalty        `json:"penalties"`
	Status           string           `json:"status"`
}

type AggregateMetrics struct {
	CompanyBenchmark struct {
		AverageScore float64 `json:"average_score"`
		PassRate     float64 `json:"pass_rate"`
	} `json:"company_benchmark"`
	ContactBenchmark struct {
		AverageScore float64 `json:"average_score"`
		ValidRate    float64 `json:"valid_rate"`
	} `json:"contact_benchmark"`
	HallucinationRate float64        `json:"hallucination_rate"`
	TotalPenalties    int            `json:"total_penalties"`
	PenaltyBreakdown  map[string]int `json:"penalty_breakdown"`
}

type ReportMetadata struct {
	LLMPrimaryProvider  string  `json:"llm_primary_provider"`
	LLMPrimaryModel     string  `json:"llm_primary_model"`
	LLMFallbackProvider string  `json:"llm_fallback_provider"`
	LLMFallbackModel    string  `json:"llm_fallback_model"`
	GeminiAPIEnabled    bool    `json:"gemini_api_enabled"`
	GeminiModel         string  `json:"gemini_model"`
	BrowserEnabled      bool    `json:"browser_enabled"`
	BatchSize           int     `json:"batch_size"`
	DurationSeconds     float64 `json:"duration_seconds"`
	CommitHash          string  `json:"commit_hash"`
}

type Report struct {
	RunID     string              `json:"run_id"`
	Timestamp string              `json:"timestamp"`
	Metadata  ReportMetadata      `json:"metadata"`
	Aggregate AggregateMetrics    `json:"aggregate"`
	Companies []CompanyEvaluation `json:"companies"`
}

func ScoreCompany(comp *db.Company) CompanyScore {
	score := CompanyScore{
		Breakdown: CompanyBreakdown{},
	}

	if isValidWebsite(comp.Website) {
		score.Breakdown.WebsiteFound = true
		score.Total += 30
		if urlResolves(comp.Website) {
			score.Breakdown.WebsiteResolves = true
			score.Total += 20
		}
	}

	if isValidCompanyLinkedIn(comp.LinkedinURL) {
		score.Breakdown.LinkedinFound = true
		score.Total += 30
	}

	if isValidURL(comp.CareersPageURL) {
		score.Breakdown.CareersPageFound = true
		score.Total += 10
		if urlResolves(comp.CareersPageURL) {
			score.Breakdown.CareersResolves = true
			score.Total += 10
		}
	}

	return score
}

func isValidURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme != "" && u.Host != ""
}

func urlResolves(raw string) bool {
	if raw == "" {
		return false
	}
	resp, err := httpClient.Head(raw)
	if err != nil {
		req, _ := http.NewRequest("GET", raw, nil)
		if req != nil {
			req.Header.Set("User-Agent", "Mozilla/5.0")
			resp, err = httpClient.Do(req)
		}
	}
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}

func ScoreContacts(comp *db.Company, contacts []db.Contact, userName string, userLinkedInURL string) (ContactScore, []Penalty) {
	score := ContactScore{
		Breakdown: ContactBreakdown{},
	}
	var penalties []Penalty

	if len(contacts) == 0 {
		return score, penalties
	}

	score.Breakdown.ContactFound = true
	score.Total += 15

	if comp.PrimaryContactID > 0 {
		score.Breakdown.PrimarySet = true
		score.Total += 15
	}

	var primaryContact *db.Contact
	for i := range contacts {
		if contacts[i].ID == comp.PrimaryContactID {
			primaryContact = &contacts[i]
			break
		}
	}

	if primaryContact == nil && len(contacts) > 0 {
		primaryContact = &contacts[0]
	}

	if primaryContact != nil {
		if isValidPersonalLinkedIn(primaryContact.LinkedinURL) {
			score.Breakdown.LinkedinValid = true
			score.Total += 20
		} else if primaryContact.LinkedinURL != "" {
			penalties = append(penalties, Penalty{
				Type:        "invalid_linkedin_url",
				ContactName: primaryContact.Name,
				Details:     fmt.Sprintf("invalid format: %s", primaryContact.LinkedinURL),
				Points:      -10,
			})
		}

		if isValidContactEmail(primaryContact.Email) {
			score.Breakdown.EmailValid = true
			score.Total += 25
		} else if primaryContact.Email != "" {
			if isGenericEmail(primaryContact.Email) {
				penalties = append(penalties, Penalty{
					Type:        "generic_email",
					ContactName: primaryContact.Name,
					Details:     primaryContact.Email,
					Points:      -15,
				})
			}
		}

		if isValidRole(primaryContact.Role) {
			score.Breakdown.RoleValid = true
			score.Total += 10
		}

		if primaryContact.Confidence == "probable" || primaryContact.Confidence == "verified" {
			score.Breakdown.ConfidenceOK = true
			score.Total += 15
		}
	}

	for i := range contacts {
		c := &contacts[i]
		if c.Confidence == "hallucinated" {
			penalties = append(penalties, Penalty{
				Type:        "hallucinated_contact",
				ContactName: c.Name,
				Details:     "confidence marked as hallucinated",
				Points:      -20,
			})
		}

		if isPlaceholderName(c.Name) {
			penalties = append(penalties, Penalty{
				Type:        "placeholder_name",
				ContactName: c.Name,
				Details:     "name matches known placeholder pattern",
				Points:      -20,
			})
		}

		if isUserAsContact(c, userName, userLinkedInURL) {
			penalties = append(penalties, Penalty{
				Type:        "user_as_contact",
				ContactName: c.Name,
				Details:     "contact matches user profile",
				Points:      -50,
			})
		}

		if isGenericEmail(c.Email) && c.Email != "" {
			alreadyPenalized := false
			for _, p := range penalties {
				if p.Type == "generic_email" && p.ContactName == c.Name {
					alreadyPenalized = true
					break
				}
			}
			if !alreadyPenalized {
				penalties = append(penalties, Penalty{
					Type:        "generic_email",
					ContactName: c.Name,
					Details:     c.Email,
					Points:      -15,
				})
			}
		}

		if c.LinkedinURL != "" && !isValidPersonalLinkedIn(c.LinkedinURL) {
			if strings.Contains(strings.ToLower(c.LinkedinURL), "/company/") {
				penalties = append(penalties, Penalty{
					Type:        "invalid_linkedin_url",
					ContactName: c.Name,
					Details:     fmt.Sprintf("company URL instead of personal: %s", c.LinkedinURL),
					Points:      -10,
				})
			}
		}
	}

	for _, p := range penalties {
		score.Total += p.Points
	}

	return score, penalties
}

func isValidWebsite(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "" || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	for _, ds := range directorySites {
		if strings.Contains(host, ds) {
			return false
		}
	}
	return true
}

func isValidCompanyLinkedIn(raw string) bool {
	if raw == "" {
		return false
	}
	re := regexp.MustCompile(`(?i)linkedin\.com/company/`)
	return re.MatchString(raw)
}

func isValidPersonalLinkedIn(raw string) bool {
	if raw == "" {
		return false
	}
	re := regexp.MustCompile(`(?i)linkedin\.com/in/`)
	return re.MatchString(raw)
}

func isValidContactEmail(raw string) bool {
	if raw == "" {
		return false
	}
	re := regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	if !re.MatchString(raw) {
		return false
	}
	return !isGenericEmail(raw)
}

func isGenericEmail(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	for _, prefix := range genericEmailPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isValidRole(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "n/a") || strings.EqualFold(trimmed, "unknown") {
		return false
	}
	return true
}

func isPlaceholderName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, ph := range placeholderNames {
		if lower == ph {
			return true
		}
	}
	return false
}

func isUserAsContact(c *db.Contact, userName string, userLinkedInURL string) bool {
	if userName == "" || userName == "Your Name" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(userName)) {
		return true
	}
	userParts := strings.Fields(strings.ToLower(userName))
	contactParts := strings.Fields(strings.ToLower(c.Name))
	if len(userParts) >= 2 && len(contactParts) >= 2 {
		if userParts[0] == contactParts[0] && userParts[len(userParts)-1] == contactParts[len(contactParts)-1] {
			return true
		}
	}

	if userLinkedInURL != "" && c.LinkedinURL != "" {
		return normalizeLinkedIn(c.LinkedinURL) == normalizeLinkedIn(userLinkedInURL)
	}

	return false
}

func normalizeLinkedIn(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	u = strings.TrimRight(u, "/")
	if idx := strings.Index(u, "?"); idx >= 0 {
		u = u[:idx]
	}
	return u
}

func ComputeAggregate(companyScores []CompanyScore, contactScores []ContactScore, allPenalties [][]Penalty, totalContacts int) AggregateMetrics {
	m := AggregateMetrics{
		PenaltyBreakdown: make(map[string]int),
	}

	if len(companyScores) == 0 {
		return m
	}

	var companySum int
	var companyPass int
	for _, cs := range companyScores {
		companySum += cs.Total
		if cs.Total >= 80 {
			companyPass++
		}
	}
	m.CompanyBenchmark.AverageScore = float64(companySum) / float64(len(companyScores))
	m.CompanyBenchmark.PassRate = float64(companyPass) / float64(len(companyScores))

	var contactSum int
	var contactValid int
	for _, cs := range contactScores {
		contactSum += cs.Total
		if cs.Total >= 60 {
			contactValid++
		}
	}
	if len(contactScores) > 0 {
		m.ContactBenchmark.AverageScore = float64(contactSum) / float64(len(contactScores))
		m.ContactBenchmark.ValidRate = float64(contactValid) / float64(len(contactScores))
	}

	var hallucinated int
	for _, penSlice := range allPenalties {
		for _, p := range penSlice {
			m.TotalPenalties++
			m.PenaltyBreakdown[p.Type]++
			if p.Type == "hallucinated_contact" {
				hallucinated++
			}
		}
	}

	if totalContacts > 0 {
		m.HallucinationRate = float64(hallucinated) / float64(totalContacts)
	}

	return m
}

func GenerateReport(
	runID string,
	metadata ReportMetadata,
	evaluations []CompanyEvaluation,
	aggregate AggregateMetrics,
) Report {
	return Report{
		RunID:     runID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Metadata:  metadata,
		Aggregate: aggregate,
		Companies: evaluations,
	}
}

func SaveReport(report Report, outputDir string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	ts := time.Now().UTC().Format("2006-01-02-150405")
	filename := fmt.Sprintf("eval-%s-%s.json", ts, report.RunID[:8])
	path := filepath.Join(outputDir, filename)

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal report: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write report: %w", err)
	}

	return path, nil
}
