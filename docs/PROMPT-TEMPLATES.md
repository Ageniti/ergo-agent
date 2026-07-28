# Prompt Templates

[English](PROMPT-TEMPLATES.md) | [简体中文](PROMPT-TEMPLATES.zh-CN.md)

`pi.prompts` exports reusable Markdown user-message templates. A UI can expose
them as custom `/` shortcuts; they do not define an Agent or modify the system
prompt.

```text
/repo-review security
        |
        v
prompts/repo-review.md
        |
        v
template arguments are expanded
        |
        v
one normal user message is sent to the active Agent
```

Ergo Agent is headless and does not parse editor text itself. A CLI, App, or
API maps `/repo-review security` to the Runtime operation:

```json
{
  "operation": "prompt_template",
  "templateName": "repo-review",
  "templateArgs": ["security"],
  "agentId": "coding-agent"
}
```

The Runtime resolves the template, expands its arguments, changes the
operation to a normal prompt, and continues through the same active Agent
session and tool loop.

## Package manifest

```json
{
  "pi": {
    "prompts": ["prompts/*.md"]
  }
}
```

The Markdown filename is the command name. `prompts/repo-review.md` registers
`/repo-review`; a frontmatter `name` field does not rename it.

```md
---
description: Review a repository with an optional focus
argument-hint: <focus>
---

Review the repository. Focus on ${1:-correctness}.
```

The template supports Pi-compatible argument forms:

| Form | Meaning |
| --- | --- |
| `$1`, `$2` | Positional arguments |
| `$@`, `$ARGUMENTS` | All arguments |
| `${1:-default}` | Positional argument with a default |
| `${@:2}` | Arguments starting at position 2 |
| `${@:2:2}` | Two arguments starting at position 2 |

Agent profiles and prompt templates are intentionally independent:

| Resource | Purpose | Registration |
| --- | --- | --- |
| Agent profile | Who runs, with which role and tools | `pi.agents` |
| Prompt template | What the active Agent should do once | `pi.prompts` |
| System prompt | Low-level Runtime harness | Agent `system-prompt` |

See `examples/agent-package` for a package that exports both an Agent and an
optional prompt template.
