ALTER TABLE agent_plans
    ADD COLUMN execution_run_id CHAR(36) NULL,
    ADD CONSTRAINT fk_agent_plans_execution_run FOREIGN KEY (execution_run_id) REFERENCES agent_runs(id);

CREATE INDEX idx_agent_plans_execution_run ON agent_plans (execution_run_id);
