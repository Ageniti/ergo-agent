package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	agentruntime "github.com/ageniti/ergo-agent/agent"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/config"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
	mysqlrepo "github.com/ageniti/ergo-agent/examples/ecs/internal/repository/mysql"
	bochaextension "github.com/ageniti/ergo-agent/extensions/bocha"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}
	repo, err := mysqlrepo.OpenWithOptions(cfg.MySQLDSN, mysqlrepo.Options{MaxOpen: cfg.DBMaxOpen, MaxIdle: cfg.DBMaxIdle, MaxLifetime: cfg.DBConnMaxLifetime})
	if err != nil {
		logger.Error("database open failed", "error", err)
		os.Exit(1)
	}
	defer repo.Close()
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 10*time.Second)
	if err := repo.Ping(pingCtx); err != nil {
		cancelPing()
		logger.Error("database ping failed", "error", err)
		os.Exit(1)
	}
	cancelPing()
	runtime := agentruntime.New(cfg.AgentRoot)
	if extension, enabled, extensionErr := bochaextension.NewFromEnv(); extensionErr != nil {
		logger.Error("Bocha search configuration error", "error", extensionErr)
		os.Exit(1)
	} else if enabled {
		runtime.RegisterExtension(extension)
		// The extension owns an in-memory copy. Do not pass the credential to
		// coding Bash or stdio MCP child processes.
		_ = os.Unsetenv("BOCHA_API_KEY")
		logger.Info("Bocha search extension enabled")
	}
	if cfg.ModelPricingJSON != "" {
		if err := runtime.RegisterModelPricingJSON([]byte(cfg.ModelPricingJSON)); err != nil {
			logger.Error("model pricing configuration error", "error", err)
			os.Exit(1)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sem := make(chan struct{}, cfg.WorkerConcurrency)
	var workers sync.WaitGroup
	ticker := time.NewTicker(cfg.WorkerPoll)
	defer ticker.Stop()
	logger.Info("agent worker started", "worker_id", cfg.WorkerID, "concurrency", cfg.WorkerConcurrency)
	if cfg.OutboxWebhookURL != "" {
		go publishOutbox(ctx, logger, repo, cfg)
	}
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() {
				workers.Wait()
				close(done)
			}()
			select {
			case <-done:
				logger.Info("agent worker stopped")
			case <-time.After(cfg.ShutdownTimeout):
				logger.Error("agent worker shutdown timed out", "timeout", cfg.ShutdownTimeout)
			}
			return
		case <-ticker.C:
			if expired, err := repo.ExpireApprovals(ctx); err != nil {
				logger.Error("expire approvals failed", "error", err)
			} else if expired > 0 {
				logger.Info("expired approvals", "count", expired)
			}
			available := cfg.WorkerConcurrency - len(sem)
			if available <= 0 {
				continue
			}
			jobs, err := repo.LeaseJobs(ctx, cfg.WorkerID, available, cfg.WorkerLease)
			if err != nil {
				logger.Error("lease jobs failed", "error", err)
				continue
			}
			for _, job := range jobs {
				sem <- struct{}{}
				workers.Add(1)
				go func(job model.Job) {
					defer func() { <-sem; workers.Done() }()
					processJob(ctx, logger, repo, runtime, cfg, job)
				}(job)
			}
		}
	}
}

type outboxRepository interface {
	LeaseOutbox(context.Context, string, int, time.Duration) ([]model.OutboxEvent, error)
	MarkOutboxPublished(context.Context, string, string) error
	FailOutbox(context.Context, string, string, string, time.Duration) error
}

type runtimeRunner interface {
	RunWithOptions(context.Context, map[string]any, agentruntime.RunOptions, agentruntime.EventSink) error
}

func publishOutbox(ctx context.Context, logger *slog.Logger, repo outboxRepository, cfg config.Config) {
	client := &http.Client{Timeout: cfg.OutboxTimeout}
	owner := cfg.WorkerID + "/outbox"
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publishOutboxOnce(ctx, logger, repo, cfg, owner, client)
		}
	}
}

func publishOutboxOnce(ctx context.Context, logger *slog.Logger, repo outboxRepository, cfg config.Config, owner string, client *http.Client) {
	events, err := repo.LeaseOutbox(ctx, owner, cfg.OutboxBatch, cfg.OutboxTimeout*2+5*time.Second)
	if err != nil {
		logger.Error("lease outbox failed", "error", err)
		return
	}
	for _, event := range events {
		body, publishErr := json.Marshal(event)
		if publishErr == nil {
			var request *http.Request
			request, publishErr = http.NewRequestWithContext(ctx, http.MethodPost, cfg.OutboxWebhookURL, bytes.NewReader(body))
			if publishErr == nil {
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", event.ID)
				request.Header.Set("X-Agent-Event-ID", event.ID)
				if cfg.OutboxSecret != "" {
					mac := hmac.New(sha256.New, []byte(cfg.OutboxSecret))
					_, _ = mac.Write(body)
					request.Header.Set("X-Agent-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
				}
				var response *http.Response
				response, publishErr = client.Do(request)
				if publishErr == nil {
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
					response.Body.Close()
					if response.StatusCode < 200 || response.StatusCode >= 300 {
						publishErr = fmt.Errorf("webhook returned %s", response.Status)
					}
				}
			}
		}
		if publishErr == nil {
			if markErr := repo.MarkOutboxPublished(ctx, event.ID, owner); markErr != nil {
				logger.Error("mark outbox published failed", "event_id", event.ID, "error", markErr)
			}
		} else {
			delay := retryDelay(event.Attempts + 1)
			if failErr := repo.FailOutbox(ctx, event.ID, owner, publishErr.Error(), delay); failErr != nil {
				logger.Error("release outbox failed", "event_id", event.ID, "error", failErr)
			}
		}
	}
}

func processJob(parent context.Context, logger *slog.Logger, repo repository.Repository, runtime runtimeRunner, cfg config.Config, job model.Job) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := validateWorkspace(job.Payload, cfg.WorkspaceRoot); err != nil {
		logger.Error("workspace validation failed", "job_id", job.ID, "run_id", job.RunID, "error", err)
		tenantID, _ := job.Payload["tenantId"].(string)
		_ = repo.AppendRunEvent(ctx, tenantID, job.RunID, "run.failed", map[string]any{"message": err.Error(), "code": "invalid_workspace"})
		_ = repo.CompleteJob(ctx, job.ID, cfg.WorkerID)
		return
	}
	if err := repo.StartRun(ctx, job.RunID); err != nil {
		tenantID, _ := job.Payload["tenantId"].(string)
		if run, getErr := repo.GetRun(context.Background(), tenantID, job.RunID); getErr == nil && run.Status == model.RunCancelled {
			_ = repo.CompleteJob(context.Background(), job.ID, cfg.WorkerID)
			return
		}
		_ = repo.FailJob(ctx, job.ID, cfg.WorkerID, err.Error(), retryDelay(job.Attempts))
		return
	}
	// Refresh the durable tree after acquiring the lease. A prior ECS task may
	// have checkpointed an assistant tool call immediately before it disappeared.
	if tenantID, ok := job.Payload["tenantId"].(string); ok {
		if sessionID, ok := job.Payload["sessionId"].(string); ok {
			if session, loadErr := repo.GetSession(ctx, tenantID, sessionID); loadErr == nil {
				entries := make([]map[string]any, 0, len(session.Entries))
				for _, entry := range session.Entries {
					entries = append(entries, entry.Payload)
				}
				job.Payload["sessionEntries"] = entries
				if session.ActiveLeafID != nil {
					job.Payload["sessionLeafId"] = *session.ActiveLeafID
				}
			}
		}
	}
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(cfg.WorkerLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := repo.HeartbeatJob(ctx, job.ID, cfg.WorkerID, cfg.WorkerLease); err != nil {
					logger.Error("job heartbeat failed", "job_id", job.ID, "error", err)
					cancel()
					return
				}
			}
		}
	}()
	pollControls := func(pollCtx context.Context) ([]agentruntime.Control, error) {
		tenantID, _ := job.Payload["tenantId"].(string)
		run, err := repo.GetRun(pollCtx, tenantID, job.RunID)
		if err != nil {
			return nil, err
		}
		if run.Status == model.RunCancelled {
			cancel()
			return nil, context.Canceled
		}
		controls, err := repo.PendingRunControls(pollCtx, job.RunID, 32)
		if err != nil {
			return nil, err
		}
		result := make([]agentruntime.Control, 0, len(controls))
		for _, control := range controls {
			result = append(result, agentruntime.Control{ID: control.ID, Action: control.Type, Content: control.Content})
		}
		return result, nil
	}
	pollInteraction := func(pollCtx context.Context, interactionID string) (agentruntime.InteractionReply, error) {
		item, err := repo.GetRunInteraction(pollCtx, job.RunID, interactionID)
		if errors.Is(err, repository.ErrNotFound) {
			return agentruntime.InteractionReply{}, nil
		}
		if err != nil {
			return agentruntime.InteractionReply{}, err
		}
		switch item.Status {
		case "answered":
			return agentruntime.InteractionReply{Ready: true, Response: item.Response}, nil
		case "cancelled", "expired":
			return agentruntime.InteractionReply{Ready: true, Cancelled: true, Response: item.Response}, nil
		default:
			return agentruntime.InteractionReply{}, nil
		}
	}
	err := runtime.RunWithOptions(ctx, job.Payload, agentruntime.RunOptions{Controls: pollControls, Interactions: pollInteraction}, func(event agentruntime.Event) error {
		tenantID, _ := job.Payload["tenantId"].(string)
		if err := repo.AppendRunEvent(ctx, tenantID, job.RunID, event.Type, event.Payload); err != nil {
			return err
		}
		if event.Type == "control.accepted" {
			if controlID, ok := event.Payload["controlId"].(string); ok {
				return repo.MarkRunControlDelivered(ctx, controlID)
			}
		}
		return nil
	})
	close(heartbeatDone)
	if err != nil {
		tenantID, _ := job.Payload["tenantId"].(string)
		if run, getErr := repo.GetRun(context.Background(), tenantID, job.RunID); getErr == nil && run.Status == model.RunCancelled {
			logger.Info("runtime cancelled", "job_id", job.ID, "run_id", job.RunID)
			if completeErr := repo.CompleteJob(context.Background(), job.ID, cfg.WorkerID); completeErr != nil && !errors.Is(completeErr, repository.ErrConflict) {
				logger.Error("complete cancelled job failed", "job_id", job.ID, "error", completeErr)
			}
			return
		}
		logger.Error("runtime job failed", "job_id", job.ID, "run_id", job.RunID, "error", err)
		_ = repo.FailJob(context.Background(), job.ID, cfg.WorkerID, err.Error(), retryDelay(job.Attempts))
		return
	}
	if err := repo.CompleteJob(context.Background(), job.ID, cfg.WorkerID); err != nil {
		logger.Error("complete job failed", "job_id", job.ID, "error", err)
	}
}

func validateWorkspace(payload map[string]any, workspaceRoot string) error {
	cwd, ok := payload["cwd"].(string)
	if !ok || strings.TrimSpace(cwd) == "" {
		return fmt.Errorf("runtime cwd is required")
	}
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	target, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return fmt.Errorf("resolve runtime cwd: %w", err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("runtime cwd is outside AGENT_WORKSPACE_ROOT")
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("stat runtime cwd: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime cwd is not a directory")
	}
	payload["cwd"] = target
	return nil
}

func retryDelay(attempt int) time.Duration {
	seconds := math.Min(300, math.Pow(2, float64(max(1, attempt))))
	return time.Duration(seconds) * time.Second
}
