# user-config

A home Claude Code 2.1.330 has been used in for a while. `.claude.json` has the
shape Claude Code writes (two-space indent, no newline at the end; taken from
the file `claude mcp add --scope user` wrote in a temporary home directory) with
the keys a used installation has: start counters, tips, a `null`, a project
with its own local-scope MCP server that happens to be named like ours, and the
person's own user-scope servers, one of them with `${ISSUES_TOKEN}`. Account
details are placeholders. Next to it: `settings.json`, the person's own skill,
slash command and sub-agent, and `.credentials.json` (the file Claude Code
keeps its login in on Linux), which a preview never copies.

The sync adds our entries to the top-level `mcpServers` and nothing else; the
project's server with our name is not ours to touch. The server that needs a
personal secret is written with `Bearer ${CONTRACT_TOKEN}`.
