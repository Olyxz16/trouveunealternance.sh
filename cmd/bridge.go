package cmd

import (
	"jobhunter/internal/scraper"
	"log"

	"github.com/spf13/cobra"
)

var bridgePort int

func init() {
	bridgeCmd.Flags().IntVarP(&bridgePort, "port", "p", 3000, "Port to listen on")
	rootCmd.AddCommand(bridgeCmd)
}

var bridgeCmd = &cobra.Command{
	Use:   "bridge",
	Short: "Start the MCP Browser Bridge (Chrome automation)",
	Run: func(cmd *cobra.Command, args []string) {
		server := scraper.NewMCPServer(bridgePort)
		if err := server.Start(); err != nil {
			log.Fatalf("MCP Bridge failed: %v", err)
		}
	},
}
