# Agent Profile

[English](README.md) | [简体中文](README.zh-CN.md)

本目录包含 `github.com/ageniti/ergo-agent` 默认提供的 Agent 套件。它们是产品资源，不是 Go 实现，因此不会进入生成的 `github.com/ageniti/ergo-core` 仓库。

每个 Markdown 文件都会加载为同一种 Go `resource.Agent`。YAML frontmatter 定义策略，正文定义该 Agent 的专属指令。

| 字段 | 含义 |
|---|---|
| `name` | `agentId` 和委派使用的稳定 ID |
| `description` | 展示给调用方的发现说明 |
| `role` | `main`、`sub` 或 `meta` |
| `tools` | 精确的工具白名单 |
| `optional-tools` | 宿主可以不提供的可选工具 |
| `delegates` | 精确 Agent 白名单，或显式的 `*` |
| `provider` / `model` | 直接运行时的可选默认值 |
| `thinking-level` | 可选默认推理等级 |
| `system-prompt` | 包内或资源根目录中的系统提示词 |

## 委派策略

Go 同时强制角色层级和 Profile 白名单：

```text
main → sub 或 meta
sub  → meta
meta → 不可委派
```

需要委派的 Profile 必须声明 `subagent` 工具和目标。省略或留空 `delegates` 表示禁止全部委派。`delegates: "*"` 只允许当前可见且角色兼容的目标，不能绕过层级；构建包时它会被冻结为精确名称。

## 发现与运行

- 内置 Profile 位于本目录，由 `agent.NewDefault()` 加载。
- 用户 Profile 位于 `~/.pi/agent/agents/`。
- 可信项目 Profile 位于 `.pi/agents/`。
- Package 通过 `package.json` 的 `pi.agents` 导出 Profile。
- 任意 Sub 或 Meta Agent 都能直接作为顶层 `agentId`；入口不需要 Chief。

打包现有 Profile 及其传递依赖：

```bash
go run ./cmd/agent-package -agent coding-agent -output ./dist/coding-agent
```

创建新的独立程序或资源包：

```bash
ergo-agent new --name reviewer --role meta
ergo-agent new --name reviewer --role meta --package-only
```

详见[Agent Package](../docs/AGENT-PACKAGES.zh-CN.md)、[独立 Agent](../docs/STANDALONE-AGENTS.zh-CN.md)和[提示词模板](../docs/PROMPT-TEMPLATES.zh-CN.md)。

`web-researcher` 是内置研究 Meta Agent。宿主注册 `extensions/bocha` 时它可以使用可选 `web_search`；否则该工具会被省略。
