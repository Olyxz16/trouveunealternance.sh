package cmd

import (
	"encoding/json"
	"fmt"
	"jobhunter/internal/config"
	"jobhunter/internal/db"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var enqueueCmd = &cobra.Command{
	Use:   "enqueue",
	Short: "Add jobs to the processing queue",
}

var enqueueCompanyCmd = &cobra.Command{
	Use:   "company",
	Short: "Enqueue a single company for enrichment",
	RunE: func(cmd *cobra.Command, args []string) error {
		companyID, _ := cmd.Flags().GetUint("id")
		if companyID == 0 {
			return fmt.Errorf("--id is required")
		}
		return enqueueJob("enrich_company", map[string]any{"company_id": companyID}, 0)
	},
}

var enqueueBatchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Enqueue multiple companies for enrichment",
	RunE: func(cmd *cobra.Command, args []string) error {
		count, _ := cmd.Flags().GetInt("count")
		if count <= 0 {
			return fmt.Errorf("--count must be > 0")
		}
		return enqueueBatchJobs(count)
	},
}

var enqueueDepartmentCmd = &cobra.Command{
	Use:   "department",
	Short: "Enqueue a department scan",
	RunE: func(cmd *cobra.Command, args []string) error {
		dept, _ := cmd.Flags().GetString("name")
		if dept == "" {
			return fmt.Errorf("--name is required")
		}
		limit, _ := cmd.Flags().GetInt("limit")
		return enqueueJob("scan_department", map[string]any{"department": dept, "limit": limit}, 0)
	},
}

var enqueueRegionCmd = &cobra.Command{
	Use:   "region",
	Short: "Enqueue a region scan",
	RunE: func(cmd *cobra.Command, args []string) error {
		region, _ := cmd.Flags().GetString("name")
		if region == "" {
			return fmt.Errorf("--name is required")
		}
		return enqueueJob("scan_region", map[string]any{"region": region}, 0)
	},
}

var enqueueRetryCmd = &cobra.Command{
	Use:   "retry-failed",
	Short: "Enqueue failed companies for re-enrichment",
	RunE: func(cmd *cobra.Command, args []string) error {
		maxAttempts, _ := cmd.Flags().GetInt("max-attempts")
		return enqueueJob("re_enrich_failed", map[string]any{"max_attempts": maxAttempts}, 0)
	},
}

var queueStatusCmd = &cobra.Command{
	Use:   "queue-status",
	Short: "Show the current job queue status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showQueueStatus()
	},
}

var queueListCmd = &cobra.Command{
	Use:   "queue-list",
	Short: "List jobs in the queue",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, _ := cmd.Flags().GetString("status")
		limit, _ := cmd.Flags().GetInt("limit")
		return listQueueJobs(status, limit)
	},
}

func init() {
	enqueueCompanyCmd.Flags().Uint("id", 0, "Company ID to enrich")
	enqueueBatchCmd.Flags().Int("count", 10, "Number of companies to enqueue")
	enqueueDepartmentCmd.Flags().String("name", "", "Department name/number")
	enqueueDepartmentCmd.Flags().Int("limit", 100, "Max companies to scan")
	enqueueRegionCmd.Flags().String("name", "", "Region name")
	enqueueRetryCmd.Flags().Int("max-attempts", 3, "Max retry attempts per company")
	queueListCmd.Flags().String("status", "", "Filter by status (pending, running, completed, failed)")
	queueListCmd.Flags().Int("limit", 50, "Max jobs to list")

	enqueueCmd.AddCommand(enqueueCompanyCmd, enqueueBatchCmd, enqueueDepartmentCmd, enqueueRegionCmd, enqueueRetryCmd)
	rootCmd.AddCommand(enqueueCmd, queueStatusCmd, queueListCmd)
}

func getQueueDB() (*db.QueueDB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable not set")
	}
	return db.NewQueueDB(dsn)
}

func enqueueJob(jobType string, payload map[string]any, priority int) error {
	queueDB, err := getQueueDB()
	if err != nil {
		return err
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	job := &db.QueueJob{
		ID:       uuid.New().String(),
		Type:     jobType,
		Status:   "pending",
		Payload:  string(payloadJSON),
		Priority: priority,
	}

	if err := queueDB.EnqueueJob(job); err != nil {
		return fmt.Errorf("failed to enqueue job: %w", err)
	}

	fmt.Printf("Job enqueued: %s (type: %s)\n", job.ID, jobType)
	return nil
}

func enqueueBatchJobs(count int) error {
	queueDB, err := getQueueDB()
	if err != nil {
		return err
	}

	// Get companies from the local SQLite database that need enrichment
	cfg := loadConfigForDB()
	localDB, err := db.NewDB(cfg, nil)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	var companies []db.Company
	if err := localDB.Where("status = ? AND company_type != 'NON_TECH'", "NEW").
		Order("relevance_score DESC").
		Limit(count).
		Find(&companies).Error; err != nil {
		return fmt.Errorf("failed to query companies: %w", err)
	}

	if len(companies) == 0 {
		fmt.Println("No companies found to enqueue")
		return nil
	}

	enqueued := 0
	for _, comp := range companies {
		payloadJSON, _ := json.Marshal(map[string]any{"company_id": comp.ID})
		job := &db.QueueJob{
			ID:       uuid.New().String(),
			Type:     "enrich_company",
			Status:   "pending",
			Payload:  string(payloadJSON),
			Priority: comp.RelevanceScore,
		}
		if err := queueDB.EnqueueJob(job); err != nil {
			fmt.Printf("Failed to enqueue company %d: %v\n", comp.ID, err)
			continue
		}
		enqueued++
	}

	fmt.Printf("Enqueued %d companies for enrichment\n", enqueued)
	return nil
}

func showQueueStatus() error {
	queueDB, err := getQueueDB()
	if err != nil {
		return err
	}

	stats, err := queueDB.GetJobQueueStats()
	if err != nil {
		return fmt.Errorf("failed to get queue stats: %w", err)
	}

	fmt.Println("Job Queue Status:")
	fmt.Println("=================")
	for status, count := range stats {
		fmt.Printf("  %-12s %d\n", status, count)
	}

	// Rate limits
	rls, err := queueDB.GetAllRateLimitStatus()
	if err == nil {
		fmt.Println("\nRate Limits:")
		fmt.Println("============")
		for _, rl := range rls {
			fmt.Printf("  %-12s %.1f/%.1f tokens, %d/%d daily\n",
				rl.ID, rl.Tokens, rl.MaxTokens, rl.DailyUsed, rl.DailyLimit)
		}
	}

	return nil
}

func listQueueJobs(status string, limit int) error {
	queueDB, err := getQueueDB()
	if err != nil {
		return err
	}

	jobs, err := queueDB.ListJobs(status, "", limit)
	if err != nil {
		return fmt.Errorf("failed to list jobs: %w", err)
	}

	if len(jobs) == 0 {
		fmt.Println("No jobs found")
		return nil
	}

	fmt.Printf("%-38s %-20s %-10s %-8s %-10s %s\n", "ID", "Type", "Status", "Priority", "Attempts", "Created")
	fmt.Println("----------------------------------------------------------------------------------------------------------------")
	for _, job := range jobs {
		created := job.CreatedAt.Format("2006-01-02 15:04")
		fmt.Printf("%-38s %-20s %-10s %-8d %-10d %s\n",
			job.ID, job.Type, job.Status, job.Priority, job.Attempts, created)
	}

	return nil
}

func loadConfigForDB() *config.Config {
	cfg := config.Load()
	return cfg
}
