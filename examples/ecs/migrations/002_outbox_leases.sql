ALTER TABLE agent_outbox
    ADD COLUMN lease_owner VARCHAR(255) NULL,
    ADD COLUMN lease_until DATETIME(6) NULL,
    ADD COLUMN next_attempt_at DATETIME(6) NOT NULL DEFAULT (UTC_TIMESTAMP(6));

CREATE INDEX idx_agent_outbox_lease ON agent_outbox (published_at, next_attempt_at, lease_until, seq);
