# Pi v0.81.1 to Go Migration Map

[English](PI-PARITY.md) | [简体中文](PI-PARITY.zh-CN.md)

Ergo draws inspiration from Pi and selectively adapts compatible behavior.
This document maps the parts that use Pi as a conformance reference; Ergo's
native architecture and additional capabilities are documented separately.
The baseline is Pi `v0.81.1`, commit
`20be4b18d4c57487f8993d2762bace129f0cf7c6`. Production code does not import,
execute, or read `pi/` or `@earendil-works/*`.

| Pi capability | Go implementation |
|---|---|
| Agent loop | `internal/engine/runtime.go` |
| message conversion | `message/` and provider adapters |
| streaming providers | native Go HTTP and AWS adapters in `provider/` |
| tool argument coercion/validation | `internal/engine/tool_validation.go`; Pi/TypeBox-compatible primitive coercion, composed JSON Schema, and additional-property behavior |
| Todo | `internal/engine/tools.go`; details persist to sessions, events, and MySQL |
| Plan Mode | `prompts/modes/plan.md`, tool filtering, read-only Bash allowlist, and plan parsing |
| Permission Gate | three official dangerous Bash-pattern classes, MCP annotations, and SHA-256 approvals |
| subagent roles | unified semantics in `resource/profile.go`, with profiles declared by `agents/*.md` |
| subagent single/parallel/chain | `internal/engine/tools.go`; role hierarchy, Profile `delegates` allowlists, limit 8, batches of 4, `{previous}`, and depth 4 |
| custom roles/scopes | bundled `agents/`, `~/.pi/agent/agents`, and explicit project/both `.pi/agents`; YAML frontmatter, discovery by declared name, and per-role provider/model selection |
| skills/templates | `resource/resources.go` and session operations |
| MCP tools/resources/templates/prompts | official MCP Go SDK; stdio and Streamable HTTP with sampling/elicitation bridges |
| extension hooks/tools/commands | `internal/engine/extensions.go`; compile-time Go registration, headless event bus, command context, and typed hooks |
| compact/navigation/labels/name/custom entries | Pi threshold and cut-point summaries, branch summaries, and Go operations |
| steer/follow-up/next-turn | MySQL controls, separate steering/follow-up queues, and all/one-at-a-time modes |
| package manager | `resource/package_manager.go`; local, Git, GitHub, and npm-semver resource packages with atomic replacement and safe tar extraction |
| image generation | `provider/image_generation.go` and `internal/engine/image_process.go`; separate OpenRouter API, static capabilities, data-URL output, error results, and optional `generate_image` tool |

## Intentionally excluded

- Pi CLI and its local authentication UI.
- Runtime dependencies on official Pi npm packages. Resource packages may come from npm registries, but Go extracts them and never executes JavaScript.

These are not part of an application-embedded headless agent's execution semantics. Use MCP for dynamic business tools and the Go Extension API for trusted in-process extensions.

## Provider details

The runtime natively implements OpenAI Responses/Chat/Codex, Azure Responses, Anthropic Messages, Google Gemini/Vertex, AWS Bedrock Converse/ConverseStream, Mistral, and Pi Messages. Like Pi, `openai` defaults to Responses; Chat Completions is an explicit compatibility protocol. Encrypted reasoning, `reasoning_details`, thinking signatures, Gemini `thoughtSignature`, and provider function-call IDs enter the session. Encrypted state is replayed only with the same provider and model; Gemini signatures are also validated as base64.

The built-in catalog covers Pi's common non-interactive API-key text providers and selects the wire protocol by provider and model. Streaming adapters preserve structured provider errors and verify terminal events when streams end early. Gemini tool images use the required model-specific function-response shape. Image generation follows Pi's separate OpenRouter behavior: non-streaming requests, static `modalities`, data-URL-only output, and `stopReason:error` on failure. Add custom providers through `HTTPProviderConfig`, environment variables, or the Go registry. Interactive OAuth login/refresh and a remote dynamic model catalog are intentionally excluded; existing Codex or Anthropic tokens can still be used by headless transports.

Every protocol has local contract tests for requests, responses, streaming, usage, tools, and signature conversion. Production releases must still run credential, model-availability, and quota smoke tests against the target account.

## Independence acceptance criteria

1. The repository has no `.ts`, `.js`, `package.json`, `package-lock.json`, or `node_modules` runtime dependency.
2. `go test ./...` and `go vet ./...` do not require `../pi`.
3. The default `worker` target contains no Node. `worker-full` is an optional external-tool runtime; Go builds and the Agent Runtime do not depend on it.
4. `prompts/` contains all system, mode, template, and built-in Agent text required at runtime.
5. Moving any local `pi/` checkout away does not affect Go builds, tests, or image builds.
