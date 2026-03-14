package cmd

import (
	"fmt"
	"jobhunter/internal/config"
	"jobhunter/internal/db"
	"log"
	"os"

	"github.com/spf13/cobra"
)

var (
	cfg *config.Config
	database *db.DB
)

var rootCmd = &cobra.Command{
	Use:   "jobhunter",
	Short: "JobHunter Go - Reworked from Python POC",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cfg = config.Load()
		var err error
		database, err = db.NewDB(cfg.DBPath)
		if err != nil {
			log.Fatalf("Failed to initialize database: %v", err)
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
