# Headless Pi Conformance Matrix

[English](CONFORMANCE.md) | [简体中文](CONFORMANCE.zh-CN.md)

Baseline: Pi `v0.81.1` / `20be4b18`. TUI rendering, themes, terminal editor,
keybindings and interactive CLI setup are deliberately out of scope. Every
other row requires a Go test before it can be marked complete.

Interactive subscription OAuth login/refresh and the remote dynamic model
catalog are out of scope for this headless runtime. A pre-obtained token may
still be supplied to a transport adapter.

Status meanings: `complete` = implemented and covered by Go conformance tests;
`partial` = usable but not yet behavior-complete; `missing` = not implemented.

| Area | Capability | Status |
|---|---|---|
| Agent loop | multi-turn text/tool loop, official sequential/parallel ordering, abort | complete |
| Agent loop | streaming text/thinking/tool arguments and runtime events | complete |
| Agent loop | steer/follow-up queues and delivery modes | complete |
| Provider | OpenAI Responses default plus explicit Chat Completions compatibility | complete |
| Provider | Anthropic Messages budget/adaptive thinking, tools and streaming | complete |
| Provider | Gemini/Vertex streaming, thought signatures, function IDs and parallel results | complete |
| Provider | Codex token/JWT headers, encrypted reasoning and Azure Responses variants | complete |
| Provider | Bedrock Converse/stream, Mistral typed reasoning and Pi Messages | complete |
| Provider | structured stream errors, terminal-event checks and cross-model signature isolation | complete |
| Provider | model-aware API-key catalog, custom protocol config, registry and gateway-owned headers | complete |
| Provider | usage/cache accounting and transport-specific retry (the built-in pricing table is intentionally excluded by product scope) | complete |
| Images | OpenRouter generation, Pi model capabilities, data URL outputs, errors and optional Agent tool | complete |
| Session | tree entries, active leaf, labels, name and custom entries | complete |
| Session | branch context, fork-before/fork-after | complete |
| Session | tree navigation with optional branch summary | complete |
| Compaction | manual model summary | complete |
| Compaction | automatic token threshold, reserve/keep-recent and queued continuation | complete |
| Tools | read/write/edit/bash/grep/find/ls and capability-safe git_read | complete |
| Tools | image read, output truncation and process cancellation | complete |
| Tools | streaming bash updates | complete |
| Tools | official parallel/sequential batch execution semantics | complete |
| Plan | read-only tools, Bash allowlist, exact context prompts, Plan parsing, DONE markers | complete |
| Plan | persisted toggle/execution state | complete |
| Plan | headless durable questionnaire | complete |
| Todo | list/add/toggle/clear and branch reconstruction | complete |
| Permission | dangerous Bash and MCP annotations, durable approval resume | complete |
| Subagent | built-ins, YAML custom roles, single/parallel/chain/depth/scope and partial-failure retention | complete |
| Resources | system prompt, AGENTS/CLAUDE context, project trust and built-in roles | complete |
| Skills | recursive user/project/.agents discovery, validation diagnostics, metadata injection, invocation | complete |
| Prompts | discovery and all official positional/default/slice argument forms | complete |
| Packages | string/object filters plus local/Git/GitHub/npm semver install/update/remove/list and diagnostics | complete |
| Extensions | Go tools/commands, generic lifecycle bus, command context and context/provider/tool hooks | complete |
| Extensions | model/thinking/actions, queues, project trust, session controls, providers and dynamic resources | complete |
| MCP | official Go SDK, stdio/HTTP, pagination, reconnect, roots | complete |
| MCP | tools/resources/templates/prompts and annotations | complete |
| MCP | host sampling | complete |
| MCP | durable elicitation continuation back into the same MCP request | complete |
| Operations | inspect/model/thinking/tools/queue/name/label/custom/package entries | complete |
| Runtime | MySQL jobs, lease, heartbeat, approval, plans, todos, outbox | complete |

Rows are updated only together with tests. `PI-PARITY.md` describes mappings;
this file is the release gate and must remain honest about incomplete work.
