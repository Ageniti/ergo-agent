# 自定义资源根目录

[English](README.md) | [简体中文](README.zh-CN.md)

此示例使用 `github.com/ageniti/ergo-core/runtime` 和应用自己的 `embed.FS`。生成的二进制只包含 `resources/` 中声明的 Agent 与 Prompt，不会链接 Ergo 默认 Chief 或完整资源套件。

## 在本仓库运行

```bash
OPENAI_API_KEY=... go run . "Run the selected meta agent"
```

每次请求都必须显式提供 `agentId`。需要多个 Agent 时，把对应 Profile、系统提示词和声明的依赖一起放进最小资源树。
