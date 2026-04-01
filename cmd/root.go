package cmd

import (
	"fmt"
	"jobhunter/internal/config"
	"jobhunter/internal/db"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var (
	cfg      *config.Config
	database *db.DB
	zLogger  *zap.Logger
)

var rootCmd = &cobra.Command{
	Use:   "jobhunter",
	Short: "JobHunter Go - Reworked from Python POC",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cfg = config.Load()

		// Initialize zap logger
		var err error
		if os.Getenv("DEBUG") != "" {
			zLogger, _ = zap.NewDevelopment()
		} else {
			zLogger, _ = zap.NewProduction()
		}

		database, err = db.NewDB(cfg, zLogger)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to initialize database: %v\n", err)
			os.Exit(1)
		}

		if err := database.Migrate(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to migrate database: %v\n", err)
			os.Exit(1)
		}

	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if zLogger != nil {
			_ = zLogger.Sync()
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
