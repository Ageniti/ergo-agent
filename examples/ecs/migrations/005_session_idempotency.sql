CREATE TABLE IF NOT EXISTS agent_session_idempotency_keys (
    tenant_id VARCHAR(191) NOT NULL,
    idempotency_key VARCHAR(191) NOT NULL,
    source_session_id CHAR(36) NOT NULL,
    session_id CHAR(36) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (tenant_id, idempotency_key),
    CONSTRAINT fk_agent_session_idempotency_source FOREIGN KEY (source_session_id) REFERENCES agent_sessions(id),
    CONSTRAINT fk_agent_session_idempotency_target FOREIGN KEY (session_id) REFERENCES agent_sessions(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
