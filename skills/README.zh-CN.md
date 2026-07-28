# Skills

[English](README.md) | [简体中文](README.zh-CN.md)

Skill 是带有 `SKILL.md` 入口的指令资源，可按需加载到 Agent 上下文中。它不是 Agent、Tool 或 MCP Server；Skill 可以指导 Agent 调用宿主已经提供的工具或外部程序。

默认 Go Runtime 不执行包内 JavaScript。纯 Markdown Skill 可直接使用；依赖 Node/helper 的 Skill 只有在宿主镜像提供对应运行时和命令时才能工作。动态业务能力优先通过 MCP 或 Go Extension 接入。

用户 Skill 位于 `~/.pi/agent/skills/`，可信项目 Skill 位于 `.pi/skills/`，Package 可通过 `package.json` 的 `pi.skills` 导出。
