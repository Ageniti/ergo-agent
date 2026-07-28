package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "new" {
		fmt.Fprintln(os.Stderr, "usage: ergo-agent new --name <name> --role sub|meta [options]")
		os.Exit(2)
	}
	if err := runNew(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runNew(arguments []string) error {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("name", "", "Agent name")
	role := flags.String("role", "", "Agent role: sub or meta")
	output := flags.String("output", "", "Output directory (default: ./<name>)")
	module := flags.String("module", "", "Go module path (app mode)")
	tools := flags.String("tools", "read, grep, find, ls", "Comma-separated Agent tools")
	model := flags.String("model", "", "Optional default model")
	license := flags.String("license", "AGPL-3.0-only", "Generated package license")
	packageOnly := flags.Bool("package-only", false, "Generate only a declarative Agent Package")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	*name, *role = strings.TrimSpace(*name), strings.TrimSpace(*role)
	if !agentNamePattern.MatchString(*name) {
		return errors.New("--name must use letters, digits, dot, underscore, or hyphen")
	}
	if *role != "sub" && *role != "meta" {
		return errors.New("--role must be sub or meta")
	}
	if *output == "" {
		*output = *name
	}
	if *module == "" {
		*module = "example.com/" + *name
	}
	toolNames := normalizedList(*tools)
	if len(toolNames) == 0 {
		return errors.New("--tools must contain at least one tool")
	}
	destination, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("output already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), ".ergo-agent-new-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	resourceRoot := staging
	if !*packageOnly {
		resourceRoot = filepath.Join(staging, "resources")
	}
	if err := writeResources(resourceRoot, *name, *role, *model, *license, toolNames); err != nil {
		return err
	}
	if *packageOnly {
		if err := writePackageReadmes(staging, *name, *role, *license, toolNames); err != nil {
			return err
		}
	} else {
		if err := writeApp(staging, *name, *role, *module, *license, toolNames); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	fmt.Printf("Created %s Agent at %s\n", *role, destination)
	return nil
}

func writeResources(root, name, role, model, license string, tools []string) error {
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "prompts", "system"), 0755); err != nil {
		return err
	}
	profile := "---\n" +
		"name: " + strconv.Quote(name) + "\n" +
		"description: " + strconv.Quote("Standalone "+role+" Agent "+name) + "\n" +
		"role: " + role + "\n" +
		"tools: " + strings.Join(tools, ", ") + "\n"
	if strings.TrimSpace(model) != "" {
		profile += "model: " + strconv.Quote(strings.TrimSpace(model)) + "\n"
	}
	profile += "system-prompt: prompts/system/" + name + ".md\n---\n\n" +
		"You are " + name + ", a standalone " + role + " Agent. Complete the assigned task within your declared capabilities.\n"
	if err := os.WriteFile(filepath.Join(root, "agents", name+".md"), []byte(profile), 0644); err != nil {
		return err
	}
	systemPrompt := "You are " + name + ", a standalone " + role + " Agent.\n\n" +
		"Available tools:\n{{TOOLS}}\n\nGuidelines:\n{{GUIDELINES}}\n" +
		"{{PROJECT_CONTEXT}}{{SKILLS}}{{APPEND_SYSTEM_PROMPT}}\n"
	if err := os.WriteFile(filepath.Join(root, "prompts", "system", name+".md"), []byte(systemPrompt), 0644); err != nil {
		return err
	}
	manifest := map[string]any{
		"name": "@ageniti/" + name, "version": "0.1.0", "license": license,
		"files": []string{"agents", "prompts/system"},
		"pi":    map[string]any{"agents": []string{"agents/*.md"}},
		"ergo": map[string]any{
			"entryAgent": name, "agentDependencies": []string{},
			"requiredTools": tools, "optionalTools": []string{},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "package.json"), append(data, '\n'), 0644)
}

func writeApp(root, name, role, module, license string, tools []string) error {
	goMod := "module " + strings.TrimSpace(module) + "\n\ngo 1.26.0\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0644); err != nil {
		return err
	}
	mainSource := `package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	ergoruntime "github.com/ageniti/ergo-core/runtime"
)

//go:embed resources
var resources embed.FS

func main() {
	provider := flag.String("provider", "openai", "model provider")
	model := flag.String("model", "gpt-5", "model ID")
	flag.Parse()
	prompt := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: app [--provider provider] [--model model] <prompt>")
		os.Exit(2)
	}
	resourceFS, err := fs.Sub(resources, "resources")
	if err != nil {
		panic(err)
	}
	agentRuntime, err := ergoruntime.NewFS(resourceFS)
	if err != nil {
		panic(err)
	}
	cwd, _ := os.Getwd()
	encoder := json.NewEncoder(os.Stdout)
	err = agentRuntime.Run(context.Background(), map[string]any{
		"agentId": ` + strconv.Quote(name) + `,
		"prompt": prompt,
		"cwd": cwd,
		"provider": *provider,
		"model": *model,
	}, nil, func(event ergoruntime.Event) error {
		return encoder.Encode(event)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSource), 0644); err != nil {
		return err
	}
	readme := fmt.Sprintf(`# %s

[English](README.md) | [简体中文](README.zh-CN.md)

Standalone Ergo %s Agent generated by `+"`ergo-agent new`"+`.

Ergo draws inspiration from Pi and preserves selected compatibility. Its
independent pure-Go Runtime provides the execution foundation.

This application imports `+"`github.com/ageniti/ergo-core/runtime`"+` and embeds
exactly its own `+"`resources/`"+` directory. Explicit entry selection keeps the
resulting application focused on this Agent's profiles, prompts, and Skills.

## Run

Requires Go 1.26 or newer and credentials for the selected model Provider.

`+"```bash"+`
go mod tidy
OPENAI_API_KEY=... go run . "your task"
`+"```"+`

Choose another Provider or model:

`+"```bash"+`
go run . --provider openai --model gpt-5 "your task"
`+"```"+`

## Project layout

`+"```text"+`
.
├── go.mod
├── main.go
└── resources/
    ├── package.json
    ├── agents/%s.md
    └── prompts/system/%s.md
`+"```"+`

The entry Agent is always selected explicitly as `+"`%s`"+`. Its initial tool
allowlist is `+"`%s`"+`.

Edit the profile to change role instructions, tools, optional model routing, or
delegation policy. By default, this Agent and its delegates inherit the model
selected for the Run. Edit the system prompt to change the lower-level runtime
harness. A Meta Agent cannot delegate; a Sub Agent must declare both the
`+"`subagent`"+` tool and exact `+"`delegates`"+` targets.

`+"`resources/package.json`"+` records the entry Agent, dependency closure, and
required/optional host tools. Keep those fields synchronized when adding
dependent Agents.

## License

The generated Agent Package declares `+"`%s`"+`. Review the Ergo Core and Ergo
Agent licenses before distribution.
`, name, role, name, name, name, strings.Join(tools, ", "), license)
	readmeZH := fmt.Sprintf(`# %s

[English](README.md) | [简体中文](README.zh-CN.md)

由 `+"`ergo-agent new`"+` 生成的独立 Ergo %s Agent。

Ergo 借鉴了 Pi 的部分设计并选择性保留兼容，其独立的纯 Go Runtime 提供执行基础。

本应用导入 `+"`github.com/ageniti/ergo-core/runtime`"+`，只嵌入自己的
`+"`resources/`"+`。显式入口选择让生成应用聚焦于该 Agent 的 Profile、Prompt 与 Skill。

## 运行

需要 Go 1.26 或更新版本，以及所选模型 Provider 的凭证。

`+"```bash"+`
go mod tidy
OPENAI_API_KEY=... go run . "你的任务"
`+"```"+`

入口 Agent 始终显式选择为 `+"`%s`"+`，初始工具白名单为 `+"`%s`"+`。
编辑 Profile 可修改角色指令、工具、可选模型路由或委派策略。默认情况下，该 Agent
及其 Delegate 继承本次 Run 选择的模型。编辑系统提示词可修改底层 Runtime 约束。
Meta Agent 不可委派；Sub Agent 必须同时声明 `+"`subagent`"+` 工具和精确的
`+"`delegates`"+`。

`+"`resources/package.json`"+` 记录入口、依赖闭包及宿主必需/可选工具。添加依赖
Agent 时必须同步这些字段。

## 许可证

生成的 Agent Package 声明 `+"`%s`"+`。分发前请检查 Ergo Core 与 Ergo Agent 许可证。
`, name, role, name, strings.Join(tools, ", "), license)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(readme), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "README.zh-CN.md"), []byte(readmeZH), 0644)
}

func writePackageReadmes(root, name, role, license string, tools []string) error {
	readme := fmt.Sprintf(`# %s

[English](README.md) | [简体中文](README.zh-CN.md)

Declarative standalone Ergo %s Agent Package generated by `+"`ergo-agent new --package-only`"+`.
Ergo draws inspiration from Pi and preserves selected compatibility while
providing its own pure-Go Runtime and resource model.
Install this resource directory into an Ergo host and run the explicit entry Agent
`+"`%s`"+`. This resource-only distribution is ready for an existing Runtime.

Required host tools: `+"`%s`"+`.

License: `+"`%s`"+`.
`, name, role, name, strings.Join(tools, ", "), license)
	readmeZH := fmt.Sprintf(`# %s

[English](README.md) | [简体中文](README.zh-CN.md)

由 `+"`ergo-agent new --package-only`"+` 生成的声明式独立 Ergo %s Agent Package。
Ergo 借鉴了 Pi 的部分设计并选择性保留兼容，同时提供自己的纯 Go Runtime 与资源模型。
把本纯资源目录安装到 Ergo 宿主后，以 `+"`%s`"+` 作为显式入口运行。

宿主必需工具：`+"`%s`"+`。

许可证：`+"`%s`"+`。
`, name, role, name, strings.Join(tools, ", "), license)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(readme), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "README.zh-CN.md"), []byte(readmeZH), 0644)
}

func normalizedList(value string) []string {
	seen := map[string]bool{}
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}
