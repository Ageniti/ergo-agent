# Prompt 资源

[English](README.md) | [简体中文](README.zh-CN.md)

本目录保存默认 Agent 的运行时提示词资源。默认内容使用英文，以便 SDK 在没有语言配置时保持一致、可移植的行为。

## 目录

- `system/`：Agent 的底层系统提示词。
- `modes/`：Plan、Execute 等运行模式补充指令。
- `templates/` 或包内 `pi.prompts`：可展开为普通用户消息的 `/` 模板。

Agent Profile 通过 `system-prompt` 绑定系统提示词；Prompt Template 不注册 Agent，也不会替换系统提示词。模板详情见[提示词模板](../docs/PROMPT-TEMPLATES.zh-CN.md)。
