-- MySQL 8.0+, utf8mb4, microsecond timestamps.
CREATE TABLE IF NOT EXISTS agent_sessions (
    id CHAR(36) PRIMARY KEY,
    tenant_id VARCHAR(191) NOT NULL,
    name VARCHAR(255) NULL,
    active_leaf_id VARCHAR(64) NULL,
    version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_sessions_tenant_id (tenant_id, id),
    KEY idx_agent_sessions_tenant_updated (tenant_id, updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_runs (
    id CHAR(36) PRIMARY KEY,
    tenant_id VARCHAR(191) NOT NULL,
    session_id CHAR(36) NOT NULL,
    agent_id VARCHAR(128) NOT NULL,
    parent_run_id CHAR(36) NULL,
    status ENUM('queued','running','awaiting_approval','completed','failed','cancelled') NOT NULL,
    input_json JSON NOT NULL,
    output_json JSON NULL,
    session_state_json JSON NULL,
    error_code VARCHAR(128) NULL,
    error_message TEXT NULL,
    prompt_bundle_version VARCHAR(128) NOT NULL,
    runtime_version VARCHAR(64) NOT NULL,
    version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    started_at DATETIME(6) NULL,
    completed_at DATETIME(6) NULL,
    CONSTRAINT fk_agent_runs_session FOREIGN KEY (session_id) REFERENCES agent_sessions(id),
    CONSTRAINT fk_agent_runs_parent FOREIGN KEY (parent_run_id) REFERENCES agent_runs(id),
    UNIQUE KEY uq_agent_runs_tenant_id (tenant_id, id),
    KEY idx_agent_runs_session_created (session_id, created_at),
    KEY idx_agent_runs_status_created (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_session_entries (
    seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(191) NOT NULL,
    session_id CHAR(36) NOT NULL,
    parent_id VARCHAR(64) NULL,
    entry_type VARCHAR(64) NOT NULL,
    payload_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_session_entries_id (session_id, id),
    KEY idx_agent_session_entries_path (session_id, seq),
    CONSTRAINT fk_agent_entries_session FOREIGN KEY (session_id) REFERENCES agent_sessions(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_events (
    seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    id CHAR(36) NOT NULL,
    tenant_id VARCHAR(191) NOT NULL,
    run_id CHAR(36) NOT NULL,
    event_type VARCHAR(96) NOT NULL,
    payload_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_events_id (id),
    KEY idx_agent_events_run_seq (run_id, seq),
    CONSTRAINT fk_agent_events_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_run_controls (
    seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    id CHAR(36) NOT NULL,
    tenant_id VARCHAR(191) NOT NULL,
    run_id CHAR(36) NOT NULL,
    control_type ENUM('steer','follow_up','next_turn') NOT NULL,
    content TEXT NOT NULL,
    status ENUM('pending','delivered') NOT NULL DEFAULT 'pending',
    created_at DATETIME(6) NOT NULL,
    delivered_at DATETIME(6) NULL,
    UNIQUE KEY uq_agent_run_controls_id (id),
    KEY idx_agent_run_controls_pending (run_id, status, seq),
    CONSTRAINT fk_agent_run_controls_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_jobs (
    id CHAR(36) PRIMARY KEY,
    run_id CHAR(36) NOT NULL,
    kind VARCHAR(64) NOT NULL,
    payload_json JSON NOT NULL,
    status ENUM('ready','leased','completed','dead') NOT NULL,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    max_attempts INT UNSIGNED NOT NULL DEFAULT 5,
    available_at DATETIME(6) NOT NULL,
    lease_owner VARCHAR(255) NULL,
    lease_until DATETIME(6) NULL,
    last_error TEXT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_jobs_run_kind (run_id, kind),
    KEY idx_agent_jobs_poll (status, available_at, lease_until),
    CONSTRAINT fk_agent_jobs_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_approvals (
    id CHAR(36) PRIMARY KEY,
    run_id CHAR(36) NOT NULL,
    tool_call_id VARCHAR(191) NOT NULL,
    tool_name VARCHAR(128) NOT NULL,
    arguments_json JSON NOT NULL,
    arguments_hash CHAR(64) NOT NULL,
    policy_version VARCHAR(64) NOT NULL,
    decision ENUM('allow','deny') NULL,
    reason TEXT NULL,
    expires_at DATETIME(6) NOT NULL,
    decided_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_approvals_tool_call (run_id, tool_call_id),
    KEY idx_agent_approvals_pending (decision, expires_at),
    CONSTRAINT fk_agent_approvals_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_plans (
    id CHAR(36) PRIMARY KEY,
    run_id CHAR(36) NOT NULL,
    status ENUM('draft','awaiting_approval','approved','executing','completed','failed','cancelled') NOT NULL,
    version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_plans_run (run_id),
    CONSTRAINT fk_agent_plans_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_plan_steps (
    id CHAR(36) PRIMARY KEY,
    plan_id CHAR(36) NOT NULL,
    position INT UNSIGNED NOT NULL,
    text TEXT NOT NULL,
    status ENUM('pending','running','completed','failed','skipped') NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_plan_steps_position (plan_id, position),
    CONSTRAINT fk_agent_plan_steps_plan FOREIGN KEY (plan_id) REFERENCES agent_plans(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_todos (
    id CHAR(36) PRIMARY KEY,
    tenant_id VARCHAR(191) NOT NULL,
    session_id CHAR(36) NOT NULL,
    run_id CHAR(36) NULL,
    ordinal INT UNSIGNED NOT NULL,
    text TEXT NOT NULL,
    completed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_todos_ordinal (session_id, ordinal),
    CONSTRAINT fk_agent_todos_session FOREIGN KEY (session_id) REFERENCES agent_sessions(id),
    CONSTRAINT fk_agent_todos_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_outbox (
    seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    id CHAR(36) NOT NULL,
    aggregate_type VARCHAR(64) NOT NULL,
    aggregate_id CHAR(36) NOT NULL,
    event_type VARCHAR(96) NOT NULL,
    payload_json JSON NOT NULL,
    created_at DATETIME(6) NOT NULL,
    published_at DATETIME(6) NULL,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    last_error TEXT NULL,
    UNIQUE KEY uq_agent_outbox_id (id),
    KEY idx_agent_outbox_unpublished (published_at, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_idempotency_keys (
    tenant_id VARCHAR(191) NOT NULL,
    idempotency_key VARCHAR(191) NOT NULL,
    run_id CHAR(36) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (tenant_id, idempotency_key),
    CONSTRAINT fk_agent_idempotency_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
