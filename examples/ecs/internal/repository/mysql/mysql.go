// Package mysqlrepo implements the ECS example repository with MySQL.
package mysqlrepo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ageniti/ergo-agent/examples/ecs/internal/model"
	"github.com/ageniti/ergo-agent/examples/ecs/internal/repository"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

type Repository struct {
	db *sql.DB
}

type Options struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
}

func Open(dsn string) (*Repository, error) {
	return OpenWithOptions(dsn, Options{MaxOpen: 20, MaxIdle: 10, MaxLifetime: 5 * time.Minute})
}

func OpenWithOptions(dsn string, options Options) (*Repository, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetConnMaxLifetime(options.MaxLifetime)
	db.SetMaxOpenConns(options.MaxOpen)
	db.SetMaxIdleConns(options.MaxIdle)
	return &Repository{db: db}, nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) Ping(ctx context.Context) error { return r.db.PingContext(ctx) }

func (r *Repository) CreateRun(ctx context.Context, input model.CreateRun) (model.Run, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.Run{}, err
	}
	defer tx.Rollback()

	if input.IdempotencyKey != "" {
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT run_id FROM agent_idempotency_keys WHERE tenant_id=? AND idempotency_key=?`, input.TenantID, input.IdempotencyKey).Scan(&existing)
		if err == nil {
			return model.Run{}, &repository.IdempotencyReplayError{RunID: existing}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return model.Run{}, err
		}
	}

	runID := newID()
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = newID()
	}
	payload, err := json.Marshal(input.Input)
	if err != nil {
		return model.Run{}, fmt.Errorf("marshal input: %w", err)
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_sessions (id, tenant_id, created_at, updated_at) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE id=id`, sessionID, input.TenantID, now, now)
	if err != nil {
		return model.Run{}, err
	}
	var sessionTenant string
	if err = tx.QueryRowContext(ctx, `SELECT tenant_id FROM agent_sessions WHERE id=? FOR UPDATE`, sessionID).Scan(&sessionTenant); err != nil {
		return model.Run{}, err
	}
	if sessionTenant != input.TenantID {
		return model.Run{}, repository.ErrConflict
	}
	var activeRuns int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE tenant_id=? AND session_id=? AND status IN ('queued','running','awaiting_approval','awaiting_input')`, input.TenantID, sessionID).Scan(&activeRuns); err != nil {
		return model.Run{}, err
	}
	if activeRuns > 0 {
		return model.Run{}, repository.ErrConflict
	}
	if input.ParentRunID != nil {
		var parentCount int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE id=? AND tenant_id=?`, *input.ParentRunID, input.TenantID).Scan(&parentCount); err != nil {
			return model.Run{}, err
		}
		if parentCount != 1 {
			return model.Run{}, repository.ErrNotFound
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_runs (id, tenant_id, session_id, agent_id, parent_run_id, status, input_json, prompt_bundle_version, runtime_version, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?, 1, ?, ?)`, runID, input.TenantID, sessionID, input.AgentID, input.ParentRunID, payload, input.PromptBundleVersion, input.RuntimeVersion, now, now)
	if err != nil {
		return model.Run{}, err
	}
	if planID, ok := input.Input["plan_id"].(string); ok && planID != "" {
		result, updateErr := tx.ExecContext(ctx, `UPDATE agent_plans p JOIN agent_runs planned ON planned.id=p.run_id SET p.status='executing', p.execution_run_id=?, p.updated_at=UTC_TIMESTAMP(6), p.version=p.version+1 WHERE p.id=? AND planned.tenant_id=? AND p.status='approved'`, runID, planID, input.TenantID)
		if updateErr != nil {
			return model.Run{}, updateErr
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return model.Run{}, repository.ErrConflict
		}
	}
	jobPayloadValue := map[string]any{
		"type": "run", "requestId": runID, "runId": runID, "sessionId": sessionID, "tenantId": input.TenantID,
		"agentId": input.AgentID, "agentScope": input.Input["agent_scope"], "prompt": input.Input["prompt"], "cwd": input.Input["cwd"],
		"provider": input.Input["provider"], "model": input.Input["model"],
		"thinkingLevel": input.Input["thinking_level"], "planMode": input.Input["plan_mode"], "planModeSpecified": inputHas(input.Input, "plan_mode"), "planId": input.Input["plan_id"], "planExecuting": input.Input["plan_executing"],
		"operation": input.Input["operation"], "customInstructions": input.Input["custom_instructions"],
		"targetEntryId": input.Input["target_entry_id"], "summarize": input.Input["summarize"],
		"replaceInstructions": input.Input["replace_instructions"], "label": input.Input["label"],
		"skillName":    input.Input["skill_name"],
		"templateName": input.Input["template_name"], "templateArgs": input.Input["template_args"],
		"images":       input.Input["images"],
		"steeringMode": input.Input["steering_mode"], "followUpMode": input.Input["follow_up_mode"],
		"transport": input.Input["transport"], "providerTimeoutMs": input.Input["provider_timeout_ms"],
		"providerMaxRetries": input.Input["provider_max_retries"], "providerMaxRetryDelayMs": input.Input["provider_max_retry_delay_ms"],
		"retryEnabled": input.Input["retry_enabled"], "retryMaxRetries": input.Input["retry_max_retries"], "retryBaseDelayMs": input.Input["retry_base_delay_ms"],
		"compactionEnabled": input.Input["compaction_enabled"], "compactionReserveTokens": input.Input["compaction_reserve_tokens"], "compactionKeepRecentTokens": input.Input["compaction_keep_recent_tokens"],
		"contextWindow": input.Input["context_window"], "maxOutputTokens": input.Input["max_output_tokens"],
		"targetProvider": input.Input["target_provider"], "targetModel": input.Input["target_model"], "targetThinkingLevel": input.Input["target_thinking_level"],
		"activeToolNames": input.Input["active_tool_names"], "customType": input.Input["custom_type"], "customData": input.Input["custom_data"],
		"customContent": input.Input["custom_content"], "display": input.Input["display"], "sessionName": input.Input["session_name"],
		"commandName": input.Input["command_name"], "commandArgs": input.Input["command_args"],
		"projectTrusted": input.Input["project_trusted"],
		"packageSource":  input.Input["package_source"], "packageScope": input.Input["package_scope"], "packagePersist": input.Input["package_persist"],
	}
	var previousState []byte
	stateErr := tx.QueryRowContext(ctx, `SELECT session_state_json FROM agent_runs WHERE tenant_id=? AND session_id=? AND id<>? AND session_state_json IS NOT NULL ORDER BY updated_at DESC LIMIT 1`, input.TenantID, sessionID, runID).Scan(&previousState)
	if stateErr == nil {
		var state map[string]any
		if json.Unmarshal(previousState, &state) == nil {
			jobPayloadValue["sessionEntries"] = state["entries"]
			jobPayloadValue["sessionLeafId"] = state["leafId"]
		}
	} else if errors.Is(stateErr, sql.ErrNoRows) {
		entryRows, queryErr := tx.QueryContext(ctx, `SELECT payload_json FROM agent_session_entries WHERE tenant_id=? AND session_id=? ORDER BY seq`, input.TenantID, sessionID)
		if queryErr != nil {
			return model.Run{}, queryErr
		}
		entries := make([]map[string]any, 0)
		for entryRows.Next() {
			var raw []byte
			if scanErr := entryRows.Scan(&raw); scanErr != nil {
				entryRows.Close()
				return model.Run{}, scanErr
			}
			var entry map[string]any
			if unmarshalErr := json.Unmarshal(raw, &entry); unmarshalErr != nil {
				entryRows.Close()
				return model.Run{}, unmarshalErr
			}
			entries = append(entries, entry)
		}
		if rowsErr := entryRows.Err(); rowsErr != nil {
			entryRows.Close()
			return model.Run{}, rowsErr
		}
		if rowsErr := entryRows.Close(); rowsErr != nil {
			return model.Run{}, rowsErr
		}
		if len(entries) > 0 {
			jobPayloadValue["sessionEntries"] = entries
		}
	} else {
		return model.Run{}, stateErr
	}
	if _, exists := jobPayloadValue["sessionLeafId"]; !exists {
		var activeLeaf sql.NullString
		if leafErr := tx.QueryRowContext(ctx, `SELECT active_leaf_id FROM agent_sessions WHERE tenant_id=? AND id=?`, input.TenantID, sessionID).Scan(&activeLeaf); leafErr != nil {
			return model.Run{}, leafErr
		} else if activeLeaf.Valid {
			jobPayloadValue["sessionLeafId"] = activeLeaf.String
		}
	}
	todoRows, err := tx.QueryContext(ctx, `SELECT ordinal, text, completed FROM agent_todos WHERE tenant_id=? AND session_id=? ORDER BY ordinal`, input.TenantID, sessionID)
	if err != nil {
		return model.Run{}, err
	}
	var todos []map[string]any
	for todoRows.Next() {
		var id int
		var text string
		var done bool
		if err := todoRows.Scan(&id, &text, &done); err != nil {
			todoRows.Close()
			return model.Run{}, err
		}
		todos = append(todos, map[string]any{"id": id, "text": text, "done": done})
	}
	if err := todoRows.Err(); err != nil {
		todoRows.Close()
		return model.Run{}, err
	}
	if err := todoRows.Close(); err != nil {
		return model.Run{}, err
	}
	if len(todos) > 0 {
		jobPayloadValue["todos"] = todos
	}
	jobPayload, _ := json.Marshal(jobPayloadValue)
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_jobs (id, run_id, kind, payload_json, status, attempts, max_attempts, available_at, created_at, updated_at) VALUES (?, ?, 'run', ?, 'ready', 0, 5, ?, ?, ?)`, newID(), runID, jobPayload, now, now, now)
	if err != nil {
		return model.Run{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_outbox (id, aggregate_type, aggregate_id, event_type, payload_json, created_at) VALUES (?, 'run', ?, 'run.queued', ?, ?)`, newID(), runID, jobPayload, now)
	if err != nil {
		return model.Run{}, err
	}
	if input.IdempotencyKey != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_idempotency_keys (tenant_id, idempotency_key, run_id, created_at) VALUES (?, ?, ?, ?)`, input.TenantID, input.IdempotencyKey, runID, now)
		if err != nil {
			return model.Run{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Run{}, err
	}
	return r.GetRun(ctx, input.TenantID, runID)
}

func inputHas(input map[string]any, key string) bool {
	_, ok := input[key]
	return ok
}

func (r *Repository) GetRun(ctx context.Context, tenantID, runID string) (model.Run, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id, tenant_id, session_id, agent_id, parent_run_id, status, input_json, output_json, error_code, error_message, prompt_bundle_version, runtime_version, version, created_at, updated_at, started_at, completed_at FROM agent_runs WHERE tenant_id=? AND id=?`, tenantID, runID)
	var run model.Run
	var inputJSON, outputJSON []byte
	var parent, errorCode, errorMessage sql.NullString
	var startedAt, completedAt sql.NullTime
	err := row.Scan(&run.ID, &run.TenantID, &run.SessionID, &run.AgentID, &parent, &run.Status, &inputJSON, &outputJSON, &errorCode, &errorMessage, &run.PromptBundleVersion, &run.RuntimeVersion, &run.Version, &run.CreatedAt, &run.UpdatedAt, &startedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Run{}, repository.ErrNotFound
	}
	if err != nil {
		return model.Run{}, err
	}
	_ = json.Unmarshal(inputJSON, &run.Input)
	if len(outputJSON) > 0 {
		_ = json.Unmarshal(outputJSON, &run.Output)
	}
	if parent.Valid {
		run.ParentRunID = &parent.String
	}
	if errorCode.Valid {
		run.ErrorCode = &errorCode.String
	}
	if errorMessage.Valid {
		run.ErrorMessage = &errorMessage.String
	}
	if startedAt.Valid {
		run.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	return run, nil
}

func (r *Repository) CancelRun(ctx context.Context, tenantID, runID string) (model.Run, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.Run{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='cancelled', completed_at=?, updated_at=?, version=version+1 WHERE tenant_id=? AND id=? AND status IN ('queued','running','awaiting_approval','awaiting_input')`, now, now, tenantID, runID)
	if err != nil {
		return model.Run{}, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		run, getErr := r.GetRun(ctx, tenantID, runID)
		if getErr != nil {
			return model.Run{}, getErr
		}
		return run, repository.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_jobs SET status='dead', lease_owner=NULL, lease_until=NULL, last_error='cancelled', updated_at=? WHERE run_id=? AND status IN ('ready','leased')`, now, runID); err != nil {
		return model.Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_plans SET status='cancelled', updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE execution_run_id=? AND status='executing'`, runID); err != nil {
		return model.Run{}, err
	}
	if err := r.recordEventTx(ctx, tx, tenantID, runID, "run.cancelled", map[string]any{"reason": "cancelled by request"}); err != nil {
		return model.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Run{}, err
	}
	return r.GetRun(ctx, tenantID, runID)
}

func (r *Repository) DecideApproval(ctx context.Context, tenantID, approvalID string, decision model.ApprovalDecision, reason string) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID, argumentHash string
	err = tx.QueryRowContext(ctx, `SELECT a.run_id, a.arguments_hash FROM agent_approvals a JOIN agent_runs r ON r.id=a.run_id WHERE a.id=? AND r.tenant_id=? AND a.decision IS NULL AND a.expires_at>UTC_TIMESTAMP(6) FOR UPDATE`, approvalID, tenantID).Scan(&runID, &argumentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ErrConflict
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_approvals SET decision=?, reason=?, decided_at=UTC_TIMESTAMP(6) WHERE id=?`, decision, reason, approvalID); err != nil {
		return err
	}
	var jobPayloadJSON, sessionStateJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT j.payload_json, r.session_state_json FROM agent_jobs j JOIN agent_runs r ON r.id=j.run_id WHERE j.run_id=? AND j.kind='run' FOR UPDATE`, runID).Scan(&jobPayloadJSON, &sessionStateJSON)
	if err != nil {
		return err
	}
	var jobPayload map[string]any
	if err = json.Unmarshal(jobPayloadJSON, &jobPayload); err != nil {
		return err
	}
	key := "approvedArgumentHashes"
	if decision == model.ApprovalDeny {
		key = "deniedArgumentHashes"
	}
	hashes := stringSlice(jobPayload[key])
	hashes = append(hashes, argumentHash)
	jobPayload[key] = hashes
	jobPayload["resume"] = true
	if len(sessionStateJSON) > 0 {
		var state map[string]any
		if json.Unmarshal(sessionStateJSON, &state) == nil {
			jobPayload["sessionEntries"] = state["entries"]
			jobPayload["sessionLeafId"] = state["leafId"]
		}
	}
	jobPayloadJSON, err = json.Marshal(jobPayload)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_jobs SET payload_json=?, status='ready', attempts=0, available_at=UTC_TIMESTAMP(6), lease_owner=NULL, lease_until=NULL, last_error=NULL, updated_at=UTC_TIMESTAMP(6) WHERE run_id=? AND kind='run'`, jobPayloadJSON, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status='queued', completed_at=NULL, error_code=NULL, error_message=NULL, updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status='awaiting_approval'`, runID); err != nil {
		return err
	}
	if err := r.recordEventTx(ctx, tx, tenantID, runID, "approval.decided", map[string]any{"approvalId": approvalID, "decision": decision, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ListRunApprovals(ctx context.Context, tenantID, runID string) ([]model.Approval, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT a.id, a.run_id, a.tool_call_id, a.tool_name, a.arguments_json, a.arguments_hash, a.decision, a.reason, a.expires_at, a.decided_at, a.created_at FROM agent_approvals a JOIN agent_runs r ON r.id=a.run_id WHERE r.tenant_id=? AND a.run_id=? ORDER BY a.created_at`, tenantID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	approvals := make([]model.Approval, 0)
	for rows.Next() {
		var approval model.Approval
		var arguments []byte
		var decision, reason sql.NullString
		var decidedAt sql.NullTime
		if err := rows.Scan(&approval.ID, &approval.RunID, &approval.ToolCallID, &approval.ToolName, &arguments, &approval.ArgumentsHash, &decision, &reason, &approval.ExpiresAt, &decidedAt, &approval.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(arguments, &approval.Arguments); err != nil {
			return nil, err
		}
		if decision.Valid {
			value := model.ApprovalDecision(decision.String)
			approval.Decision = &value
		}
		if reason.Valid {
			approval.Reason = &reason.String
		}
		if decidedAt.Valid {
			approval.DecidedAt = &decidedAt.Time
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

func (r *Repository) ListRunInteractions(ctx context.Context, tenantID, runID string) ([]model.Interaction, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT i.id, i.run_id, i.tool_call_id, i.kind, i.request_json, i.response_json, i.status, i.expires_at, i.answered_at, i.created_at FROM agent_interactions i JOIN agent_runs r ON r.id=i.run_id WHERE r.tenant_id=? AND i.run_id=? ORDER BY i.created_at`, tenantID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	interactions := []model.Interaction{}
	for rows.Next() {
		var item model.Interaction
		var requestJSON []byte
		var responseJSON []byte
		var answeredAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.RunID, &item.ToolCallID, &item.Kind, &requestJSON, &responseJSON, &item.Status, &item.ExpiresAt, &answeredAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(requestJSON, &item.Request); err != nil {
			return nil, err
		}
		if len(responseJSON) > 0 {
			if err := json.Unmarshal(responseJSON, &item.Response); err != nil {
				return nil, err
			}
		}
		if answeredAt.Valid {
			item.AnsweredAt = &answeredAt.Time
		}
		interactions = append(interactions, item)
	}
	return interactions, rows.Err()
}

func (r *Repository) AnswerInteraction(ctx context.Context, tenantID, interactionID string, response any, cancelled bool) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID, requestKind string
	err = tx.QueryRowContext(ctx, `SELECT i.run_id, i.kind FROM agent_interactions i JOIN agent_runs r ON r.id=i.run_id WHERE i.id=? AND r.tenant_id=? AND i.status='pending' AND i.expires_at>UTC_TIMESTAMP(6) FOR UPDATE`, interactionID, tenantID).Scan(&runID, &requestKind)
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ErrConflict
	}
	if err != nil {
		return err
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		return err
	}
	status := "answered"
	if cancelled {
		status = "cancelled"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_interactions SET response_json=?, status=?, answered_at=UTC_TIMESTAMP(6) WHERE id=?`, responseJSON, status, interactionID); err != nil {
		return err
	}
	if requestKind == "mcp_elicitation" {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status=CASE WHEN EXISTS (SELECT 1 FROM agent_interactions pending WHERE pending.run_id=? AND pending.status='pending') THEN 'awaiting_input' ELSE 'running' END, updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status IN ('running','awaiting_input')`, runID, runID); err != nil {
			return err
		}
		if err := r.recordEventTx(ctx, tx, tenantID, runID, "input.responded", map[string]any{"interactionId": interactionID, "status": status}); err != nil {
			return err
		}
		return tx.Commit()
	}
	var jobPayloadJSON, sessionStateJSON []byte
	if err = tx.QueryRowContext(ctx, `SELECT j.payload_json, r.session_state_json FROM agent_jobs j JOIN agent_runs r ON r.id=j.run_id WHERE j.run_id=? AND j.kind='run' FOR UPDATE`, runID).Scan(&jobPayloadJSON, &sessionStateJSON); err != nil {
		return err
	}
	var jobPayload map[string]any
	if err = json.Unmarshal(jobPayloadJSON, &jobPayload); err != nil {
		return err
	}
	jobPayload["resume"] = true
	if cancelled {
		jobPayload["resumePrompt"] = "The user cancelled the " + requestKind + ". Continue without those answers or ask only if required."
	} else {
		jobPayload["resumePrompt"] = "The user answered the " + requestKind + ":\n" + string(responseJSON) + "\nContinue using these answers."
	}
	if len(sessionStateJSON) > 0 {
		var state map[string]any
		if json.Unmarshal(sessionStateJSON, &state) == nil {
			jobPayload["sessionEntries"] = state["entries"]
			jobPayload["sessionLeafId"] = state["leafId"]
		}
	}
	jobPayloadJSON, err = json.Marshal(jobPayload)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_jobs SET payload_json=?, status='ready', attempts=0, available_at=UTC_TIMESTAMP(6), lease_owner=NULL, lease_until=NULL, last_error=NULL, updated_at=UTC_TIMESTAMP(6) WHERE run_id=? AND kind='run'`, jobPayloadJSON, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status='queued', completed_at=NULL, error_code=NULL, error_message=NULL, updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status='awaiting_input'`, runID); err != nil {
		return err
	}
	if err := r.recordEventTx(ctx, tx, tenantID, runID, "input.responded", map[string]any{"interactionId": interactionID, "status": status}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) GetRunInteraction(ctx context.Context, runID, interactionID string) (model.Interaction, error) {
	var item model.Interaction
	var requestJSON, responseJSON []byte
	var answeredAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT id, run_id, tool_call_id, kind, request_json, response_json, status, expires_at, answered_at, created_at FROM agent_interactions WHERE id=? AND run_id=?`, interactionID, runID).Scan(&item.ID, &item.RunID, &item.ToolCallID, &item.Kind, &requestJSON, &responseJSON, &item.Status, &item.ExpiresAt, &answeredAt, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Interaction{}, repository.ErrNotFound
	}
	if err != nil {
		return model.Interaction{}, err
	}
	if err := json.Unmarshal(requestJSON, &item.Request); err != nil {
		return model.Interaction{}, err
	}
	if len(responseJSON) > 0 {
		if err := json.Unmarshal(responseJSON, &item.Response); err != nil {
			return model.Interaction{}, err
		}
	}
	if answeredAt.Valid {
		item.AnsweredAt = &answeredAt.Time
	}
	return item, nil
}

func (r *Repository) recordEventTx(ctx context.Context, tx *sql.Tx, tenantID, runID, eventType string, payload map[string]any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventID := newID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_events (id, tenant_id, run_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(6))`, eventID, tenantID, runID, eventType, payloadJSON); err != nil {
		return err
	}
	outboxJSON, err := json.Marshal(map[string]any{"id": eventID, "tenant_id": tenantID, "run_id": runID, "type": eventType, "payload": payload})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_outbox (id, aggregate_type, aggregate_id, event_type, payload_json, created_at) VALUES (?, 'run', ?, ?, ?, UTC_TIMESTAMP(6))`, eventID, runID, eventType, outboxJSON)
	return err
}

func (r *Repository) StartRun(ctx context.Context, runID string) error {
	// A leased job is the exclusive execution fence. Allow an expired lease to
	// resume a run that was waiting inside an MCP elicitation on another ECS task.
	result, err := r.db.ExecContext(ctx, `UPDATE agent_runs SET status='running', started_at=COALESCE(started_at, UTC_TIMESTAMP(6)), completed_at=NULL, error_code=NULL, error_message=NULL, updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status IN ('queued','failed','running','awaiting_input')`, runID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *Repository) AppendRunEvent(ctx context.Context, tenantID, runID, eventType string, payload map[string]any) error {
	normalizedPayload, payloadJSON, err := normalizeEventPayload(payload)
	if err != nil {
		return err
	}
	payload = normalizedPayload
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	eventID := newID()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_events (id, tenant_id, run_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(6))`, eventID, tenantID, runID, eventType, payloadJSON)
	if err != nil {
		return err
	}
	outboxJSON, err := json.Marshal(map[string]any{"id": eventID, "tenant_id": tenantID, "run_id": runID, "type": eventType, "payload": payload})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_outbox (id, aggregate_type, aggregate_id, event_type, payload_json, created_at) VALUES (?, 'run', ?, ?, ?, UTC_TIMESTAMP(6))`, eventID, runID, eventType, outboxJSON); err != nil {
		return err
	}
	switch eventType {
	case "run.completed":
		_, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status='completed', output_json=?, completed_at=UTC_TIMESTAMP(6), updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status='running'`, payloadJSON, runID)
	case "run.failed":
		message, _ := payload["message"].(string)
		_, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status='failed', error_code='runtime_error', error_message=?, completed_at=UTC_TIMESTAMP(6), updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status IN ('queued','running')`, message, runID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE agent_plans SET status='failed', updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE execution_run_id=? AND status='executing'`, runID)
		}
	case "approval.requested":
		approvalID, _ := payload["approvalId"].(string)
		toolCallID, _ := payload["toolCallId"].(string)
		toolName, _ := payload["toolName"].(string)
		argumentHash, _ := payload["argumentsHash"].(string)
		policyVersion, _ := payload["policyVersion"].(string)
		argumentsJSON, marshalErr := json.Marshal(payload["input"])
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_approvals (id, run_id, tool_call_id, tool_name, arguments_json, arguments_hash, policy_version, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 15 MINUTE), UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE id=id`, approvalID, runID, toolCallID, toolName, argumentsJSON, argumentHash, policyVersion)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status='awaiting_approval', updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status='running'`, runID)
	case "input.requested":
		interactionID, _ := payload["interactionId"].(string)
		toolCallID, _ := payload["toolCallId"].(string)
		kind, _ := payload["kind"].(string)
		requestJSON, marshalErr := json.Marshal(payload["request"])
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_interactions (id, run_id, tool_call_id, kind, request_json, status, expires_at, created_at) VALUES (?, ?, ?, ?, ?, 'pending', DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 24 HOUR), UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE id=id`, interactionID, runID, toolCallID, kind, requestJSON)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status=CASE WHEN EXISTS (SELECT 1 FROM agent_interactions pending WHERE pending.id=? AND pending.status='pending') THEN 'awaiting_input' ELSE status END, updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status='running'`, interactionID, runID)
	case "session.snapshot":
		err = r.persistSessionSnapshot(ctx, tx, tenantID, runID, payload, payloadJSON)
	case "plan.created":
		err = r.persistPlan(ctx, tx, runID, payload)
	case "plan.progress":
		err = r.persistPlanProgress(ctx, tx, runID, payload)
	case "todo.updated":
		err = r.persistTodos(ctx, tx, tenantID, runID, payload)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// normalizeEventPayload establishes one representation at the runtime/storage
// boundary. Runtime events naturally contain typed slices and structs, while
// the persistence projections consume JSON-shaped []any/map[string]any values.
func normalizeEventPayload(payload map[string]any) (map[string]any, []byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, nil, err
	}
	return normalized, encoded, nil
}

func (r *Repository) persistSessionSnapshot(ctx context.Context, tx *sql.Tx, tenantID, runID string, payload map[string]any, payloadJSON []byte) error {
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM agent_runs WHERE id=? AND tenant_id=?`, runID, tenantID).Scan(&sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET session_state_json=?, updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=?`, payloadJSON, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_session_entries WHERE session_id=?`, sessionID); err != nil {
		return err
	}
	entries, _ := payload["entries"].([]any)
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := entry["id"].(string)
		entryType, _ := entry["type"].(string)
		if id == "" || entryType == "" {
			continue
		}
		var parent any
		if parentID, ok := entry["parentId"].(string); ok && parentID != "" {
			parent = parentID
		}
		entryJSON, marshalErr := json.Marshal(entry)
		if marshalErr != nil {
			return marshalErr
		}
		createdAt := time.Now().UTC()
		if timestamp, ok := entry["timestamp"].(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, timestamp); parseErr == nil {
				createdAt = parsed.UTC()
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_session_entries (id, tenant_id, session_id, parent_id, entry_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, tenantID, sessionID, parent, entryType, entryJSON, createdAt); err != nil {
			return err
		}
	}
	leafID, _ := payload["leafId"].(string)
	var leaf any
	if leafID != "" {
		leaf = leafID
	}
	var sessionName any
	for index := len(entries) - 1; index >= 0; index-- {
		entry, ok := entries[index].(map[string]any)
		if !ok || entry["type"] != "session_info" {
			continue
		}
		if name, ok := entry["name"].(string); ok {
			sessionName = name
		}
		break
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET active_leaf_id=?, name=COALESCE(?, name), updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND tenant_id=?`, leaf, sessionName, sessionID, tenantID)
	return err
}

func (r *Repository) GetRunPlan(ctx context.Context, tenantID, runID string) (model.Plan, error) {
	return r.queryPlan(ctx, `SELECT p.id, p.run_id, r.session_id, p.execution_run_id, p.status, p.version, p.created_at, p.updated_at FROM agent_plans p JOIN agent_runs r ON r.id=p.run_id WHERE r.tenant_id=? AND (p.run_id=? OR p.execution_run_id=?)`, tenantID, runID, runID)
}

func (r *Repository) GetPlan(ctx context.Context, tenantID, planID string) (model.Plan, error) {
	return r.queryPlan(ctx, `SELECT p.id, p.run_id, r.session_id, p.execution_run_id, p.status, p.version, p.created_at, p.updated_at FROM agent_plans p JOIN agent_runs r ON r.id=p.run_id WHERE r.tenant_id=? AND p.id=?`, tenantID, planID)
}

func (r *Repository) queryPlan(ctx context.Context, query string, args ...any) (model.Plan, error) {
	var plan model.Plan
	var executionRunID sql.NullString
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&plan.ID, &plan.RunID, &plan.SessionID, &executionRunID, &plan.Status, &plan.Version, &plan.CreatedAt, &plan.UpdatedAt); errors.Is(err, sql.ErrNoRows) {
		return model.Plan{}, repository.ErrNotFound
	} else if err != nil {
		return model.Plan{}, err
	}
	if executionRunID.Valid {
		plan.ExecutionRunID = &executionRunID.String
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, position, text, status FROM agent_plan_steps WHERE plan_id=? ORDER BY position`, plan.ID)
	if err != nil {
		return model.Plan{}, err
	}
	defer rows.Close()
	plan.Steps = make([]model.PlanStep, 0)
	for rows.Next() {
		var step model.PlanStep
		if err := rows.Scan(&step.ID, &step.Position, &step.Text, &step.Status); err != nil {
			return model.Plan{}, err
		}
		plan.Steps = append(plan.Steps, step)
	}
	return plan, rows.Err()
}

func (r *Repository) DecidePlan(ctx context.Context, tenantID, planID string, approved bool) error {
	status := "cancelled"
	if approved {
		status = "approved"
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID string
	if err := tx.QueryRowContext(ctx, `SELECT p.run_id FROM agent_plans p JOIN agent_runs r ON r.id=p.run_id WHERE p.id=? AND r.tenant_id=? FOR UPDATE`, planID, tenantID).Scan(&runID); errors.Is(err, sql.ErrNoRows) {
		return repository.ErrNotFound
	} else if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_plans SET status=?, updated_at=UTC_TIMESTAMP(6), version=version+1 WHERE id=? AND status='awaiting_approval'`, status, planID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	if err := r.recordEventTx(ctx, tx, tenantID, runID, "plan.decided", map[string]any{"planId": planID, "decision": map[bool]string{true: "approve", false: "deny"}[approved]}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ListTodos(ctx context.Context, tenantID, sessionID string) ([]model.Todo, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, session_id, run_id, ordinal, text, completed, created_at, updated_at FROM agent_todos WHERE tenant_id=? AND session_id=? ORDER BY ordinal`, tenantID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	todos := make([]model.Todo, 0)
	for rows.Next() {
		var todo model.Todo
		var runID sql.NullString
		if err := rows.Scan(&todo.ID, &todo.SessionID, &runID, &todo.Ordinal, &todo.Text, &todo.Completed, &todo.CreatedAt, &todo.UpdatedAt); err != nil {
			return nil, err
		}
		if runID.Valid {
			todo.RunID = &runID.String
		}
		todos = append(todos, todo)
	}
	return todos, rows.Err()
}

func (r *Repository) GetSession(ctx context.Context, tenantID, sessionID string) (model.Session, error) {
	var session model.Session
	var name, leaf, parent sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT id, tenant_id, name, active_leaf_id, parent_session_id, version, created_at, updated_at FROM agent_sessions WHERE tenant_id=? AND id=?`, tenantID, sessionID).Scan(&session.ID, &session.TenantID, &name, &leaf, &parent, &session.Version, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, repository.ErrNotFound
	}
	if err != nil {
		return model.Session{}, err
	}
	if name.Valid {
		session.Name = &name.String
	}
	if leaf.Valid {
		session.ActiveLeafID = &leaf.String
	}
	if parent.Valid {
		session.ParentSessionID = &parent.String
	}
	rows, err := r.db.QueryContext(ctx, `SELECT seq, id, parent_id, entry_type, payload_json, created_at FROM agent_session_entries WHERE tenant_id=? AND session_id=? ORDER BY seq`, tenantID, sessionID)
	if err != nil {
		return model.Session{}, err
	}
	defer rows.Close()
	session.Entries = make([]model.SessionEntry, 0)
	for rows.Next() {
		var entry model.SessionEntry
		var parent sql.NullString
		var payload []byte
		if err := rows.Scan(&entry.Sequence, &entry.ID, &parent, &entry.Type, &payload, &entry.CreatedAt); err != nil {
			return model.Session{}, err
		}
		if parent.Valid {
			entry.ParentID = &parent.String
		}
		if err := json.Unmarshal(payload, &entry.Payload); err != nil {
			return model.Session{}, err
		}
		session.Entries = append(session.Entries, entry)
	}
	return session, rows.Err()
}

func (r *Repository) ForkSession(ctx context.Context, tenantID, sourceSessionID, targetEntryID, position, requestedID, idempotencyKey string) (model.Session, error) {
	var replaySessionID, replaySourceID string
	if err := r.db.QueryRowContext(ctx, `SELECT session_id, source_session_id FROM agent_session_idempotency_keys WHERE tenant_id=? AND idempotency_key=?`, tenantID, idempotencyKey).Scan(&replaySessionID, &replaySourceID); err == nil {
		if replaySourceID != sourceSessionID {
			return model.Session{}, repository.ErrConflict
		}
		return r.GetSession(ctx, tenantID, replaySessionID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.Session{}, err
	}
	defer tx.Rollback()

	var sourceName, activeLeaf sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT name, active_leaf_id FROM agent_sessions WHERE tenant_id=? AND id=? FOR UPDATE`, tenantID, sourceSessionID).Scan(&sourceName, &activeLeaf); errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, repository.ErrNotFound
	} else if err != nil {
		return model.Session{}, err
	}
	leafID := strings.TrimSpace(targetEntryID)
	if leafID == "" && activeLeaf.Valid {
		leafID = activeLeaf.String
	}
	if leafID == "" {
		return model.Session{}, repository.ErrNotFound
	}

	var parent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT parent_id FROM agent_session_entries WHERE tenant_id=? AND session_id=? AND id=?`, tenantID, sourceSessionID, leafID).Scan(&parent); errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, repository.ErrNotFound
	} else if err != nil {
		return model.Session{}, err
	}
	if position == "before" {
		if !parent.Valid {
			return model.Session{}, repository.ErrConflict
		}
		leafID = parent.String
	}

	path := make([]string, 0)
	current := leafID
	for current != "" {
		path = append(path, current)
		var next sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT parent_id FROM agent_session_entries WHERE tenant_id=? AND session_id=? AND id=?`, tenantID, sourceSessionID, current).Scan(&next); errors.Is(err, sql.ErrNoRows) {
			return model.Session{}, repository.ErrNotFound
		} else if err != nil {
			return model.Session{}, err
		}
		if next.Valid {
			current = next.String
		} else {
			current = ""
		}
	}

	newSessionID := strings.TrimSpace(requestedID)
	if newSessionID == "" {
		newSessionID = deterministicSessionID(tenantID, idempotencyKey)
	}
	now := time.Now().UTC()
	var name any
	if sourceName.Valid {
		name = sourceName.String
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions (id, tenant_id, parent_session_id, name, active_leaf_id, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, newSessionID, tenantID, sourceSessionID, name, leafID, now, now); err != nil {
		if isDuplicateKey(err) {
			_ = tx.Rollback()
			var existingSessionID, existingSourceID string
			if replayErr := r.db.QueryRowContext(ctx, `SELECT session_id, source_session_id FROM agent_session_idempotency_keys WHERE tenant_id=? AND idempotency_key=?`, tenantID, idempotencyKey).Scan(&existingSessionID, &existingSourceID); replayErr == nil && existingSourceID == sourceSessionID {
				return r.GetSession(ctx, tenantID, existingSessionID)
			}
			return model.Session{}, repository.ErrConflict
		}
		return model.Session{}, err
	}
	for index := len(path) - 1; index >= 0; index-- {
		result, err := tx.ExecContext(ctx, `INSERT INTO agent_session_entries (id, tenant_id, session_id, parent_id, entry_type, payload_json, created_at) SELECT id, tenant_id, ?, parent_id, entry_type, payload_json, created_at FROM agent_session_entries WHERE tenant_id=? AND session_id=? AND id=?`, newSessionID, tenantID, sourceSessionID, path[index])
		if err != nil {
			return model.Session{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.Session{}, repository.ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_session_idempotency_keys (tenant_id, idempotency_key, source_session_id, session_id, created_at) VALUES (?, ?, ?, ?, ?)`, tenantID, idempotencyKey, sourceSessionID, newSessionID, now); err != nil {
		return model.Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Session{}, err
	}
	return r.GetSession(ctx, tenantID, newSessionID)
}

func deterministicSessionID(tenantID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(tenantID + "\x00" + idempotencyKey))
	value := hex.EncodeToString(digest[:16])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:32]
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func (r *Repository) ListSessionRuns(ctx context.Context, tenantID, sessionID string, limit int) ([]model.Run, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM agent_runs WHERE tenant_id=? AND session_id=? ORDER BY created_at DESC LIMIT ?`, tenantID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	runs := make([]model.Run, 0, len(ids))
	for _, id := range ids {
		run, err := r.GetRun(ctx, tenantID, id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (r *Repository) persistTodos(ctx context.Context, tx *sql.Tx, tenantID, runID string, payload map[string]any) error {
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM agent_runs WHERE id=? AND tenant_id=?`, runID, tenantID).Scan(&sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_todos WHERE tenant_id=? AND session_id=?`, tenantID, sessionID); err != nil {
		return err
	}
	todos, _ := payload["todos"].([]any)
	for index, raw := range todos {
		todo, _ := raw.(map[string]any)
		text, _ := todo["text"].(string)
		done, _ := todo["done"].(bool)
		if text == "" {
			continue
		}
		ordinal := index + 1
		if id, ok := todo["id"].(float64); ok {
			ordinal = int(id)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_todos (id, tenant_id, session_id, run_id, ordinal, text, completed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, newID(), tenantID, sessionID, runID, ordinal, text, done); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) ListRunEvents(ctx context.Context, tenantID, runID string, after uint64, limit int) ([]model.RunEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT e.seq, e.id, e.run_id, e.event_type, e.payload_json, e.created_at FROM agent_events e JOIN agent_runs r ON r.id=e.run_id WHERE r.tenant_id=? AND e.run_id=? AND e.seq>? ORDER BY e.seq LIMIT ?`, tenantID, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]model.RunEvent, 0, limit)
	for rows.Next() {
		var event model.RunEvent
		var payload []byte
		if err := rows.Scan(&event.Sequence, &event.ID, &event.RunID, &event.Type, &payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *Repository) EnqueueRunControl(ctx context.Context, tenantID, runID, controlType, content string) (model.RunControl, error) {
	control := model.RunControl{ID: newID(), RunID: runID, Type: controlType, Content: content}
	result, err := r.db.ExecContext(ctx, `INSERT INTO agent_run_controls (id, tenant_id, run_id, control_type, content, status, created_at) SELECT ?, ?, id, ?, ?, 'pending', UTC_TIMESTAMP(6) FROM agent_runs WHERE id=? AND tenant_id=? AND status='running'`, control.ID, tenantID, controlType, content, runID, tenantID)
	if err != nil {
		return model.RunControl{}, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		if _, err := r.GetRun(ctx, tenantID, runID); errors.Is(err, repository.ErrNotFound) {
			return model.RunControl{}, repository.ErrNotFound
		}
		return model.RunControl{}, repository.ErrConflict
	}
	return control, nil
}

func (r *Repository) PendingRunControls(ctx context.Context, runID string, limit int) ([]model.RunControl, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, run_id, control_type, content FROM agent_run_controls WHERE run_id=? AND status='pending' ORDER BY seq LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	controls := make([]model.RunControl, 0, limit)
	for rows.Next() {
		var control model.RunControl
		if err := rows.Scan(&control.ID, &control.RunID, &control.Type, &control.Content); err != nil {
			return nil, err
		}
		controls = append(controls, control)
	}
	return controls, rows.Err()
}

func (r *Repository) MarkRunControlDelivered(ctx context.Context, controlID string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE agent_run_controls SET status='delivered', delivered_at=UTC_TIMESTAMP(6) WHERE id=? AND status='pending'`, controlID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *Repository) persistPlan(ctx context.Context, tx *sql.Tx, runID string, payload map[string]any) error {
	planID := newID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_plans (id, run_id, status, version, created_at, updated_at) VALUES (?, ?, 'awaiting_approval', 1, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE status='awaiting_approval', version=version+1, updated_at=UTC_TIMESTAMP(6)`, planID, runID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_plans WHERE run_id=?`, runID).Scan(&planID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_plan_steps WHERE plan_id=?`, planID); err != nil {
		return err
	}
	steps, _ := payload["steps"].([]any)
	for index, raw := range steps {
		step, _ := raw.(map[string]any)
		text, _ := step["text"].(string)
		if text == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_plan_steps (id, plan_id, position, text, status, created_at, updated_at) VALUES (?, ?, ?, ?, 'pending', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, newID(), planID, index+1, text); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) persistPlanProgress(ctx context.Context, tx *sql.Tx, runID string, payload map[string]any) error {
	var planID string
	if err := tx.QueryRowContext(ctx, `SELECT p.id FROM agent_plans p JOIN agent_runs planned ON planned.id=p.run_id JOIN agent_runs current_run ON current_run.id=? AND current_run.session_id=planned.session_id WHERE p.execution_run_id=? OR p.execution_run_id IS NULL ORDER BY (p.execution_run_id=?) DESC, p.created_at DESC LIMIT 1`, runID, runID, runID).Scan(&planID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	completed, _ := payload["completedSteps"].([]any)
	for _, raw := range completed {
		position, ok := raw.(float64)
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_plan_steps ps JOIN agent_plans p ON p.id=ps.plan_id SET ps.status='completed', ps.updated_at=UTC_TIMESTAMP(6), p.status='executing', p.updated_at=UTC_TIMESTAMP(6) WHERE p.id=? AND ps.position=?`, planID, int(position)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_plans p SET p.status='completed', p.updated_at=UTC_TIMESTAMP(6) WHERE p.id=? AND NOT EXISTS (SELECT 1 FROM agent_plan_steps ps WHERE ps.plan_id=p.id AND ps.status<>'completed')`, planID); err != nil {
		return err
	}
	return nil
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items)+1)
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	if strings, ok := value.([]string); ok {
		return append([]string(nil), strings...)
	}
	return result
}

func (r *Repository) LeaseJobs(ctx context.Context, owner string, limit int, lease time.Duration) ([]model.Job, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_jobs SET status='dead', last_error=COALESCE(last_error,'lease expired after maximum attempts'), lease_owner=NULL, lease_until=NULL, updated_at=UTC_TIMESTAMP(6) WHERE status='leased' AND lease_until<UTC_TIMESTAMP(6) AND attempts>=max_attempts`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs r JOIN agent_jobs j ON j.run_id=r.id SET r.status='failed', r.error_code='executor_lost', r.error_message=j.last_error, r.completed_at=UTC_TIMESTAMP(6), r.updated_at=UTC_TIMESTAMP(6), r.version=r.version+1 WHERE j.status='dead' AND r.status='running'`); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, run_id, kind, payload_json, attempts, max_attempts FROM agent_jobs WHERE status IN ('ready','leased') AND attempts<max_attempts AND available_at<=UTC_TIMESTAMP(6) AND (status='ready' OR lease_until<UTC_TIMESTAMP(6)) ORDER BY available_at, created_at LIMIT ? FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]model.Job, 0, limit)
	for rows.Next() {
		var job model.Job
		var payload []byte
		if err := rows.Scan(&job.ID, &job.RunID, &job.Kind, &payload, &job.Attempts, &job.MaxAttempts); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(payload, &job.Payload)
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	until := time.Now().UTC().Add(lease)
	leaseMicros := lease.Microseconds()
	for i := range jobs {
		_, err = tx.ExecContext(ctx, `UPDATE agent_jobs SET status='leased', lease_owner=?, lease_until=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND), attempts=attempts+1, updated_at=UTC_TIMESTAMP(6) WHERE id=?`, owner, leaseMicros, jobs[i].ID)
		if err != nil {
			return nil, err
		}
		jobs[i].LeaseOwner = &owner
		jobs[i].LeaseUntil = &until
		jobs[i].Attempts++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *Repository) ExpireApprovals(ctx context.Context) (int64, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs r JOIN agent_approvals a ON a.run_id=r.id SET r.status='failed', r.error_code='approval_timeout', r.error_message='approval expired', r.completed_at=UTC_TIMESTAMP(6), r.updated_at=UTC_TIMESTAMP(6), r.version=r.version+1 WHERE r.status='awaiting_approval' AND a.decision IS NULL AND a.expires_at<=UTC_TIMESTAMP(6)`)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_approvals SET decision='deny', reason='expired', decided_at=UTC_TIMESTAMP(6) WHERE decision IS NULL AND expires_at<=UTC_TIMESTAMP(6)`); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *Repository) HeartbeatJob(ctx context.Context, jobID, owner string, lease time.Duration) error {
	result, err := r.db.ExecContext(ctx, `UPDATE agent_jobs SET lease_until=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND), updated_at=UTC_TIMESTAMP(6) WHERE id=? AND lease_owner=? AND status='leased'`, lease.Microseconds(), jobID, owner)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *Repository) CompleteJob(ctx context.Context, jobID, owner string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE agent_jobs SET status='completed', lease_owner=NULL, lease_until=NULL, updated_at=UTC_TIMESTAMP(6) WHERE id=? AND lease_owner=? AND status='leased'`, jobID, owner)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *Repository) FailJob(ctx context.Context, jobID, owner, failure string, retryAfter time.Duration) error {
	result, err := r.db.ExecContext(ctx, `UPDATE agent_jobs SET status=IF(attempts>=max_attempts,'dead','ready'), last_error=?, available_at=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND), lease_owner=NULL, lease_until=NULL, updated_at=UTC_TIMESTAMP(6) WHERE id=? AND lease_owner=? AND status='leased'`, failure, retryAfter.Microseconds(), jobID, owner)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *Repository) LeaseOutbox(ctx context.Context, owner string, limit int, lease time.Duration) ([]model.OutboxEvent, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT seq, id, aggregate_type, aggregate_id, event_type, payload_json, attempts, created_at FROM agent_outbox WHERE published_at IS NULL AND attempts<20 AND next_attempt_at<=UTC_TIMESTAMP(6) AND (lease_until IS NULL OR lease_until<UTC_TIMESTAMP(6)) ORDER BY seq LIMIT ? FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	events := make([]model.OutboxEvent, 0, limit)
	for rows.Next() {
		var event model.OutboxEvent
		var payload []byte
		if err := rows.Scan(&event.Sequence, &event.ID, &event.AggregateType, &event.AggregateID, &event.Type, &payload, &event.Attempts, &event.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(payload, &event.Payload); err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_outbox SET lease_owner=?, lease_until=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND), attempts=attempts+1 WHERE id=? AND published_at IS NULL`, owner, lease.Microseconds(), event.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

func (r *Repository) MarkOutboxPublished(ctx context.Context, eventID, owner string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE agent_outbox SET published_at=UTC_TIMESTAMP(6), lease_owner=NULL, lease_until=NULL, last_error=NULL WHERE id=? AND lease_owner=? AND published_at IS NULL`, eventID, owner)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *Repository) FailOutbox(ctx context.Context, eventID, owner, failure string, retryAfter time.Duration) error {
	result, err := r.db.ExecContext(ctx, `UPDATE agent_outbox SET last_error=?, next_attempt_at=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? MICROSECOND), lease_owner=NULL, lease_until=NULL WHERE id=? AND lease_owner=? AND published_at IS NULL`, failure, retryAfter.Microseconds(), eventID, owner)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return repository.ErrConflict
	}
	return nil
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Sprintf("generate id: %v", err))
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], bytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], bytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], bytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], bytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], bytes[10:16])
	return string(encoded)
}
