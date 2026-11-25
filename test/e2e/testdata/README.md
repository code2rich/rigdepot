# End-to-end fixtures

`home/` is the home directory of a person who uses all three AI coding tools
and has configured each of them by hand. It is put together from the
`user-config` fixtures of the adapters' contract tests (see the README next to
each of them for where the files come from and which tool version wrote them):

- Claude Code 2.1.330 (`builtin/adapters/claudecode/testdata/homes/user-config`):
  strict JSON `.claude.json` without a final newline, the person's own
  user-scope MCP servers, a project with its own server named like one of
  ours, `settings.json`, own skill, slash command and sub-agent, credentials.
- Codex 0.172.0 (`builtin/adapters/codex/testdata/homes/user-config`), with
  `config.toml` saved the way an editor on Windows saves it: a byte order mark
  and CRLF line endings. Comments, settings the adapter does not know, the
  person's own `[mcp_servers.*]` tables and a profile after them; an own skill
  named `release-notes` in `~/.agents/skills` (the resource repository has a
  resource with the same code), an old one in `~/.codex/skills`.
- OpenCode 2.4.1 (`builtin/adapters/opencode/testdata/homes/user-config`):
  `opencode.jsonc` with comments and trailing commas, the person's own servers
  under `mcp.servers`, own skill, command and agent, credentials in the data
  directory.

Account details are placeholders. Git must not convert these files (see
`.gitattributes`): the CRLF and the byte order mark are the point.

The tests copy `home/` into a temporary directory, sync from a copy of
`examples/resources` made into a local git repository, and uninstall. After the
uninstall the copy must be byte for byte what it was before.
