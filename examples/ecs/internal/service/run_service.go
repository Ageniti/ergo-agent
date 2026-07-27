// Package service implements ECS run orchestration.
package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
	"github.com/ageniti/ergo-agent/internal/buildinfo"
)

const (
	PromptBundleVersion = buildinfo.PromptBundleVersion
	RuntimeVersion      = buildinfo.RuntimeVersion
)

type RunService struct{ repo repository.Repository }

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

func NewRunService(repo repository.Repository) *RunService { return &RunService{repo: repo} }

type CreateRunRequest struct {
	TenantID       string         `json:"tenant_id"`
	SessionID      string         `json:"session_id,omitempty"`
	AgentID        string         `json:"agent_id"`
	ParentRunID    *string        `json:"parent_run_id,omitempty"`
	Input          map[string]any `json:"input"`
	IdempotencyKey string         `json:"idempotency_key"`
}

func (s *RunService) Create(ctx context.Context, request CreateRunRequest) (model.Run, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.TenantID == "" {
		return model.Run{}, invalid("tenant_id is required")
	}
	if len(request.AgentID) < 1 || len(request.AgentID) > 64 || strings.IndexFunc(request.AgentID, func(value rune) bool {
		return !(value == '-' || value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9')
	}) >= 0 {
		return model.Run{}, invalid("agent_id must match [A-Za-z0-9_-]{1,64}")
	}
	if request.Input == nil {
		return model.Run{}, invalid("input is required")
	}
	for _, field := range []string{"cwd", "provider", "model"} {
		value, ok := request.Input[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return model.Run{}, invalid("input.%s is required", field)
		}
		request.Input[field] = strings.TrimSpace(value)
	}
	if thinking, exists := request.Input["thinking_level"]; exists {
		value, ok := thinking.(string)
		allowed := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
		if !ok || !allowed[value] {
			return model.Run{}, invalid("input.thinking_level is invalid")
		}
	}
	if planMode, exists := request.Input["plan_mode"]; exists {
		if _, ok := planMode.(bool); !ok {
			return model.Run{}, invalid("input.plan_mode must be boolean")
		}
	}
	if trusted, exists := request.Input["project_trusted"]; exists {
		if _, ok := trusted.(bool); !ok {
			return model.Run{}, invalid("input.project_trusted must be boolean")
		}
	}
	if rawScope, exists := request.Input["agent_scope"]; exists {
		scope, ok := rawScope.(string)
		if !ok || (scope != "user" && scope != "project" && scope != "both") {
			return model.Run{}, invalid("input.agent_scope must be user, project, or both")
		}
	}
	operation, _ := request.Input["operation"].(string)
	if operation == "" {
		operation = "prompt"
		request.Input["operation"] = operation
	}
	allowedOperations := map[string]bool{"prompt": true, "compact": true, "navigate_tree": true, "skill": true, "prompt_template": true, "set_model": true, "set_thinking_level": true, "set_active_tools": true, "set_queue_modes": true, "set_plan_mode": true, "append_custom_entry": true, "append_custom_message": true, "set_label": true, "set_session_name": true, "extension_command": true, "inspect": true, "package_install": true, "package_update": true, "package_remove": true, "package_list": true}
	if !allowedOperations[operation] {
		return model.Run{}, invalid("input.operation is invalid")
	}
	if operation == "prompt" {
		prompt, ok := request.Input["prompt"].(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return model.Run{}, invalid("input.prompt is required")
		}
		request.Input["prompt"] = strings.TrimSpace(prompt)
	}
	if operation == "navigate_tree" {
		target, ok := request.Input["target_entry_id"].(string)
		if !ok || strings.TrimSpace(target) == "" {
			return model.Run{}, invalid("input.target_entry_id is required")
		}
	}
	if operation == "skill" {
		name, ok := request.Input["skill_name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return model.Run{}, invalid("input.skill_name is required")
		}
	}
	if operation == "prompt_template" {
		name, ok := request.Input["template_name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return model.Run{}, invalid("input.template_name is required")
		}
		if args, exists := request.Input["template_args"]; exists {
			values, ok := args.([]any)
			if !ok {
				return model.Run{}, invalid("input.template_args must be an array of strings")
			}
			for _, value := range values {
				if _, ok := value.(string); !ok {
					return model.Run{}, invalid("input.template_args must be an array of strings")
				}
			}
		}
	}
	if operation == "package_install" || operation == "package_update" || operation == "package_remove" {
		source, ok := request.Input["package_source"].(string)
		if !ok || strings.TrimSpace(source) == "" {
			return model.Run{}, invalid("input.package_source is required")
		}
		scope, _ := request.Input["package_scope"].(string)
		if scope != "" && scope != "user" && scope != "project" {
			return model.Run{}, invalid("input.package_scope must be user or project")
		}
		if persist, exists := request.Input["package_persist"]; exists {
			if _, ok := persist.(bool); !ok {
				return model.Run{}, invalid("input.package_persist must be boolean")
			}
		}
	}
	if operation == "set_model" {
		for _, field := range []string{"target_provider", "target_model"} {
			if value, ok := request.Input[field].(string); !ok || strings.TrimSpace(value) == "" {
				return model.Run{}, invalid("input.%s is required", field)
			}
		}
	}
	if operation == "set_thinking_level" {
		value, _ := request.Input["target_thinking_level"].(string)
		allowed := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
		if !allowed[value] {
			return model.Run{}, invalid("input.target_thinking_level is invalid")
		}
	}
	if operation == "set_active_tools" {
		values, ok := request.Input["active_tool_names"].([]any)
		if !ok {
			return model.Run{}, invalid("input.active_tool_names must be an array of strings")
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return model.Run{}, invalid("input.active_tool_names must be an array of strings")
			}
		}
	}
	if operation == "set_plan_mode" {
		if _, ok := request.Input["plan_mode"].(bool); !ok {
			return model.Run{}, invalid("input.plan_mode must be boolean")
		}
		if value, exists := request.Input["plan_executing"]; exists {
			if _, ok := value.(bool); !ok {
				return model.Run{}, invalid("input.plan_executing must be boolean")
			}
		}
	}
	if operation == "append_custom_entry" || operation == "append_custom_message" {
		value, ok := request.Input["custom_type"].(string)
		if !ok || strings.TrimSpace(value) == "" || len(value) > 128 {
			return model.Run{}, invalid("input.custom_type is required and must not exceed 128 characters")
		}
	}
	if operation == "append_custom_message" {
		if _, ok := request.Input["custom_content"].(string); !ok {
			return model.Run{}, invalid("input.custom_content is required")
		}
	}
	if operation == "set_label" {
		if value, ok := request.Input["target_entry_id"].(string); !ok || strings.TrimSpace(value) == "" {
			return model.Run{}, invalid("input.target_entry_id is required")
		}
	}
	if operation == "set_session_name" {
		if value, ok := request.Input["session_name"].(string); !ok || len(value) > 255 {
			return model.Run{}, invalid("input.session_name is required and must not exceed 255 characters")
		}
	}
	if operation == "extension_command" {
		value, ok := request.Input["command_name"].(string)
		if !ok || strings.TrimSpace(value) == "" || len(value) > 128 {
			return model.Run{}, invalid("input.command_name is required and must not exceed 128 characters")
		}
		if args, exists := request.Input["command_args"]; exists {
			if value, ok := args.(string); !ok || len(value) > 128*1024 {
				return model.Run{}, invalid("input.command_args must be a string not exceeding 128 KiB")
			}
		}
	}
	for _, field := range []string{"steering_mode", "follow_up_mode"} {
		if raw, exists := request.Input[field]; exists {
			value, ok := raw.(string)
			if !ok || value != "all" && value != "one-at-a-time" {
				return model.Run{}, invalid("input.%s is invalid", field)
			}
		}
	}
	if raw, exists := request.Input["transport"]; exists {
		value, ok := raw.(string)
		if !ok || value != "auto" && value != "sse" && value != "websocket" && value != "websocket-cached" {
			return model.Run{}, invalid("input.transport is invalid")
		}
	}
	for _, field := range []string{"provider_timeout_ms", "provider_max_retries", "provider_max_retry_delay_ms", "retry_max_retries", "retry_base_delay_ms", "compaction_reserve_tokens", "compaction_keep_recent_tokens", "context_window", "max_output_tokens"} {
		if raw, exists := request.Input[field]; exists {
			value, ok := raw.(float64)
			if !ok || value < 0 || value > 24*60*60*1000 || value != float64(int64(value)) {
				return model.Run{}, invalid("input.%s must be a non-negative integer", field)
			}
		}
	}
	for _, field := range []string{"retry_enabled", "compaction_enabled"} {
		if raw, exists := request.Input[field]; exists {
			if _, ok := raw.(bool); !ok {
				return model.Run{}, invalid("input.%s must be boolean", field)
			}
		}
	}
	if prompt, _ := request.Input["prompt"].(string); len(prompt) > 512*1024 {
		return model.Run{}, invalid("input.prompt exceeds 512 KiB")
	}
	if rawImages, exists := request.Input["images"]; exists {
		images, ok := rawImages.([]any)
		if !ok || len(images) > 10 {
			return model.Run{}, invalid("input.images must contain at most 10 images")
		}
		allowed := map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}
		totalEncoded := 0
		for _, raw := range images {
			image, ok := raw.(map[string]any)
			if !ok {
				return model.Run{}, invalid("input.images entries must be objects")
			}
			data, dataOK := image["data"].(string)
			mime, mimeOK := image["mimeType"].(string)
			totalEncoded += len(data)
			if !dataOK || data == "" || len(data) > 14*1024*1024 || totalEncoded > 28*1024*1024 || !mimeOK || !allowed[mime] {
				return model.Run{}, invalid("input.images contains an invalid image")
			}
			if _, err := base64.StdEncoding.DecodeString(data); err != nil {
				return model.Run{}, invalid("input.images data must be base64")
			}
		}
	}
	if request.IdempotencyKey == "" {
		return model.Run{}, invalid("idempotency_key is required")
	}
	run, err := s.repo.CreateRun(ctx, model.CreateRun{
		TenantID: request.TenantID, SessionID: request.SessionID, AgentID: request.AgentID,
		ParentRunID: request.ParentRunID, Input: request.Input, IdempotencyKey: request.IdempotencyKey,
		PromptBundleVersion: PromptBundleVersion, RuntimeVersion: RuntimeVersion,
	})
	var replay *repository.IdempotencyReplayError
	if err != nil && errors.As(err, &replay) {
		return s.repo.GetRun(ctx, request.TenantID, replay.RunID)
	}
	return run, err
}

func (s *RunService) Get(ctx context.Context, tenantID, runID string) (model.Run, error) {
	return s.repo.GetRun(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(runID))
}

func (s *RunService) Cancel(ctx context.Context, tenantID, runID string) (model.Run, error) {
	return s.repo.CancelRun(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(runID))
}

func (s *RunService) DecideApproval(ctx context.Context, tenantID, approvalID string, decision model.ApprovalDecision, reason string) error {
	if decision != model.ApprovalAllow && decision != model.ApprovalDeny {
		return invalid("decision must be allow or deny")
	}
	return s.repo.DecideApproval(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(approvalID), decision, strings.TrimSpace(reason))
}

func (s *RunService) AnswerInteraction(ctx context.Context, tenantID, interactionID string, response any, cancelled bool) error {
	if response == nil && !cancelled {
		return invalid("response is required unless cancelled is true")
	}
	return s.repo.AnswerInteraction(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(interactionID), response, cancelled)
}

func (s *RunService) ListRunApprovals(ctx context.Context, tenantID, runID string) ([]model.Approval, error) {
	return s.repo.ListRunApprovals(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(runID))
}

func (s *RunService) ListRunInteractions(ctx context.Context, tenantID, runID string) ([]model.Interaction, error) {
	return s.repo.ListRunInteractions(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(runID))
}

func (s *RunService) SendControl(ctx context.Context, tenantID, runID, controlType, content string) (model.RunControl, error) {
	controlType = strings.TrimSpace(controlType)
	content = strings.TrimSpace(content)
	if controlType != "steer" && controlType != "follow_up" && controlType != "next_turn" {
		return model.RunControl{}, invalid("delivery must be steer, follow_up, or next_turn")
	}
	if content == "" {
		return model.RunControl{}, invalid("content is required")
	}
	if len(content) > 128*1024 {
		return model.RunControl{}, invalid("content exceeds 128 KiB")
	}
	return s.repo.EnqueueRunControl(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(runID), controlType, content)
}

func (s *RunService) GetRunPlan(ctx context.Context, tenantID, runID string) (model.Plan, error) {
	return s.repo.GetRunPlan(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(runID))
}

func (s *RunService) DecidePlan(ctx context.Context, tenantID, planID, decision string) error {
	decision = strings.TrimSpace(decision)
	if decision != "approve" && decision != "deny" {
		return invalid("decision must be approve or deny")
	}
	return s.repo.DecidePlan(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(planID), decision == "approve")
}

func (s *RunService) ExecutePlan(ctx context.Context, tenantID, planID, idempotencyKey string) (model.Run, error) {
	tenantID = strings.TrimSpace(tenantID)
	plan, err := s.repo.GetPlan(ctx, tenantID, strings.TrimSpace(planID))
	if err != nil {
		return model.Run{}, err
	}
	if plan.Status != "approved" {
		return model.Run{}, repository.ErrConflict
	}
	source, err := s.repo.GetRun(ctx, tenantID, plan.RunID)
	if err != nil {
		return model.Run{}, err
	}
	remaining := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		if step.Status != "completed" {
			remaining = append(remaining, fmt.Sprintf("%d. %s", step.Position, step.Text))
		}
	}
	if len(remaining) == 0 {
		return model.Run{}, invalid("plan has no remaining steps")
	}
	input := make(map[string]any, len(source.Input)+3)
	for key, value := range source.Input {
		input[key] = value
	}
	input["operation"] = "prompt"
	input["plan_mode"] = false
	input["plan_executing"] = true
	input["plan_id"] = plan.ID
	input["prompt"] = "Execute the approved plan in order. Keep the todo state updated as work progresses. In the final response, include [DONE:n] for every numbered step completed so plan progress can be persisted.\n\nPlan:\n" + strings.Join(remaining, "\n")
	return s.Create(ctx, CreateRunRequest{
		TenantID: tenantID, SessionID: source.SessionID, AgentID: source.AgentID,
		ParentRunID: &source.ID, Input: input, IdempotencyKey: idempotencyKey,
	})
}

func (s *RunService) ListTodos(ctx context.Context, tenantID, sessionID string) ([]model.Todo, error) {
	tenantID, sessionID = strings.TrimSpace(tenantID), strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, invalid("session_id is required")
	}
	return s.repo.ListTodos(ctx, tenantID, sessionID)
}

func (s *RunService) GetSession(ctx context.Context, tenantID, sessionID string) (model.Session, error) {
	return s.repo.GetSession(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(sessionID))
}

func (s *RunService) ForkSession(ctx context.Context, tenantID, sourceSessionID, targetEntryID, position, requestedID, idempotencyKey string) (model.Session, error) {
	tenantID = strings.TrimSpace(tenantID)
	sourceSessionID = strings.TrimSpace(sourceSessionID)
	targetEntryID = strings.TrimSpace(targetEntryID)
	position = strings.TrimSpace(position)
	requestedID = strings.TrimSpace(requestedID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if tenantID == "" || sourceSessionID == "" {
		return model.Session{}, invalid("tenant_id and session_id are required")
	}
	if position == "" {
		position = "before"
	}
	if position != "before" && position != "at" {
		return model.Session{}, invalid("position must be before or at")
	}
	if requestedID != "" && !validUUID(requestedID) {
		return model.Session{}, invalid("requested session_id must be a UUID")
	}
	if idempotencyKey == "" || len(idempotencyKey) > 191 {
		return model.Session{}, invalid("Idempotency-Key is required and must not exceed 191 characters")
	}
	return s.repo.ForkSession(ctx, tenantID, sourceSessionID, targetEntryID, position, requestedID, idempotencyKey)
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func (s *RunService) ListSessionRuns(ctx context.Context, tenantID, sessionID string, limit int) ([]model.Run, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return s.repo.ListSessionRuns(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(sessionID), limit)
}
