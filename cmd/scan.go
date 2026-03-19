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
	"os"
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
		
		// 1. Redirect logs to file
		logFile, err := os.OpenFile("jobhunter.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to open log file: %v\n", err)
			os.Exit(1)
		}
		defer logFile.Close()
		log.SetOutput(logFile)

		// 2. Setup TUI
		logCh := make(chan tui.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		engine := pipeline.NewEngine(database)
		sirene := collector.NewSireneCollector(database, cfg.SireneParquetPath, cfg.SireneULParquetPath)
		
		go func() {
			time.Sleep(100 * time.Millisecond)
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
			log.SetOutput(os.Stderr)
			log.Fatalf("TUI Error: %v", err)
		}
	},
}

var scoreCmd = &cobra.Command{
	Use:   "score",
	Short: "Score unscored companies in DB",
	Run: func(cmd *cobra.Command, args []string) {
		runID := uuid.New().String()
		
		// 1. Redirect logs to file
		logFile, err := os.OpenFile("jobhunter.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to open log file: %v\n", err)
			os.Exit(1)
		}
		defer logFile.Close()
		log.SetOutput(logFile)

		// 2. Setup TUI
		logCh := make(chan tui.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		go func() {
			time.Sleep(100 * time.Millisecond)
			err := scoreUnscoredWithTUI(context.Background(), runID, p, logCh)
			if err != nil {
				logCh <- tui.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Scoring failed: %v", err)}
			}
			p.Send(tui.PipelineDoneMsg{})
		}()

		if _, err := p.Run(); err != nil {
			log.SetOutput(os.Stderr)
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
	p.Send(tui.TotalUpdateMsg(len(unscored)))
	
	// Setup LLM
	primary, fallback := llm.InitProviders(cfg.LLMPrimary, cfg.LLMFallback, cfg)

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
