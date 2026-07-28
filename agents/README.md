# Agent Profiles

[English](README.md) | [简体中文](README.zh-CN.md)

This directory contains the default Agent suite shipped by
`github.com/ageniti/ergo-agent`. These profiles are product resources, not Go
implementations, and are deliberately excluded from the generated
`github.com/ageniti/ergo-core` repository.

Every Markdown file becomes the same Go `resource.Agent` type. YAML
frontmatter defines policy and the Markdown body defines the Agent-specific
instructions:

```yaml
---
name: reviewer
description: Reviews a repository without modifying it
role: meta
tools: read, grep, find, ls
optional-tools: web_search
thinking-level: high
system-prompt: prompts/system/coding-agent.md
---

Review the requested change and cite the evidence you inspected.
```

Supported profile fields:

| Field | Meaning |
| --- | --- |
| `name` | Stable Agent ID used by `agentId` and delegation |
| `description` | Discovery text shown to callers |
| `role` | `main`, `sub`, or `meta` |
| `tools` | Exact tool allowlist |
| `optional-tools` | Allowed tools that may be absent from the host |
| `delegates` | Exact Agent allowlist, or explicit `*` |
| `provider` / `model` | Optional explicit model-routing override |
| `thinking-level` | Optional default reasoning level |
| `system-prompt` | Package-local or resource-root system prompt |

The bundled Profiles omit `provider` and `model`. They inherit the model
selected by the Run or restored Session, so Chief and every delegated Agent use
one model by default. A custom Profile may set these fields for intentional
per-Agent routing; use that advanced option when the host manages the required
Provider credentials, cost policy, and data routing.

## Delegation policy

Go enforces both the role hierarchy and the profile allowlist:

```text
main → sub or meta
sub  → meta
meta → no delegation
```

A delegating profile must list the `subagent` tool and its targets:

```yaml
role: sub
tools: read, grep, subagent
delegates: planner, reviewer
```

An omitted or empty `delegates` value denies all delegation.
`delegates: "*"` allows every visible role-compatible target; it never bypasses
the hierarchy. Package builds freeze `*` into exact names so installed
authority cannot silently expand later.

## Discovery and execution

- Built-in profiles live here and are loaded by `agent.NewDefault()`.
- User profiles live in `~/.pi/agent/agents/`.
- Trusted project profiles live in `.pi/agents/`.
- Packages export profiles through `package.json` → `pi.agents`.
- Any Sub or Meta Agent may also be selected directly as the top-level
  `agentId`; Chief is not required as an entry point.

Build an existing profile and its transitive delegates as one self-contained
package:

```bash
go run ./cmd/agent-package \
  -agent coding-agent \
  -output ./dist/coding-agent
```

Create a brand-new independent program or resource package:

```bash
ergo-agent new --name reviewer --role meta
ergo-agent new --name reviewer --role meta --package-only
```

See [Agent Packages](../docs/AGENT-PACKAGES.md),
[Standalone Agents](../docs/STANDALONE-AGENTS.md), and
[Prompt Templates](../docs/PROMPT-TEMPLATES.md).

`web-researcher` is the bundled research Meta Agent. It can use the optional
`web_search` tool when the host registers `extensions/bocha`; without that
extension the optional tool is omitted.
