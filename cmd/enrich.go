package cmd

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/enricher"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"jobhunter/internal/tui"
	"log"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Channel for TUI logs
		logCh := make(chan tui.LogMsg, 100)
		
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
		
		logger, _ := zap.NewDevelopment()
		defer logger.Sync()

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

		var targetCompanies []db.Company

		for _, c := range companies {
			if c.Status == "NEW" && c.RelevanceScore > 0 {
				targetCompanies = append(targetCompanies, c)
			}
		}

		if len(targetCompanies) > batchSize {
			targetCompanies = targetCompanies[:batchSize]
		}

		if len(targetCompanies) == 0 {
			fmt.Println("No scored companies found for enrichment.")
			return
		}

		// Initialize TUI
		m := tui.NewPipelineModel(runID, logCh)
		m.Total = len(targetCompanies)
		p := tea.NewProgram(m)

		// Run enrichment in background
		go func() {
			for _, c := range targetCompanies {
				p.Send(tui.CompanyUpdateMsg{
					ID:     c.ID,
					Name:   c.Name,
					Step:   "Researching",
					Status: tui.StatusRunning,
				})

				err := enr.EnrichCompany(ctx, c.ID, runID)
				
				if err != nil {
					p.Send(tui.CompanyUpdateMsg{
						ID:      c.ID,
						Name:    c.Name,
						Step:    "Failed",
						Status:  tui.StatusError,
						Message: err.Error(),
					})
					logCh <- tui.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Failed to enrich %s: %v", c.Name, err)}
				} else {
					p.Send(tui.CompanyUpdateMsg{
						ID:     c.ID,
						Name:   c.Name,
						Step:   "Done",
						Status: tui.StatusDone,
					})
					logCh <- tui.LogMsg{Level: "INFO", Text: fmt.Sprintf("Successfully enriched %s", c.Name)}
				}
				
				// Small delay to avoid aggressive scraping
				time.Sleep(1 * time.Second)
			}
			p.Send(tui.PipelineDoneMsg{})
		}()

		if _, err := p.Run(); err != nil {
			log.Fatalf("TUI Error: %v", err)
		}
	},
}
