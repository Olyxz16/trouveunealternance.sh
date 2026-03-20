package cmd

import (
	"context"
	"fmt"
	"io"
	"jobhunter/internal/db"
	"jobhunter/internal/enricher"
	"jobhunter/internal/llm"
	"jobhunter/internal/pipeline"
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

var (
	batchSize int
	companyID int
	noTUI     bool
)

func init() {
	enrichCmd.Flags().IntVarP(&batchSize, "batch", "b", 10, "Number of companies to enrich")
	enrichCmd.Flags().IntVarP(&companyID, "id", "i", 0, "Specific company ID to enrich")
	enrichCmd.Flags().BoolVar(&noTUI, "no-tui", false, "Disable TUI and log to stdout")
	rootCmd.AddCommand(enrichCmd)
}

type TUIReporter struct {
	program *tea.Program
	noTUI   bool
	logCh   chan<- pipeline.LogMsg
}

func (r *TUIReporter) Update(upd pipeline.ProgressUpdate) {
	if !r.noTUI {
		r.program.Send(upd)
	}
}

func (r *TUIReporter) Log(msg pipeline.LogMsg) {
	if !r.noTUI {
		r.logCh <- msg
	} else {
		log.Printf("%s: %s", msg.Level, msg.Text)
	}
}

type tuiLogCore struct {
	zapcore.LevelEnabler
	logCh chan<- pipeline.LogMsg
}

func (c *tuiLogCore) With(fields []zapcore.Field) zapcore.Core { return c }
func (c *tuiLogCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}
func (c *tuiLogCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	c.logCh <- pipeline.LogMsg{
		Level: entry.Level.CapitalString(),
		Text:  entry.Message,
	}
	return nil
}
func (c *tuiLogCore) Sync() error { return nil }

var enrichCmd = &cobra.Command{
	Use:   "enrich",
	Short: "Enrich companies with website and contact info",
	Run: func(cmd *cobra.Command, args []string) {
		runID := uuid.New().String()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 1. Redirect logs to file immediately to keep terminal clean
		var logWriter io.Writer
		logFile, err := os.OpenFile("jobhunter.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to open log file: %v\n", err)
			os.Exit(1)
		}
		defer logFile.Close()
		
		if noTUI {
			logWriter = io.MultiWriter(os.Stdout, logFile)
		} else {
			logWriter = logFile
		}
		log.SetOutput(logWriter)

		// 2. Pre-flight: Get companies
		var targetCompanies []db.Company
		if companyID != 0 {
			c, err := database.GetCompany(uint(companyID))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: Failed to get company %d: %v\n", companyID, err)
				os.Exit(1)
			}
			targetCompanies = append(targetCompanies, *c)
		} else {
			companies, err := database.GetCompaniesForEnrichment()
			if err != nil {
				log.Printf("Error: Failed to query database: %v", err)
				fmt.Fprintf(os.Stderr, "Error: Failed to query database: %v\n", err)
				os.Exit(1)
			}

			for _, c := range companies {
				if c.Status == "NEW" && c.RelevanceScore > 0 {
					targetCompanies = append(targetCompanies, c)
				}
			}

			if len(targetCompanies) > batchSize {
				targetCompanies = targetCompanies[:batchSize]
			}
		}

		if len(targetCompanies) == 0 {
			fmt.Println("No scored companies found for enrichment.")
			return
		}

		// 4. Setup TUI and background worker
		logCh := make(chan pipeline.LogMsg, 100)
		m := tui.NewPipelineModel(runID, logCh)
		p := tea.NewProgram(m, tea.WithAltScreen())

		// 3. Setup zap logger for the components
		encoderConfig := zap.NewDevelopmentEncoderConfig()
		fileCore := zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig),
			zapcore.AddSync(logFile),
			zap.InfoLevel,
		)
		
		var core zapcore.Core
		if noTUI {
			core = fileCore
		} else {
			tuiCore := &tuiLogCore{
				LevelEnabler: zap.InfoLevel,
				logCh:        logCh,
			}
			core = zapcore.NewTee(fileCore, tuiCore)
		}
		
		logger := zap.New(core)
		defer logger.Sync()

		worker := func() {
			reporter := &TUIReporter{program: p, noTUI: noTUI, logCh: logCh}

			// Small delay to ensure TUI has started and received WindowSizeMsg
			if !noTUI {
				time.Sleep(100 * time.Millisecond)
				p.Send(tui.TotalUpdateMsg(len(targetCompanies)))
			}
			reporter.Log(pipeline.LogMsg{Level: "INFO", Text: "Initializing enrichment pipeline..."})

			// Setup LLM
			reporter.Log(pipeline.LogMsg{Level: "INFO", Text: "Connecting to LLM providers..."})
			primary, fallback := llm.InitProviders(cfg.LLMPrimary, cfg.LLMFallback, cfg)

			llmClient := llm.NewClient(primary, fallback, cfg.OpenRouterRPM, database)
			classifier := enricher.NewClassifier(llmClient, database)

			var geminiAPI *llm.GeminiAPIProvider
			if cfg.GeminiAPIKey != "" {
				geminiAPI = llm.NewGeminiAPIProvider(cfg.GeminiAPIKey, cfg.GeminiAPIModel)
				reporter.Log(pipeline.LogMsg{Level: "INFO", Text: "Gemini API search grounding enabled for URL discovery"})
			} else {
				reporter.Log(pipeline.LogMsg{Level: "WARN", Text: "GEMINI_API_KEY not set — falling back to DuckDuckGo for discovery"})
			}

			// Setup Scraper
			reporter.Log(pipeline.LogMsg{Level: "INFO", Text: "Launching browser instance..."})
			httpFetcher := scraper.NewHTTPFetcher()
			browserFetcher, err := scraper.NewBrowserFetcher(
				cfg.BrowserCookiesPath,
				cfg.BrowserDisplay,
				cfg.BrowserHeadless,
				cfg.BrowserBinaryPath,
				logger,
			)
			if err != nil {
				reporter.Log(pipeline.LogMsg{Level: "WARN", Text: fmt.Sprintf("Browser failed: %v. Using HTTP only.", err)})
			} else {
				defer browserFetcher.Close()
				reporter.Log(pipeline.LogMsg{Level: "INFO", Text: "Browser ready."})
			}

			forceDomains := strings.Split(cfg.ForceBrowserDomains, ",")
			extractor := scraper.NewExtractor()
			cascade := scraper.NewCascadeFetcher(httpFetcher, browserFetcher, forceDomains, database, extractor, logger)
			enr := enricher.NewEnricher(database, cascade, classifier, geminiAPI)
			enr.SetReporter(reporter)

			if !noTUI {
				p.Send(tui.ReadyMsg{})
			}
			reporter.Log(pipeline.LogMsg{Level: "INFO", Text: fmt.Sprintf("Enriching %d companies...", len(targetCompanies))})

			// Start Processing
			for _, c := range targetCompanies {
				err := enr.EnrichCompany(ctx, c.ID, runID)
				
				if err != nil {
					reporter.Update(pipeline.ProgressUpdate{
						ID:      int(c.ID),
						Name:    c.Name,
						Step:    "Failed",
						Status:  pipeline.StatusError,
						Message: err.Error(),
					})
					reporter.Log(pipeline.LogMsg{Level: "ERROR", Text: fmt.Sprintf("Failed to enrich %s: %v", c.Name, err)})
				} else {
					reporter.Update(pipeline.ProgressUpdate{
						ID:     int(c.ID),
						Name:   c.Name,
						Step:   "Done",
						Status: pipeline.StatusDone,
					})
					reporter.Log(pipeline.LogMsg{Level: "INFO", Text: fmt.Sprintf("Successfully enriched %s", c.Name)})
				}
				
				time.Sleep(1 * time.Second)
			}
			if !noTUI {
				p.Send(tui.PipelineDoneMsg{})
			}
		}

		if noTUI {
			worker()
		} else {
			go worker()
			// Start TUI (blocks until exit)
			if _, err := p.Run(); err != nil {
				log.SetOutput(os.Stderr) // restore for final error
				log.Fatalf("TUI Error: %v", err)
			}
		}
	},
}
