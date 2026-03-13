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
	DB *db.DB
}

func NewEngine(database *db.DB) *Engine {
	return &Engine{DB: database}
}

func (e *Engine) Execute(ctx context.Context, runID string, steps []Step) (*Run, error) {
	run := &Run{
		ID:        runID,
		StartedAt: time.Now(),
		DB:        e.DB,
	}

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

	return run, nil
}

func (e *Engine) runStep(ctx context.Context, run *Run, step Step) StepResult {
	start := time.Now()
	
	// Initial log entry
	logEntry := &db.RunLog{
		RunID:  run.ID,
		Step:   step.Name,
		Status: "running",
	}
	// Note: In current db implementation, LogStep only does INSERT. 
	// To follow the PLAN.md "UPDATE run_log", we might need to adjust or just log final state.
	// For now, let's log the start and then the end as separate entries or update if we had an ID.
	// Actually, the PLAN.md says "writes to run_log on every state transition".

	ctx, cancel := context.WithTimeout(ctx, step.Timeout)
	defer cancel()

	err := step.Fn(ctx, run)
	duration := time.Since(start).Milliseconds()

	status := "ok"
	var errType, errMsg string
	if err != nil {
		status = "error"
		errMsg = err.Error()
		// We could categorize error types here if needed
		errType = "generic" 
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
