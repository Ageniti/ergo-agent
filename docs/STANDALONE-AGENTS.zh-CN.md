# 独立 Agent 开发

[English](STANDALONE-AGENTS.md) | [简体中文](STANDALONE-AGENTS.zh-CN.md)

`ergo-agent new` 可以生成可独立运行的 Sub 或 Meta Agent。生成应用导入最小的 `github.com/ageniti/ergo-core/runtime`，只嵌入自己的 `resources/`，并显式传入 `agentId`；它不会导入 SDK 默认 Chief 资源包。

安装命令：

```bash
go install github.com/ageniti/ergo-agent/cmd/ergo-agent@latest
```

安装 CLI 时会下载一次完整 `ergo-agent` Module；之后生成的项目只依赖 `ergo-core`。若使用预编译 CLI，则连这次源码下载也不需要。

## 生成独立 Go 程序

```bash
ergo-agent new --name reviewer-agent --role meta --module example.com/reviewer-agent
cd reviewer-agent
go mod tidy
OPENAI_API_KEY=... go run . "Review this repository."
```

输出只包含 `go.mod`、`main.go`、英文/中文 README，以及最小 `resources/`。构建出的单一可执行文件只包含所选 Profile 和 Prompt，不包含 Chief。

## 只生成可安装 Agent Package

```bash
ergo-agent new --name reviewer-agent --role meta --package-only
```

该输出没有 `main.go` 和 Runtime，只是供现有 Ergo 宿主安装的声明式资源包。

## 依赖

新 Agent 默认没有 Agent 依赖。只有角色允许时才能在 Profile 中加入 `subagent` 和 `delegates`，并把依赖 Profile、系统提示词放进同一资源树，同时在 `package.json` 的 `ergo.agentDependencies` 中列出。Meta Agent 不可委派；校验器会拒绝缺失或角色不兼容的依赖。

现有仓库 Agent 可使用 `cmd/agent-package`，它会递归收集并冻结入口 Agent 已声明的依赖闭包。

## Runtime 选择

| 需求 | 导入或命令 | 是否包含 Chief |
|---|---|---|
| 只有自己的 Agent | `github.com/ageniti/ergo-core/runtime` | 否 |
| 完整默认套件 | `github.com/ageniti/ergo-agent/agent` + `agent.NewDefault()` | 是 |
| 供现有宿主安装的资源 | `ergo-agent new --package-only` | 不含 Runtime |

三种方式共享同一个执行引擎，区别只在链接哪些资源、是否提供默认入口。
