# Runtime 与企业控制面

[English](ARCHITECTURE.md) | [简体中文](ARCHITECTURE.zh-CN.md)

## 进程边界

API 和 Worker 都是 Go 二进制。Worker 从 MySQL 获取 job 后，直接调用 `agent.Runtime`；不存在 Node 子进程或 JSONL bridge。模型、MCP 与工具事件通过 callback 写入同一套 MySQL 状态机。

```text
queued -> running -> completed
                  -> failed (after bounded job retry)
                  -> awaiting_approval -> queued -> running
                  -> awaiting_input -> queued/running
queued/running/awaiting_approval/awaiting_input -> cancelled
```

Run 创建事务同时写 job、idempotency 与 outbox。Worker 在 READ COMMITTED 事务中使用 `SELECT ... FOR UPDATE SKIP LOCKED` 获取租约，并以 `lease/3` 心跳。执行语义是 at-least-once；外部副作用工具仍必须自行幂等。

## Runtime 分层

- `provider/`：文本 Provider 协议适配器、模型感知工厂、内置 API-key 目录、流式传输、注册表及独立图片生成；
- `message/`：Message、Image 与 ToolCall 数据契约；
- `tool/`：Tool Definition/Result 契约；
- `session/`：应用持有的 SessionController、control 与 interaction 契约；
- `resource/`：Agent profiles、Prompt、Skill、template、项目上下文及 Go 原生资源包管理；
- `runtime/`：应用自带资源的最小公开入口，不导入默认 embed 包且不设置隐式入口 Agent；
- `internal/engine/runtime.go`：Agent loop、tool call、session entries、控制消息与 subagent；
- `internal/engine/tools.go`：coding/Todo/subagent 工具；
- `internal/engine/mcp.go`：MCP JSON-RPC client；
- `internal/engine/compaction.go`：Session compaction 与分支摘要；
- `internal/engine/extensions.go`：App 编译期 Go Extension 生命周期；
- `agent/`：保留 `agent.New()` 和原公开类型的兼容 SDK 门面。

模块根目录使用 Go `embed` 编入默认 `agents/`、`prompts/`、`skills/` 与
`docs/`。`agent.NewDefault()` 首次调用时将它们按原路径物化到进程级临时资源
目录，使 Prompt 中的文件路径以及 Agent 的 `read` 工具保持原有语义；
`agent.New(root)` 仍直接使用调用方提供的资源目录。

只构建一个自带资源的 Sub/Meta Agent 时，应用应导入 `runtime/` 而不是
`agent/` 或 `runner/`。`runtime.NewFS` 只物化调用方通过 `embed.FS` 提供的资源，
且运行请求必须明确包含 `agentId`。因此链接器的 import graph 不会到达模块根的
默认 embed 包；Chief 资源不会进入该二进制。CLI 脚手架与
`examples/sdk-app/custom-root` 均使用这条路径。

Extension 采用进程启动时显式注册，不动态加载不受信任的 `.so`。业务扩展需要热插拔时优先使用 MCP。

Provider 公共边界是 `Provider`/`StreamingProvider`。`ProviderAPI` 表示 wire protocol，`HTTPProviderConfig` 用于构造自定义 endpoint，`ModelProviderFactory` 允许 OpenCode、xAI、Fireworks、Cloudflare 等同一供应商的不同模型选择不同协议。`ProviderRegistry` 仍允许应用覆盖任何内置名称。

适配器保留直接 HTTP wire control，而没有把公共边界绑定到某一家 SDK。这样 OpenAI/Azure/Codex、Gemini/Vertex 以及兼容网关可以共用相同的 session、签名、header hook 和错误语义；AWS Bedrock 与 MCP 这类具有独立传输模型的能力仍使用官方 Go SDK。

协议适配器已物理迁移到 `provider`，资源实现已物理迁移到 `resource`，剩余紧耦合
的执行逻辑作为一个私有整体位于 `internal/engine`。`agent.New()`、原公开类型身份
和现有运行行为通过类型别名与直接构造器转发保持兼容；不存在第二套 Runtime，也
不改变依赖注入或状态所有权。

SDK 包关系如下（箭头表示 import 方向）：

```text
existing applications ──> agent ───────────────┐
default-suite users ─────> runner ──────────────┤
standalone Agents ───────> runtime ─────────────┤
extension users ─────────> extensions ──────────┤
                                                 v
                                        internal/engine
                                           │   │
                   ┌───────────────────────┘   └──────────────┐
                   v                                           v
           provider / resource                         session / tool
                 │                                           │
                 └──────────────> tool ──────────────────────┘
                                      │
                                      v
                                   message
```

`internal/architecture` 将这组依赖方向以及 `agent` 只能保留类型别名和构造器转发
固化为测试；未来新增代码如果让基础契约反向依赖 Runtime，测试会直接失败。

## Core 独立发布

`github.com/ageniti/ergo-agent` 是唯一人工维护的源码仓库。
`cmd/export-core` 从其中选择 Runtime、engine、provider、resource、tool、
message、session 与 extensions，改写 Module 路径后生成
`github.com/ageniti/ergo-core`。导出结果不包含 `agents/`、`prompts/`、
`skills/`、完整 SDK embed、示例或 ECS 控制面。

`.github/workflows/publish-core.yml` 会先验证完整源码和导出的独立 Module，再把
结果同步到只读发布仓库。两者使用相同版本标签；开发者只依赖 `ergo-core`
时，Go 不需要下载完整的 `ergo-agent` 仓库。

## 多实例

- API 无状态，客户端重试由 tenant + idempotency key 收敛。
- Worker 无本地 session；快照和 active leaf 位于 MySQL。
- lease 到期后其他 ECS task 可接管 job。
- workspace 文件不进入 MySQL；所有 Worker 必须通过同一 EFS access point 看到稳定路径，或使用具备等价一致性的外部 workspace provisioner。task ephemeral storage 只可用于 Bash 截断日志等临时数据。
- outbox 同样使用 lease，消费者按 event id 去重并验证可选 HMAC。
- 发布时 migration 是一次性 ECS task；API/Worker deployment 使用 circuit breaker 和自动回滚。

## 审批恢复

Runtime 对 `{toolName,input}` 稳定排序后计算 SHA-256，并由 run/tool-call/hash 派生稳定 approval UUID。未批准的危险调用生成 `approval.requested`，随后写 `session.snapshot` 和 `run.paused`。App 决定 allow/deny 后，MySQL 将原 job 重新排队，并把批准/拒绝 hash 与 session entries 注入恢复 payload。Questionnaire 与 MCP elicitation 同样使用稳定 interaction ID，可被其他 ECS task 继续。

## Session 操作

Session tree 使用带 `id/parentId/type/timestamp` 的 Go JSON entries。支持 snapshot、fork、active leaf、navigate、labels、session name、自定义 entry/message、compaction entry，以及模型、thinking、active-tools 和 queue-mode 变更记录。

Extension 的 session replacement 通过 `SessionController` 接口注入。这样 `new/fork/switch`
仍保留 Pi command-context 语义，但 Runtime 不直接依赖 MySQL；ECS worker 负责把接口绑定到持久化控制面。
