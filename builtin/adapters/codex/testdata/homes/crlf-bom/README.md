# crlf-bom

`config.toml` as an editor on Windows saves it: a byte order mark, CRLF line
endings and no line break at the end of the file. The person wrote
`[mcp_servers]` with a header and one server inside it as an inline table,
then another server with a header of its own. Codex (codex-cli 0.172.0) reads
all of this. Our tables are appended after the last line with CRLF, and
nothing before them changes. Git must not convert these files (see
.gitattributes).
