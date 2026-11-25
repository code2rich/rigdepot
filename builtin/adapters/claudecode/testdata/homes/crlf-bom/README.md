# crlf-bom

`.claude.json` and `settings.json` after a hand edit in a Windows editor: CRLF
line endings and a byte order mark. Claude Code 2.1.330 reads such a file (and
rewrites it with LF and no mark the next time it saves its state; checked with
`claude mcp list` in a temporary home directory). The sync keeps the line
endings and the mark; only our entries are added.
