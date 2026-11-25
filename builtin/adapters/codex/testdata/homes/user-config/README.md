# user-config

A Codex configuration as people really have it. The `[mcp_servers.*]` tables
are in the shape `codex mcp add` of codex-cli 0.172.0 writes them; the rest
was edited by hand: comments, settings the adapter does not know, a trusted
project, a profile after the servers, a stdio server with its own `env` table
and an HTTP server with a bearer token variable. There is a skill of the
person's own in `~/.agents/skills`, an old one in the deprecated
`~/.codex/skills`, an `AGENTS.md` and a credentials file (`auth.json`, with
placeholders). None of it may change: the sync only appends its own tables to
the end of `config.toml`.
