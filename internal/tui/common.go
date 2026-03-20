package tui

import (
	"jobhunter/internal/pipeline"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	Accent   = lipgloss.Color("#4af0a0") // matches index.html --accent
	Accent2  = lipgloss.Color("#4ab8f0")
	Warn     = lipgloss.Color("#f0c44a")
	Danger   = lipgloss.Color("#f04a6e")
	Dim      = lipgloss.Color("#4a5268")
	Surface  = lipgloss.Color("#13161b")

	TagTech     = lipgloss.NewStyle().Foreground(Accent).Border(lipgloss.RoundedBorder()).Padding(0, 1)
	TagAdjacent = lipgloss.NewStyle().Foreground(Accent2).Border(lipgloss.RoundedBorder()).Padding(0, 1)
	Bold        = lipgloss.NewStyle().Bold(true)
	DimStyle    = lipgloss.NewStyle().Foreground(Dim)
	TitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(Accent).MarginBottom(1)
)

// WaitForLog is a Cmd that waits for a log message on a channel.
func WaitForLog(ch <-chan pipeline.LogMsg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}
