# inline-table

The person wrote all their MCP servers as one inline table:
`mcp_servers = { docs = { … } }`. Codex (codex-cli 0.172.0) reads it, but TOML
does not allow adding a `[mcp_servers.<name>]` table to an inline table, and
the person's text is not rewritten. Every MCP server is reported as
`config_unwritable` and `config.toml` stays as it is; the skill is still
delivered.
