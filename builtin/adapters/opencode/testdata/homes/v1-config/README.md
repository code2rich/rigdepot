# v1-config

OpenCode 1 (`opencode --version` prints `1.18.8`). Version 1 lists MCP servers
directly under `mcp`, one key per server, and would take a `servers` key for a
server without a type, refusing the whole file. So the sync writes
`mcp.rgd-<code>`. The file is in version 1's shape (the type definitions of
@opencode-ai/sdk 1.18.8): the person's own servers, one with `"enabled": false`,
a field version 2 no longer has.
