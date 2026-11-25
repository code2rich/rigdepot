# legacy-config

A home that older versions of Claude Code wrote `~/.claude/.config.json` into.
When that file exists, Claude Code 2.1.330 uses it instead of `~/.claude.json`
(checked in a temporary home directory: `claude mcp add --scope user` wrote into
it, and `claude mcp list` listed only its servers, not those of a
`~/.claude.json` next to it). Our servers go into `.config.json`; `.claude.json`
is not touched.
