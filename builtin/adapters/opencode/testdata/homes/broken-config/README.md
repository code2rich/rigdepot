# broken-config

`opencode.jsonc` was cut off in the middle (a crash while saving). It does not
parse, so nothing is known about what is in it: the adapter must not plan to
write into it, and the core writes nothing at all to OpenCode in this run, not
even the skill. OpenCode 2.4.1.
