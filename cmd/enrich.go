package cmd

import (
	"context"
	"fmt"
	"jobhunter/internal/enricher"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var batchSize int

func init() {
	enrichCmd.Flags().IntVarP(&batchSize, "batch", "b", 10, "Number of companies to enrich")
	rootCmd.AddCommand(enrichCmd)
}

var enrichCmd = &cobra.Command{
	Use:   "enrich",
	Short: "Enrich companies with website and contact info",
	Run: func(cmd *cobra.Command, args []string) {
		runID := uuid.New().String()
		ctx := context.Background()

		// Setup Logger
		logger, _ := zap.NewDevelopment()
		defer logger.Sync()

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

		// Setup Scraper
		httpFetcher := scraper.NewHTTPFetcher()
		
		browserFetcher, err := scraper.NewBrowserFetcher(
			cfg.BrowserCookiesPath,
			cfg.BrowserDisplay,
			cfg.BrowserHeadless,
			cfg.BrowserBinaryPath,
			logger,
		)
		if err != nil {
			log.Fatalf("Failed to start browser: %v", err)
		}
		defer browserFetcher.Close()

		forceDomains := strings.Split(cfg.ForceBrowserDomains, ",")
		extractor := scraper.NewExtractor()
		
		cascade := scraper.NewCascadeFetcher(httpFetcher, browserFetcher, forceDomains, database, extractor, logger)
		
		enr := enricher.NewEnricher(database, cascade, classifier)

		// Get companies to enrich: status = 'NEW' AND relevance_score > 0
		companies, err := database.GetCompaniesForEnrichment()
		if err != nil {
			log.Fatalf("Failed to get companies: %v", err)
		}

		count := 0
		for _, c := range companies {
			if count >= batchSize {
				break
			}
			// Only enrich if it's new and scored
			if c.Status != "NEW" || c.RelevanceScore == 0 {
				continue
			}

			fmt.Printf("▶ Enriching [%d/%d] %s...\n", count+1, batchSize, c.Name)
			err := enr.EnrichCompany(ctx, c.ID, runID)
			if err != nil {
				fmt.Printf("  ❌ Error: %v\n", err)
			} else {
				fmt.Printf("  ✅ Done\n")
			}
			count++
			
			// Small delay to avoid aggressive scraping
			time.Sleep(2 * time.Second)
		}

		fmt.Printf("\n✓ Enrichment complete. Processed %d companies.\n", count)
	},
}
