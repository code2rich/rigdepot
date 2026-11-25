---
name: "acme-jira"
description: "读写需求、任务和缺陷，查询迭代计划 (MCP server Jira, called through acme mcp call jira)"
---

# Jira

读写需求、任务和缺陷，查询迭代计划

This tool cannot connect to this MCP server directly. Call one of its tools by running this command in a shell, with the tool's arguments as a JSON object:

```sh
acme mcp call jira <tool> '<arguments as JSON>'
```

Run `acme mcp call jira` without a tool to see every tool with the arguments it takes. The command prints what the tool returns. It connects to the server and supplies any personal secrets the server needs; never put a secret on the command line.

## Tools

- `search_issues`: 按 JQL 查询事项
- `create_issue`: 新建事项 (changes data: ask the person before calling it)
