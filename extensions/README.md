# Go Extension API

[English](README.md) | [简体中文](README.zh-CN.md)

Extensions add native capabilities to an Ergo Runtime at application build
time. There is no JavaScript extension loader and no runtime `.so` plugin
loading: the host imports an Extension package, constructs it, and registers it
explicitly.

For an independent application, import Extension contracts from Core:

```go
import (
    agentextensions "github.com/ageniti/ergo-core/extensions"
    ergoruntime "github.com/ageniti/ergo-core/runtime"
)
```

Applications using the complete distribution may use the equivalent
`github.com/ageniti/ergo-agent/extensions` compatibility path. These are aliases
to the same engine contracts, not a second Extension Runtime.

## Capabilities

An Extension can:

- register model-callable tools and host commands;
- add dynamic skills and prompt resources;
- modify input, system context, messages, model selection, and thinking level;
- allow, deny, or rewrite tool calls and results;
- observe Provider request/headers/response and Agent lifecycle events;
- participate in session switching, forks, compaction, trust, and shutdown.

Plan Mode, Todo, permission checks, Agent delegation, Skills, prompt templates,
sessions, and MCP are built into Core. They do not require an Extension.

## Bocha web search

`extensions/bocha` is the first-party native Go integration that registers the
`web_search` Tool:

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

`bocha.NewFromEnv()` reads:

| Variable | Purpose |
| --- | --- |
| `BOCHA_API_KEY` | Required API credential |
| `BOCHA_API_BASE_URL` | Optional endpoint override |
| `BOCHA_SEARCH_TIMEOUT` | Optional duration, default `30s` |

The model-callable Tool accepts:

| Parameter | Behavior |
| --- | --- |
| `query` | Required natural-language search |
| `freshness` | `noLimit`, relative period, date, or date range |
| `summary` | Request result summaries; default `true` |
| `count` | Result count from 1–50; default `10` |

The Tool accepts Bocha's `{code,msg,data}` envelope and its unwrapped
Bing-compatible response shape. It returns structured sources for citation.

Bocha AI Search (`/v1/ai-search`) is a different API that may generate answers
and vertical cards. `web_search` intentionally uses the Web Search API; the
bundled `web-researcher` Meta Agent performs multi-query research and synthesis
on top of that Tool.

For hot-pluggable or remotely operated tools, prefer MCP instead of compiling a
Go Extension.
