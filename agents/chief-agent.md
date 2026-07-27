---
name: chief-agent
description: General-purpose main agent with full tools and optional delegation
role: main
tools: read, bash, edit, write, grep, find, ls, todo, generate_image, web_search, subagent, mcp:*
optional-tools: generate_image, web_search, mcp:*
delegates: "*"
thinking-level: medium
system-prompt: prompts/system/chief-agent.md
---
