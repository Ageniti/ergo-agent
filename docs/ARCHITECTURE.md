# Runtime and Enterprise Control Plane

[English](ARCHITECTURE.md) | [简体中文](ARCHITECTURE.zh-CN.md)

## Process boundary

The API and Worker are both Go binaries. After the Worker claims a job from MySQL, it calls `agent.Runtime` directly. There is no Node subprocess or JSONL bridge. Model, MCP, and tool events are written through callbacks into the same MySQL state machine.

```text
App -> API -> MySQL jobs/outbox/sessions <- Worker -> Provider
                                              |----> MCP
                                              |----> workspace tools
```

Run creation writes the job, idempotency record, and outbox entry in one transaction. Workers claim leases under READ COMMITTED with `SELECT ... FOR UPDATE SKIP LOCKED` and heartbeat every `lease/3`. Execution is at least once; tools with external side effects must still be idempotent.

## Runtime layers

- `provider/`: text-provider adapters, model-aware factories, API-key catalog, streaming transports, registries, and independent image generation.
- `message/`: message, image, and tool-call contracts.
- `tool/`: tool definition and result contracts.
- `session/`: application-owned `SessionController`, control, and interaction contracts.
- `resource/`: agent profiles, prompts, skills, templates, project context, and native Go resource packages.
- `runtime/`: minimal public entry point for applications that bring their own resources. It neither imports the default embed package nor selects an implicit entry agent.
- `internal/engine/`: agent loop, tools, session entries, control messages, subagents, compaction, and extension lifecycle.
- `agent/`: compatibility SDK facade preserving `agent.New()` and the original public types.

The module root embeds the default `agents/`, `prompts/`, `skills/`, and `docs/` trees. On first use, `agent.NewDefault()` materializes them at their original relative paths in a process-level temporary directory. This preserves file-path semantics in prompts and agent `read` tools. `agent.New(root)` continues to use a caller-provided resource directory directly.

To build one Sub or Meta Agent with only its own resources, import `runtime/` instead of `agent/` or `runner/`. `runtime.NewFS` materializes only the resources supplied through `embed.FS`, and each run must specify `agentId`. The linker therefore never reaches the module-root default embed package, so Chief resources are absent from that binary. The CLI scaffold and `examples/sdk-app/custom-root` use this path.

Extensions are registered explicitly at process startup; untrusted `.so` files are never loaded dynamically. Prefer MCP for hot-pluggable business integrations.

The provider boundary is `Provider`/`StreamingProvider`. `ProviderAPI` identifies the wire protocol, `HTTPProviderConfig` constructs custom endpoints, and `ModelProviderFactory` lets a provider select different protocols per model. Applications may override any built-in name through `ProviderRegistry`.

Adapters retain direct HTTP wire control rather than binding the public boundary to one vendor SDK. OpenAI, Azure, Codex, Gemini, Vertex, and compatible gateways can share session, signature, header-hook, and error semantics. AWS Bedrock and MCP, which have distinct transport models, use their official Go SDKs.

Protocol adapters live in `provider`, resource implementations in `resource`, and the remaining tightly coupled execution logic is one private unit under `internal/engine`. Type aliases and direct constructor forwarding preserve `agent.New()`, public type identity, and existing behavior. There is no second runtime and no change to dependency injection or state ownership.

```text
message  tool  session
   ^      ^      ^
   |      |      |
 provider   resource
      \       /
       internal/engine
          ^      ^
          |      |
       runtime  agent
```

`internal/architecture` enforces these dependency directions and restricts `agent` to type aliases and constructor forwarding. A reverse dependency from a foundational contract to the runtime fails the architecture tests.

## Independent Core distribution

`github.com/ageniti/ergo-agent` is the only hand-maintained source repository. `cmd/export-core` selects runtime, engine, provider, resource, tool, message, session, and extension code, then rewrites the module path to produce `github.com/ageniti/ergo-core`. The export excludes default agents, prompts, skills, the full SDK embed, examples, and the ECS control plane.

`.github/workflows/publish-core.yml` validates both the full source and the exported standalone module before synchronizing it to the read-only distribution repository. Both repositories use the same version tag. A developer depending only on `ergo-core` does not download the complete `ergo-agent` repository.

## Multiple instances

- The API is stateless; tenant plus idempotency key collapses client retries.
- Workers keep no local session state; snapshots and the active leaf live in MySQL.
- Another ECS task may reclaim a job after its lease expires.
- Workspace files do not enter MySQL. Every Worker must see stable paths through the same EFS access point or an equivalent external workspace provisioner. Task ephemeral storage is only for temporary data.
- The outbox also uses leases; consumers deduplicate by event ID and may verify an HMAC.
- Migrations run as a one-off ECS task. API and Worker deployments use a circuit breaker and automatic rollback.

## Approval resume

The runtime stably sorts `{toolName,input}`, computes its SHA-256 hash, and derives a stable approval UUID from the run, tool call, and hash. A dangerous call without approval emits `approval.requested`, then `session.snapshot` and `run.paused`. After the application allows or denies it, MySQL requeues the original job and injects the decision hash and session entries into the resume payload. Questionnaires and MCP elicitation use stable interaction IDs and can likewise continue on another ECS task.

## Session operations

The session tree uses Go JSON entries with `id`, `parentId`, `type`, and `timestamp`. It supports snapshots, forks, active leaves, navigation, labels, session names, custom entries/messages, compaction entries, and recorded changes to model, thinking, active tools, and queue mode.

Extensions receive session replacement through `SessionController`. This preserves Pi command-context semantics for `new`, `fork`, and `switch` while keeping the runtime independent of MySQL; the ECS Worker binds the interface to the persistent control plane.
