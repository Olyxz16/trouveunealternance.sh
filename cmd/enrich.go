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
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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

		// 1. Redirect logs to file immediately to keep terminal clean
		logFile, err := os.OpenFile("jobhunter.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to open log file: %v\n", err)
			os.Exit(1)
		}
		defer logFile.Close()
		log.SetOutput(logFile)

		// 2. Pre-flight: Get companies
		companies, err := database.GetCompaniesForEnrichment()
		if err != nil {
			log.Printf("Error: Failed to query database: %v", err)
			fmt.Fprintf(os.Stderr, "Error: Failed to query database: %v\n", err)
			os.Exit(1)
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

		// 3. Setup zap logger for the components
		encoderConfig := zap.NewDevelopmentEncoderConfig()
		core := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(logFile),
			zap.InfoLevel,
		)
		logger := zap.New(core)
		defer logger.Sync()

		// 4. Setup TUI and background worker
		logCh := make(chan tui.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		go func() {
			// Small delay to ensure TUI has started and received WindowSizeMsg
			time.Sleep(100 * time.Millisecond)
			p.Send(tui.TotalUpdateMsg(len(targetCompanies)))
			logCh <- tui.LogMsg{Level: "INFO", Text: "Initializing enrichment pipeline..."}

			// Setup LLM
			logCh <- tui.LogMsg{Level: "INFO", Text: "Connecting to LLM providers..."}
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

			var geminiAPI *llm.GeminiAPIProvider
			if cfg.GeminiAPIKey != "" {
				geminiAPI = llm.NewGeminiAPIProvider(cfg.GeminiAPIKey, cfg.GeminiAPIModel)
				logCh <- tui.LogMsg{Level: "INFO", Text: "Gemini API search grounding enabled for URL discovery"}
			} else {
				logCh <- tui.LogMsg{Level: "WARN", Text: "GEMINI_API_KEY not set — falling back to DuckDuckGo for discovery"}
			}

			// Setup Scraper
			logCh <- tui.LogMsg{Level: "INFO", Text: "Launching browser instance..."}
			httpFetcher := scraper.NewHTTPFetcher()
			browserFetcher, err := scraper.NewBrowserFetcher(
				cfg.BrowserCookiesPath,
				cfg.BrowserDisplay,
				cfg.BrowserHeadless,
				cfg.BrowserBinaryPath,
				logger,
			)
			if err != nil {
				logCh <- tui.LogMsg{Level: "WARN", Text: fmt.Sprintf("Browser failed: %v. Using HTTP only.", err)}
			} else {
				defer browserFetcher.Close()
				logCh <- tui.LogMsg{Level: "INFO", Text: "Browser ready."}
			}

			forceDomains := strings.Split(cfg.ForceBrowserDomains, ",")
			extractor := scraper.NewExtractor()
			cascade := scraper.NewCascadeFetcher(httpFetcher, browserFetcher, forceDomains, database, extractor, logger)
			enr := enricher.NewEnricher(database, cascade, classifier, geminiAPI)

			p.Send(tui.ReadyMsg{})
			logCh <- tui.LogMsg{Level: "INFO", Text: fmt.Sprintf("Enriching %d companies...", len(targetCompanies))}

			// Start Processing
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
				
				time.Sleep(1 * time.Second)
			}
			p.Send(tui.PipelineDoneMsg{})
		}()

		// Start TUI (blocks until exit)
		if _, err := p.Run(); err != nil {
			log.SetOutput(os.Stderr) // restore for final error
			log.Fatalf("TUI Error: %v", err)
		}
	},
}
