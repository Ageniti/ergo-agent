# 声明式 Agent Package

[English](README.md) | [简体中文](README.zh-CN.md)

这个示例同时导出一个 Agent Profile 和一个可选 Prompt Template。Package 是资源分发单元，不包含独立 Runtime；安装后由现有 Ergo 宿主发现和运行。

## 在可信项目中使用

把 Package 安装到项目 scope，并以 `agentScope: project` 或 `both` 运行入口 Agent。`projectTrusted` 必须为 `true`，否则项目 Agent、Prompt、Skill 和 Package 都不会加载。

## 安装远程 Package

Package Manager 支持本地路径、Git/GitHub 和 npm semver 来源。Go 会安全解包声明式资源，但不会执行包内 JavaScript。生产环境应固定版本和来源。

## 通用 Pi Package 与 Ergo 自包含 Package

通用 Pi Package 通过 `pi.agents`、`pi.prompts` 和 `pi.skills` 导出资源。Ergo 自包含 Agent Package 还声明 `ergo.entryAgent`、完整的 `agentDependencies`，以及 `requiredTools`/`optionalTools`。构建器会递归收集委派依赖并冻结通配符，使安装保持原子、离线可用且不会跨包串用依赖版本。

构建与校验方式见 [Agent Package](../../docs/AGENT-PACKAGES.zh-CN.md)。
