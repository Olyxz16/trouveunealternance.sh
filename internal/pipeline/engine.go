package pipeline

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"log"
	"time"
)

type StepFn func(ctx context.Context, run *Run) error

type Step struct {
	Name        string
	Fn          StepFn
	Timeout     time.Duration
	StopOnError bool
}

type StepResult struct {
	Name       string
	Status     string // ok | error | skipped | needs_review
	Err        error
	DurationMs int64
}

type Run struct {
	ID        string
	StartedAt time.Time
	Steps     []StepResult
	DB        *db.DB
}

type Engine struct {
	DB       *db.DB
	reporter Reporter
}

func NewEngine(database *db.DB) *Engine {
	return &Engine{
		DB:       database,
		reporter: NilReporter{},
	}
}

func (e *Engine) SetReporter(r Reporter) {
	if r == nil {
		e.reporter = NilReporter{}
	} else {
		e.reporter = r
	}
}

func (e *Engine) Execute(ctx context.Context, runID string, steps []Step) (*Run, error) {
	run := &Run{
		ID:        runID,
		StartedAt: time.Now(),
		DB:        e.DB,
	}

	e.reporter.Log(LogMsg{Level: "INFO", Text: fmt.Sprintf("Starting pipeline run %s", runID)})

	if err := e.DB.CreatePipelineRun(run.ID); err != nil {
		return nil, fmt.Errorf("failed to create pipeline run in DB: %w", err)
	}

	finalStatus := "done"

	for _, step := range steps {
		result := e.runStep(ctx, run, step)
		run.Steps = append(run.Steps, result)

		if result.Status == "error" {
			finalStatus = "partial"
			if step.StopOnError {
				finalStatus = "failed"
				break
			}
		}
	}

	if err := e.DB.FinalizePipelineRun(run.ID, finalStatus); err != nil {
		log.Printf("Failed to finalize pipeline run %s: %v", run.ID, err)
	}

	e.reporter.Log(LogMsg{Level: "INFO", Text: fmt.Sprintf("Pipeline run %s finished with status: %s", runID, finalStatus)})

	return run, nil
}

func (e *Engine) runStep(ctx context.Context, run *Run, step Step) StepResult {
	start := time.Now()
	e.reporter.Log(LogMsg{Level: "INFO", Text: fmt.Sprintf("Executing step: %s", step.Name)})

	// Initial log entry
	logEntry := &db.RunLog{
		RunID:  run.ID,
		Step:   step.Name,
		Status: "running",
	}

	ctx, cancel := context.WithTimeout(ctx, step.Timeout)
	defer cancel()

	err := step.Fn(ctx, run)
	duration := time.Since(start).Milliseconds()

	status := "ok"
	var errType, errMsg string
	if err != nil {
		status = "error"
		errMsg = err.Error()
		errType = "generic" 
		e.reporter.Log(LogMsg{Level: "ERROR", Text: fmt.Sprintf("Step %s failed: %v", step.Name, err)})
	} else {
		e.reporter.Log(LogMsg{Level: "INFO", Text: fmt.Sprintf("Step %s completed in %v", step.Name, time.Since(start).Round(time.Millisecond))})
	}

	logEntry.Status = status
	logEntry.ErrorType = db.ToNullString(errType)
	logEntry.ErrorMsg = db.ToNullString(errMsg)
	logEntry.DurationMS = duration

	if err := run.DB.LogStep(logEntry); err != nil {
		log.Printf("Failed to log step %s: %v", step.Name, err)
	}

	return StepResult{
		Name:       step.Name,
		Status:     status,
		Err:        err,
		DurationMs: duration,
	}
}
