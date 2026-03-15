package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type PipelineStatus string

const (
	StatusRunning PipelineStatus = "running"
	StatusDone    PipelineStatus = "done"
	StatusError   PipelineStatus = "error"
	StatusPending PipelineStatus = "pending"
)

type CompanyRow struct {
	ID      int
	Name    string
	Step    string
	Status  PipelineStatus
	Message string
}

type PipelineModel struct {
	RunID     string
	StartTime time.Time
	Companies []CompanyRow
	Logs      []LogMsg
	LogChan   <-chan LogMsg
	
	spinner  spinner.Model
	progress progress.Model
	viewport viewport.Model
	width    int
	height   int
	
	Total     int
	Processed int
	Success   int
	Failed    int
	
	Done bool
}

func NewPipelineModel(runID string, logChan <-chan LogMsg) *PipelineModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(Accent)

	return &PipelineModel{
		RunID:     runID,
		StartTime: time.Now(),
		LogChan:   logChan,
		spinner:   s,
		progress:  progress.New(progress.WithDefaultGradient()),
		viewport:  viewport.New(0, 0),
	}
}

func (m PipelineModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, WaitForLog(m.LogChan))
}

func (m PipelineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = 10 // Fixed height for logs at bottom
		m.progress.Width = msg.Width - 4

	case LogMsg:
		m.Logs = append(m.Logs, msg)
		logLines := []string{}
		for _, l := range m.Logs {
			logLines = append(logLines, fmt.Sprintf("[%s] %s", l.Level, l.Text))
		}
		m.viewport.SetContent(strings.Join(logLines, "\n"))
		m.viewport.GotoBottom()
		return m, WaitForLog(m.LogChan)

	case CompanyUpdateMsg:
		found := false
		for i, c := range m.Companies {
			if c.ID == msg.ID {
				m.Companies[i].Step = msg.Step
				m.Companies[i].Status = msg.Status
				m.Companies[i].Message = msg.Message
				found = true
				break
			}
		}
		if !found {
			m.Companies = append(m.Companies, CompanyRow{
				ID:      msg.ID,
				Name:    msg.Name,
				Step:    msg.Step,
				Status:  msg.Status,
				Message: msg.Message,
			})
		}
		
		// Update aggregate counts
		if msg.Status == StatusDone {
			m.Processed++
			m.Success++
		} else if msg.Status == StatusError {
			m.Processed++
			m.Failed++
		}
		return m, nil

	case PipelineDoneMsg:
		m.Done = true
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m PipelineModel) View() string {
	var s strings.Builder

	// Header
	elapsed := time.Since(m.StartTime).Round(time.Second)
	s.WriteString(TitleStyle.Render(fmt.Sprintf("JobHunter Pipeline | Run: %s | %s", m.RunID, elapsed)))
	s.WriteString("\n\n")

	// Progress
	if m.Total > 0 {
		pct := float64(m.Processed) / float64(m.Total)
		s.WriteString(m.progress.ViewAs(pct))
		s.WriteString("\n\n")
	}

	// Companies
	s.WriteString("Companies:\n")
	for i := len(m.Companies) - 1; i >= 0 && i > len(m.Companies)-10; i-- {
		c := m.Companies[i]
		icon := m.spinner.View()
		if c.Status == StatusDone {
			icon = lipgloss.NewStyle().Foreground(Accent).Render("✓")
		} else if c.Status == StatusError {
			icon = lipgloss.NewStyle().Foreground(Danger).Render("✗")
		}
		
		s.WriteString(fmt.Sprintf(" %s %-20s | %-15s | %s\n", icon, truncate(c.Name, 20), c.Step, DimStyle.Render(c.Message)))
	}
	
	// Pad middle space
	s.WriteString(strings.Repeat("\n", max(0, m.height-20-len(m.Companies))))

	// Stats
	footer := fmt.Sprintf(" Total: %d | Done: %d | Success: %d | Failed: %d", m.Total, m.Processed, m.Success, m.Failed)
	s.WriteString(lipgloss.NewStyle().Background(Dim).Foreground(Surface).Width(m.width).Render(footer))
	s.WriteString("\n\n")

	// Logs
	s.WriteString("Logs:\n")
	s.WriteString(m.viewport.View())

	if m.Done {
		s.WriteString("\n " + Bold.Render("Pipeline Complete! Press 'q' to quit."))
	}

	return s.String()
}

// Msg types for communication
type CompanyUpdateMsg struct {
	ID      int
	Name    string
	Step    string
	Status  PipelineStatus
	Message string
}

type PipelineDoneMsg struct{}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
