# Security Notes

[English](SECURITY.md) | [简体中文](SECURITY.zh-CN.md)

The Agent SDK and default `worker` have no npm or JavaScript runtime dependency. Audit them with `go list -m all`, `govulncheck`, and base-image scanning. The optional `worker-full` is a separate compatibility distribution; its Node image, npm CLI, Skill helpers, and npm MCP packages need their own image, SBOM, vulnerability, and version-locking controls.

Coding Agent can execute shell commands. Permission rules reduce accidental misuse; they are not a sandbox. File tools accept absolute paths and Bash can access resources visible to the container. The real boundary must be the ECS task/container, one workspace mount, a read-only root filesystem, least-privilege IAM, controlled egress, and short-lived credentials. Untrusted tenants must not share a Worker task.

Like Pi, Coding Bash inherits the Worker environment. MCP stdio removes inherited variables whose names contain token, secret, password, credential, API key, DSN, or AWS container credentials, and receives only dedicated environment values supplied explicitly by configuration.

The ECS Worker constructs the native search Extension from `BOCHA_API_KEY` and immediately calls `os.Unsetenv`. Coding Bash and subsequent stdio MCP children do not inherit it; the key remains only in the tool HTTP-client closure. Custom hosts should do the same or construct `bocha.Config` directly from a secret provider.

MCP server configuration is trusted deployment configuration. Operations supply HTTP URLs/headers and stdio executables/environment. MCP tools with `destructiveHint` or `openWorldHint` require application approval.

`worker-full` enables Node helpers through Bash and npm stdio MCP servers, but JavaScript is not thereby trusted. Production images should preinstall and pin dependencies. Never run `npm install` or `npx` at runtime from an untrusted project. Task-temporary npm cache and prefix directories must not hold durable credentials.

`project_trusted=false` blocks project `.pi` packages, agents, prompts, skills, and instructions. A project Agent Package direct entry also requires `agentScope: project` or `both`; the ECS API uses `input.agent_scope`. Package extraction rejects traversal, symlink escape, oversized files, excessive file counts, and decompression expansion, then installs atomically. System prompts must remain inside the package root, and package JavaScript is never executed. Multi-tenant deployments should disable global user-package mutation.

Delegation is enforced twice. The role hierarchy permits main to sub/meta, sub to meta, and meta to none. The caller Profile's `delegates` narrows targets by Agent name. An absent allowlist denies all delegation; standalone builds freeze `"*"` into an exact list. A runtime wildcard cannot bypass role, project-trust, workspace, or depth limits.

Reviewer has no general Bash or file-write tool. Its structured `git_read` permits only `status`, `diff`, `log`, and `show`; the runtime rejects write operations and disables external diff, text conversion, pagers, and interactive credential prompts.

The API uses a Bearer service token, tenant header, request-size limits, strict JSON, HTTP timeouts, and panic recovery. Place production APIs in private subnets and allow access only from the application backend security group.

Events, snapshots, and tool arguments may contain user data and source code. Define retention, RDS encryption/PITR, and log-redaction policies. Never log prompts, provider keys, or database DSNs.
