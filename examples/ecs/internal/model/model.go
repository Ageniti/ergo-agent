// Package model defines the ECS example's persistent domain records.
package model

import "time"

type RunStatus string

const (
	RunQueued           RunStatus = "queued"
	RunRunning          RunStatus = "running"
	RunAwaitingApproval RunStatus = "awaiting_approval"
	RunAwaitingInput    RunStatus = "awaiting_input"
	RunCompleted        RunStatus = "completed"
	RunFailed           RunStatus = "failed"
	RunCancelled        RunStatus = "cancelled"
)

type Run struct {
	ID                  string         `json:"id"`
	TenantID            string         `json:"tenant_id"`
	SessionID           string         `json:"session_id"`
	AgentID             string         `json:"agent_id"`
	ParentRunID         *string        `json:"parent_run_id,omitempty"`
	Status              RunStatus      `json:"status"`
	Input               map[string]any `json:"input"`
	Output              map[string]any `json:"output,omitempty"`
	ErrorCode           *string        `json:"error_code,omitempty"`
	ErrorMessage        *string        `json:"error_message,omitempty"`
	PromptBundleVersion string         `json:"prompt_bundle_version"`
	RuntimeVersion      string         `json:"runtime_version"`
	Version             uint64         `json:"version"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	StartedAt           *time.Time     `json:"started_at,omitempty"`
	CompletedAt         *time.Time     `json:"completed_at,omitempty"`
}

type CreateRun struct {
	TenantID            string
	SessionID           string
	AgentID             string
	ParentRunID         *string
	Input               map[string]any
	IdempotencyKey      string
	PromptBundleVersion string
	RuntimeVersion      string
}

type ApprovalDecision string

const (
	ApprovalAllow ApprovalDecision = "allow"
	ApprovalDeny  ApprovalDecision = "deny"
)

type Approval struct {
	ID            string            `json:"id"`
	RunID         string            `json:"run_id"`
	ToolCallID    string            `json:"tool_call_id"`
	ToolName      string            `json:"tool_name"`
	Arguments     map[string]any    `json:"arguments"`
	ArgumentsHash string            `json:"arguments_hash"`
	Decision      *ApprovalDecision `json:"decision,omitempty"`
	Reason        *string           `json:"reason,omitempty"`
	ExpiresAt     time.Time         `json:"expires_at"`
	DecidedAt     *time.Time        `json:"decided_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

type Interaction struct {
	ID         string         `json:"id"`
	RunID      string         `json:"run_id"`
	ToolCallID string         `json:"tool_call_id"`
	Kind       string         `json:"kind"`
	Request    map[string]any `json:"request"`
	Response   any            `json:"response,omitempty"`
	Status     string         `json:"status"`
	ExpiresAt  time.Time      `json:"expires_at"`
	AnsweredAt *time.Time     `json:"answered_at,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type Job struct {
	ID          string
	RunID       string
	Kind        string
	Payload     map[string]any
	Attempts    int
	MaxAttempts int
	LeaseOwner  *string
	LeaseUntil  *time.Time
}

type RunEvent struct {
	Sequence  uint64         `json:"sequence"`
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}

type RunControl struct {
	ID      string `json:"id"`
	RunID   string `json:"run_id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type PlanStep struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Text     string `json:"text"`
	Status   string `json:"status"`
}

type Plan struct {
	ID             string     `json:"id"`
	RunID          string     `json:"run_id"`
	SessionID      string     `json:"session_id"`
	ExecutionRunID *string    `json:"execution_run_id,omitempty"`
	Status         string     `json:"status"`
	Version        uint64     `json:"version"`
	Steps          []PlanStep `json:"steps"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type Todo struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	RunID     *string   `json:"run_id,omitempty"`
	Ordinal   int       `json:"ordinal"`
	Text      string    `json:"text"`
	Completed bool      `json:"completed"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Session struct {
	ID              string         `json:"id"`
	TenantID        string         `json:"tenant_id"`
	ParentSessionID *string        `json:"parent_session_id,omitempty"`
	Name            *string        `json:"name,omitempty"`
	ActiveLeafID    *string        `json:"active_leaf_id,omitempty"`
	Version         uint64         `json:"version"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Entries         []SessionEntry `json:"entries,omitempty"`
}

type SessionEntry struct {
	Sequence  uint64         `json:"sequence"`
	ID        string         `json:"id"`
	ParentID  *string        `json:"parent_id,omitempty"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}

type OutboxEvent struct {
	Sequence      uint64         `json:"sequence"`
	ID            string         `json:"id"`
	AggregateType string         `json:"aggregate_type"`
	AggregateID   string         `json:"aggregate_id"`
	Type          string         `json:"type"`
	Payload       map[string]any `json:"payload"`
	Attempts      int            `json:"attempts"`
	CreatedAt     time.Time      `json:"created_at"`
}
