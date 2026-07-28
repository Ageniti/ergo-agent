# Agent Package

[English](AGENT-PACKAGES.md) | [简体中文](AGENT-PACKAGES.zh-CN.md)

每个 Agent Profile 都能发布为独立的 npm、Git 或本地 Package。一个 Agent Package 有且只有一个入口 Agent，并包含委派策略所需的完整 Profile 依赖闭包。

## 构建

```bash
go run ./cmd/agent-package \
  -root . \
  -agent coding-agent \
  -output ./dist/coding-agent \
  -name @acme/coding-agent \
  -version 1.0.0
```

输出路径必须不存在，命令不会覆盖现有目录。

## 依赖解析

构建器读取入口 Profile 的 `delegates`，递归打包目标及其依赖。缺失或角色不兼容会导致构建失败；未声明白名单则不增加依赖；`"*"` 会在构建时展开并冻结为精确名称。

所有依赖都放进一个自包含 Package。安装器不再获取传递 npm/Git 依赖，因此安装是原子的、可离线的，也不会产生跨包版本或卸载冲突。

## 输出与 Manifest

输出包含英文/中文 README、许可证、`package.json`、全部 Agent Profile 及其包内系统提示词。默认继承根目录许可证，也可以用 `-license` 覆盖。

`pi.agents` 导出 Profile；`ergo.entryAgent` 声明入口；`ergo.agentDependencies` 是冻结后的依赖；`requiredTools` 与 `optionalTools` 是所含 Profile 的工具并集。Runtime 在 Agent 启动时检查必需能力，可选能力不可用时会省略。

Agent 依赖属于资源，会被打包；Go Tool 实现仍由宿主在编译期提供。

## 安装与运行

使用 `agent.PackageManager` 安装到可信的 project scope，然后以 Package 入口 `agentId` 运行。安装只会向宿主 Registry 增加 Profile，不会删除其他 Agent。若应用只能暴露该 Package，应改用自定义资源根目录。

同名 Package Profile 可以覆盖内置 Profile；委派会优先在调用方自己的 Package 中解析依赖，避免不同 Package 的同名依赖互相串用。
