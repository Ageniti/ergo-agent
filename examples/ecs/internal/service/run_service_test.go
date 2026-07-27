package service

import (
	"context"
	"testing"
	"time"

	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
)

type fakeRepository struct {
	created model.CreateRun
	run     model.Run
	err     error
}

func (f *fakeRepository) Ping(context.Context) error { return nil }
func (f *fakeRepository) CreateRun(_ context.Context, input model.CreateRun) (model.Run, error) {
	f.created = input
	return f.run, f.err
}
func (f *fakeRepository) GetRun(_ context.Context, tenantID, runID string) (model.Run, error) {
	f.run.TenantID, f.run.ID = tenantID, runID
	return f.run, nil
}
func (f *fakeRepository) CancelRun(context.Context, string, string) (model.Run, error) {
	return model.Run{}, nil
}
func (f *fakeRepository) DecideApproval(context.Context, string, string, model.ApprovalDecision, string) error {
	return nil
}
func (f *fakeRepository) ListRunApprovals(context.Context, string, string) ([]model.Approval, error) {
	return nil, nil
}
func (f *fakeRepository) ListRunInteractions(context.Context, string, string) ([]model.Interaction, error) {
	return nil, nil
}
func (f *fakeRepository) GetRunInteraction(context.Context, string, string) (model.Interaction, error) {
	return model.Interaction{}, repository.ErrNotFound
}
func (f *fakeRepository) AnswerInteraction(context.Context, string, string, any, bool) error {
	return nil
}
func (f *fakeRepository) StartRun(context.Context, string) error { return nil }
func (f *fakeRepository) AppendRunEvent(context.Context, string, string, string, map[string]any) error {
	return nil
}
func (f *fakeRepository) ListRunEvents(context.Context, string, string, uint64, int) ([]model.RunEvent, error) {
	return nil, nil
}
func (f *fakeRepository) EnqueueRunControl(context.Context, string, string, string, string) (model.RunControl, error) {
	return model.RunControl{}, nil
}
func (f *fakeRepository) PendingRunControls(context.Context, string, int) ([]model.RunControl, error) {
	return nil, nil
}
func (f *fakeRepository) MarkRunControlDelivered(context.Context, string) error { return nil }
func (f *fakeRepository) GetRunPlan(context.Context, string, string) (model.Plan, error) {
	return model.Plan{}, repository.ErrNotFound
}
func (f *fakeRepository) GetPlan(context.Context, string, string) (model.Plan, error) {
	return model.Plan{}, repository.ErrNotFound
}
func (f *fakeRepository) DecidePlan(context.Context, string, string, bool) error { return nil }
func (f *fakeRepository) ListTodos(context.Context, string, string) ([]model.Todo, error) {
	return nil, nil
}
func (f *fakeRepository) GetSession(context.Context, string, string) (model.Session, error) {
	return model.Session{}, repository.ErrNotFound
}
func (f *fakeRepository) ForkSession(context.Context, string, string, string, string, string, string) (model.Session, error) {
	return model.Session{}, nil
}
func (f *fakeRepository) ListSessionRuns(context.Context, string, string, int) ([]model.Run, error) {
	return nil, nil
}
func (f *fakeRepository) LeaseJobs(context.Context, string, int, time.Duration) ([]model.Job, error) {
	return nil, nil
}
func (f *fakeRepository) HeartbeatJob(context.Context, string, string, time.Duration) error {
	return nil
}
func (f *fakeRepository) CompleteJob(context.Context, string, string) error { return nil }
func (f *fakeRepository) FailJob(context.Context, string, string, string, time.Duration) error {
	return nil
}

func validRequest() CreateRunRequest {
	return CreateRunRequest{
		TenantID: " tenant ", AgentID: "coding-agent", IdempotencyKey: "request-1",
		Input: map[string]any{"prompt": " build it ", "cwd": "/workspace/job", "provider": "openai", "model": "gpt-5"},
	}
}

func TestCreateValidatesAndPinsVersions(t *testing.T) {
	repo := &fakeRepository{run: model.Run{ID: "run-1"}}
	_, err := NewRunService(repo).Create(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if repo.created.TenantID != "tenant" {
		t.Fatalf("tenant was not normalized: %q", repo.created.TenantID)
	}
	if repo.created.Input["prompt"] != "build it" {
		t.Fatalf("prompt was not normalized: %#v", repo.created.Input["prompt"])
	}
	if repo.created.PromptBundleVersion != PromptBundleVersion || repo.created.RuntimeVersion != RuntimeVersion {
		t.Fatal("runtime provenance was not pinned")
	}
}

func TestCreateRejectsIncompleteRuntimeInput(t *testing.T) {
	request := validRequest()
	delete(request.Input, "model")
	if _, err := NewRunService(&fakeRepository{}).Create(context.Background(), request); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCreateValidatesAndPreservesAgentScope(t *testing.T) {
	request := validRequest()
	request.Input["agent_scope"] = "project"
	repo := &fakeRepository{run: model.Run{ID: "run-1"}}
	if _, err := NewRunService(repo).Create(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if repo.created.Input["agent_scope"] != "project" {
		t.Fatalf("agent scope was not preserved: %#v", repo.created.Input["agent_scope"])
	}
	request.Input["agent_scope"] = "global"
	if _, err := NewRunService(&fakeRepository{}).Create(context.Background(), request); err == nil {
		t.Fatal("invalid agent scope was accepted")
	}
}

func TestCreateReturnsExistingRunForIdempotencyReplay(t *testing.T) {
	repo := &fakeRepository{err: &repository.IdempotencyReplayError{RunID: "existing"}}
	run, err := NewRunService(repo).Create(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "existing" {
		t.Fatalf("got run %q", run.ID)
	}
}
