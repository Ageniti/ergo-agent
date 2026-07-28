// Command export-core creates the source tree published as
// github.com/ageniti/ergo-core.
//
// Ergo Agent is the only editable source of truth. The core repository is a
// generated, read-only distribution with no bundled Agent profiles or prompts.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	sourceModule = "github.com/ageniti/ergo-agent"
	coreModule   = "github.com/ageniti/ergo-core"
)

var coreDirectories = []string{
	"extensions",
	"internal/buildinfo",
	"internal/engine",
	"message",
	"provider",
	"resource",
	"runtime",
	"session",
	"tool",
}

var exportedTestDirectories = map[string]bool{
	"extensions": true,
	"provider":   true,
	"runtime":    true,
}

func main() {
	output := flag.String("output", "", "new output directory")
	flag.Parse()
	if strings.TrimSpace(*output) == "" {
		fmt.Fprintln(os.Stderr, "usage: export-core -output <new-directory>")
		os.Exit(2)
	}
	if err := exportCore(".", *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func exportCore(sourceRoot, output string) error {
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(sourceRoot, "go.mod")); err != nil {
		return errors.New("export-core must run from the ergo-agent module root")
	}
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("output already exists: %s", output)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(output), ".ergo-core-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	for _, name := range []string{"go.mod", "go.sum", "LICENSE", "LICENSE-COMMERCIAL.md", "NOTICE"} {
		if err := copyTransformedFile(filepath.Join(sourceRoot, name), filepath.Join(staging, name)); err != nil {
			return err
		}
	}
	for _, directory := range coreDirectories {
		if err := exportDirectory(sourceRoot, staging, directory); err != nil {
			return err
		}
	}
	readme := `# Ergo Core

[English](README.md) | [简体中文](README.zh-CN.md)

Pure Go execution SDK for applications that ship their own Ergo Agents.

Ergo draws inspiration from Pi and preserves selected behavioral and resource
compatibility. Its independent, self-contained pure-Go Runtime provides the
execution foundation.

This is a generated, read-only distribution of the canonical
[` + sourceModule + `](https://` + sourceModule + `) repository. Submit issues,
changes, and releases there. Do not edit this repository directly.

## What is included

- Agent execution loop, tools, sessions, compaction, MCP, and Extension API;
- Provider adapters and model/image contracts;
- Agent profile, prompt, Skill, and package loaders;
- optional native Go integrations such as Bocha ` + "`web_search`" + `.

## What is excluded

- Chief, Coding, and all default Agent profiles;
- default system prompts and bundled Skills;
- CLI/application examples and the ECS/MySQL control plane;
- JavaScript Extension loading.

## Install

` + "```bash" + `
go get ` + coreModule + `/runtime
` + "```" + `

` + "```go" + `
import ergoruntime "` + coreModule + `/runtime"

agentRuntime := ergoruntime.New("./resources")
err := agentRuntime.Run(ctx, map[string]any{
    "agentId": "my-agent",
    "prompt":  "Complete the assigned task.",
    "cwd":     ".",
}, nil, sink)
` + "```" + `

Core uses explicit entry selection, so every run provides ` + "`agentId`" + `.
Use [` + sourceModule + `](https://` + sourceModule + `) when you want Ergo's
complete default Agent suite or the ` + "`ergo-agent`" + ` scaffolding CLI.

## License

AGPL-3.0-only OR a separately executed Ergo Commercial License. See
` + "`LICENSE`" + ` and ` + "`LICENSE-COMMERCIAL.md`" + `.
`
	readmeZH := `# Ergo Core

[English](README.md) | [简体中文](README.zh-CN.md)

面向自带 Ergo Agent 资源的应用程序的纯 Go 执行 SDK。

Ergo 借鉴了 Pi 的部分设计并选择性保留行为与资源兼容，其独立、自包含的纯 Go
Runtime 提供执行基础。

这是由规范源码仓库 [` + sourceModule + `](https://` + sourceModule + `)
自动生成的只读发行版。Issue、修改和发布都应在规范仓库进行，请勿直接编辑本仓库。

## 包含内容

- Agent 执行循环、工具、Session、压缩、MCP 与 Extension API；
- Provider 适配器以及模型/图片契约；
- Agent Profile、Prompt、Skill 和 Package 加载器；
- 博查 ` + "`web_search`" + ` 等可选原生 Go 集成。

## 不包含内容

- Chief、Coding 等全部默认 Agent Profile；
- 默认系统提示词和内置 Skill；
- CLI、应用示例和 ECS/MySQL 控制面；
- JavaScript Extension 加载。

## 安装

` + "```bash" + `
go get ` + coreModule + `/runtime
` + "```" + `

Core 使用显式入口选择，每次运行都提供 ` + "`agentId`" + `。需要完整默认
Agent 套件或 ` + "`ergo-agent`" + ` 脚手架 CLI 时，请使用
[` + sourceModule + `](https://` + sourceModule + `)。

## 许可证

AGPL-3.0-only，或另行签署的 Ergo Commercial License。详见
` + "`LICENSE`" + ` 和 ` + "`LICENSE-COMMERCIAL.md`" + `。
`
	if err := os.WriteFile(filepath.Join(staging, "README.md"), []byte(readme), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, "README.zh-CN.md"), []byte(readmeZH), 0644); err != nil {
		return err
	}
	return os.Rename(staging, output)
}

func exportDirectory(sourceRoot, staging, directory string) error {
	source := filepath.Join(sourceRoot, directory)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("core export does not accept symlinks: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if filepath.Ext(relative) != ".go" &&
			relative != filepath.Join("extensions", "README.md") &&
			relative != filepath.Join("extensions", "README.zh-CN.md") {
			return nil
		}
		if strings.HasSuffix(relative, "_test.go") && !exportedTestDirectories[topDirectory(relative)] {
			return nil
		}
		return copyTransformedFile(path, filepath.Join(staging, relative))
	})
}

func topDirectory(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func copyTransformedFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	data = []byte(strings.ReplaceAll(string(data), sourceModule, coreModule))
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0644)
}
