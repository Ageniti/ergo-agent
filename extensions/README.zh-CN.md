# Go Extension API

[English](README.md) | [简体中文](README.zh-CN.md)

Extension 用于把可信、编译期确定的 Go 能力注册到 Runtime。它可以提供工具、命令和生命周期 Hook，同时保持 Agent 执行引擎与业务实现解耦。需要热插拔或跨语言集成时使用 MCP。

## 能力

- 注册强类型工具和命令。
- 订阅资源、Session、Agent、Provider 与 Tool 生命周期事件。
- 通过宿主控制审批和外部副作用。
- 不动态加载不受信任的 Go plugin 或 JavaScript。

## 博查联网搜索

`extensions/bocha` 把博查 API 暴露为统一工具名 `web_search`。因此 Agent Prompt 与 Provider 无需知道供应商名称。宿主传入 `bocha.Config` 或安全读取 `BOCHA_API_KEY` 后注册 Extension；没有配置时可选工具会被省略。

内置 `web-researcher` 可以并行提出多条检索、综合结果并保留来源链接。API key 不应进入 Prompt、日志或子进程环境。
