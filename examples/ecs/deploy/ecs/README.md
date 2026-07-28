# AWS ECS deployment runbook

[English](README.md) | [简体中文](README.zh-CN.md)

This directory contains task-definition templates for the production reference
control plane. They describe the API/Worker container contract; they do not
create networking, databases, storage, IAM, or autoscaling resources.

## Required infrastructure

- private ECR repositories;
- RDS MySQL 8 Multi-AZ, preferably behind RDS Proxy;
- an EFS access point shared by every Worker that may resume a run;
- Secrets Manager entries for database, Provider, Bocha, and webhook secrets;
- separate ECS execution and task roles with least privilege;
- CloudWatch log groups, metrics, alarms, and deployment rollback;
- private subnets, security groups, and an ALB for the API service.

Keep this infrastructure in the organization's Terraform/CDK repository so
network, KMS, logging, and tagging policies remain centralized.

## Images

Build the API and one Worker target:

```bash
docker build -f examples/ecs/Dockerfile.api \
  -t <account>.dkr.ecr.<region>.amazonaws.com/ergo-agent-api:<sha> .

docker build -f examples/ecs/Dockerfile.worker \
  --target worker \
  -t <account>.dkr.ecr.<region>.amazonaws.com/ergo-agent-worker:<sha> .
```

Worker choices:

| Target | Contents | Use when |
| --- | --- | --- |
| `worker` | Go binary plus `bash`, `git`, `rg`, and `fd` | Default pure-Go deployment |
| `worker-full` | `worker` plus Node, npm, npx, and curl | A pinned Skill helper or stdio MCP requires Node |

Both targets run the same Go Agent engine. `worker-full` does not add a
JavaScript Extension loader and does not automatically execute npm package
code. Pin and install required Node dependencies at image-build time.

Deploy immutable image digests rather than mutable tags. A service should stay
on one Worker target/digest during a rollout.

## Release order

1. Render `${...}` placeholders in the task definitions from CI-managed
   environment configuration. Never commit real ARNs or secrets.
2. Push API and Worker images and resolve their immutable digests.
3. Run `/app/agent-migrate` as a one-off ECS task using the API image.
4. Stop if migration fails.
5. Roll the API service, then Worker service, with deployment circuit breaker
   and automatic rollback enabled.
6. Verify `/healthz`, queue processing, lease heartbeat, Provider connectivity,
   EFS access, and event delivery.

## Scaling and reliability

- Run at least two API tasks behind the ALB.
- Start Workers with `desiredCount=2` and `concurrency=1`.
- Scale from ready-job count and oldest-job age.
- Keep `worker tasks × DB_MAX_OPEN` within RDS Proxy/database budgets.
- Treat execution as at-least-once; externally mutating tools must be
  idempotent.
- Do not use task ephemeral storage as the resumable workspace.
- Validate outbox HMAC and deduplicate by `X-Agent-Event-ID`.

Alert on dead jobs, expired leases, runtime failures, approval/input backlog,
outbox attempts approaching 20, database saturation, and EFS errors.

See the root [README](../../../../README.md),
[Architecture](../../../../docs/ARCHITECTURE.md), and
[Security](../../../../docs/SECURITY.md).
