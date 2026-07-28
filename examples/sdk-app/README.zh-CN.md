# 完整 Go SDK 应用

[English](README.md) | [简体中文](README.zh-CN.md)

本示例导入 `github.com/ageniti/ergo-agent/agent` 并使用 `agent.NewDefault()`，因此包含完整默认 Agent、Prompt 和 Skill 套件。

## 运行

```bash
OPENAI_API_KEY=... go run . "Review this repository"
```

## 应导入哪个 SDK？

| 需求 | 选择 |
|---|---|
| 完整默认套件 | `ergo-agent/agent` |
| 仅自带 Agent 资源 | `ergo-core/runtime` |
| 只生成可安装资源 | `ergo-agent new --package-only` |

若不希望二进制包含 Chief，请使用同目录的 `custom-root` 示例。
