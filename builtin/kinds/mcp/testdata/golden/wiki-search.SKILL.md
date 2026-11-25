---
name: "acme-wiki-search"
description: "在团队 Wiki 里搜索页面 — 要查团队文档、规范或者会议纪要时 (MCP server Wiki 搜索, called through acme mcp call wiki-search)"
---

# Wiki 搜索

在团队 Wiki 里搜索页面

When: 要查团队文档、规范或者会议纪要时

This MCP server is used on demand: it is not connected to this tool. Call one of its tools by running this command in a shell, with the tool's arguments as a JSON object:

```sh
acme mcp call wiki-search <tool> '<arguments as JSON>'
```

Run `acme mcp call wiki-search` without a tool to see every tool with the arguments it takes. The command prints what the tool returns. It connects to the server and supplies any personal secrets the server needs; never put a secret on the command line.

## Tools

- `search_pages`: 按关键词搜索页面
- `read_page`
