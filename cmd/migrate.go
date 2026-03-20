package cmd

import (
	"fmt"
	"log"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(migrateCmd)
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Run database migrations",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Running database migrations...")
		err := database.Migrate()
		if err != nil {
			log.Fatalf("Migration failed: %v", err)
		}
		fmt.Println("✓ Migrations completed successfully.")
	},
}
