# Prompt resources

[English](README.md) | [简体中文](README.zh-CN.md)

These are the default prompts distributed by `ergo-agent`. They are auditable
product resources and are not included in the generated `ergo-core` Module.
A standalone Agent must supply its own system prompt.

## Layout

| Path | Purpose |
| --- | --- |
| `system/chief-agent.md` | Domain-neutral default main Agent |
| `system/coding-agent.md` | Ergo coding harness with Pi-compatible behavior |
| `modes/plan.md` | Read-only planning behavior |
| `modes/execute-plan.md` | Plan execution context |

The prompt builder expands:

- `{{TOOLS}}`
- `{{GUIDELINES}}`
- `{{PROJECT_CONTEXT}}`
- `{{SKILLS}}`
- `{{CWD}}`
- `{{APPEND_SYSTEM_PROMPT}}`

An Agent profile chooses its system prompt through frontmatter:

```yaml
system-prompt: prompts/system/coding-agent.md
```

Self-contained Agent Packages must use a package-local path. The package
validator rejects missing or escaping prompt paths.

Prompt templates are different from system prompts. A `pi.prompts` entry
expands into a normal user message for the currently selected Agent; it does
not register or switch Agents. See
[Prompt Templates](../docs/PROMPT-TEMPLATES.md).
