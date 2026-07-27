ALTER TABLE agent_sessions
    ADD COLUMN parent_session_id CHAR(36) NULL,
    ADD CONSTRAINT fk_agent_sessions_parent FOREIGN KEY (parent_session_id) REFERENCES agent_sessions(id);

CREATE INDEX idx_agent_sessions_parent ON agent_sessions (parent_session_id);
