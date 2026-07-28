# Ergo Agent

[English](README.md) | [简体中文](README.zh-CN.md)

[![License: AGPL v3 or Commercial](https://img.shields.io/badge/license-AGPL--3.0--only%20OR%20Commercial-blue.svg)](LICENSE)

Ergo Agent is an independent, pure-Go, headless Agent SDK for embedding Agents
in Go applications, shipping standalone specialists, and running horizontally
scalable Agent services.

Ergo draws inspiration from
[Pi](https://github.com/badlogic/pi-mono) and preserves selected compatibility
with its execution behavior, prompts, tool contracts, and resource conventions.
Its independent pure-Go Runtime, SDK, role system, integrations, packaging, and
enterprise control plane extend well beyond that compatibility layer.

| Borrowed or adapted from Pi | Designed and implemented by Ergo |
|---|---|
| Selected Agent-loop and tool-call behavior | Pure-Go Runtime and public SDK layering |
| Prompt, tool-contract, and session compatibility targets | `main` / `sub` / `meta` role model and enforced delegation |
| `.pi` resource paths and `pi.*` package manifests | Native Go Extensions, MCP integration, and standalone Agent packaging |
| Pi `v0.81.1` conformance reference | Durable approvals, MySQL/ECS control plane, Bocha search, and enterprise operations |

## Start with four concepts

| Concept | Meaning |
|---|---|
| **Runtime** | The shared Go execution engine: model loop, tools, sessions, delegation, MCP, and events |
| **Agent** | A Markdown Profile declaring identity, role, tools, prompt, optional model routing, and delegation policy |
| **Role** | The `main`, `sub`, or `meta` delegation boundary |
| **Agent Package** | Declarative Agent resources installed into an existing Runtime |

Every run resolves one entry Agent. Pass `agentId` to select it explicitly;
the minimal Core Runtime requires this, while the complete default Runtime may
fall back to `chief-agent` when it is omitted. The selected Agent can complete
the task itself or delegate through the `subagent` tool. There is one Runtime
and one Agent type; Chief, Coding, and specialist Agents are different
Profiles running on the same engine.

## Agent roles

Roles control **outgoing delegation only**:

| Role | Purpose | May delegate to | May run directly as entry |
|---|---|---|---:|
| `main` | Top-level orchestrator for broad requests | `sub`, `meta` | Yes |
| `sub` | Capable worker for a domain or workflow | `meta` | Yes |
| `meta` | Focused leaf specialist | Nobody | Yes |

```text
main ──→ sub ──→ meta
  └────────────→ meta
```

The Profile declares tool access separately from Role. Entry selection is also
independent: a caller can run `chief-agent`, `coding-agent`, `reviewer`, or
`web-researcher` directly.

Delegation is allowed only when all of these are true:

1. the caller's role may delegate to the target role;
2. the caller declares the `subagent` tool;
3. the target is listed in the caller's `delegates` allowlist;
4. the target is visible in the current resource scope;
5. runtime depth and project-trust checks pass.

Delegation is deny-by-default. `delegates: "*"` explicitly enables all visible
role-compatible targets; package builds freeze it into exact names.

### Model selection

The Run or restored Session selects the default `provider` and `model`. Every
built-in Main, Sub, and Meta Agent inherits that selection, including delegated
calls:

```text
Run(model=gpt-5) → chief-agent → coding-agent → reviewer
                     gpt-5          gpt-5         gpt-5
```

Custom Profiles may declare `provider` and `model` for intentional per-Agent
routing. This remains an advanced opt-in for hosts that explicitly manage
multiple Provider credentials, costs, and data-routing policies. The bundled
Profiles leave both fields unset.

## Default Agent suite

The full `ergo-agent` Module embeds these Profiles:

| Agent | Role | Responsibility | Delegates |
|---|---|---|---|
| `chief-agent` | `main` | General entry and orchestration | Coding Agent and compatible Meta Agents |
| `coding-agent` | `sub` | Repository investigation and implementation | Scout, Planner, Reviewer, Worker, Web Researcher |
| `scout` | `meta` | Fast codebase discovery and compressed handoff | None |
| `planner` | `meta` | Read-only implementation planning | None |
| `reviewer` | `meta` | Read-only code and security review | None |
| `worker` | `meta` | Isolated delegated task execution | None |
| `web-researcher` | `meta` | Parallel web research with cited synthesis | None |

Example call paths:

```text
App → chief-agent → coding-agent → reviewer
App → coding-agent → scout
App → reviewer
App → web-researcher → web_search
```

The last two examples demonstrate direct specialist entry. See
[Agent Profiles](agents/README.md).

## Choose the distribution

| What you want | Use | Contents |
|---|---|---:|
| Complete built-in suite | `github.com/ageniti/ergo-agent/agent` | Runtime + default Agents |
| Your own minimal Agent application | `github.com/ageniti/ergo-core/runtime` | Core Runtime |
| Resources for an existing Ergo host | Agent Package | Agent resources |
| Help creating either of the last two | optional `ergo-agent` CLI | Developer scaffolder |

## Repository and dependency relationship

There is one maintained codebase and two Go distributions:

```text
github.com/ageniti/ergo-agent              canonical source repository
├── agent/                                 complete SDK facade
├── agents/ + prompts/ + skills/           default product resources
├── cmd/ergo-agent/                        scaffolding CLI
└── cmd/export-core
    └── github.com/ageniti/ergo-core       generated Core distribution
        └── runtime/                        minimal application entry
```

`ergo-core` is the minimal distribution boundary generated from selected
packages in `ergo-agent`. Both distributions share the canonical implementation.
Changes are made once in `ergo-agent`, tested there, exported by
`cmd/export-core`, tested again as an independent Go Module, and then published
to the Core repository.

The dependency direction is one-way:

```text
ergo-agent source ──release export──→ ergo-core ──imported by──→ minimal apps
        └────────────────────────────→ complete-suite apps
```

| Consumer | Imports | What Go downloads | Runtime resources |
|---|---|---|---|
| Complete-suite application | `ergo-agent/agent` | `ergo-agent` Module | Default Agents, prompts, and Skills |
| Minimal custom application | `ergo-core/runtime` | `ergo-core` Module | Application-owned resources |
| CLI installed with `go install` | `ergo-agent/cmd/ergo-agent` | `ergo-agent` once to build the CLI | Generates a Core-based project |
| Prebuilt CLI user | downloaded CLI binary | Generated project resolves `ergo-core` | Generated resources |
| Existing Ergo host | Agent Package | Host-selected Runtime + resource package | Package Agents and prompts |

The canonical `ergo-agent` `go.mod` resolves packages from its own source Module
and upstream Go dependencies. References to `ergo-core` in this repository
belong to documentation, release export logic, and source templates emitted by
the CLI for minimal external applications.

### Use the complete suite

```bash
go get github.com/ageniti/ergo-agent@latest
```

```go
import agent "github.com/ageniti/ergo-agent/agent"

agentRuntime, err := agent.NewDefault()
if err != nil {
    return err
}
err = agentRuntime.RunWithOptions(ctx, map[string]any{
    "agentId": "chief-agent", // may be any visible Agent
    "prompt":  "Inspect this project.",
    "cwd":     ".",
}, agent.RunOptions{}, sink)
```

`agent.NewDefault()` loads the embedded suite. `agent.New(root)` loads a
complete caller-provided resource root through the compatibility SDK.

### Build a standalone Sub or Meta Agent

The CLI is an optional developer scaffolder used during project creation:

```bash
go install github.com/ageniti/ergo-agent/cmd/ergo-agent@latest

ergo-agent new \
  --name reviewer-agent \
  --role meta \
  --module example.com/reviewer-agent

cd reviewer-agent
go mod tidy
OPENAI_API_KEY=... go run . "Review this repository."
```

The generated application imports `github.com/ageniti/ergo-core/runtime`,
embeds its own `resources/`, and passes an explicit `agentId`. Its binary and
Go dependency graph therefore contain the Core Runtime plus the resources
selected by that application.

The table above shows the download boundary for source-installed and prebuilt
CLI usage. Projects produced by either form resolve the generated Core
distribution.

The scaffolder creates Sub and Meta Agents because they are the reusable
specialist roles. A custom Main Agent is the same Profile format with
`role: main` and can be built manually with Core.

See [Standalone Agents](docs/STANDALONE-AGENTS.md).

### Build only an Agent Package

```bash
ergo-agent new \
  --name reviewer-agent \
  --role meta \
  --package-only
```

This produces a resource-only directory containing Markdown Profiles, prompts,
and `package.json`, ready for installation into an existing Ergo host.

To package an existing Profile plus its complete delegation dependency closure:

```bash
go run ./cmd/agent-package \
  -agent coding-agent \
  -output ./dist/coding-agent
```

The package builder includes every declared delegate recursively, copies their
package-local system prompts, freezes wildcard delegation, and records the
entry Agent and host-tool contract.

See [Agent Packages](docs/AGENT-PACKAGES.md).

## Define an Agent

An Agent is a Markdown file with YAML frontmatter:

```md
---
name: repository-agent
description: Implements repository changes and may request focused reviews
role: sub
tools: read, grep, find, ls, edit, write, subagent
optional-tools: web_search
delegates: reviewer, web-researcher
thinking-level: high
system-prompt: prompts/system/repository-agent.md
---

You are Ergo's repository implementation specialist.
Complete the requested change and verify the result.
```

`tools` is an exact capability allowlist. `optional-tools` supports graceful
startup across hosts with different capabilities. `delegates` names Agent
resource dependencies. The Profile inherits the Run/Session model. Add
`provider` and `model` only when the host intentionally routes this Agent to a
different model.

Profiles can be loaded from:

- embedded resources shipped by the host;
- user resources under `~/.pi/agent/agents/`;
- trusted project resources under `.pi/agents/`;
- local, Git, GitHub, or npm resource packages through `pi.agents`.

The `.pi` paths and `pi.*` manifest fields are retained for Pi Package
compatibility. The executing Agent still identifies itself as Ergo.

## Prompts, Skills, Tools, Extensions, and MCP

These resources have distinct jobs:

| Resource | Purpose |
|---|---|
| Agent Profile | Identity, role, tools, optional model routing, and delegation |
| System prompt | Low-level Runtime behavior used by a Profile |
| Prompt template | A reusable `/`-style user message for the active Agent |
| Skill | Instructions loaded on demand |
| Tool | A model-callable capability implemented by the host |
| Extension | A trusted Go component registered at build/startup |
| MCP server | A hot-pluggable local or remote capability provider |

Prompt templates expand into messages for the currently selected Agent. Skills
teach an Agent how to use capabilities supplied by the host.

The first-party Bocha Extension registers the provider-neutral `web_search`
Tool. The `web-researcher` Agent plans parallel searches and returns a
source-linked synthesis on top of that Tool:

```go
import bocha "github.com/ageniti/ergo-core/extensions/bocha"

extension, err := bocha.New(bocha.Config{
    APIKey: os.Getenv("BOCHA_API_KEY"),
})
if err != nil {
    return err
}
agentRuntime.RegisterExtension(extension)
```

Use Go Extensions for trusted in-process capabilities and MCP for
hot-pluggable or cross-language tools. MCP supports stdio and Streamable HTTP,
tools, resources, templates, prompts, sampling, elicitation, pagination,
roots, and reconnect.

See [Prompts](prompts/README.md), [Prompt Templates](docs/PROMPT-TEMPLATES.md),
[Skills](skills/README.md), and [Extensions](extensions/README.md).

## Runtime capabilities

- streaming multi-turn model and tool loop;
- OpenAI, Azure, Anthropic, Gemini/Vertex, Bedrock, Mistral, Pi Messages, and
  configurable compatible Providers;
- sessions, branching, compaction, steering, follow-up queues, and snapshots;
- Plan Mode, Todo, questionnaires, and durable approval resume;
- role- and Profile-constrained Agent delegation;
- native Go Extensions, MCP, Skills, Prompt Templates, and resource packages;
- optional OpenRouter image generation and Bocha web search;
- optional MySQL/ECS job, lease, outbox, and multi-instance reference service.

See [Architecture](docs/ARCHITECTURE.md),
[Conformance](docs/CONFORMANCE.md), [Pi migration](docs/PI-PARITY.md),
[Security](docs/SECURITY.md), and the
[ECS runbook](examples/ecs/deploy/ecs/README.md).

## Repository layout

```text
agent/
├── agent/                  # Complete compatibility SDK
├── runtime/                # Minimal public Runtime; exported to ergo-core
├── provider/ message/ tool/ session/
├── resource/               # Profiles, prompts, skills, packages
├── extensions/             # Go Extension API and native integrations
├── internal/engine/        # Private execution engine
├── agents/ prompts/ skills/# Default product resources
├── cmd/ergo-agent/         # Optional scaffolding CLI
├── cmd/agent-package/      # Self-contained package builder
├── cmd/export-core/        # Generates the read-only Core distribution
└── examples/               # SDK, package, and ECS examples
```

## Development

Requires Go 1.26 or newer:

```bash
go test ./...
go vet ./...
```

The default build and test path requires only the Go toolchain.

## License

Ergo Agent is available under AGPL-3.0-only or a separately executed commercial
license. See [LICENSE](LICENSE), [Commercial License](LICENSE-COMMERCIAL.md),
and [NOTICE](NOTICE).

Commercial licensing: Yiliu Li — `yiliu.li@outlook.com`.
