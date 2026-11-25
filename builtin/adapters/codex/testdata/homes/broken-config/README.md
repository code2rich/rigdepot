# broken-config

`config.toml` was cut off in the middle of a string (a crash while saving). It
does not parse, so nothing is known about what is in it: the adapter plans no
key in it, and the core writes nothing at all to Codex in this run, not even
the skill.
