package tui

import (
	"fmt"
	"jobhunter/internal/pipeline"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type CompanyRow struct {
	ID      int
	Name    string
	Step    string
	Status  pipeline.Status
	Message string
}

type PipelineModel struct {
	RunID     string
	StartTime time.Time
	Companies []CompanyRow
	Logs      []pipeline.LogMsg
	LogChan   <-chan pipeline.LogMsg
	
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

func NewPipelineModel(runID string, logChan <-chan pipeline.LogMsg) *PipelineModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(Accent)

	return &PipelineModel{
		RunID:     runID,
		StartTime: time.Now(),
		LogChan:   logChan,
		spinner:   s,
		progress:  progress.New(progress.WithDefaultGradient()),
		viewport:  viewport.New(80, 10),
		width:     80,
		height:    24,
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

	case pipeline.LogMsg:
		m.Logs = append(m.Logs, msg)
		logLines := []string{}
		for _, l := range m.Logs {
			logLines = append(logLines, fmt.Sprintf("[%s] %s", l.Level, l.Text))
		}
		m.viewport.SetContent(strings.Join(logLines, "\n"))
		m.viewport.GotoBottom()
		return m, WaitForLog(m.LogChan)

	case pipeline.ProgressUpdate:
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
		if msg.Status == pipeline.StatusDone {
			m.Processed++
			m.Success++
		} else if msg.Status == pipeline.StatusError {
			m.Processed++
			m.Failed++
		}
		return m, nil

	case TotalUpdateMsg:
		m.Total = int(msg)
		return m, nil

	case ReadyMsg:
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
	header := TitleStyle.Render(fmt.Sprintf("JobHunter Pipeline | Run: %s | %s", m.RunID, elapsed))
	s.WriteString(header + "\n\n")

	// Progress
	if m.Total > 0 {
		pct := float64(m.Processed) / float64(m.Total)
		s.WriteString(m.progress.ViewAs(pct))
		s.WriteString("\n\n")
	}

	// Recent Activity
	s.WriteString(Bold.Render("Recent Activity:") + "\n")
	
	// Calculate available space for company rows
	// Header(3) + Progress(3) + Labels(1) + Stats(2) + LogsLabel(1) + Viewport(8) + Footer(2) = ~20 lines
	logHeight := 8
	if m.height < 25 {
		logHeight = 5
	}
	
	maxRows := m.height - (12 + logHeight)
	if maxRows < 1 {
		maxRows = 1
	}
	
	count := 0
	// Show companies that are currently running or recently finished
	for i := len(m.Companies) - 1; i >= 0 && count < maxRows; i-- {
		c := m.Companies[i]
		icon := m.spinner.View()
		if c.Status == pipeline.StatusDone {
			icon = lipgloss.NewStyle().Foreground(Accent).Render("✓")
		} else if c.Status == pipeline.StatusError {
			icon = lipgloss.NewStyle().Foreground(Danger).Render("✗")
		}
		
		name := truncate(c.Name, 20)
		statusColor := DimStyle
		if c.Status == pipeline.StatusRunning {
			statusColor = lipgloss.NewStyle().Foreground(Accent2)
		}
		
		msg := c.Message
		if msg != "" {
			msg = " - " + msg
		}
		
		row := fmt.Sprintf(" %s %-20s | %s%s", icon, name, statusColor.Render(fmt.Sprintf("%-15s", c.Step)), DimStyle.Render(truncate(msg, m.width-45)))
		s.WriteString(lipgloss.NewStyle().MaxWidth(m.width).Render(row) + "\n")
		count++
	}
	
	// Pad middle space to keep footer at bottom
	currentHeight := lipgloss.Height(s.String())
	targetStatsPos := m.height - (logHeight + 5)
	if targetStatsPos > currentHeight {
		s.WriteString(strings.Repeat("\n", targetStatsPos-currentHeight))
	}

	// Stats
	footerContent := fmt.Sprintf(" Total: %d | Done: %d | Success: %d | Failed: %d ", m.Total, m.Processed, m.Success, m.Failed)
	footer := lipgloss.NewStyle().
		Background(Dim).
		Foreground(Surface).
		Width(m.width).
		Render(footerContent)
	s.WriteString(footer + "\n\n")

	// Logs
	m.viewport.Height = logHeight
	s.WriteString(Bold.Render("Logs:") + "\n")
	s.WriteString(m.viewport.View())

	if m.Done {
		s.WriteString("\n " + Bold.Render("Pipeline Complete! Press 'q' to quit."))
	}

	return s.String()
}

// Msg types for communication
type TotalUpdateMsg int
type ReadyMsg struct{}
type PipelineDoneMsg struct{}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

