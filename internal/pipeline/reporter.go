package pipeline

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusError   Status = "error"
)

type ProgressUpdate struct {
	ID      int
	Name    string
	Step    string
	Status  Status
	Message string
}

type LogMsg struct {
	Level string
	Text  string
}

type Reporter interface {
	Update(ProgressUpdate)
	Log(LogMsg)
}

// NilReporter is a no-op reporter
type NilReporter struct{}

func (n NilReporter) Update(ProgressUpdate) {}
func (n NilReporter) Log(LogMsg)             {}
