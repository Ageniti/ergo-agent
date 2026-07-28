# Skills

[English](README.md) | [简体中文](README.zh-CN.md)

Skills are on-demand instruction bundles discovered by the Go resource loader.
Each Skill lives in its own directory and has a complete `SKILL.md`:

```text
skills/
└── example-skill/
    ├── SKILL.md
    ├── scripts/
    └── references/
```

Minimum frontmatter:

```yaml
---
name: example-skill
description: When and why the Agent should use this skill
---
```

The Runtime first injects compact discovery metadata. When a Skill is selected,
the Agent reads the full `SKILL.md` before following it. Referenced scripts,
assets, and documentation remain relative to the Skill directory.

Discovery scopes:

- bundled Skills in the complete `ergo-agent` distribution;
- user Skills under `~/.pi/agent/skills/` and `~/.agents/skills/`;
- trusted project Skills under `.pi/skills/` and `.agents/skills/`;
- installed packages exported through `pi.skills`.

`ergo-core` contains the discovery and execution capability but no bundled
Skills. A standalone Agent includes only the Skills placed in its own resource
tree or installed by its host.

JavaScript is not required by the Skill format. If a Skill explicitly invokes
a Node helper, the deployment image must provide Node and install the pinned
dependency; the default pure-Go Runtime does not execute package JavaScript
automatically.
