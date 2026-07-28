# Pi v0.81.1 → Go 迁移对照

[English](PI-PARITY.md) | [简体中文](PI-PARITY.zh-CN.md)

Ergo 借鉴了 Pi 的部分设计并选择性适配兼容行为。本文件映射以 Pi 作为兼容参考的
部分；Ergo 的原生架构与扩展能力由其他文档分别说明。参考基准为 Pi `v0.81.1`、commit
`20be4b18d4c57487f8993d2762bace129f0cf7c6`。生产代码不导入、执行或读取
`pi/` 与 `@earendil-works/*`。

| Pi 能力 | Go 实现 |
|---|---|
| coding system prompt | `prompts/system/coding-agent.md` + `Resources.BuildSystemPrompt` |
| project context / skills prompt | `resource/resources.go` |
| Agent loop / tool calls | `internal/engine/runtime.go` + `provider/` streaming adapters |
| read/bash/edit/write/grep/find/ls | `internal/engine/tools.go` |
| tool argument coercion/validation | `internal/engine/tool_validation.go`；Pi/TypeBox 的 primitive coercion、组合 JSON Schema 与 additional-property 行为 |
| Todo | `internal/engine/tools.go`，details 写 session/event/MySQL |
| Plan Mode | `prompts/modes/plan.md`、tool 筛选、只读 Bash allowlist、计划解析 |
| questionnaire | `input.requested` + MySQL interactions + App response API |
| Permission Gate | Bash 三类官方危险模式 + MCP annotations + SHA-256 approval |
| subagent roles | `resource/profile.go` 统一角色语义，`agents/*.md` 声明可选 profile |
| subagent single/parallel/chain | `internal/engine/tools.go`；角色层级 + Profile `delegates` 白名单、8 个上限、4 个一批、`{previous}`、深度 4 |
| custom roles / scope | bundled `agents/`、`~/.pi/agent/agents` 与显式 project/both `.pi/agents`；统一 YAML frontmatter、按声明 name 发现、角色 provider/model 切换 |
| Skills / templates | `resource/resources.go` 与 session operations |
| MCP tools/resources/templates/prompts | 官方 MCP Go SDK，stdio + Streamable HTTP + sampling/elicitation bridge |
| Extension hooks/tools/commands | `internal/engine/extensions.go`，Go 编译期注册、通用 headless event bus、command context、项目可信/资源/session/agent/provider/tool 强类型 hooks |
| session snapshot/tree/leaf | Go JSON entries + MySQL session tables |
| compact/navigate/labels/name/custom entries | Pi 阈值/cut-point 摘要、branch summary 与 Go operations |
| steer/follow-up/next-turn | MySQL controls + 独立 steering/follow-up queues + all/one-at-a-time |
| retry/lease/outbox/multi-instance | Go Worker + MySQL repository |
| package manager | `resource/package_manager.go`；local/Git/GitHub/npm semver Agent/Skill/Prompt 资源包，原子替换与安全 tar 解包 |
| image generation | `provider/image_generation.go` + `internal/engine/image_process.go`；独立 OpenRouter image API、静态模型能力表、data URL 图片输出、失败 result、可选 `generate_image` tool |

## 有意不迁移

- TUI、terminal renderer、editor、themes、keybindings；
- JavaScript/TypeScript Extension loader；
- Pi CLI 与其本地认证 UI；
- 对官方 Pi npm 包的运行时依赖（资源 package 可以来自 npm registry，但由 Go 解包且不会执行 JavaScript）。

这些内容不属于 App 内嵌 headless Agent 的执行语义。动态业务工具使用 MCP；进程内受信任扩展使用 Go Extension API。

## Provider

Go runtime 原生实现 OpenAI Responses/Chat/Codex、Azure Responses、Anthropic Messages、Google Gemini/Vertex、AWS Bedrock Converse/ConverseStream、Mistral 与 Pi Messages。`openai` 与 Pi 一样默认使用 Responses，Chat Completions 作为显式兼容协议。Responses encrypted reasoning、OpenAI-compatible `reasoning_details`、Anthropic/Bedrock thinking signature，以及 Gemini text/tool thoughtSignature 与 provider function-call ID 会进入 session。加密状态只在相同 provider/model 上重放；Gemini 还校验 base64 签名，避免切换模型后向新端点发送无效状态。

内置目录覆盖 Pi 的常用无交互 API-key 文本供应商，并根据 provider/model 选择 Responses、OpenAI-compatible、Anthropic-compatible 或 Google 协议。流式适配器保留结构化 provider 错误，并对 OpenAI、Responses、Anthropic 与 Pi Messages 的提前断流执行终止事件检查。Gemini 工具图片会按 Gemini 3+ 与旧模型要求生成不同的 function-response wire shape。图片生成沿用 Pi 的独立 OpenRouter API：非流式请求、`modalities` 由静态模型能力决定、只接受 data URL 输出，失败返回 `stopReason:error`。自定义 Provider 可通过 `HTTPProviderConfig`、环境变量或动态 Go registry 接入。交互式 OAuth 登录/刷新和远程动态模型目录明确排除；已取得的 Codex/Anthropic token 仍可用于 headless transport。

所有协议均有本地请求、响应、stream、usage、tool 和签名转换合约测试；生产发布仍应在目标账号执行凭证、模型可用性与配额 smoke test。mock 合约测试不能代替真实供应商验收。

## 独立性验收

以下条件必须始终成立：

1. `agent/` 下不存在 `.ts/.js/package.json/package-lock.json/node_modules`；
2. `go test ./...` 与 `go vet ./...` 不需要 `../pi`；
3. 默认 `worker` target 不包含 Node，`worker-full` 仅作为可选外部工具运行时，
   Go build 和 Agent Runtime 不依赖它；
4. `prompts/` 包含运行所需的 system、mode、template 与内置 Agent 文本；
5. 将 `pi/` 移走后，Go 构建、单测和镜像构建仍通过。
