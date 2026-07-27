# Complete Go SDK application

This example embeds the complete Ergo Agent distribution with
`agent.NewDefault()`. The resulting application includes the default Chief,
Coding, Sub/Meta Agent profiles, system prompts, skills, and execution Runtime.

Use this integration style when the application wants the complete default
suite:

```go
import agent "github.com/ageniti/ergo-agent/agent"

agentRuntime, err := agent.NewDefault()
```

The example also registers the optional native Bocha extension when
`BOCHA_API_KEY` is present.

## Run

From the repository root:

```bash
OPENAI_API_KEY=... go run ./examples/sdk-app
```

The example explicitly selects `chief-agent`, but any bundled or installed
profile can be used as `agentId`.

## Which SDK should I import?

| Requirement | Import |
| --- | --- |
| Complete default Agent suite | `github.com/ageniti/ergo-agent/agent` |
| Only application-owned Agents | `github.com/ageniti/ergo-core/runtime` |
| Declarative resources for another host | No Runtime; build an Agent Package |

The [`custom-root`](custom-root/) example demonstrates application-owned
resources inside this canonical repository. New standalone projects should
normally be generated with:

```bash
ergo-agent new --name my-agent --role meta
```

That generated project imports `ergo-core`, not the complete `ergo-agent`
Module.
