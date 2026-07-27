package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/service"
)

type apiRepository struct {
	repository.Repository
	created    model.CreateRun
	run        model.Run
	events     []model.RunEvent
	eventAfter uint64
	eventLimit int
	pingErr    error
}

func (r *apiRepository) Ping(context.Context) error { return r.pingErr }

func (r *apiRepository) CreateRun(_ context.Context, input model.CreateRun) (model.Run, error) {
	r.created = input
	return r.run, nil
}

func (r *apiRepository) GetRun(_ context.Context, tenantID, runID string) (model.Run, error) {
	if r.run.ID == "" {
		return model.Run{}, repository.ErrNotFound
	}
	r.run.ID = runID
	r.run.TenantID = tenantID
	return r.run, nil
}

func (r *apiRepository) ListRunEvents(_ context.Context, _ string, _ string, after uint64, limit int) ([]model.RunEvent, error) {
	r.eventAfter, r.eventLimit = after, limit
	return r.events, nil
}

func newTestServer(repo *apiRepository) http.Handler {
	return NewServer(service.NewRunService(repo), repo, slog.New(slog.NewTextHandler(testWriter{t: nil}, nil)), "test-token").Handler()
}

// testWriter discards request logs so handler tests stay focused on the response contract.
type testWriter struct{ t *testing.T }

func (testWriter) Write(data []byte) (int, error) { return len(data), nil }

func TestDecodeJSONRejectsTrailingValues(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"value":"one"} {"value":"two"}`))
	response := httptest.NewRecorder()
	var destination struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(response, request, &destination); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	if response.Code != 400 || !strings.Contains(response.Body.String(), "exactly one JSON value") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlerEnforcesServiceAuthenticationTenantAndHeaders(t *testing.T) {
	handler := newTestServer(&apiRepository{})
	for _, test := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{name: "missing token", status: http.StatusUnauthorized},
		{name: "missing tenant", headers: map[string]string{"Authorization": "Bearer test-token"}, status: http.StatusBadRequest},
		{name: "authenticated", headers: map[string]string{"Authorization": "Bearer test-token", "X-Tenant-ID": "tenant-a", "X-Request-ID": "request-a"}, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("missing security headers: %#v", response.Header())
			}
			if response.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing request ID")
			}
		})
	}
}

func TestHandlerCreatesRunWithAuthenticatedTenantAndIdempotencyKey(t *testing.T) {
	repo := &apiRepository{run: model.Run{ID: "run-1", Status: model.RunQueued}}
	handler := newTestServer(repo)
	request := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(`{"agent_id":"coding-agent","input":{"prompt":" build it ","cwd":"/workspace/project","provider":"openai","model":"gpt-5"}}`))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("X-Tenant-ID", "tenant-a")
	request.Header.Set("Idempotency-Key", "request-a")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.created.TenantID != "tenant-a" || repo.created.IdempotencyKey != "request-a" {
		t.Fatalf("created=%+v", repo.created)
	}
	if repo.created.Input["prompt"] != "build it" {
		t.Fatalf("untrimmed input: %#v", repo.created.Input)
	}
}

func TestHandlerListsEventsWithBoundedPagination(t *testing.T) {
	repo := &apiRepository{
		run:    model.Run{ID: "run-1", Status: model.RunRunning},
		events: []model.RunEvent{{Sequence: 8, Type: "run.started"}},
	}
	handler := newTestServer(repo)
	request := httptest.NewRequest(http.MethodGet, "/v1/runs/run-1/events?after=7&limit=999", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("X-Tenant-ID", "tenant-a")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.eventAfter != 7 || repo.eventLimit != 100 {
		t.Fatalf("pagination after=%d limit=%d", repo.eventAfter, repo.eventLimit)
	}
	var body struct {
		Events []model.RunEvent `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Sequence != 8 {
		t.Fatalf("events=%+v", body.Events)
	}
}

func TestAwaitingStatesCloseCurrentSSEStream(t *testing.T) {
	for _, status := range []model.RunStatus{
		model.RunCompleted,
		model.RunFailed,
		model.RunCancelled,
		model.RunAwaitingApproval,
		model.RunAwaitingInput,
	} {
		if !isTerminalRun(status) {
			t.Fatalf("status %q should end the current stream", status)
		}
	}
	for _, status := range []model.RunStatus{model.RunQueued, model.RunRunning} {
		if isTerminalRun(status) {
			t.Fatalf("status %q should keep streaming", status)
		}
	}
}
