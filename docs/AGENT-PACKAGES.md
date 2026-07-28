# Agent Packages

[English](AGENT-PACKAGES.md) | [简体中文](AGENT-PACKAGES.zh-CN.md)

Every Agent profile can be published as an independent npm, Git, or local
package. An Agent Package has one declared entry Agent and contains the complete
profile dependency closure needed for its delegation policy.

## Build

From a resource root:

```bash
go run ./cmd/agent-package \
  -root . \
  -agent coding-agent \
  -output ./dist/coding-agent \
  -name @acme/coding-agent \
  -version 1.0.0 \
  -license MIT
```

The output path must not already exist. The command never overwrites an
existing package directory.

## Dependency resolution

The builder reads the entry profile's `delegates`:

```yaml
role: sub
tools: read, subagent
delegates: planner, reviewer
```

It packages `planner` and `reviewer`, then recursively resolves their own
delegate policies. Missing or role-incompatible dependencies fail the build.
An omitted allowlist adds no Agent dependencies. `delegates: "*"` expands to
all role-compatible Agents in the source resource root at build time and is
written back as an exact frozen allowlist.

Dependencies are bundled into one self-contained package. The installer does
not fetch transitive npm or Git packages, so installation remains atomic,
offline-capable, and free of cross-package version or removal conflicts.

## Output

```text
dist/coding-agent/
├── README.md
├── README.zh-CN.md
├── LICENSE
├── LICENSE-COMMERCIAL.md
├── NOTICE
├── package.json
├── agents/
│   ├── coding-agent.md
│   ├── planner.md
│   ├── reviewer.md
│   ├── scout.md
│   ├── web-researcher.md
│   └── worker.md
└── prompts/
    └── system/
        └── ...
```

Every included Agent receives a packaged system prompt. The Runtime resolves
that prompt relative to the Agent Package instead of requiring a matching
prompt in the host's embedded defaults. Under the default inherited-license
mode, the builder copies `LICENSE`, `LICENSE-COMMERCIAL.md`, and `NOTICE` from
the resource root into the distributable package. An explicit `-license` flag
overrides that behavior and the generated package manifest's license field.

The generated manifest contains:

```json
{
  "pi": {
    "agents": ["agents/*.md"]
  },
  "ergo": {
    "entryAgent": "coding-agent",
    "agentDependencies": [
      "planner",
      "reviewer",
      "scout",
      "web-researcher",
      "worker"
    ],
    "requiredTools": [
      "bash",
      "edit",
      "find",
      "git_read",
      "grep",
      "ls",
      "read",
      "subagent",
      "todo",
      "web_search",
      "write"
    ],
    "optionalTools": [
      "generate_image",
      "mcp:*"
    ]
  }
}
```

`requiredTools` is the union of non-optional tools declared by the included
profiles. `optionalTools` is the remaining union. A profile marks optional
capabilities explicitly:

```yaml
tools: read, web_search, generate_image
optional-tools: generate_image
```

The installer validates the entry, exact dependency closure, frozen delegation
policy, package-local prompts, and manifest tool sets. Whenever an included
Agent starts, the Runtime fails fast if that profile's required native or
external capability (for example `web_search` on `web-researcher`) is not
supplied by the host. This keeps an entry Agent usable until an unavailable
optional specialist is actually invoked. Optional tools are omitted when
unavailable. Agent dependencies are packaged; Go Tool implementations remain
compile-time host capabilities.

## Install and run

Install the package:

```go
manager := agent.PackageManager{CWD: projectRoot}
_, err := manager.Install(ctx, "./dist/coding-agent", "project", true)
```

Run its entry Agent:

```go
err := runtime.RunWithOptions(ctx, map[string]any{
    "agentId":        "coding-agent",
    "agentScope":     "project",
    "projectTrusted": true,
    "prompt":         "Implement the requested change.",
    "cwd":            projectRoot,
}, agent.RunOptions{}, sink)
```

Installing a package adds its profiles to the host's visible Agent registry.
It does not remove the host's other profiles. An application that must expose
only the packaged Agents should use a custom resource root.

A selected package profile overrides an embedded profile with the same name,
which makes independently versioned built-in-compatible packages runnable.
Loose `.pi/agents` and user profiles do not override embedded roles. Delegated
Agents inherit the caller's scope unless `agentScope` is explicitly overridden.
When multiple packages contain the same dependency name, delegation resolves
inside the caller's own package first so dependency versions cannot cross.
