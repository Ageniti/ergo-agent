# Headless Pi 兼容矩阵

[English](CONFORMANCE.md) | [简体中文](CONFORMANCE.zh-CN.md)

基准：Pi `v0.81.1` / `20be4b18`。TUI 渲染、主题、终端编辑器、快捷键和交互式 CLI 设置明确不在范围内；其他条目只有具备 Go 兼容测试后才能标记为完成。

交互式订阅 OAuth 登录/刷新和远程动态模型目录不属于本 Headless Runtime。已预先取得的 Token 仍可传给 Transport Adapter。

状态：`complete` 表示已实现并有 Go 兼容测试；`partial` 表示可用但行为尚未完整；`missing` 表示尚未实现。

| 区域 | 能力 | 状态 |
|---|---|---|
| Agent loop | 多轮文本/工具循环、官方串并行顺序、终止 | complete |
| Agent loop | 文本、thinking、工具参数流与 Runtime 事件 | complete |
| Agent loop | steer/follow-up 队列与投递模式 | complete |
| Provider | 默认 OpenAI Responses 与显式 Chat Completions 兼容模式 | complete |
| Provider | Anthropic Messages budget/adaptive thinking、工具与流式 | complete |
| Provider | Gemini/Vertex 流式、thought signature、function ID 与并行结果 | complete |
| Provider | Codex token/JWT header、加密 reasoning 与 Azure Responses | complete |
| Provider | Bedrock Converse/stream、Mistral typed reasoning 与 Pi Messages | complete |
| Provider | 结构化流错误、终止事件检查与跨模型签名隔离 | complete |
| Provider | 模型感知 API-key 目录、自定义协议、Registry 与 Gateway header | complete |
| Provider | usage/cache 记账和 Transport 专属重试（按产品范围不含内置价格表） | complete |
| Images | OpenRouter 生成、Pi 模型能力、data URL、错误与可选 Agent Tool | complete |
| Session | 树 Entry、active leaf、label、名称与自定义 Entry | complete |
| Session | 分支上下文、fork-before/fork-after | complete |
| Session | 可选分支摘要的树导航 | complete |
| Compaction | 手动模型摘要 | complete |
| Compaction | 自动 Token 阈值、保留区与排队续接 | complete |
| Tools | read/write/edit/bash/grep/find/ls 与安全 `git_read` | complete |
| Tools | 图片读取、输出截断和进程取消 | complete |
| Tools | Bash 流式更新 | complete |
| Tools | 官方并行/串行批处理语义 | complete |
| Plan | 只读工具、Bash 白名单、上下文 Prompt、Plan 解析与 DONE | complete |
| Plan | 持久化开关与执行状态 | complete |
| Plan | Headless 持久化问卷 | complete |
| Todo | list/add/toggle/clear 与分支重建 | complete |
| Permission | 危险 Bash、MCP annotation 与持久化审批恢复 | complete |
| Subagent | 内置/YAML 角色、单个/并行/链式/深度/scope 与部分失败保留 | complete |
| Resources | system prompt、AGENTS/CLAUDE 上下文、项目信任与内置角色 | complete |
| Skills | user/project/.agents 递归发现、校验、metadata 注入与调用 | complete |
| Prompts | 发现与官方位置/默认值/切片参数形式 | complete |
| Packages | string/object filter、本地/Git/GitHub/npm semver 全生命周期与诊断 | complete |
| Extensions | Go Tool/Command、生命周期总线、Command context 与强类型 Hook | complete |
| Extensions | model/thinking/action、队列、信任、Session、Provider 与动态资源 | complete |
| MCP | 官方 Go SDK、stdio/HTTP、分页、重连与 roots | complete |
| MCP | tools/resources/templates/prompts 与 annotation | complete |
| MCP | Host sampling | complete |
| MCP | 持久化 elicitation 回到同一 MCP 请求 | complete |
| Operations | inspect/model/thinking/tools/queue/name/label/custom/package Entry | complete |
| Runtime | MySQL job、lease、heartbeat、审批、Plan、Todo 与 outbox | complete |

只有测试同步更新时才能修改表格状态。[PI 迁移对照](PI-PARITY.zh-CN.md)描述映射；本文件是发布门禁，必须如实反映未完成项。
