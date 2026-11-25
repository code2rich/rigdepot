# moved-root

`CLAUDE_CONFIG_DIR` is set to `~/work/claude`. Claude Code 2.1.330 then keeps
everything there, `.claude.json` included (checked: `claude mcp add --scope
user` wrote `$CLAUDE_CONFIG_DIR/.claude.json`, and `claude mcp list` ignored a
`~/.claude.json` in the home directory). The `~/.claude.json` and `~/.claude`
left from before the move stay exactly as they are.
