package cmd

import (
	"jobhunter/internal/api"
	"log"

	"github.com/spf13/cobra"
)

var dashboardAddr string

func init() {
	dashboardCmd.Flags().StringVarP(&dashboardAddr, "addr", "a", ":8080", "Address to listen on")
	rootCmd.AddCommand(dashboardCmd)
}

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Start the web dashboard",
	Run: func(cmd *cobra.Command, args []string) {
		server := api.NewServer(database)
		if err := server.Start(dashboardAddr); err != nil {
			log.Fatalf("Dashboard failed: %v", err)
		}
	},
}
