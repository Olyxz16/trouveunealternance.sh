package cmd

import (
	"fmt"
	"jobhunter/internal/tui"
	"log"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(statsCmd)
}

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show current database stats",
	Run: func(cmd *cobra.Command, args []string) {
		stats, err := database.GetStats()
		if err != nil {
			log.Fatalf("Failed to get stats: %v", err)
		}
		fmt.Println(tui.RenderStats(stats))
	},
}
