# config-dir-explicit

`CLAUDE_CONFIG_DIR` is set to `~/.claude`, the directory it would be anyway.
Claude Code 2.1.330 still takes the variable as set and keeps its state in
`~/.claude/.claude.json`, not in `~/.claude.json` (checked with `claude mcp add
--scope user` in a temporary home directory). Both files exist here; only the
one inside `~/.claude` gets our entries.
