package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type SchedulerModel struct {
	StartTime time.Time
	LastRun   time.Time
	NextRun   time.Time
	Logs      []string
	
	width  int
	height int
}

func NewSchedulerModel() *SchedulerModel {
	return &SchedulerModel{
		StartTime: time.Now(),
		NextRun:   time.Now().Add(1 * time.Hour), // Example
	}
}

func (m SchedulerModel) Init() tea.Cmd {
	return nil
}

func (m SchedulerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m SchedulerModel) View() string {
	var s strings.Builder

	s.WriteString(TitleStyle.Render("JobHunter Scheduler"))
	s.WriteString("\n\n")

	s.WriteString(fmt.Sprintf(" System started: %s\n", m.StartTime.Format("15:04:05")))
	s.WriteString(fmt.Sprintf(" Next run in:    %s\n", time.Until(m.NextRun).Round(time.Second)))
	s.WriteString("\n")

	s.WriteString(Bold.Render(" Activity Log:") + "\n")
	for _, l := range m.Logs {
		s.WriteString(" " + l + "\n")
	}

	footer := "\n Press 'q' to quit"
	s.WriteString(DimStyle.Render(footer))

	return s.String()
}
