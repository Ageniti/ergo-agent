# Custom resource root

[English](README.md) | [简体中文](README.zh-CN.md)

This example proves that the Runtime can execute an application-owned Agent
without loading Chief or any other default profile. It selects `only-meta`
explicitly and supplies a complete custom resource root:

```text
resources/
├── agents/
│   └── only-meta.md
└── prompts/
    └── system/
        └── minimal.md
```

The profile defines the specialist role, tools, and policy. The system prompt
is the lower-level harness into which the Runtime injects tool descriptions,
shared guidelines, project context, skills, and working-directory context.

## Run inside this repository

```bash
OPENAI_API_KEY=... go run ./examples/sdk-app/custom-root
```

This in-repository example imports `github.com/ageniti/ergo-agent/runtime` so
the canonical repository can test its local implementation directly. A newly
generated external project instead imports:

```go
import ergoruntime "github.com/ageniti/ergo-core/runtime"
```

Both paths use the same engine. The external `ergo-core` Module is the
download-isolated distribution and contains no bundled Agent resources.

For a new standalone application, prefer the generated layout:

```bash
ergo-agent new --name only-meta --role meta
```

For deployment, embed the resources as the generated application does, or ship
the directory separately and set `AGENT_RESOURCE_ROOT`.
