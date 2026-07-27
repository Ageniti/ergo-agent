package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentruntime "github.com/ageniti/ergo-agent/agent"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/config"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
)

func TestValidateWorkspaceCanonicalizesAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{"cwd": project}
	if err := validateWorkspace(payload, root); err != nil {
		t.Fatal(err)
	}
	if payload["cwd"] != project {
		t.Fatalf("cwd=%q, want canonical %q", payload["cwd"], project)
	}

	for _, payload := range []map[string]any{
		{},
		{"cwd": filepath.Join(root, "missing")},
		{"cwd": escape},
		{"cwd": filepath.Join(root, "file")},
	} {
		if err := validateWorkspace(payload, root); err == nil {
			t.Fatalf("accepted invalid workspace payload: %#v", payload)
		}
	}
}

func TestRetryDelayIsExponentialAndCapped(t *testing.T) {
	for attempt, want := range map[int]time.Duration{
		0:  2 * time.Second,
		1:  2 * time.Second,
		2:  4 * time.Second,
		8:  256 * time.Second,
		20: 300 * time.Second,
	} {
		if got := retryDelay(attempt); got != want {
			t.Fatalf("attempt=%d delay=%s want=%s", attempt, got, want)
		}
	}
}

type fakeOutboxRepository struct {
	events    []model.OutboxEvent
	published []string
	failed    []string
	delays    []time.Duration
}

func (r *fakeOutboxRepository) LeaseOutbox(context.Context, string, int, time.Duration) ([]model.OutboxEvent, error) {
	return r.events, nil
}

func (r *fakeOutboxRepository) MarkOutboxPublished(_ context.Context, eventID, _ string) error {
	r.published = append(r.published, eventID)
	return nil
}

func (r *fakeOutboxRepository) FailOutbox(_ context.Context, eventID, _, _ string, delay time.Duration) error {
	r.failed = append(r.failed, eventID)
	r.delays = append(r.delays, delay)
	return nil
}

func TestPublishOutboxOnceSignsSuccessfulDeliveryAndReleasesFailures(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Idempotency-Key") != "event-"+string(rune('0'+requests)) {
			t.Fatalf("unexpected idempotency key %q", r.Header.Get("Idempotency-Key"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write(body)
		wantSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-Agent-Signature") != wantSignature {
			t.Fatalf("signature=%q want=%q", r.Header.Get("X-Agent-Signature"), wantSignature)
		}
		if requests == 2 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer server.Close()
	repo := &fakeOutboxRepository{events: []model.OutboxEvent{
		{ID: "event-1", Attempts: 0, Payload: map[string]any{"kind": "first"}},
		{ID: "event-2", Attempts: 1, Payload: map[string]any{"kind": "second"}},
	}}
	cfg := config.Config{WorkerID: "worker-1", OutboxBatch: 10, OutboxTimeout: time.Second, OutboxWebhookURL: server.URL, OutboxSecret: "secret"}
	publishOutboxOnce(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), repo, cfg, "worker-1/outbox", server.Client())
	if len(repo.published) != 1 || repo.published[0] != "event-1" {
		t.Fatalf("published=%v", repo.published)
	}
	if len(repo.failed) != 1 || repo.failed[0] != "event-2" || len(repo.delays) != 1 || repo.delays[0] != 4*time.Second {
		t.Fatalf("failed=%v delays=%v", repo.failed, repo.delays)
	}
}

type fakeWorkerRepository struct {
	repository.Repository
	run       model.Run
	events    []string
	completed int
	failed    int
	delay     time.Duration
}

func (r *fakeWorkerRepository) StartRun(context.Context, string) error { return nil }

func (r *fakeWorkerRepository) GetRun(context.Context, string, string) (model.Run, error) {
	return r.run, nil
}

func (r *fakeWorkerRepository) AppendRunEvent(_ context.Context, _ string, _ string, eventType string, _ map[string]any) error {
	r.events = append(r.events, eventType)
	return nil
}

func (r *fakeWorkerRepository) CompleteJob(context.Context, string, string) error {
	r.completed++
	return nil
}

func (r *fakeWorkerRepository) FailJob(_ context.Context, _ string, _ string, _ string, delay time.Duration) error {
	r.failed++
	r.delay = delay
	return nil
}

type fakeRuntime struct {
	err    error
	events []agentruntime.Event
}

func (r fakeRuntime) RunWithOptions(_ context.Context, _ map[string]any, _ agentruntime.RunOptions, sink agentruntime.EventSink) error {
	for _, event := range r.events {
		if err := sink(event); err != nil {
			return err
		}
	}
	return r.err
}

func TestProcessJobRejectsOutsideWorkspaceWithoutCallingRuntime(t *testing.T) {
	root := t.TempDir()
	repo := &fakeWorkerRepository{}
	processJob(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), repo, nil, config.Config{WorkspaceRoot: root, WorkerID: "worker"}, model.Job{
		ID: "job", RunID: "run", Payload: map[string]any{"tenantId": "tenant", "cwd": t.TempDir()},
	})
	if repo.completed != 1 || len(repo.events) != 1 || repo.events[0] != "run.failed" {
		t.Fatalf("completed=%d events=%v", repo.completed, repo.events)
	}
}

func TestProcessJobPersistsRuntimeEventsAndCompletesOrRetries(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	successRepo := &fakeWorkerRepository{run: model.Run{Status: model.RunRunning}}
	processJob(context.Background(), logger, successRepo, fakeRuntime{events: []agentruntime.Event{{Type: "run.started"}}}, config.Config{
		WorkspaceRoot: root, WorkerID: "worker", WorkerLease: time.Minute,
	}, model.Job{ID: "job-success", RunID: "run", Payload: map[string]any{"tenantId": "tenant", "cwd": project}})
	if successRepo.completed != 1 || len(successRepo.events) != 1 || successRepo.events[0] != "run.started" {
		t.Fatalf("completed=%d events=%v", successRepo.completed, successRepo.events)
	}

	failedRepo := &fakeWorkerRepository{run: model.Run{Status: model.RunRunning}}
	processJob(context.Background(), logger, failedRepo, fakeRuntime{err: errors.New("provider unavailable")}, config.Config{
		WorkspaceRoot: root, WorkerID: "worker", WorkerLease: time.Minute,
	}, model.Job{ID: "job-failure", RunID: "run", Attempts: 3, Payload: map[string]any{"tenantId": "tenant", "cwd": project}})
	if failedRepo.failed != 1 || failedRepo.delay != 8*time.Second {
		t.Fatalf("failed=%d delay=%s", failedRepo.failed, failedRepo.delay)
	}
}
