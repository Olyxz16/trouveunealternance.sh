package tui

import (
	"fmt"
	"jobhunter/internal/db"

	"github.com/charmbracelet/lipgloss"
)

func RenderStats(stats db.Stats) string {
	title := TitleStyle.Render("JobHunter Stats")

	// Job Stats Table
	jobHeader := Bold.Render("Jobs")
	jobTable := fmt.Sprintf(
		"Total: %d\nNew Today: %d",
		stats.TotalJobs, stats.NewJobsToday,
	)

	// Prospect Stats Table
	prospectHeader := Bold.Render("Prospects")
	prospectTable := fmt.Sprintf(
		"Total: %d\nNew Today: %d",
		stats.TotalProspects, stats.NewProspectsToday,
	)

	// Status Breakdown
	statusHeader := Bold.Render("Prospects by Status")
	statusContent := ""
	for status, count := range stats.ProspectsByStatus {
		statusContent += fmt.Sprintf("%-12s: %d\n", status, count)
	}

	// Layout
	col1 := lipgloss.JoinVertical(lipgloss.Left, jobHeader, jobTable, "", prospectHeader, prospectTable)
	col2 := lipgloss.JoinVertical(lipgloss.Left, statusHeader, statusContent)

	content := lipgloss.JoinHorizontal(lipgloss.Top, col1, "    ", col2)
	
	return lipgloss.JoinVertical(lipgloss.Left, title, content)
}
