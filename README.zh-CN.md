# Ergo Agent

[English](README.md) | [简体中文](README.zh-CN.md)

[![License: AGPL v3 or Commercial](https://img.shields.io/badge/license-AGPL--3.0--only%20OR%20Commercial-blue.svg)](LICENSE)

Ergo Agent 是一个独立的纯 Go Headless Agent SDK，可用于把 Agent 嵌入 Go
应用、发布独立的专业 Agent，以及运行可水平扩展的 Agent 服务。

Ergo 借鉴了 [Pi](https://github.com/badlogic/pi-mono) 的部分设计，并选择性兼容
其执行行为、Prompt、工具契约和资源约定。在这层兼容之上，Ergo 拥有独立的纯 Go
Runtime、SDK、Role 系统、集成、打包方式和企业控制面。

| 借鉴或适配自 Pi | Ergo 自主设计与实现 |
|---|---|
| 部分 Agent Loop 与 Tool Call 行为 | 纯 Go Runtime 与公开 SDK 分层 |
| Prompt、工具契约和 Session 的兼容目标 | `main` / `sub` / `meta` Role 与强制委派边界 |
| `.pi` 资源路径与 `pi.*` Package Manifest | 原生 Go Extension、MCP 集成和独立 Agent 打包 |
| Pi `v0.81.1` 兼容参考 | 持久化审批、MySQL/ECS 控制面、博查搜索和企业运行能力 |

## 先理解四个概念

| 概念 | 含义 |
|---|---|
| **Runtime** | 共享的 Go 执行引擎：模型循环、工具、Session、委派、MCP 和事件 |
| **Agent** | 声明身份、Role、工具、Prompt、模型默认值和委派策略的 Markdown Profile |
| **Role** | `main`、`sub`、`meta` 三种委派边界 |
| **Agent Package** | 安装到现有 Runtime 的声明式 Agent 资源 |

每次运行都会解析一个入口 Agent。传入 `agentId` 可以明确选择入口：最小 Core
Runtime 强制要求该字段；完整默认 Runtime 在省略时可以回退到 `chief-agent`。
选中的 Agent 可以直接完成任务，也可以通过 `subagent` 工具委派。系统只有一套
Runtime 和一种 Agent 类型；Chief、Coding 和各种专业 Agent 只是不同 Profile，
并运行在同一套引擎上。

## Agent Role

Role **只控制向外委派权限**：

| Role | 用途 | 可以委派给 | 可以直接作为入口 |
|---|---|---|---:|
| `main` | 处理宽泛请求的顶层协调者 | `sub`、`meta` | 可以 |
| `sub` | 负责某个领域或工作流的执行 Agent | `meta` | 可以 |
| `meta` | 聚焦单项能力的叶子专家 | 无 | 可以 |

```text
main ──→ sub ──→ meta
  └────────────→ meta
```

Profile 独立声明工具权限；入口选择也与 Role 分开。调用方可以直接运行
`chief-agent`、`coding-agent`、`reviewer` 或 `web-researcher`。

只有同时满足以下条件才允许委派：

1. 调用方 Role 可以委派给目标 Role；
2. 调用方声明了 `subagent` 工具；
3. 目标位于调用方的 `delegates` 白名单；
4. 目标在当前资源 Scope 中可见；
5. 通过 Runtime 深度和项目信任检查。

委派默认关闭。`delegates: "*"` 显式启用所有可见且 Role 兼容的目标；构建
Package 时会被冻结成精确名称。

## 默认 Agent 套件

完整 `ergo-agent` Module 内置以下 Profile：

| Agent | Role | 职责 | 委派对象 |
|---|---|---|---|
| `chief-agent` | `main` | 通用入口与任务协调 | Coding Agent 和兼容的 Meta Agent |
| `coding-agent` | `sub` | 仓库分析与代码实现 | Scout、Planner、Reviewer、Worker、Web Researcher |
| `scout` | `meta` | 快速探索代码库并压缩上下文 | 无 |
| `planner` | `meta` | 只读实现规划 | 无 |
| `reviewer` | `meta` | 只读代码与安全审查 | 无 |
| `worker` | `meta` | 在隔离上下文中执行被委派任务 | 无 |
| `web-researcher` | `meta` | 并行联网搜索并带来源综合 | 无 |

典型调用链：

```text
App → chief-agent → coding-agent → reviewer
App → coding-agent → scout
App → reviewer
App → web-researcher → web_search
```

后两条链路展示了专业 Agent 直接入口。详见
[Agent Profile](agents/README.zh-CN.md)。

## 选择发行方式

| 你的目标 | 使用方式 | 产物内容 |
|---|---|---:|
| 使用完整内置套件 | `github.com/ageniti/ergo-agent/agent` | Runtime + 默认 Agent |
| 构建自己的最小 Agent 应用 | `github.com/ageniti/ergo-core/runtime` | Core Runtime |
| 给已有 Ergo 宿主提供资源 | Agent Package | Agent 资源 |
| 快速创建后两种产物 | 可选 `ergo-agent` CLI | 开发脚手架 |

## 仓库与依赖关系

项目只有一套维护中的代码库，对外提供两个 Go 发行物：

```text
github.com/ageniti/ergo-agent              规范源码仓库
├── agent/                                 完整 SDK 门面
├── agents/ + prompts/ + skills/           默认产品资源
├── cmd/ergo-agent/                        脚手架 CLI
└── cmd/export-core
    └── github.com/ageniti/ergo-core       自动生成的 Core 发行物
        └── runtime/                        最小应用入口
```

`ergo-core` 是从 `ergo-agent` 中选定 Package 自动生成的最小发行边界，两个发行物
共享唯一的规范实现。代码统一在 `ergo-agent` 修改并测试；`cmd/export-core`
导出后，会以独立 Go Module 再次测试，随后发布到 Core 仓库。

依赖方向是单向的：

```text
ergo-agent 源码 ──发布导出──→ ergo-core ──被导入──→ 最小应用
       └────────────────────→ 完整套件应用
```

| 使用者 | 导入内容 | Go 下载内容 | Runtime 资源 |
|---|---|---|---|
| 完整套件应用 | `ergo-agent/agent` | `ergo-agent` Module | 默认 Agent、Prompt 和 Skill |
| 最小自定义应用 | `ergo-core/runtime` | `ergo-core` Module | 应用自己的资源 |
| 通过 `go install` 安装 CLI | `ergo-agent/cmd/ergo-agent` | 下载一次 `ergo-agent` 编译 CLI | 生成基于 Core 的项目 |
| 使用预编译 CLI | 下载 CLI 二进制 | 生成项目解析 `ergo-core` | 生成的资源 |
| 已有 Ergo 宿主 | Agent Package | 宿主选择的 Runtime + 资源 Package | Package 内 Agent 与 Prompt |

规范 `ergo-agent` 的 `go.mod` 解析自身源码 Module 与上游 Go 依赖。仓库中出现的
`ergo-core` 引用分别服务于文档、发布导出逻辑，以及 CLI 为外部最小应用生成的
源码模板。

### 使用完整套件

```bash
go get github.com/ageniti/ergo-agent@latest
```

```go
import agent "github.com/ageniti/ergo-agent/agent"

agentRuntime, err := agent.NewDefault()
if err != nil {
    return err
}
err = agentRuntime.RunWithOptions(ctx, map[string]any{
    "agentId": "chief-agent", // 可以换成任何当前可见 Agent
    "prompt":  "Inspect this project.",
    "cwd":     ".",
}, agent.RunOptions{}, sink)
```

`agent.NewDefault()` 加载内置完整套件。`agent.New(root)` 是使用完整 SDK 加载
调用方自定义完整资源根目录的兼容入口。

### 构建独立 Sub 或 Meta Agent

CLI 只是可选脚手架，不属于 Runtime：

```bash
go install github.com/ageniti/ergo-agent/cmd/ergo-agent@latest

ergo-agent new \
  --name reviewer-agent \
  --role meta \
  --module example.com/reviewer-agent

cd reviewer-agent
go mod tidy
OPENAI_API_KEY=... go run . "Review this repository."
```

生成应用只导入 `github.com/ageniti/ergo-core/runtime`，只嵌入自己的
`resources/`，并明确传入 `agentId`。它的二进制与 Go 依赖图由 Core Runtime 和
该应用选择的资源组成。

上表分别说明了源码安装 CLI 与预编译 CLI 的下载边界；两种方式生成的项目都会
解析 Core 发行物。

脚手架用于生成可复用的 Sub/Meta 专家 Agent。自定义 Main Agent 使用相同 Profile
格式，把 `role` 写为 `main` 后通过 Core 手动构建即可。

详见[独立 Agent](docs/STANDALONE-AGENTS.zh-CN.md)。

### 只构建 Agent Package

```bash
ergo-agent new \
  --name reviewer-agent \
  --role meta \
  --package-only
```

产物是由 Markdown Profile、Prompt 和 `package.json` 组成的纯资源目录，可直接
安装到已有 Ergo 宿主。

把已有 Profile 及其完整委派依赖闭包打成 Package：

```bash
go run ./cmd/agent-package \
  -agent coding-agent \
  -output ./dist/coding-agent
```

构建器会递归加入所有已声明 Delegate、复制其包内 System Prompt、冻结通配符委派，
并记录入口 Agent 和宿主工具契约。

详见 [Agent Package](docs/AGENT-PACKAGES.zh-CN.md)。

## 定义一个 Agent

Agent 是带 YAML frontmatter 的 Markdown 文件：

```md
---
name: repository-agent
description: Implements repository changes and may request focused reviews
role: sub
tools: read, grep, find, ls, edit, write, subagent
optional-tools: web_search
delegates: reviewer, web-researcher
provider: openai
model: gpt-5
thinking-level: high
system-prompt: prompts/system/repository-agent.md
---

You are Ergo's repository implementation specialist.
Complete the requested change and verify the result.
```

`tools` 是精确能力白名单。`optional-tools` 让同一 Agent 可以适配能力不同的宿主。
`delegates` 声明 Agent 资源依赖。

Profile 可以来自：

- 宿主内嵌资源；
- `~/.pi/agent/agents/` 用户资源；
- `.pi/agents/` 可信项目资源；
- 通过 `pi.agents` 导出的本地、Git、GitHub 或 npm 资源 Package。

`.pi` 路径和 `pi.*` Manifest 字段为了兼容 Pi Package 而保留；实际执行的 Agent
仍然认同自己是 Ergo。

## Prompt、Skill、Tool、Extension 与 MCP

| 资源 | 作用 |
|---|---|
| Agent Profile | 身份、Role、工具、模型默认值与委派策略 |
| System Prompt | Profile 使用的底层 Runtime 行为 |
| Prompt Template | 发给当前 Agent 的可复用 `/` 用户消息 |
| Skill | 按需加载的任务指令 |
| Tool | 宿主实现、可由模型调用的能力 |
| Extension | 编译或启动时注册的可信 Go 组件 |
| MCP Server | 可热插拔的本地或远程能力提供方 |

Prompt Template 会展开为发送给当前 Agent 的消息；Skill 用于指导 Agent 使用宿主
提供的能力。

官方博查 Extension 注册与供应商无关的 `web_search` Tool。`web-researcher` 在该
Tool 上进行并行搜索规划并返回带来源的综合结果：

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

可信的进程内能力使用 Go Extension；需要热插拔或跨语言时使用 MCP。MCP 支持
stdio、Streamable HTTP、Tool、Resource、Template、Prompt、Sampling、
Elicitation、分页、Roots 和重连。

详见 [Prompt](prompts/README.zh-CN.md)、
[Prompt Template](docs/PROMPT-TEMPLATES.zh-CN.md)、
[Skill](skills/README.zh-CN.md)和 [Extension](extensions/README.zh-CN.md)。

## Runtime 能力

- 流式多轮模型与工具循环；
- OpenAI、Azure、Anthropic、Gemini/Vertex、Bedrock、Mistral、Pi Messages
  和可配置兼容 Provider；
- Session、分支、压缩、Steering、Follow-up 队列与 Snapshot；
- Plan Mode、Todo、问卷和持久化审批恢复；
- 受 Role 与 Profile 双重约束的 Agent 委派；
- 原生 Go Extension、MCP、Skill、Prompt Template 和资源 Package；
- 可选 OpenRouter 图片生成和博查联网搜索；
- 可选 MySQL/ECS Job、Lease、Outbox 与多实例参考服务。

详见[架构](docs/ARCHITECTURE.zh-CN.md)、
[兼容矩阵](docs/CONFORMANCE.zh-CN.md)、
[Pi 迁移](docs/PI-PARITY.zh-CN.md)、
[安全](docs/SECURITY.zh-CN.md)和
[ECS 部署手册](examples/ecs/deploy/ecs/README.zh-CN.md)。

## 仓库结构

```text
agent/
├── agent/                  # 完整兼容 SDK
├── runtime/                # 最小公开 Runtime；导出到 ergo-core
├── provider/ message/ tool/ session/
├── resource/               # Profile、Prompt、Skill、Package
├── extensions/             # Go Extension API 与原生集成
├── internal/engine/        # 私有执行引擎
├── agents/ prompts/ skills/# 默认产品资源
├── cmd/ergo-agent/         # 可选脚手架 CLI
├── cmd/agent-package/      # 自包含 Package 构建器
├── cmd/export-core/        # 生成只读 Core 发行版
└── examples/               # SDK、Package 与 ECS 示例
```

## 开发

需要 Go 1.26 或更新版本：

```bash
go test ./...
go vet ./...
```

默认构建和测试只需要 Go 工具链。

## License

Ergo Agent 使用 AGPL-3.0-only 或另行签署的 Commercial License。详见
[LICENSE](LICENSE)、[Commercial License](LICENSE-COMMERCIAL.md)和
[NOTICE](NOTICE)。

商业授权：Yiliu Li — `yiliu.li@outlook.com`。
