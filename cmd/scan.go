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
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var (
	depts        []string
	minHeadcount int
	scanNoTUI    bool
	scoreNoTUI   bool
)

func init() {
	scanCmd.Flags().StringSliceVarP(&depts, "dept", "d", []string{"86"}, "Department codes to scan")
	scanCmd.Flags().IntVarP(&minHeadcount, "min-hc", "m", 5, "Minimum headcount")
	scanCmd.Flags().BoolVar(&scanNoTUI, "no-tui", false, "Disable TUI and log to stdout")
	scoreCmd.Flags().BoolVar(&scoreNoTUI, "no-tui", false, "Disable TUI and log to stdout")
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(scoreCmd)
}

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan SIRENE dataset for tech companies",
	Run: func(cmd *cobra.Command, args []string) {
		runID := uuid.New().String()

		logFile, err := os.OpenFile("jobhunter.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to open log file: %v\n", err)
			os.Exit(1)
		}
		defer logFile.Close()

		sirene := collector.NewSireneCollector(database, cfg)

		if scanNoTUI {
			fmt.Println("Scanning SIRENE dataset...")
			total, new, err := sirene.Scan(context.Background(), depts, minHeadcount)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Scan error: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("SIRENE Scan results: Found %d candidates, %d new.\n", total, new)

			fmt.Println("Scoring companies...")
			err = runScoring(context.Background(), runID, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Scoring error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Done.")
			return
		}

		logCh := make(chan pipeline.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		reporter := &TUIReporter{program: p, noTUI: false, logCh: logCh}
		engine := pipeline.NewEngine(database)
		engine.SetReporter(reporter)

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
						return runScoring(ctx, runID, p)
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
			zLogger.Fatal("TUI Error", zap.Error(err))
		}
	},
}

var scoreCmd = &cobra.Command{
	Use:   "score",
	Short: "Score unscored companies in DB",
	Run: func(cmd *cobra.Command, args []string) {
		runID := uuid.New().String()

		logFile, err := os.OpenFile("jobhunter.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to open log file: %v\n", err)
			os.Exit(1)
		}
		defer logFile.Close()

		if scoreNoTUI {
			err := runScoring(context.Background(), runID, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Scoring error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		logCh := make(chan pipeline.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		go func() {
			time.Sleep(100 * time.Millisecond)
			err := runScoring(context.Background(), runID, p)
			if err != nil {
				logCh <- pipeline.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Scoring failed: %v", err)}
			}
			p.Send(tui.PipelineDoneMsg{})
		}()

		if _, err := p.Run(); err != nil {
			zLogger.Fatal("TUI Error", zap.Error(err))
		}
	},
}

func runScoring(ctx context.Context, runID string, p *tea.Program) error {
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
		if p != nil {
			p.Send(tui.TotalUpdateMsg(0))
		}
		fmt.Println("No unscored companies found.")
		return nil
	}

	fmt.Printf("Scoring %d companies...\n", len(unscored))

	if p != nil {
		p.Send(tui.TotalUpdateMsg(len(unscored)))
	}

	primary, fallback := llm.InitProviders(cfg.LLMPrimary, cfg.LLMFallback, cfg, zLogger)
	llmClient := llm.NewClient(primary, fallback, cfg.OpenRouterRPM, database, zLogger)
	classifier := enricher.NewClassifier(llmClient, database)

	scored := 0
	skipped := 0
	failed := 0

	for _, c := range unscored {
		if p != nil {
			p.Send(pipeline.ProgressUpdate{
				ID:     int(c.ID),
				Name:   c.Name,
				Step:   "Scoring",
				Status: pipeline.StatusRunning,
			})
		}

		score, err := classifier.ScoreCompany(ctx, c, runID)
		if err != nil {
			failed++
			if p != nil {
				p.Send(pipeline.ProgressUpdate{
					ID:      int(c.ID),
					Name:    c.Name,
					Step:    "Failed",
					Status:  pipeline.StatusError,
					Message: err.Error(),
				})
			}
			fmt.Fprintf(os.Stderr, "Failed to score %s: %v\n", c.Name, err)
			continue
		}

		if score.RelevanceScore > 0 && score.CompanyType != "NON_TECH" {
			scored++
		} else {
			skipped++
		}

		if p != nil {
			p.Send(pipeline.ProgressUpdate{
				ID:     int(c.ID),
				Name:   c.Name,
				Step:   fmt.Sprintf("%s (%d)", score.CompanyType, score.RelevanceScore),
				Status: pipeline.StatusDone,
			})
		}

		if (scored+skipped+failed)%10 == 0 {
			fmt.Printf("  Scored: %d, Skipped: %d, Failed: %d, Total: %d/%d\n", scored, skipped, failed, scored+skipped+failed, len(unscored))
		}
	}

	fmt.Printf("Done. Scored: %d, Skipped (score 0 or NON_TECH): %d, Failed: %d\n", scored, skipped, failed)
	return nil
}
