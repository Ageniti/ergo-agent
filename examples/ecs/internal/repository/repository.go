// Package repository defines persistence contracts for the ECS example.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrIdempotencyReplay = errors.New("idempotency replay")
)

type IdempotencyReplayError struct{ RunID string }

func (e *IdempotencyReplayError) Error() string { return "idempotency replay: " + e.RunID }
func (e *IdempotencyReplayError) Unwrap() error { return ErrIdempotencyReplay }

type Repository interface {
	Ping(context.Context) error
	CreateRun(context.Context, model.CreateRun) (model.Run, error)
	GetRun(context.Context, string, string) (model.Run, error)
	CancelRun(context.Context, string, string) (model.Run, error)
	DecideApproval(context.Context, string, string, model.ApprovalDecision, string) error
	ListRunApprovals(context.Context, string, string) ([]model.Approval, error)
	ListRunInteractions(context.Context, string, string) ([]model.Interaction, error)
	GetRunInteraction(context.Context, string, string) (model.Interaction, error)
	AnswerInteraction(context.Context, string, string, any, bool) error
	StartRun(context.Context, string) error
	AppendRunEvent(context.Context, string, string, string, map[string]any) error
	ListRunEvents(context.Context, string, string, uint64, int) ([]model.RunEvent, error)
	EnqueueRunControl(context.Context, string, string, string, string) (model.RunControl, error)
	PendingRunControls(context.Context, string, int) ([]model.RunControl, error)
	MarkRunControlDelivered(context.Context, string) error
	GetRunPlan(context.Context, string, string) (model.Plan, error)
	GetPlan(context.Context, string, string) (model.Plan, error)
	DecidePlan(context.Context, string, string, bool) error
	ListTodos(context.Context, string, string) ([]model.Todo, error)
	GetSession(context.Context, string, string) (model.Session, error)
	ForkSession(context.Context, string, string, string, string, string, string) (model.Session, error)
	ListSessionRuns(context.Context, string, string, int) ([]model.Run, error)
	LeaseJobs(context.Context, string, int, time.Duration) ([]model.Job, error)
	HeartbeatJob(context.Context, string, string, time.Duration) error
	CompleteJob(context.Context, string, string) error
	FailJob(context.Context, string, string, string, time.Duration) error
}
