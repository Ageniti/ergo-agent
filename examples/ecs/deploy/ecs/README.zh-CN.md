# AWS ECS 部署手册

[English](README.md) | [简体中文](README.zh-CN.md)

本目录描述无状态 API 与 Worker 的生产部署。API 接收请求并写入 MySQL job/outbox/session；Worker 使用租约领取任务并直接运行 Go Runtime。

## 必需基础设施

- 私有子网中的 ECS/Fargate 服务。
- RDS MySQL，用于 job、outbox、session、审批和交互状态。
- 所有 Worker 可见的稳定 workspace（例如 EFS access point）。
- 最小权限 IAM task role、Secrets Manager/Parameter Store、受控网络出口。
- API 前的负载均衡和仅允许应用后端访问的安全组。

## 镜像

默认 `worker` 是纯 Go 镜像。只有确实需要 Node Skill helper 或 npm stdio MCP 时才使用独立的 `worker-full`，并对它额外执行 SBOM、依赖锁定和漏洞扫描。

## 发布顺序

1. 构建并扫描不可变镜像。
2. 用一次性 ECS task 执行数据库迁移。
3. 部署 API，再部署 Worker。
4. 对目标 Provider、模型、权限暂停/恢复和 MCP 运行 smoke test。
5. 使用 deployment circuit breaker 自动回滚失败版本。

## 扩缩容与可靠性

API 无状态；Worker 不保存本地 Session。任务是 at-least-once，外部副作用工具必须幂等。Worker 按租约心跳，超时任务可由其他实例接管；outbox 消费者按 event ID 去重。

更多边界见[根 README](../../../../README.zh-CN.md)、[架构](../../../../docs/ARCHITECTURE.zh-CN.md)与[安全说明](../../../../docs/SECURITY.zh-CN.md)。
