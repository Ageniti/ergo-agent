# Declarative Agent Package

[English](README.md) | [简体中文](README.zh-CN.md)

This directory is a minimal Pi-compatible resource package. It contains:

```text
agent-package/
├── package.json
├── agents/example-meta.md
└── prompts/repo-review.md
```

It contains no Runtime, Chief/default Agents, or executable JavaScript.
`pi.agents` exports the profile; `pi.prompts` exports an optional `/repo-review`
user-message template.

## Use from a trusted project

Add the package source to `.pi/settings.json`:

```json
{
  "packages": [
    "./path/to/agent-package"
  ]
}
```

Run the installed Agent directly:

```go
err := agentRuntime.RunWithOptions(ctx, map[string]any{
    "agentId":        "example-meta",
    "agentScope":     "project",
    "projectTrusted": true,
    "prompt":         "Summarize the repository architecture.",
    "cwd":            projectRoot,
}, options, sink)
```

Project resources are ignored when `projectTrusted` is false.

## Install a remote package

The Go-native package manager supports local paths, Git/GitHub sources, and npm
resource packages without executing package JavaScript:

```go
manager := resource.PackageManager{CWD: projectRoot}
_, err := manager.Install(
    ctx,
    "github:owner/example-meta-agent",
    "project",
    true,
)
```

## Generic Pi package vs self-contained Ergo package

This example intentionally has only a `pi` manifest. It relies on its host's
default system prompt and can be consumed by compatible Pi-style hosts.

A self-contained Ergo Agent Package additionally declares:

```json
{
  "ergo": {
    "entryAgent": "example-meta",
    "agentDependencies": [],
    "requiredTools": ["find", "grep", "ls", "read"],
    "optionalTools": []
  }
}
```

It must carry a package-local system prompt. Ergo validates the entry Agent,
dependency closure, tool contract, role-compatible delegation, and prompt
paths before installation or execution.

Build a self-contained package from an existing profile:

```bash
go run ./cmd/agent-package \
  -root . \
  -agent coding-agent \
  -output ./dist/coding-agent
```

The builder recursively includes the entry Agent's declared delegates and
their system prompts. It freezes wildcard delegates into exact names.

Create a new package from scratch:

```bash
ergo-agent new \
  --name repository-reviewer \
  --role meta \
  --package-only
```

Agent profiles and prompt templates are separate concepts. Invoking
`/repo-review [focus]` expands `prompts/repo-review.md` into one user message
for the current Agent; it does not register or switch Agents.

See [Agent Package contract](../../docs/AGENT-PACKAGES.md) and
[Prompt Templates](../../docs/PROMPT-TEMPLATES.md).
