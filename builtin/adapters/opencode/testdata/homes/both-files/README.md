# both-files

Both `opencode.json` and `opencode.jsonc` exist. OpenCode 2.4.1 reads and
merges both, and its own commands disagree about which one to change
(`opencode mcp add --global` writes `opencode.json`, a settings change from
the interface writes `opencode.jsonc`). The adapter does not guess: every MCP
server is reported as config_unwritable, and the skill, command and agent are
still delivered. Neither file changes.
