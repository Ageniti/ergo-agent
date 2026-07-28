# Prompt Template

[English](PROMPT-TEMPLATES.md) | [简体中文](PROMPT-TEMPLATES.zh-CN.md)

`pi.prompts` 导出可复用的 Markdown 用户消息模板。UI 可以把它们显示成 `/` 快捷命令；它们不定义 Agent，也不修改系统提示词。

例如 `/repo-review security` 会找到 `prompts/repo-review.md`，展开参数，然后把结果作为一条普通用户消息发送给当前 Agent。Headless Runtime 不解析编辑器文本；CLI、App 或 API 应调用 `prompt_template` operation，并传入 `templateName`、`templateArgs` 和 `agentId`。

## Package Manifest

```json
{
  "pi": {
    "prompts": ["prompts/*.md"]
  }
}
```

Markdown 文件名就是命令名。`prompts/repo-review.md` 注册 `/repo-review`，frontmatter 的 `name` 不会改名。

支持 `$1`、`$2`、`$@`、`$ARGUMENTS`、`${1:-default}`、`${@:2}` 和 `${@:2:2}` 等 Pi 兼容参数形式。

三种资源相互独立：

| 资源 | 用途 | 注册位置 |
|---|---|---|
| Agent Profile | 谁运行、角色与工具 | `pi.agents` |
| Prompt Template | 当前 Agent 这一次做什么 | `pi.prompts` |
| System Prompt | Runtime 的底层行为约束 | Agent `system-prompt` |

`examples/agent-package` 展示了同时导出 Agent 和可选 Prompt Template 的 Package。
