package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"jobhunter/internal/db"
	"jobhunter/internal/eval"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	evalBatch  int
	evalAll    bool
	evalJSON   bool
	evalOutput string
)

func init() {
	evalCmd.Flags().IntVarP(&evalBatch, "batch", "b", 20, "Number of companies to evaluate")
	evalCmd.Flags().BoolVarP(&evalAll, "all", "a", false, "Evaluate all companies in DB (ignores --batch)")
	evalCmd.Flags().BoolVar(&evalJSON, "json", false, "Output only JSON report to stdout")
	evalCmd.Flags().StringVar(&evalOutput, "output", "data/eval", "Directory to save JSON report")
	rootCmd.AddCommand(evalCmd)
}

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Evaluate enrichment accuracy of companies in DB",
	Run: func(cmd *cobra.Command, args []string) {
		start := time.Now()

		var companies []db.Company
		var err error

		if evalAll {
			companies, err = getEnrichedCompaniesAll()
		} else {
			companies, err = getEnrichedCompanies(evalBatch)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to fetch companies: %v\n", err)
			os.Exit(1)
		}

		if len(companies) == 0 {
			fmt.Println("No enriched companies found for evaluation.")
			return
		}

		userName := loadUserName()
		userLinkedInURL := loadUserLinkedInURL()
		commitHash := getCommitHash()

		var evaluations []eval.CompanyEvaluation
		var companyScores []eval.CompanyScore
		var contactScores []eval.ContactScore
		var allPenalties [][]eval.Penalty
		totalContacts := 0

		for _, comp := range companies {
			contacts, err := database.GetContacts(comp.ID)
			if err != nil {
				contacts = nil
			}

			cScore := eval.ScoreCompany(&comp)
			ctScore, penalties := eval.ScoreContacts(&comp, contacts, userName, userLinkedInURL)

			companyScores = append(companyScores, cScore)
			contactScores = append(contactScores, ctScore)
			allPenalties = append(allPenalties, penalties)
			totalContacts += len(contacts)

			var contactDetails []string
			for _, c := range contacts {
				detail := fmt.Sprintf("%s (%s)", c.Name, c.Role)
				if c.Email != "" {
					detail += fmt.Sprintf(" — %s", c.Email)
				}
				contactDetails = append(contactDetails, detail)
			}

			evaluations = append(evaluations, eval.CompanyEvaluation{
				ID:               comp.ID,
				Name:             comp.Name,
				CompanyScore:     cScore.Total,
				CompanyBreakdown: cScore.Breakdown,
				ContactScore:     ctScore.Total,
				ContactsCount:    len(contacts),
				ContactDetails:   contactDetails,
				Penalties:        penalties,
				Status:           comp.Status,
			})
		}

		aggregate := eval.ComputeAggregate(companyScores, contactScores, allPenalties, totalContacts)

		filterMetrics := getFilterMetrics()

		metadata := eval.ReportMetadata{
			LLMPrimaryProvider:  cfg.LLMPrimary,
			LLMPrimaryModel:     cfg.OpenRouterModel,
			LLMFallbackProvider: cfg.LLMFallback,
			LLMFallbackModel:    cfg.GeminiAPIModel,
			GeminiAPIEnabled:    cfg.GeminiAPIKey != "",
			GeminiModel:         cfg.GeminiAPIModel,
			BrowserEnabled:      !cfg.BrowserHeadless || cfg.BrowserCookiesPath != "",
			BatchSize:           len(companies),
			DurationSeconds:     time.Since(start).Seconds(),
			CommitHash:          commitHash,
		}

		runID := uuid.New().String()
		report := eval.GenerateReport(runID, metadata, evaluations, aggregate)

		if evalJSON {
			out, _ := json.MarshalIndent(report, "", "  ")
			fmt.Println(string(out))
			return
		}

		printScorecard(report, filterMetrics)

		path, err := eval.SaveReport(report, evalOutput)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save report: %v\n", err)
		} else {
			fmt.Printf("\nFull report saved to: %s\n", path)
		}
	},
}

func getAllCompanies() ([]db.Company, error) {
	var companies []db.Company
	err := database.Order("id").Find(&companies).Error
	return companies, err
}

func getEnrichedCompaniesAll() ([]db.Company, error) {
	var companies []db.Company
	err := database.Where("status != 'NEW'").Order("id").Find(&companies).Error
	return companies, err
}

func getEnrichedCompanies(limit int) ([]db.Company, error) {
	var companies []db.Company
	err := database.Where("status IN ('TO_CONTACT', 'NO_CONTACT_FOUND')").Order("id").Limit(limit).Find(&companies).Error
	return companies, err
}

type filterMetrics struct {
	TotalCompanies int64
	NewUnscored    int64
	ScoredZero     int64
	ScoredPositive int64
	SkippedNonTech int64
}

func getFilterMetrics() filterMetrics {
	var m filterMetrics
	database.Model(&db.Company{}).Count(&m.TotalCompanies)
	database.Model(&db.Company{}).Where("status = 'NEW' AND relevance_score = 0").Count(&m.NewUnscored)
	database.Model(&db.Company{}).Where("status = 'NEW' AND relevance_score > 0").Count(&m.ScoredPositive)
	database.Model(&db.Company{}).Where("status = 'NEW' AND relevance_score = 0").Count(&m.ScoredZero)
	m.SkippedNonTech = m.TotalCompanies - m.NewUnscored - m.ScoredZero - m.ScoredPositive
	return m
}

func loadUserName() string {
	data, err := os.ReadFile("profile.json")
	if err != nil {
		return ""
	}
	var profile struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return ""
	}
	return profile.Name
}

func loadUserLinkedInURL() string {
	data, err := os.ReadFile("profile.json")
	if err != nil {
		return ""
	}
	var profile struct {
		LinkedInURL string `json:"linkedin_url"`
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return ""
	}
	return profile.LinkedInURL
}

func getCommitHash() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func printScorecard(report eval.Report, fm filterMetrics) {
	m := report.Aggregate
	meta := report.Metadata

	fmt.Println("=== Enrichment Accuracy Report ===")
	fmt.Printf("Run: %s | Batch: %d | Duration: %.0fs | Commit: %s\n",
		report.RunID[:8], meta.BatchSize, meta.DurationSeconds, meta.CommitHash)
	fmt.Printf("Models: %s/%s + gemini/%s (search=%v)\n\n",
		meta.LLMPrimaryProvider, meta.LLMPrimaryModel,
		meta.GeminiModel, meta.GeminiAPIEnabled)

	fmt.Println("Pipeline Filter Metrics:")
	fmt.Printf("  Total companies in DB:    %d\n", fm.TotalCompanies)
	fmt.Printf("  NEW (unscored):           %d (skipped by enrich)\n", fm.NewUnscored)
	fmt.Printf("  NEW (scored 0, skipped):  %d (deemed irrelevant)\n", fm.ScoredZero)
	fmt.Printf("  NEW (scored >0, eligible):%d\n", fm.ScoredPositive)
	fmt.Printf("  Already enriched:         %d\n\n", fm.TotalCompanies-fm.NewUnscored-fm.ScoredZero-fm.ScoredPositive)

	fmt.Println("Company Benchmark (website + linkedin + careers):")
	fmt.Printf("  Average: %.1f/100  |  Pass Rate: %.0f%%\n",
		m.CompanyBenchmark.AverageScore,
		m.CompanyBenchmark.PassRate*100)

	// Company breakdown stats
	var websiteFound, websiteResolves, linkedinFound, careersFound, careersResolves int
	for _, e := range report.Companies {
		if e.CompanyBreakdown.WebsiteFound {
			websiteFound++
		}
		if e.CompanyBreakdown.WebsiteResolves {
			websiteResolves++
		}
		if e.CompanyBreakdown.LinkedinFound {
			linkedinFound++
		}
		if e.CompanyBreakdown.CareersPageFound {
			careersFound++
		}
		if e.CompanyBreakdown.CareersResolves {
			careersResolves++
		}
	}
	n := len(report.Companies)
	fmt.Printf("  Website found: %d/%d (%.0f%%)  |  Resolves: %d/%d (%.0f%%)\n",
		websiteFound, n, pct(websiteFound, n),
		websiteResolves, n, pct(websiteResolves, n))
	fmt.Printf("  LinkedIn found: %d/%d (%.0f%%)\n",
		linkedinFound, n, pct(linkedinFound, n))
	fmt.Printf("  Careers page found: %d/%d (%.0f%%)  |  Resolves: %d/%d (%.0f%%)\n\n",
		careersFound, n, pct(careersFound, n),
		careersResolves, n, pct(careersResolves, n))

	fmt.Println("Contact Benchmark (PRIMARY):")
	fmt.Printf("  Average: %.1f/100  |  Valid Rate: %.0f%%\n\n",
		m.ContactBenchmark.AverageScore,
		m.ContactBenchmark.ValidRate*100)

	fmt.Println("Penalties:")
	for ptype, count := range m.PenaltyBreakdown {
		fmt.Printf("  %-25s %d\n", ptype+":", count)
	}
	if m.TotalPenalties == 0 {
		fmt.Println("  None")
	}

	fmt.Println()

	type scored struct {
		eval.CompanyEvaluation
	}
	var sorted []scored
	for _, e := range report.Companies {
		sorted = append(sorted, scored{e})
	}

	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].ContactScore > sorted[i].ContactScore {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	if len(sorted) > 0 {
		fmt.Println("Top performers:")
		n := minInt(3, len(sorted))
		for i := 0; i < n; i++ {
			fmt.Printf("  %d. %-20s — %d pts\n", i+1, truncate(sorted[i].Name, 20), sorted[i].ContactScore)
		}

		fmt.Println("\nWorst performers:")
		start := len(sorted) - 3
		if start < 0 {
			start = 0
		}
		for i := start; i < len(sorted); i++ {
			fmt.Printf("  %d. %-20s — %d pts", i+1, truncate(sorted[i].Name, 20), sorted[i].ContactScore)
			if len(sorted[i].Penalties) > 0 {
				pTypes := make([]string, 0)
				for _, p := range sorted[i].Penalties {
					pTypes = append(pTypes, p.Type)
				}
				fmt.Printf(" (%s)", strings.Join(pTypes, ", "))
			}
			fmt.Println()
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func pct(num, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(num) / float64(total) * 100
}
