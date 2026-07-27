# Ergo Agent

[![License: AGPL v3 or Commercial](https://img.shields.io/badge/license-AGPL--3.0--only%20OR%20Commercial-blue.svg)](LICENSE)

面向 App 内嵌和独立 Agent 发行场景的纯 Go、无 TUI Agent SDK。完整
`ergo-agent` Module 通过 Go `embed` 携带默认 Agent profiles、prompts 与
skills；独立发布的 `ergo-core` 只包含执行引擎和 SDK 能力，不包含 Chief 或任何
默认 Agent 资源。运行时不依赖 Node.js、npm 或官方 Pi SDK。Pi `v0.81.1`
仅作为行为、prompt 与工具契约的迁移基准。

## 发布物

| 发布物 | Go Module / 命令 | 包含默认 Agent | 面向场景 |
| --- | --- | ---: | --- |
| Ergo Core | `github.com/ageniti/ergo-core` | 否 | 自建 Sub/Meta Agent、嵌入自有资源 |
| Ergo Agent | `github.com/ageniti/ergo-agent` | 是 | Chief、Coding Agent 与完整默认套件 |
| CLI | `ergo-agent` | 脚手架本身不进入产物 | 创建独立程序或纯资源包 |

`ergo-agent` 是唯一人工维护的源码仓库。`ergo-core` 由
[`cmd/export-core`](cmd/export-core/) 选择并改写核心包后自动发布，禁止直接维护
第二套实现。

## 结构

```text
agent/
├── embed.go                # 编译默认 Agent/Prompt/Skill/Docs 资源
├── agent/                  # 向后兼容的完整 SDK 门面
├── runtime/                # 最小 SDK 入口；发布到 ergo-core/runtime
├── runner/                 # 聚焦 Runtime 执行入口
├── provider/               # Provider 契约、协议、流式、注册表、图片生成
├── message/                # Message、Image、ToolCall 契约
├── tool/                   # Tool Definition/Call/Result 契约
├── session/                # Session、control、interaction 契约
├── resource/               # Agent profile、Skill、Prompt、Package 实现
├── extensions/             # 编译期 Go Extension 契约
├── cmd/agent-package/      # 构建自包含 Agent Package
├── cmd/ergo-agent/         # new 脚手架：独立 Sub/Meta Agent
├── cmd/export-core/        # 生成只读 ergo-core 发布源码
├── internal/engine/        # 私有 Agent loop、Tools、MCP、Compaction、Extension 生命周期
├── agents/                 # 可选 Agent Markdown profiles
├── prompts/                # system/mode/templates prompts
├── skills/                 # Skills
├── config/                 # MCP 配置示例
├── examples/agent-package/ # 可安装的 Agent 与 / Prompt Template 示例
├── examples/sdk-app/       # 无数据库的 Go SDK 应用示例
└── examples/ecs/           # 可选 ECS/MySQL 生产参考实现
    ├── cmd/                # API、Worker、migration 进程
    ├── internal/           # HTTP、service、MySQL 控制面
    ├── migrations/         # MySQL 8 schema
    └── deploy/ecs/         # ECS task definition 模板
```

现有框架使用者可以继续只导入 `github.com/ageniti/ergo-agent/agent`。核心契约与实现已按
`runner/provider/message/tool/session/resource/extensions` 分包；`agent` 使用 Go
类型别名保留原 API 和类型身份，私有执行内核位于 `internal/engine`。分包不包装
Runtime、不复制状态，也不改变 Provider 选择、Agent loop、工具执行、Session
恢复或资源发现行为。`examples/ecs` 不是运行框架的前置条件，而是一套可复制的
生产控制面参考实现。

## 三种直接入口

| 目标 | 使用方式 | 入口 |
| --- | --- | --- |
| 只开发并编译一个 Sub/Meta Agent | 最小 Go SDK 程序 | `ergo-agent new` |
| 发布给现有宿主安装 | 声明式 Agent Package | `ergo-agent new --package-only` |
| 使用 Ergo 默认 Chief 与完整 Agent 套件 | 完整 Go SDK | `agent.NewDefault()` |

最快的新建方式：

```bash
go install github.com/ageniti/ergo-agent/cmd/ergo-agent@latest
ergo-agent new --name reviewer-agent --role meta
cd reviewer-agent
go mod tidy
OPENAI_API_KEY=... go run . "Review this repository."
```

生成程序只导入 `github.com/ageniti/ergo-core/runtime`，并只 embed 自己的
`resources/`；没有隐式入口，也不会包含 Chief/default Agent 资源。若只需要给
其他宿主安装，增加 `--package-only`，产物不含 `main.go` 和 Runtime。完整说明见
[`docs/STANDALONE-AGENTS.md`](docs/STANDALONE-AGENTS.md)。

注意：上面的 `go install` 会从源码安装 CLI，因此会下载一次 `ergo-agent`
Module。CLI 安装完成后，它生成的项目只依赖 `ergo-core`。使用预编译 CLI
发行文件时，则不需要为安装脚手架下载完整 Go Module。

### 使用 Go SDK 开发应用

安装模块后不需要复制默认 Markdown 资源：

```bash
go get github.com/ageniti/ergo-agent@latest
```

```go
runtime, err := agent.NewDefault()
if err != nil {
    return err
}
err = runtime.RunWithOptions(ctx, map[string]any{
    "agentId": "chief-agent",
    "prompt":  "Inspect this project.",
    "cwd":     ".",
}, agent.RunOptions{}, sink)
```

`NewDefault()` 使用编入完整 SDK 的默认资源；兼容门面 `agent.New(root)` 继续
用于完整 Module 内的自定义资源根。要求下载和二进制都与默认资源隔离时，导入
`github.com/ageniti/ergo-core/runtime` 并调用 `runtime.New(root)` 或
`runtime.NewFS(resources)`；该入口强制每次运行明确传 `agentId`。只增加自定义
Agent、但仍使用完整宿主时，无需复制默认 Prompt，可以把 Profile 放在
`$AGENT_CONFIG_DIR/agents/`（默认 `~/.pi/agent/agents/`）。

### 发布 Agent Package

Sub/Meta Agent 可以作为只包含声明式资源的本地、Git 或 npm 包发布，不需要复制
Runtime，也不需要 JavaScript。包通过 `package.json` 的 `pi.agents` 导出 Profile：

```json
{
  "name": "@acme/research-agent",
  "version": "1.0.0",
  "pi": {
    "agents": ["agents/research-agent.md"]
  }
}
```

项目级安装的 Agent 可直接作为入口运行：

```go
err = runtime.RunWithOptions(ctx, map[string]any{
    "agentId":       "research-agent",
    "agentScope":    "project",
    "projectTrusted": true,
    "prompt":        "Research this topic.",
    "cwd":           projectRoot,
}, agent.RunOptions{}, sink)
```

完整模板见 `examples/agent-package`。普通 Pi Profile 未声明 `system-prompt` 时使用宿主的
默认 system prompt；由 Ergo 构建器生成的独立包则必须携带包内 system prompt。
Profile 正文是该 Agent 的角色指令。使用 `New(customRoot)`
替换全部内置资源属于 Go SDK 的高级用法，见 `examples/sdk-app/custom-root`，
不是另一种 Agent 打包格式。

每个 Agent 都可以作为独立入口构建。构建器会递归收集其 `delegates` 依赖并复制
对应 system prompts：

```bash
go run ./cmd/agent-package \
  -agent coding-agent \
  -output ./dist/coding-agent
```

`coding-agent` 的包会包含自身以及 `scout/planner/reviewer/worker/web-researcher`；
单个 Meta Agent 的包只包含自身。`delegates: "*"` 会在构建时展开并冻结为源资源根内
所有角色兼容 Agent。产物的 `package.json` 通过 `ergo.entryAgent`、
`ergo.agentDependencies`、`ergo.requiredTools` 和 `ergo.optionalTools`
记录入口及依赖；安装与运行时会校验该契约。完整说明见
`docs/AGENT-PACKAGES.md`。

Agent Package 还可以通过 `pi.prompts` 导出可复用的 Prompt Template。例如 App
可以把 `prompts/repo-review.md` 显示为 `/repo-review`；无 TUI Runtime 接收
`operation: prompt_template`，展开参数后将正文作为一条普通用户消息交给当前
Agent。它不会注册、绑定或切换 Agent。完整格式见
`docs/PROMPT-TEMPLATES.md`。

博查联网搜索是可选的编译期 Go Extension，不要求 Node。ECS Worker 在
`BOCHA_API_KEY` 存在时自动注册；其他 SDK 宿主可以显式注册：

```go
import bochaextension "github.com/ageniti/ergo-agent/extensions/bocha"

extension, err := bochaextension.New(bochaextension.Config{
    APIKey: os.Getenv("BOCHA_API_KEY"),
})
if err != nil {
    return err
}
runtime.RegisterExtension(extension)
```

注册后，Chief/Coding Agent 可以直接调用 `web_search`，也可以把复杂调研委派给
内嵌的 `web-researcher` Meta Agent。它同样可以作为独立入口运行：

```go
err = runtime.RunWithOptions(ctx, map[string]any{
    "agentId": "web-researcher",
    "prompt":  "搜索并比较国内可用的 Agent 搜索 API。",
    "cwd":     ".",
}, agent.RunOptions{}, sink)
```

## 运行架构

```mermaid
flowchart LR
    App["App backend"] --> API["Go API · ECS Service"]
    API --> DB[("RDS MySQL 8")]
    W1["Go Worker"] -->|"SKIP LOCKED lease + heartbeat"| DB
    W2["Go Worker"] -->|"SKIP LOCKED lease + heartbeat"| DB
    W1 --> LLM["Provider HTTP API"]
    W1 --> MCP["stdio / Streamable HTTP MCP"]
    W1 --> FS["isolated workspace"]
```

API 和 Worker 无本地业务状态，可以在 ECS 横向扩容。MySQL 是 run、session、plan、todo、approval、job lease、event 和 outbox 的事实来源。

## Agent 能力

- Go 原生模型循环：system/user/assistant/tool messages、tool calling、多轮执行、usage 与事件。
- Provider：按协议族原生实现 OpenAI Responses/Chat/Codex、Azure Responses、Anthropic Messages、Google Gemini/Vertex、AWS Bedrock Converse、Mistral 与 Pi Messages。`openai` 默认使用当前 Responses API，只有显式 `openai-chat`/`openai-completions` 才使用 Chat Completions。流式错误、提前断流、加密推理/思维签名、Gemini function ID 与工具图片均按协议处理，跨模型时不会错误重放旧签名。常用 API-key 供应商由内置目录映射到这些协议，私有服务可用 `AGENT_PROVIDER_<NAME>_API/BASE_URL/API_KEY/HEADERS` 接入。
- 图片生成：与 Pi 一样独立于聊天 Provider。内置 OpenRouter 一次性图片 API、Pi 当前静态模型能力表、text-to-image 与 reference-image editing；`OPENROUTER_API_KEY` 存在时，`chief-agent` 与 `coding-agent` 自动获得可选 `generate_image` tool。工具返回图片附件，不把图片生成伪装成文本聊天或流式 tool call。
- Coding tools：`read / bash / edit / write / grep / find / ls`；Reviewer 使用无 shell、无写命令的 `git_read`；包含官方多块 edit、BOM/CRLF、模糊字符匹配、截断/续读、Bash 流式更新与完整输出临时文件。
- Todo：`list / add / toggle / clear`，tool result 与 session tree 一起持久化。
- Plan Mode：禁用 edit/write，Bash 使用 Pi 的只读 allowlist，抽取 `Plan:` 步骤与 `[DONE:n]`；`questionnaire` 通过 MySQL interaction + App API 实现，不依赖 TUI。
- Permission：Pi 危险 Bash 规则以及 MCP `destructiveHint/openWorldHint` 进入 App 审批，参数稳定序列化后 SHA-256 去重。
- Agent：所有主/子/元 Agent 都是同一个 Go `agent.Agent` 类型，具体名称、工具、模型和 Prompt 来自 Markdown profile。委派同时经过角色层级与 Profile `delegates` 白名单：main 最多调用 sub/meta，sub 最多调用 meta，meta 不能委派；未声明 `delegates` 默认全部拒绝，只有显式 `delegates: "*"` 才允许所有当前可见且角色兼容的目标。默认 `chief-agent` 是领域中立的全能力主 Agent；`coding-agent` 只允许调用其明确列出的 Meta specialists。另支持 user/project profile，以及 single、最多 8 个 parallel（每批 4 个）、chain 和 `{previous}`，深度上限 4。
- Agent、Skills 与 prompt templates：运行时从本项目及受控 `.pi` 资源目录发现；`pi.agents` 注册 Agent，`pi.prompts` 注册交给当前 Agent 的 `/` 快捷任务模板。Go 原生 package manager 支持本地、Git、GitHub 与 npm（含 semver）资源包的 install/update/remove/list，不执行包内 JavaScript。
- MCP：官方 Go SDK；stdio、Streamable HTTP、roots、reconnect/pagination、tools、resources、resource templates、prompts、sampling 与 elicitation bridge。
- Go Extension API：注册工具、普通/command-context 命令、项目可信判断、动态 skills/prompts、session/tree/compaction、input/message、模型/thinking、Provider headers/request/response 与工具生命周期 hooks。
- 联网搜索：可选 `extensions/bocha` 原生 Go Tool，支持自然语言查询、时间过滤、结构化来源和摘要；`web-researcher` Meta Agent 负责多查询、来源选择与带链接综合。搜索摘要只作为线索，不等同于完整网页正文验证。
- Session：tree/leaf snapshot、fork、带 branch summary 的 navigate、label/name、手动/自动 compaction、双消息队列、模型/思考级别/active tools 设置及 inspect。
- Context overflow：与 Pi 相同的一次 compact-and-retry；已成功生成答案但 usage 填满窗口时只 compact，不重复答案。

没有 TUI、renderer、终端编辑器、快捷键循环或 JavaScript extension loader。

## Prompt 与角色

运行时 prompt 是仓库内可审计文件，不再隐藏在依赖包中：

- `prompts/system/coding-agent.md`：Pi coding-agent 完整基准 prompt；
- `prompts/system/chief-agent.md`：领域中立、可直接工作也可委派的主 Agent prompt；
- `prompts/modes/*.md`：Plan 与执行计划上下文；
- `prompts/templates/*.md` 与 `pi.prompts`：展开为普通用户消息的 `/` 快捷任务模板；
- `agents/*.md`：使用统一格式声明可选 Agent profile；
- `resource/profile.go`：统一 Agent 类型、三种 role 与调用层级。

需要委派的 Profile 必须同时声明 `subagent` tool 和目标白名单：

```yaml
role: sub
tools: read, grep, subagent
delegates: scout, reviewer
```

经过 manifest 校验的随包 profile 优先于同名内置 profile；松散项目 profile 仍不能
覆盖内置角色。子 Agent 默认继承调用方 scope，也可以显式选择 `user`/`project`/`both`；
顶层请求使用同名 `agentScope` 直接选择入口。ECS API 对应字段为 `input.agent_scope`。
当 `input.project_trusted=false` 时，项目 agent、skills、prompts、packages 与 `AGENTS.md/CLAUDE.md` 全部隔离；用户级和内置资源仍可用。
多租户 ECS 默认只允许修改 workspace 内的 project packages。只有单租户或由运维独占的部署才应设置 `AGENT_ALLOW_GLOBAL_PACKAGE_MUTATIONS=true`；全局 user packages 通常在镜像构建/发布阶段预置。

## 本地运行

最小框架示例只要求 Go 1.26：

```bash
go run ./examples/sdk-app
```

ECS/MySQL 参考实现提供两个 Worker target。默认 `worker` 只包含 Go
二进制与 `bash/git/rg/fd`；可选 `worker-full` 额外包含 Node 24、npm、npx 与
curl，用于执行依赖 Node 的 Skill helper 和 npm stdio MCP。两者运行相同的 Go
Worker 二进制，不改变 Agent loop、Session、Provider 或工具链路。

```bash
cp examples/ecs/.env.example examples/ecs/.env
docker compose -f examples/ecs/compose.yaml up --build
```

需要完整兼容环境时，在 `.env` 中设置：

```text
AGENT_WORKER_DOCKER_TARGET=worker-full
```

也可以分别构建并发布两个不可变镜像：

```bash
docker build -f examples/ecs/Dockerfile.worker --target worker -t ergo-agent-worker:go .
docker build -f examples/ecs/Dockerfile.worker --target worker-full -t ergo-agent-worker:full .
```

`worker-full` 只提供 JavaScript 运行时，不增加 Pi JavaScript/TypeScript
Extension loader，也不会自动执行 npm 资源包中的代码。Skill 自带的 npm 依赖
应在镜像构建阶段安装，或把该 Skill 放在可写 workspace 后显式执行
`npm install`；`npx` 与用户级 npm CLI 使用容器内临时目录，Task 重启后不会保留。

创建 Run：

```bash
curl -X POST http://localhost:8080/v1/runs \
  -H 'Authorization: Bearer local-development-token-change-me-123456' \
  -H 'X-Tenant-ID: tenant-a' \
  -H 'Idempotency-Key: request-001' \
  -H 'Content-Type: application/json' \
  -d '{"agent_id":"chief-agent","input":{"prompt":"检查项目","cwd":"/workspace/project-a","provider":"openai","model":"gpt-5","thinking_level":"high"}}'
```

OpenAI 使用 `OPENAI_API_KEY`；Anthropic 使用 `ANTHROPIC_API_KEY`，也接受已经取得的 `ANTHROPIC_OAUTH_TOKEN`；Gemini 优先使用 `GEMINI_API_KEY`，并兼容 `GOOGLE_API_KEY`。Vertex 支持 `GOOGLE_CLOUD_API_KEY` 或 ADC，Bedrock 使用 AWS SDK 默认凭证链（包括 ECS task role），Mistral 使用 `MISTRAL_API_KEY`。

博查搜索使用 `BOCHA_API_KEY`；可选
`BOCHA_API_BASE_URL`（默认 `https://api.bochaai.com/v1/web-search`）和
`BOCHA_SEARCH_TIMEOUT`（默认 `30s`）。Worker 注册扩展后会从子进程环境移除 API
Key，Tool 内部保留只读副本。

DeepSeek、Groq、OpenRouter、Cerebras、NVIDIA、Together、Fireworks、Kimi、MiniMax、Moonshot、Qwen Token Plan、Xiaomi、Z.AI、OpenCode、xAI、Cloudflare 与 Radius 等常用文本 Provider 已有内置 API-key 配置，只需设置各自标准环境变量。可通过 `agent.BuiltinProviderNames()` 获取当前目录。自定义服务示例：

```text
AGENT_PROVIDER_PRIVATE_API=openai-completions
AGENT_PROVIDER_PRIVATE_BASE_URL=https://models.example.com/v1
AGENT_PROVIDER_PRIVATE_API_KEY=...
AGENT_PROVIDER_PRIVATE_HEADERS={"X-Tenant":"tenant-a"}
```

`AGENT_PROVIDER_<NAME>_API` 支持 `openai-responses`、`openai-completions`、`anthropic-messages`、`google-generative-ai`、`google-vertex`、`mistral-conversations` 与 `pi-messages`。Azure 另支持 `AZURE_OPENAI_BASE_URL/RESOURCE_NAME/API_VERSION/DEPLOYMENT_NAME_MAP`。

图片生成使用 `OPENROUTER_API_KEY`，默认模型为 `google/gemini-3.1-flash-image`；可用 `AGENT_IMAGE_MODEL` 与 `AGENT_IMAGE_BASE_URL` 覆盖。`agent.NewOpenRouterImageGenerator(...)` 也可在不创建 Runtime 的服务中直接使用，`agent.BuiltinImageModels()` 返回随 Pi 对照更新的静态能力表。

交互式订阅登录/刷新（GitHub Copilot、ChatGPT、Claude、xAI、Radius）和 Pi 的动态远程模型目录不是 headless Runtime 的职责。服务端可以注入已经取得的 token，或者通过 `RegisterProvider` 接入自己的 credential provider。

MCP 配置通过 `AGENT_MCP_CONFIG=/app/config/mcp.json` 指定。默认 `worker`
中的 stdio server 必须是镜像中已安装的独立可执行文件；`worker-full` 还可以把
`node`、`npm` 或 `npx` 配置为 stdio command。生产环境应锁定 npm 包版本并在
镜像构建阶段预取，避免每次 Task 启动时从 registry 下载未固定代码。

App 交互采用无状态恢复协议：`GET /v1/runs/{runID}/interactions` 获取 questionnaire/MCP elicitation；`POST /v1/interactions/{interactionID}/response` 提交 JSON `response` 或 `cancelled:true`。Worker 会从 MySQL session snapshot 恢复，不要求请求命中原 ECS 实例。

ECS 多实例 Worker 必须挂载同一个 EFS workspace（模板已启用 access point、IAM 与传输加密），或由外部 workspace 控制面保证任意接管实例可看到同一路径。MySQL 保存 Agent 状态但不保存源码文件；不能把 task 本地临时盘当成可接管 workspace。

API、MySQL/ECS 细节见 [架构](docs/ARCHITECTURE.md)、[ECS 示例](examples/ecs/deploy/ecs/README.md)、[Pi 迁移对照](docs/PI-PARITY.md)、[严格兼容矩阵](docs/CONFORMANCE.md) 与 [安全](docs/SECURITY.md)。

## License

Copyright © 2026 Yiliu Li.

本项目采用双许可证：

- 开源使用：[`AGPL-3.0-only`](LICENSE)；
- 闭源集成、私有分发或不采用 AGPL 的企业使用：需要单独签署
  [Commercial License](LICENSE-COMMERCIAL.md)。

SPDX 表达式：

```text
AGPL-3.0-only OR LicenseRef-Ergo-Commercial
```

商业授权请联系 [yiliu.li@outlook.com](mailto:yiliu.li@outlook.com)。
外部贡献在合并前需要完成 CLA，详见 [CONTRIBUTING.md](CONTRIBUTING.md)。
