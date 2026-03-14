package cmd

import (
	"context"
	"fmt"
	"jobhunter/internal/collector"
	"jobhunter/internal/db"
	"jobhunter/internal/enricher"
	"jobhunter/internal/llm"
	"jobhunter/internal/pipeline"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	depts       []string
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
		engine := pipeline.NewEngine(database)

		sirene := collector.NewSireneCollector(database, cfg.SireneParquetPath)
		
		steps := []pipeline.Step{
			{
				Name:    "scan_sirene",
				Timeout: 1 * time.Hour,
				Fn: func(ctx context.Context, run *pipeline.Run) error {
					total, new, err := sirene.Scan(ctx, depts, minHeadcount)
					if err != nil {
						return err
					}
					fmt.Printf("✓ SIRENE Scan complete: Found %d candidates, %d new added to DB.\n", total, new)
					return nil
				},
			},
			{
				Name:    "score_new",
				Timeout: 30 * time.Minute,
				Fn: func(ctx context.Context, run *pipeline.Run) error {
					return scoreUnscored(ctx, runID)
				},
			},
		}

		_, err := engine.Execute(context.Background(), runID, steps)
		if err != nil {
			log.Fatalf("Pipeline execution failed: %v", err)
		}
	},
}

var scoreCmd = &cobra.Command{
	Use:   "score",
	Short: "Score unscored companies in DB",
	Run: func(cmd *cobra.Command, args []string) {
		err := scoreUnscored(context.Background(), uuid.New().String())
		if err != nil {
			log.Fatalf("Scoring failed: %v", err)
		}
	},
}

func scoreUnscored(ctx context.Context, runID string) error {
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
		fmt.Println("No unscored companies found.")
		return nil
	}

	fmt.Printf("Scoring %d companies...\n", len(unscored))

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
		fmt.Printf("  Scoring %s...", c.Name)
		score, err := classifier.ScoreCompany(ctx, c, runID)
		if err != nil {
			fmt.Printf(" ❌ Error: %v\n", err)
			continue
		}
		fmt.Printf(" ✅ %s (Score: %d)\n", score.CompanyType, score.RelevanceScore)
	}

	return nil
}
