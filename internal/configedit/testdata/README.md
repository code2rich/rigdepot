# configedit fixtures

Every file here is bytes, not text (`.gitattributes` marks `testdata/**` as `-text`): the CRLF
and byte-order-mark samples must reach the tests exactly as they are.

## json/

Modelled on what the tools write; account data replaced by placeholders.

| File | Models |
| --- | --- |
| `claude-user.json` | `~/.claude.json` of Claude Code 2.1: state keys, a server of the person's own, per-project settings |
| `claude-user-crlf-bom.json` | the same with CRLF line endings and a UTF-8 byte order mark |
| `claude-no-mcp.json` | a fresh install: no `mcpServers` yet (the parent has to be created) |
| `claude-empty-mcp.json` | `mcpServers` is `{}` |
| `claude-no-final-newline.json` | no line ending after the last `}` |
| `claude-oneline.json`, `claude-oneline-spaced.json` | minified, and minified with spaces |
| `claude-mcp-not-object.json` | `mcpServers` is an array: cannot be written |
| `claude-duplicate-key.json` | `mcpServers` appears twice: ambiguous, cannot be written |
| `claude-truncated.json` | cut off in the middle: does not parse |
| `claude-with-comment.json` | a comment: not strict JSON, fine as JSONC |
| `empty.json` | a file that does not exist yet |
| `opencode-user.jsonc` | `opencode.jsonc` of OpenCode 2.2: comments of both kinds, trailing commas everywhere, `mcp.servers` with the person's servers |
| `opencode-user-crlf-bom.jsonc` | the same with CRLF and a byte order mark |
| `opencode-no-servers.jsonc` | no `mcp` at all, a comment after the last member |
| `opencode-empty-with-comment.jsonc` | `mcp.servers` is empty except for a comment |
| `opencode-block-comment.jsonc` | a block comment after an empty object |
| `opencode-tabs.jsonc` | indented with tabs |
| `opencode-broken.jsonc` | an unterminated block comment: does not parse |

## golden/

What `Set` makes of each fixture, byte for byte. `go test ./internal/configedit -update` rewrites
them; review the diff.
