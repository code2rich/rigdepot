# unknown-version

OpenCode is installed, but what it prints for `--version` holds no version
(a development build), and there is no configuration file to tell the version
by. The servers go directly under `mcp`: version 1 reads only that, and
version 2 (checked on 2.4.1) still reads it as legacy entries, while version 1
would refuse a file with `mcp.servers`.
