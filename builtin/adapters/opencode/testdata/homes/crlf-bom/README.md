# crlf-bom

`opencode.json` as an editor on Windows saves it: a byte order mark, CRLF line
endings, tabs for indentation, no line break at the end of the file, and an
empty `mcp` object, so that the sync has to create the `servers` level.
OpenCode 2.4.1. Git must not convert this file (see .gitattributes).
