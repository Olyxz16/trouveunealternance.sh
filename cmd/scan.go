package cmd

import (
	"context"
	"fmt"
	"jobhunter/internal/collector"
	"jobhunter/internal/db"
	"jobhunter/internal/enricher"
	"jobhunter/internal/llm"
	"jobhunter/internal/pipeline"
	"jobhunter/internal/tui"
	"log"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	depts        []string
	minHeadcount int
)

func init() {
	scanCmd.Flags().StringSliceVarP(&depts, "dept", "d", []string{"86"}, "Department codes to scan")
	scanCmd.Flags().IntVarP(&minHeadcount, "min-hc", "m", 5, "Minimum headcount")
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(scoreCmd)
}

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan SIRENE dataset for tech companies",
	Run: func(cmd *cobra.Command, args []string) {
		runID := uuid.New().String()
		
		// Channel for TUI logs
		logCh := make(chan tui.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m)

		engine := pipeline.NewEngine(database)
		sirene := collector.NewSireneCollector(database, cfg.SireneParquetPath, cfg.SireneULParquetPath)
		
		go func() {
			steps := []pipeline.Step{
				{
					Name:    "scan_sirene",
					Timeout: 1 * time.Hour,
					Fn: func(ctx context.Context, run *pipeline.Run) error {
						logCh <- tui.LogMsg{Level: "INFO", Text: "Starting SIRENE scan..."}
						total, new, err := sirene.Scan(ctx, depts, minHeadcount)
						if err != nil {
							return err
						}
						logCh <- tui.LogMsg{Level: "INFO", Text: fmt.Sprintf("SIRENE Scan complete: Found %d candidates, %d new.", total, new)}
						return nil
					},
				},
				{
					Name:    "score_new",
					Timeout: 30 * time.Minute,
					Fn: func(ctx context.Context, run *pipeline.Run) error {
						return scoreUnscoredWithTUI(ctx, runID, p, logCh)
					},
				},
			}

			_, err := engine.Execute(context.Background(), runID, steps)
			if err != nil {
				logCh <- tui.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Pipeline failed: %v", err)}
			}
			p.Send(tui.PipelineDoneMsg{})
		}()

		if _, err := p.Run(); err != nil {
			log.Fatalf("TUI Error: %v", err)
		}
	},
}

var scoreCmd = &cobra.Command{
	Use:   "score",
	Short: "Score unscored companies in DB",
	Run: func(cmd *cobra.Command, args []string) {
		runID := uuid.New().String()
		logCh := make(chan tui.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m)

		go func() {
			err := scoreUnscoredWithTUI(context.Background(), runID, p, logCh)
			if err != nil {
				logCh <- tui.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Scoring failed: %v", err)}
			}
			p.Send(tui.PipelineDoneMsg{})
		}()

		if _, err := p.Run(); err != nil {
			log.Fatalf("TUI Error: %v", err)
		}
	},
}

func scoreUnscoredWithTUI(ctx context.Context, runID string, p *tea.Program, logCh chan tui.LogMsg) error {
	companies, err := database.GetCompaniesForEnrichment()
	if err != nil {
		return err
	}

	var unscored []db.Company
	for _, c := range companies {
		if c.RelevanceScore == 0 && c.Status == "NEW" {
			unscored = append(unscored, c)
		}
	}

	if len(unscored) == 0 {
		logCh <- tui.LogMsg{Level: "INFO", Text: "No unscored companies found."}
		return nil
	}

	logCh <- tui.LogMsg{Level: "INFO", Text: fmt.Sprintf("Scoring %d companies...", len(unscored))}
	
	// We can't easily update the 'Total' in the model from here without some changes
	// but we can send updates for each company.

	// Setup LLM
	var primary, fallback llm.Provider
	if cfg.LLMPrimary == "openrouter" {
		primary = llm.NewOpenRouterProvider(cfg.OpenRouterAPIKey, cfg.OpenRouterModel)
	} else {
		primary = llm.NewGeminiCLIProvider(cfg.GeminiCLIPath)
	}

	if cfg.LLMFallback == "gemini_cli" {
		fallback = llm.NewGeminiCLIProvider(cfg.GeminiCLIPath)
	} else if cfg.LLMFallback == "openrouter" {
		fallback = llm.NewOpenRouterProvider(cfg.OpenRouterAPIKey, cfg.OpenRouterModel)
	}

	llmClient := llm.NewClient(primary, fallback, cfg.OpenRouterRPM, database)
	classifier := enricher.NewClassifier(llmClient, database)

	for _, c := range unscored {
		p.Send(tui.CompanyUpdateMsg{
			ID:     c.ID,
			Name:   c.Name,
			Step:   "Scoring",
			Status: tui.StatusRunning,
		})

		score, err := classifier.ScoreCompany(ctx, c, runID)
		if err != nil {
			p.Send(tui.CompanyUpdateMsg{
				ID:      c.ID,
				Name:    c.Name,
				Step:    "Failed",
				Status:  tui.StatusError,
				Message: err.Error(),
			})
			logCh <- tui.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Failed to score %s: %v", c.Name, err)}
			continue
		}

		p.Send(tui.CompanyUpdateMsg{
			ID:     c.ID,
			Name:   c.Name,
			Step:   fmt.Sprintf("Scored: %d", score.RelevanceScore),
			Status: tui.StatusDone,
		})
	}

	return nil
}
