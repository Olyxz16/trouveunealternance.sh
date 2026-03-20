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
		logCh := make(chan pipeline.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		reporter := &TUIReporter{program: p, noTUI: false, logCh: logCh}
		engine := pipeline.NewEngine(database)
		engine.SetReporter(reporter)

		sirene := collector.NewSireneCollector(database, cfg)
		
		go func() {
			time.Sleep(100 * time.Millisecond)
			steps := []pipeline.Step{
				{
					Name:    "scan_sirene",
					Timeout: 1 * time.Hour,
					Fn: func(ctx context.Context, run *pipeline.Run) error {
						total, new, err := sirene.Scan(ctx, depts, minHeadcount)
						if err != nil {
							return err
						}
						reporter.Log(pipeline.LogMsg{Level: "INFO", Text: fmt.Sprintf("SIRENE Scan results: Found %d candidates, %d new.", total, new)})
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
				// Handled by engine logging
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
		logCh := make(chan pipeline.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		go func() {
			time.Sleep(100 * time.Millisecond)
			err := scoreUnscoredWithTUI(context.Background(), runID, p, logCh)
			if err != nil {
				logCh <- pipeline.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Scoring failed: %v", err)}
			}
			p.Send(tui.PipelineDoneMsg{})
		}()

		if _, err := p.Run(); err != nil {
			log.SetOutput(os.Stderr)
			log.Fatalf("TUI Error: %v", err)
		}
	},
}

func scoreUnscoredWithTUI(ctx context.Context, runID string, p *tea.Program, logCh chan pipeline.LogMsg) error {
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
		logCh <- pipeline.LogMsg{Level: "INFO", Text: "No unscored companies found."}
		return nil
	}

	logCh <- pipeline.LogMsg{Level: "INFO", Text: fmt.Sprintf("Scoring %d companies...", len(unscored))}
	p.Send(tui.TotalUpdateMsg(len(unscored)))
	
	// Setup LLM
	primary, fallback := llm.InitProviders(cfg.LLMPrimary, cfg.LLMFallback, cfg)

	llmClient := llm.NewClient(primary, fallback, cfg.OpenRouterRPM, database, nil)
	classifier := enricher.NewClassifier(llmClient, database)

	for _, c := range unscored {
		p.Send(pipeline.ProgressUpdate{
			ID:     int(c.ID),
			Name:   c.Name,
			Step:   "Scoring",
			Status: pipeline.StatusRunning,
		})

		score, err := classifier.ScoreCompany(ctx, c, runID)
		if err != nil {
			p.Send(pipeline.ProgressUpdate{
				ID:      int(c.ID),
				Name:    c.Name,
				Step:    "Failed",
				Status:  pipeline.StatusError,
				Message: err.Error(),
			})
			logCh <- pipeline.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Failed to score %s: %v", c.Name, err)}
			continue
		}

		p.Send(pipeline.ProgressUpdate{
			ID:     int(c.ID),
			Name:   c.Name,
			Step:   fmt.Sprintf("Scored: %d", score.RelevanceScore),
			Status: pipeline.StatusDone,
		})
	}

	return nil
}
