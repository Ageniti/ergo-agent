# Security

Agent SDK 和默认 `worker` 没有 npm 或 JavaScript 运行时依赖，因此不存在旧
Pi/npm 依赖树的审计例外。依赖审计以 `go list -m all`、`govulncheck` 和基础
镜像扫描为准。可选 `worker-full` 是独立的兼容发行物；其 Node 基础镜像、全局
npm CLI、Skill helper 与 npm MCP 包必须进入额外的镜像/SBOM/漏洞扫描和版本锁定
流程，不能沿用纯 Go 镜像的审计结论。

Coding Agent 可以执行 shell。Permission 规则用于降低误操作，不是沙箱：文件工具接受绝对路径，Bash 也能访问容器可见资源。安全边界必须由 ECS task/container、单一 workspace mount、只读根文件系统、最小 IAM、网络出口控制和短期 provider 凭证构成。互不信任租户不能共享一个 Worker task。

Coding Bash 与 Pi 一样继承 Worker 环境，因此容器中可见的凭证也可能被命令读取；这正是 task role、短期凭证、最小 IAM、网络出口和租户隔离必须作为真实安全边界的原因。MCP stdio 默认移除名称包含 token、secret、password、credential、API key、DSN 以及 AWS container credentials 的继承变量，只接收配置中显式注入的专用 env。

ECS Worker 从 `BOCHA_API_KEY` 构造原生搜索 Extension 后立即调用
`os.Unsetenv`，因此后续 Coding Bash 与 stdio MCP 子进程不会继承该变量；Key
只保留在 Tool HTTP client 闭包中。自定义 SDK 宿主若从环境读取 Key，也应采用
相同做法，或直接从自己的 secret provider 构造 `bocha.Config`。

MCP server 配置属于受信任部署配置。HTTP URL/header 与 stdio executable/env 由运维提供；带 `destructiveHint` 或 `openWorldHint` 的 MCP tool 需要 App 审批。

`worker-full` 允许模型通过 Bash 运行 Node helper，也允许运维配置 npm stdio
MCP，但不会把 JavaScript 变成可信代码。生产镜像应预装并锁定依赖，禁止从不受信
项目在运行时执行 `npm install`/`npx`；npm cache 和用户级 prefix 位于 Task 临时
目录，不能用来保存长期凭证。

`project_trusted=false` 会阻止读取项目 `.pi` packages/agents/prompts/skills 与项目指令。项目 Agent Package 直接入口还必须显式选择 `agentScope: project` 或 `both`；ECS API 使用 `input.agent_scope`。资源 package 的 tar 解包拒绝目录穿越、符号链接逃逸、超大单文件、过多文件和解压膨胀，安装采用同文件系统原子替换；包内 system prompt 必须位于包根目录内，包内 JavaScript 永不执行。多租户部署默认禁止全局 user package 变更，只允许可信 workspace 内的 project scope。全局变更开关仅适合单租户运维环境。

Agent 委派由 Runtime 双重强制：角色层级规定 main 只能到 sub/meta、sub 只能到
meta、meta 不能委派；调用方 Profile 的 `delegates` 再按 Agent 名称收窄目标。未声明
白名单默认拒绝全部委派，构建独立包时 `"*"` 会冻结为精确名单；运行时通配符也只能放宽到当前 scope 可见且角色兼容的 Agent，
不能越过角色、project trust、workspace 边界或深度限制。

Reviewer 没有通用 Bash 或文件写工具，只能通过结构化 `git_read` 执行
`status/diff/log/show`；Runtime 不接受 commit、checkout、reset 等写命令，也禁用
external diff、textconv、pager 和交互式凭证提示。

API 使用 Bearer service token、tenant header、请求大小限制、严格 JSON、HTTP timeout 和 panic recovery。生产 API 应位于私有子网，只允许 App backend security group 访问。

events、session snapshots、tool arguments 可能包含用户数据和代码，必须设置数据保留策略、RDS 加密/PITR 和日志脱敏。禁止将 prompt、provider key 或数据库 DSN 写入应用日志。
