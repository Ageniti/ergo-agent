ALTER TABLE agent_runs MODIFY COLUMN status ENUM('queued','running','awaiting_approval','awaiting_input','completed','failed','cancelled') NOT NULL;

CREATE TABLE IF NOT EXISTS agent_interactions (
    id CHAR(36) PRIMARY KEY,
    run_id CHAR(36) NOT NULL,
    tool_call_id VARCHAR(191) NOT NULL,
    kind VARCHAR(64) NOT NULL,
    request_json JSON NOT NULL,
    response_json JSON NULL,
    status ENUM('pending','answered','cancelled','expired') NOT NULL DEFAULT 'pending',
    expires_at DATETIME(6) NOT NULL,
    answered_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_agent_interactions_tool_call (run_id, tool_call_id),
    KEY idx_agent_interactions_pending (status, expires_at),
    CONSTRAINT fk_agent_interactions_run FOREIGN KEY (run_id) REFERENCES agent_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
