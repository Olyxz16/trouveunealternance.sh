package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"jobhunter/internal/config"
	"jobhunter/internal/db"
	"jobhunter/internal/enricher"
	"jobhunter/internal/llm"
	"jobhunter/internal/scraper"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Start a queue worker that processes enrichment jobs",
	RunE: func(cmd *cobra.Command, args []string) error {
		listen, _ := cmd.Flags().GetBool("listen")
		processN, _ := cmd.Flags().GetInt("process")

		if !listen && processN == 0 {
			return fmt.Errorf("specify --listen for continuous mode or --process=N for batch mode")
		}

		return runWorker(listen, processN)
	},
}

func init() {
	workerCmd.Flags().Bool("listen", false, "Run worker continuously, polling for jobs")
	workerCmd.Flags().Int("process", 0, "Process N jobs and exit")
	rootCmd.AddCommand(workerCmd)
}

func runWorker(listen bool, processN int) error {
	cfg := config.Load()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable not set")
	}

	queueDB, err := db.NewQueueDB(dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to queue database: %w", err)
	}

	logger, _ := zap.NewProduction()
	defer logger.Sync()

	workerID := fmt.Sprintf("worker-%d", time.Now().UnixNano())
	logger.Info("Worker starting", zap.String("worker_id", workerID))

	processed := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("Received signal, shutting down...", zap.String("signal", sig.String()))
		cancel()
	}()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Worker stopped", zap.Int("processed", processed))
			return nil
		default:
		}

		job, err := queueDB.DequeueJob(workerID)
		if err != nil {
			logger.Error("Failed to dequeue job", zap.Error(err))
			time.Sleep(5 * time.Second)
			continue
		}

		if job == nil {
			if !listen {
				logger.Info("No more jobs to process", zap.Int("processed", processed))
				return nil
			}
			time.Sleep(2 * time.Second)
			continue
		}

		logger.Info("Processing job",
			zap.String("job_id", job.ID),
			zap.String("type", job.Type),
			zap.Int("attempt", job.Attempts+1))

		err = processJob(ctx, queueDB, cfg, job, workerID, logger)
		if err != nil {
			logger.Error("Job failed",
				zap.String("job_id", job.ID),
				zap.Error(err))
			if ferr := queueDB.FailJob(job.ID, err.Error()); ferr != nil {
				logger.Error("Failed to update job status", zap.Error(ferr))
			}
		} else {
			logger.Info("Job completed", zap.String("job_id", job.ID))
			if ferr := queueDB.CompleteJob(job.ID); ferr != nil {
				logger.Error("Failed to update job status", zap.Error(ferr))
			}
		}

		processed++
		if processN > 0 && processed >= processN {
			logger.Info("Processed target number of jobs", zap.Int("processed", processed))
			return nil
		}
	}
}

func processJob(ctx context.Context, queueDB *db.QueueDB, cfg *config.Config, job *db.QueueJob, workerID string, logger *zap.Logger) error {
	switch job.Type {
	case "enrich_company":
		return processEnrichCompany(ctx, queueDB, cfg, job, workerID, logger)
	case "discover_urls":
		return processDiscoverURLs(ctx, queueDB, cfg, job, workerID, logger)
	case "re_enrich_failed":
		return processReEnrichFailed(ctx, queueDB, cfg, job, workerID, logger)
	default:
		return fmt.Errorf("unknown job type: %s", job.Type)
	}
}

func processEnrichCompany(ctx context.Context, queueDB *db.QueueDB, cfg *config.Config, job *db.QueueJob, workerID string, logger *zap.Logger) error {
	var payload struct {
		CompanyID uint `json:"company_id"`
	}
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("invalid job payload: %w", err)
	}

	onCooldown, nextAllowed, err := queueDB.IsCompanyOnCooldown(payload.CompanyID)
	if err != nil {
		logger.Warn("Failed to check cooldown", zap.Error(err))
	}
	if onCooldown {
		logger.Info("Company on cooldown, skipping",
			zap.Uint("company_id", payload.CompanyID),
			zap.Time("next_allowed", *nextAllowed))
		return queueDB.FailJob(job.ID, fmt.Sprintf("on cooldown until %s", nextAllowed.Format(time.RFC3339)))
	}

	err = runEnrichment(ctx, cfg, payload.CompanyID, workerID, logger)

	cooldownStatus := "success"
	if err != nil {
		cooldownStatus = "failed"
	}
	if cerr := queueDB.SetCompanyCooldown(payload.CompanyID, 24*time.Hour, cooldownStatus); cerr != nil {
		logger.Warn("Failed to set cooldown", zap.Error(cerr))
	}

	return err
}

func processDiscoverURLs(ctx context.Context, queueDB *db.QueueDB, cfg *config.Config, job *db.QueueJob, workerID string, logger *zap.Logger) error {
	var payload struct {
		CompanyIDs []uint `json:"company_ids"`
	}
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("invalid job payload: %w", err)
	}

	for _, companyID := range payload.CompanyIDs {
		onCooldown, _, err := queueDB.IsCompanyOnCooldown(companyID)
		if err != nil {
			logger.Warn("Failed to check cooldown", zap.Error(err))
			continue
		}
		if onCooldown {
			continue
		}

		err = runEnrichment(ctx, cfg, companyID, workerID, logger)
		if err != nil {
			logger.Warn("URL discovery failed", zap.Uint("company_id", companyID), zap.Error(err))
		}

		status := "success"
		if err != nil {
			status = "failed"
		}
		if cerr := queueDB.SetCompanyCooldown(companyID, 1*time.Hour, status); cerr != nil {
			logger.Warn("Failed to set cooldown", zap.Error(cerr))
		}
	}

	return nil
}

func processReEnrichFailed(ctx context.Context, queueDB *db.QueueDB, cfg *config.Config, job *db.QueueJob, workerID string, logger *zap.Logger) error {
	logger.Info("re_enrich_failed job type - creates sub-jobs for each failed company")
	return nil
}

func runEnrichment(ctx context.Context, cfg *config.Config, companyID uint, runID string, logger *zap.Logger) error {
	database, err := db.NewDB(cfg, logger)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	primary, fallback := llm.InitProviders(cfg.LLMPrimary, cfg.LLMFallback, cfg, logger)
	llmCacheEnabled := cfg.Cache.LLMResponses.Enabled
	llmCacheTTL := cfg.Cache.LLMResponses.TTLHours
	llmClient := llm.NewClientWithCache(primary, fallback, cfg.OpenRouterRPM, database, logger, llmCacheEnabled, llmCacheTTL)

	browserFetcher, err := scraper.NewBrowserFetcher(
		cfg.BrowserCookiesPath,
		cfg.BrowserDisplay,
		cfg.BrowserHeadless,
		cfg.BrowserBinaryPath,
		logger,
		cfg,
	)
	if err != nil {
		return fmt.Errorf("failed to init browser: %w", err)
	}
	defer browserFetcher.Close()

	httpFetcher := scraper.NewHTTPFetcher()
	forceDomains := []string{}
	if cfg.ForceBrowserDomains != "" {
		for _, d := range []string{cfg.ForceBrowserDomains} {
			forceDomains = append(forceDomains, d)
		}
	}
	cascade := scraper.NewCascadeFetcher(httpFetcher, browserFetcher, forceDomains, database, nil, logger, cfg)

	var geminiAPI *llm.GeminiAPIProvider
	if cfg.GeminiAPIKey != "" {
		geminiAPI = llm.NewGeminiAPIProvider(cfg.GeminiAPIKey, cfg.GeminiAPIModel, logger)
	}

	classifier := enricher.NewClassifier(llmClient, database)
	enrich := enricher.NewEnricher(database, cfg, cascade, classifier, geminiAPI, logger, "")

	return enrich.EnrichCompany(ctx, companyID, runID)
}
