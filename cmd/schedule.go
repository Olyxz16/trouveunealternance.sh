package cmd

import (
	"jobhunter/internal/tui"
	"log"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(scheduleCmd)
}

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Start the persistent scheduler",
	Run: func(cmd *cobra.Command, args []string) {
		m := tui.NewSchedulerModel()
		p := tea.NewProgram(m)

		if _, err := p.Run(); err != nil {
			log.Fatalf("TUI Error: %v", err)
		}
	},
}
