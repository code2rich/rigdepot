# broken-config

`.claude.json` was cut off in the middle of a write: it ends inside the
`projects` object. Claude Code rewrites this file all the time, so this is the
way it really breaks. Nothing may be written to Claude Code at all, not even
the skill, which does not depend on the file.
