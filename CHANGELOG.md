<p align="right"><b>English</b> · <a href="CHANGELOG.zh-CN.md">简体中文</a></p>

# Changelog

One section per version, newest first. The text under a version's heading is that version's release notes on GitHub: `go run ./tools/dev release` publishes it, and a release candidate (`v0.2.0-rc.1`) uses the section of the version it leads to. Before 1.0, a minor version (`0.Y.0`) may include incompatible changes; they are listed under "Needs attention".

## 0.1.0 (not released yet)

The first release of Rigdepot: the command line `rgd`, and the team server `rgd-server` with its web console. Both come from one repository and share one version.

### What is in it

- **`rgd` without a server.** `sync --repo` installs the skills, MCP servers, workflows, agents and knowledge bases of a Git repository into Claude Code, Codex and OpenCode. `sync --plan` shows every file and configuration entry first; nothing in a tool's global configuration changes without asking. `status` (with `--diff`) finds what was changed since, `uninstall` reverts exactly what the local manifest lists, `check` runs the eight intake rules, `doctor` checks the machine. Also `enable` / `disable`, `flow next`, `config` and shell completions.
- **`rgd` with a server.** `login` (a code confirmed in the browser), `sync` from the server's catalog with install reports, personal secrets sealed for each device (`secret`), MCP servers started with their secrets filled in (`mcp run`, and one single-entry server for all of them, `mcp serve`), `feedback`.
- **`rgd-server`.** Setup wizard, local accounts, OIDC and GitLab sign-in, invitations and join requests, products with their members and roles, access tokens, resource repositories pulled on a schedule, the catalog with drafts, intake checks, versions, publishing and rollback, uploads and MCP servers registered on the web, delivery to `rgd`, personal secrets encrypted at rest, feedback, in-app and email notifications, an audit log with a hash chain, HTTPS (certificate files or ACME), backups, `doctor`, `config check`, metrics, and handing out `rgd` from its own address for networks without internet access.
- **Web console**, embedded in `rgd-server`: every page of M1, in English and Chinese, light and dark.
- **Getting it.** Archives of `rgd` for macOS, Linux and Windows on amd64 and arm64, `install.sh` and `install.ps1` (download, verify the SHA-256 checksum, put in place; nothing else), a Homebrew cask (`brew install --cask code2rich/tap/rigdepot`) and a Scoop manifest (`code2rich/scoop-bucket`). The server as archives for Linux, Debian and RPM packages with a systemd unit, and a container image for linux/amd64 and linux/arm64 (`ghcr.io/code2rich/rigdepot-server`), also as a file for `docker load` on networks that cannot reach a registry. Every file is listed in `checksums.txt`. Third-party licenses are in `THIRD_PARTY_NOTICES` in every archive and package.

### Known limitations

- **Not verified on a Windows machine.** `rgd` is cross-compiled for Windows on amd64 and arm64, but neither the program nor `install.ps1` has been run on Windows. Linux was verified in containers only, not on a Linux machine. CI has not run on GitHub Actions yet; this release was checked by running `go run ./tools/dev check` on macOS and `check-linux` in a Linux container.
- **Binaries are not signed or notarized.** On macOS, an `rgd` downloaded with a browser is stopped by Gatekeeper on first run; the install script and Homebrew are not affected (the cask removes the quarantine mark from the one file it installs). On Windows, a browser download may show a SmartScreen warning; Scoop and the install script do not. `checksums.txt` is not signed either: verify downloads against it with SHA-256.
- **SBOMs** are attached only if the release machine had `syft`.
- The single-entry MCP server (`rgd mcp serve`) is verified in Claude Code and Codex, not yet in OpenCode.
- Unattended server setup (`rgd-server setup --from`) is not available; use the setup wizard.
- Some server texts are English only: error details in API responses, audit action names, the names of check rules.

### Upgrading

This is the first version; there is nothing to upgrade from. The local manifest, the repository format (`schema: 1`) and the API (`/api/v1`) start here; later versions read what this one writes.
