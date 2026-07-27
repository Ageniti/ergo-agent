package mysqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
	"github.com/ageniti/ergo-agent/examples/ecs/migrations"
	_ "github.com/go-sql-driver/mysql"
)

func TestForkSessionCopiesOnlySelectedBranch(t *testing.T) {
	dsn := os.Getenv("AGENT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENT_TEST_MYSQL_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, ctx, db)
	repo := &Repository{db: db}
	tenantID, sourceID := "test-"+newID(), newID()
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_sessions (id, tenant_id, active_leaf_id, version, created_at, updated_at) VALUES (?, ?, 'leaf', 1, ?, ?)`, sourceID, tenantID, now, now); err != nil {
		t.Fatal(err)
	}
	entries := []struct{ id, parent string }{{"root", ""}, {"middle", "root"}, {"leaf", "middle"}}
	for _, item := range entries {
		payload, _ := json.Marshal(map[string]any{"id": item.id, "parentId": nullableString(item.parent), "type": "custom", "customType": "test", "timestamp": now.Format(time.RFC3339Nano)})
		if _, err := db.ExecContext(ctx, `INSERT INTO agent_session_entries (id, tenant_id, session_id, parent_id, entry_type, payload_json, created_at) VALUES (?, ?, ?, ?, 'custom', ?, ?)`, item.id, tenantID, sourceID, nullableString(item.parent), payload, now); err != nil {
			t.Fatal(err)
		}
	}
	forked, err := repo.ForkSession(ctx, tenantID, sourceID, "leaf", "before", "", "fork-test")
	if err != nil {
		t.Fatal(err)
	}
	if forked.ParentSessionID == nil || *forked.ParentSessionID != sourceID {
		t.Fatalf("parent session = %v, want %s", forked.ParentSessionID, sourceID)
	}
	if forked.ActiveLeafID == nil || *forked.ActiveLeafID != "middle" {
		t.Fatalf("active leaf = %v, want middle", forked.ActiveLeafID)
	}
	if len(forked.Entries) != 2 || forked.Entries[0].ID != "root" || forked.Entries[1].ID != "middle" {
		t.Fatalf("unexpected fork entries: %#v", forked.Entries)
	}
	replayed, err := repo.ForkSession(ctx, tenantID, sourceID, "leaf", "before", "", "fork-test")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != forked.ID {
		t.Fatalf("idempotency replay returned %s, want %s", replayed.ID, forked.ID)
	}
}

func TestJobLeaseFencesConcurrentECSWorkersAndCanBeRecovered(t *testing.T) {
	dsn := os.Getenv("AGENT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENT_TEST_MYSQL_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, ctx, db)
	repo := &Repository{db: db}
	tenant := "lease-" + newID()
	run, err := repo.CreateRun(ctx, model.CreateRun{
		TenantID: tenant, AgentID: "coding-agent",
		Input:               map[string]any{"prompt": "test", "cwd": "/workspace/test"},
		PromptBundleVersion: "test", RuntimeVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	type leaseResult struct {
		owner string
		jobs  []model.Job
		err   error
	}
	start := make(chan struct{})
	results := make(chan leaseResult, 2)
	var wait sync.WaitGroup
	for _, owner := range []string{"ecs-a", "ecs-b"} {
		wait.Add(1)
		go func(owner string) {
			defer wait.Done()
			<-start
			jobs, leaseErr := repo.LeaseJobs(ctx, owner, 1, 30*time.Second)
			results <- leaseResult{owner: owner, jobs: jobs, err: leaseErr}
		}(owner)
	}
	close(start)
	wait.Wait()
	close(results)

	var leased model.Job
	var owner string
	total := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		total += len(result.jobs)
		if len(result.jobs) == 1 {
			leased, owner = result.jobs[0], result.owner
		}
	}
	if total != 1 || leased.RunID != run.ID {
		t.Fatalf("leased total=%d job=%+v run=%s", total, leased, run.ID)
	}
	if err := repo.HeartbeatJob(ctx, leased.ID, "not-"+owner, 30*time.Second); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("wrong owner heartbeat=%v", err)
	}
	if err := repo.FailJob(ctx, leased.ID, owner, "recoverable", 0); err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.LeaseJobs(ctx, "ecs-recovery", 1, 30*time.Second)
	if err != nil || len(recovered) != 1 || recovered[0].ID != leased.ID {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if err := repo.CompleteJob(ctx, recovered[0].ID, "ecs-recovery"); err != nil {
		t.Fatal(err)
	}
}

func TestRunEventProjectionsControlsInteractionsAndOutbox(t *testing.T) {
	dsn := os.Getenv("AGENT_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENT_TEST_MYSQL_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, ctx, db)
	repo := &Repository{db: db}
	tenant := "projection-" + newID()
	run, err := repo.CreateRun(ctx, model.CreateRun{
		TenantID: tenant, AgentID: "coding-agent",
		Input:               map[string]any{"prompt": "test", "cwd": "/workspace/test"},
		PromptBundleVersion: "test", RuntimeVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.StartRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	userEntryID, assistantEntryID := "user-"+newID(), "assistant-"+newID()
	interactionID, approvalID := newID(), newID()
	if err := repo.AppendRunEvent(ctx, tenant, run.ID, "session.snapshot", map[string]any{
		"leafId": assistantEntryID,
		"entries": []map[string]any{
			{"id": userEntryID, "type": "message", "timestamp": timestamp},
			{"id": assistantEntryID, "parentId": userEntryID, "type": "message", "timestamp": timestamp},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendRunEvent(ctx, tenant, run.ID, "todo.updated", map[string]any{
		"todos": []map[string]any{{"id": 3, "text": "persisted", "done": false}},
	}); err != nil {
		t.Fatal(err)
	}
	session, err := repo.GetSession(ctx, tenant, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.ActiveLeafID == nil || *session.ActiveLeafID != assistantEntryID || len(session.Entries) != 2 {
		t.Fatalf("session projection=%+v", session)
	}
	todos, err := repo.ListTodos(ctx, tenant, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 1 || todos[0].Ordinal != 3 || todos[0].Text != "persisted" {
		t.Fatalf("todos=%+v", todos)
	}
	control, err := repo.EnqueueRunControl(ctx, tenant, run.ID, "steer", "continue with tests")
	if err != nil {
		t.Fatal(err)
	}
	controls, err := repo.PendingRunControls(ctx, run.ID, 10)
	if err != nil || len(controls) != 1 || controls[0].ID != control.ID {
		t.Fatalf("controls=%+v err=%v", controls, err)
	}
	if err := repo.MarkRunControlDelivered(ctx, control.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendRunEvent(ctx, tenant, run.ID, "input.requested", map[string]any{
		"interactionId": interactionID, "toolCallId": "tool-" + newID(), "kind": "questionnaire",
		"request": map[string]any{"questions": []string{"continue?"}},
	}); err != nil {
		t.Fatal(err)
	}
	interactions, err := repo.ListRunInteractions(ctx, tenant, run.ID)
	if err != nil || len(interactions) != 1 || interactions[0].Status != "pending" {
		t.Fatalf("interactions=%+v err=%v", interactions, err)
	}
	if err := repo.AnswerInteraction(ctx, tenant, interactionID, map[string]any{"answer": "yes"}, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.StartRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendRunEvent(ctx, tenant, run.ID, "approval.requested", map[string]any{
		"approvalId": approvalID, "toolCallId": "tool-" + newID(), "toolName": "bash",
		"argumentsHash": "hash", "policyVersion": "v1", "input": map[string]any{"command": "pwd"},
	}); err != nil {
		t.Fatal(err)
	}
	approvals, err := repo.ListRunApprovals(ctx, tenant, run.ID)
	if err != nil || len(approvals) != 1 || approvals[0].ToolName != "bash" {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	if err := repo.DecideApproval(ctx, tenant, approvalID, model.ApprovalAllow, "approved"); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListRunEvents(ctx, tenant, run.ID, 0, 100)
	if err != nil || len(events) < 5 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	outbox, err := repo.LeaseOutbox(ctx, "outbox-test", 100, time.Minute)
	if err != nil || len(outbox) < len(events)+1 {
		t.Fatalf("outbox=%+v err=%v", outbox, err)
	}
	if err := repo.MarkOutboxPublished(ctx, outbox[0].ID, "outbox-test"); err != nil {
		t.Fatal(err)
	}
	if len(outbox) > 1 {
		if err := repo.FailOutbox(ctx, outbox[1].ID, "outbox-test", "temporary", 0); err != nil {
			t.Fatal(err)
		}
	}
}

func resetIntegrationDatabase(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `UPDATE agent_sessions SET parent_session_id=NULL`); err != nil {
		t.Fatalf("clear session parents: %v", err)
	}
	for _, table := range []string{
		"agent_outbox",
		"agent_session_idempotency_keys",
		"agent_idempotency_keys",
		"agent_todos",
		"agent_plan_steps",
		"agent_plans",
		"agent_approvals",
		"agent_interactions",
		"agent_run_controls",
		"agent_events",
		"agent_jobs",
		"agent_runs",
		"agent_session_entries",
		"agent_sessions",
	} {
		if _, err := db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
