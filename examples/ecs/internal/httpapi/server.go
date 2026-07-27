// Package httpapi implements the ECS example's HTTP control plane.
package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/service"
)

type Server struct {
	runs  *service.RunService
	repo  repository.Repository
	log   *slog.Logger
	mux   *http.ServeMux
	token string
}

func NewServer(runs *service.RunService, repo repository.Repository, logger *slog.Logger, token string) *Server {
	server := &Server{runs: runs, repo: repo, log: logger, mux: http.NewServeMux(), token: token}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.recoverer(s.requestLogger(s.requestID(s.securityHeaders(s.authenticate(s.mux)))))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("POST /v1/runs", s.createRun)
	s.mux.HandleFunc("GET /v1/capabilities", s.capabilities)
	s.mux.HandleFunc("GET /v1/runs/{runID}", s.getRun)
	s.mux.HandleFunc("GET /v1/runs/{runID}/events", s.getRunEvents)
	s.mux.HandleFunc("GET /v1/runs/{runID}/events/stream", s.streamRunEvents)
	s.mux.HandleFunc("GET /v1/runs/{runID}/plan", s.getRunPlan)
	s.mux.HandleFunc("GET /v1/runs/{runID}/approvals", s.listRunApprovals)
	s.mux.HandleFunc("GET /v1/runs/{runID}/interactions", s.listRunInteractions)
	s.mux.HandleFunc("POST /v1/runs/{runID}/cancel", s.cancelRun)
	s.mux.HandleFunc("POST /v1/runs/{runID}/messages", s.sendRunMessage)
	s.mux.HandleFunc("POST /v1/plans/{planID}/decision", s.decidePlan)
	s.mux.HandleFunc("POST /v1/plans/{planID}/execute", s.executePlan)
	s.mux.HandleFunc("GET /v1/sessions/{sessionID}/todos", s.listTodos)
	s.mux.HandleFunc("GET /v1/sessions/{sessionID}", s.getSession)
	s.mux.HandleFunc("POST /v1/sessions/{sessionID}/fork", s.forkSession)
	s.mux.HandleFunc("GET /v1/sessions/{sessionID}/runs", s.listSessionRuns)
	s.mux.HandleFunc("POST /v1/sessions/{sessionID}/operations", s.createSessionOperation)
	s.mux.HandleFunc("POST /v1/approvals/{approvalID}/decision", s.decideApproval)
	s.mux.HandleFunc("POST /v1/interactions/{interactionID}/response", s.answerInteraction)
}

func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"behavior_baseline": "pi-v0.81.1", "runtime_version": service.RuntimeVersion, "runtime_engine": "go-native",
		"default_agent":      "chief-agent",
		"agent_roles":        []string{"main", "sub", "meta"},
		"builtin_tools":      []string{"read", "bash", "edit", "write", "grep", "find", "ls", "todo", "subagent", "questionnaire"},
		"session_operations": []string{"prompt", "compact", "navigate_tree", "skill", "prompt_template", "set_model", "set_thinking_level", "set_active_tools", "set_queue_modes", "set_plan_mode", "append_custom_entry", "append_custom_message", "set_label", "set_session_name", "extension_command", "inspect", "package_install", "package_update", "package_remove", "package_list"},
		"live_delivery":      []string{"steer", "follow_up", "next_turn"},
		"mcp_transports":     []string{"stdio", "streamable_http"},
		"mcp_capabilities":   []string{"tools", "resources", "resource_templates", "prompts"},
		"features":           []string{"plan_mode", "todos", "permissions", "skills", "prompt_templates", "resource_packages", "questionnaire", "headless_extensions", "extension_commands", "session_tree", "session_fork", "compaction", "retry", "subagents", "mcp", "sse", "outbox"},
	})
}

func (s *Server) listRunApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.runs.ListRunApprovals(r.Context(), tenantID(r), r.PathValue("runID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load approvals")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

func (s *Server) listRunInteractions(w http.ResponseWriter, r *http.Request) {
	items, err := s.runs.ListRunInteractions(r.Context(), tenantID(r), r.PathValue("runID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load interactions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interactions": items})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.runs.GetSession(r.Context(), tenantID(r), r.PathValue("sessionID"))
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load session")
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) forkSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TargetEntryID string `json:"target_entry_id"`
		Position      string `json:"position"`
		SessionID     string `json:"session_id"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	session, err := s.runs.ForkSession(r.Context(), tenantID(r), r.PathValue("sessionID"), request.TargetEntryID, request.Position, request.SessionID, r.Header.Get("Idempotency-Key"))
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "session or target entry not found")
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		writeError(w, http.StatusConflict, "invalid_target", "cannot fork before the root entry")
		return
	}
	var validation *service.ValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
		return
	}
	if err != nil {
		s.log.Error("fork session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to fork session")
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s *Server) listSessionRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.runs.ListSessionRuns(r.Context(), tenantID(r), r.PathValue("sessionID"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load session runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) getRunPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.runs.GetRunPlan(r.Context(), tenantID(r), r.PathValue("runID"))
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load plan")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) decidePlan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Decision string `json:"decision"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	err := s.runs.DecidePlan(r.Context(), tenantID(r), r.PathValue("planID"), request.Decision)
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		writeError(w, http.StatusConflict, "invalid_state", "plan is already decided")
		return
	}
	var validation *service.ValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to decide plan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func (s *Server) executePlan(w http.ResponseWriter, r *http.Request) {
	run, err := s.runs.ExecutePlan(r.Context(), tenantID(r), r.PathValue("planID"), r.Header.Get("Idempotency-Key"))
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		writeError(w, http.StatusConflict, "invalid_state", "plan is not approved")
		return
	}
	var validation *service.ValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
		return
	}
	if err != nil {
		s.log.Error("execute plan failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to execute plan")
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request) {
	todos, err := s.runs.ListTodos(r.Context(), tenantID(r), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load todos")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"todos": todos})
}

func (s *Server) createSessionOperation(w http.ResponseWriter, r *http.Request) {
	var request service.CreateRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	request.TenantID = tenantID(r)
	request.SessionID = r.PathValue("sessionID")
	request.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	run, err := s.runs.Create(r.Context(), request)
	if err != nil {
		var validation *service.ValidationError
		if errors.As(err, &validation) {
			writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
			return
		}
		if errors.Is(err, repository.ErrConflict) {
			writeError(w, http.StatusConflict, "conflict", "session is not available")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "failed to create session operation")
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) sendRunMessage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Delivery string `json:"delivery"`
		Content  string `json:"content"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	control, err := s.runs.SendControl(r.Context(), tenantID(r), r.PathValue("runID"), request.Delivery, request.Content)
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		writeError(w, http.StatusConflict, "invalid_state", "run is not currently running")
		return
	}
	var validation *service.ValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
		return
	}
	if err != nil {
		s.log.Error("enqueue run message failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to enqueue message")
		return
	}
	writeJSON(w, http.StatusAccepted, control)
}

func (s *Server) getRunEvents(w http.ResponseWriter, r *http.Request) {
	if _, err := s.runs.Get(r.Context(), tenantID(r), r.PathValue("runID")); errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load run")
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	events, err := s.repo.ListRunEvents(r.Context(), tenantID(r), r.PathValue("runID"), after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) streamRunEvents(w http.ResponseWriter, r *http.Request) {
	runID, tenant := r.PathValue("runID"), tenantID(r)
	run, err := s.runs.Get(r.Context(), tenant, runID)
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load run")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unavailable", "streaming is unavailable")
		return
	}
	afterText := r.URL.Query().Get("after")
	if afterText == "" {
		afterText = r.Header.Get("Last-Event-ID")
	}
	after, _ := strconv.ParseUint(afterText, 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(350 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	terminal := isTerminalRun(run.Status)
	for {
		events, loadErr := s.repo.ListRunEvents(r.Context(), tenant, runID, after, 100)
		if loadErr != nil {
			s.log.Warn("event stream query failed", "run_id", runID, "error", loadErr)
			return
		}
		for _, event := range events {
			body, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return
			}
			if _, writeErr := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, body); writeErr != nil {
				return
			}
			after = event.Sequence
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		if terminal && len(events) == 0 {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			latest, getErr := s.runs.Get(r.Context(), tenant, runID)
			if getErr != nil {
				return
			}
			terminal = isTerminalRun(latest.Status)
		}
	}
}

func isTerminalRun(status model.RunStatus) bool {
	return status == model.RunCompleted || status == model.RunFailed || status == model.RunCancelled ||
		status == model.RunAwaitingApproval || status == model.RunAwaitingInput
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.repo.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var request service.CreateRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	request.TenantID = tenantID(r)
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		request.IdempotencyKey = key
	}
	run, err := s.runs.Create(r.Context(), request)
	if err != nil {
		var validation *service.ValidationError
		if errors.As(err, &validation) {
			writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
			return
		}
		if errors.Is(err, repository.ErrConflict) {
			writeError(w, http.StatusConflict, "conflict", "session is not available for this tenant")
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "invalid_parent", "parent run not found")
			return
		}
		s.log.Error("create run failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to create run")
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.runs.Get(r.Context(), tenantID(r), r.PathValue("runID"))
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to load run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.runs.Cancel(r.Context(), tenantID(r), r.PathValue("runID"))
	if errors.Is(err, repository.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		writeError(w, http.StatusConflict, "invalid_state", "run cannot be cancelled")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to cancel run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Decision model.ApprovalDecision `json:"decision"`
		Reason   string                 `json:"reason"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	err := s.runs.DecideApproval(r.Context(), tenantID(r), r.PathValue("approvalID"), request.Decision, request.Reason)
	if errors.Is(err, repository.ErrConflict) {
		writeError(w, http.StatusConflict, "invalid_state", "approval is expired or already decided")
		return
	}
	if err != nil {
		var validation *service.ValidationError
		if errors.As(err, &validation) {
			writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
			return
		}
		s.log.Error("approval decision failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to decide approval")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func (s *Server) answerInteraction(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Response  any  `json:"response"`
		Cancelled bool `json:"cancelled"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	err := s.runs.AnswerInteraction(r.Context(), tenantID(r), r.PathValue("interactionID"), request.Response, request.Cancelled)
	if errors.Is(err, repository.ErrConflict) {
		writeError(w, http.StatusConflict, "invalid_state", "interaction is expired or already answered")
		return
	}
	if err != nil {
		var validation *service.ValidationError
		if errors.As(err, &validation) {
			writeError(w, http.StatusBadRequest, "invalid_request", validation.Error())
			return
		}
		s.log.Error("interaction response failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to answer interaction")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func tenantID(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-Tenant-ID")) }

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid service credential")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") && tenantID(r) == "" {
			writeError(w, http.StatusBadRequest, "tenant_required", "X-Tenant-ID is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			var value [16]byte
			if _, err := rand.Read(value[:]); err == nil {
				requestID = hex.EncodeToString(value[:])
			}
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("request body must contain exactly one JSON value")
		}
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("panic", "error", recovered, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
