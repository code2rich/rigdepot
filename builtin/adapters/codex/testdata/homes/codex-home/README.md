# codex-home

`CODEX_HOME` points to `~/work/codex-home`, so `config.toml` is there. A stale
`~/.codex/config.toml` from before the move is left exactly as it is. Skills
still go to `~/.agents/skills`: Codex takes that from the home directory, not
from `CODEX_HOME`.
