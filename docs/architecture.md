<p align="right"><b>English</b> · <a href="architecture.zh-CN.md">简体中文</a></p>

# Architecture for contributors

This page explains how Rigdepot is put together, for people who want to change it. Read [CONTRIBUTING.md](../CONTRIBUTING.md) first: it gets you to a passing check and walks through the common changes. This page is the map behind them. Every package also documents itself (`go doc ./internal/sync`, `go doc ./ext/adapter` …); when this page and a package's documentation disagree, the package is right, and a pull request that fixes this page is welcome.

## Contents

- [Two programs, one repository](#two-programs-one-repository)
- [Package groups and dependency rules](#package-groups-and-dependency-rules)
- [The command line](#the-command-line)
- [The server](#the-server)
- [The HTTP API](#the-http-api)
- [Extension points](#extension-points)
- [Names and translations](#names-and-translations)
- [Tests](#tests)
- [Generated files](#generated-files)

## Two programs, one repository

| | `rgd` | `rgd-server` |
| --- | --- | --- |
| Runs on | Each person's computer: macOS, Linux, Windows | One Linux machine of the team (macOS for development) |
| Job | Read resources, work out what each AI tool needs, show it, write it after consent, record it, undo it | Who may use what, the catalog and its versions, delivery, audit, notifications, the web console |
| Entry point | [cmd/rgd/main.go](../cmd/rgd/main.go) → `internal/cli` | [cmd/rgd-server/main.go](../cmd/rgd-server/main.go) → `internal/server` |
| Carries | No database driver, no sign-in protocol, no web assets | No code that edits other tools' configuration |

Both are built from one Go module, `github.com/code2rich/rigdepot`, and released together under one version. Both are pure Go (`CGO_ENABLED=0`): SQLite is `modernc.org/sqlite`, so every target cross-compiles from any machine. `rgd` works without the server; with one, it signs in and takes its resources from the server instead of a repository.

The entry points are a few lines: they import the registries of built-in items (`builtin/all`, plus `builtin/all/server` and `modules/all` for the server) and call the core. That is how the core stays free of any particular tool, kind or rule.

## Package groups and dependency rules

```
ext/            public interfaces and shared types; standard library only
builtin/        implementations of ext/ (adapters, kinds, checks, identity, notifiers)
modules/        optional modules, compiled in or out with build tags
internal/       the core
  shared          brand, buildinfo, i18n, logging, repoformat, bundle, check, gitrepo, testenv
  command line    cli, cliout, localconfig, home, manifest, configedit, executor, sandbox,
                  desired, sync, flow, apiclient, keyring, device, localsecrets, mcprun, doctor
  server          internal/server and everything below it
```

The groups are data in [tools/dev/layout.go](../tools/dev/layout.go), and `go run ./tools/dev check` enforces the rules R1–R8 listed in [CONTRIBUTING.md](../CONTRIBUTING.md#three-rules-to-remember) against them. The reasons behind the rules:

- **`builtin/` sees only `ext/`** (R2), so that a built-in item is written exactly the way an outside contributor would write one, and a missing capability shows up as a gap in `ext/` instead of a shortcut into the core.
- **The core does not know the built-in items** (R3). `go build -tags rgd_core_only` builds and tests the core with none of them; `check` runs it.
- **The two programs do not share their halves** (R5). `rgd` stays small and has nothing to do with databases; `rgd-server` cannot edit a tool's configuration even by mistake. Neither links the `testing` package.
- **Only the executor writes into the user's home directory** (R7). Backups, atomic writes, the rule that a file that does not parse is not written, verification and the manifest exist once, and are reviewed once.

A new internal package must be added to `layout.go`; `check` fails until it is.

## The command line

### From a command to the engine

`internal/cli` builds the command tree with cobra. Before it does, it reads the global options loosely to know the language, so that even an error about a broken configuration is in the right language. Settings are resolved by `internal/localconfig` (flags > `RGD_*` variables > `~/.rigdepot/config.toml` > defaults; `rgd config list` shows where each value came from). Output for people and for `--json`, colours, width and exit codes are `internal/cliout`'s; every command's `--json` is one document with a `schema` field such as `rgd.check/v1`.

### The local data directory

Everything `rgd` keeps for itself is in `~/.rigdepot` (`--home` or `RGD_HOME` move it), laid out by `internal/home`:

| Path | What |
| --- | --- |
| `config.toml` | Local configuration |
| `manifest.json`, `manifest.journal`, `lock` | The manifest of everything written, its intent log, the cross-process lock |
| `state/` | `consent.json` (which global config files the person agreed to change), `desired.json` (the last desired state from the server) |
| `cache/` | `repos/` (bare clones of resource repositories), `bundles/` (resource bundles from the server) |
| `backup/` | Copies of other tools' config files from before the first change |
| `kept/` | The person's own versions of files that `--overwrite` replaced |
| `secrets/` | `vault.json`, the personal secrets sealed for this device; the device key and token too when there is no system keychain |
| `mcp/` | One file per MCP server synced here, saying how to reach it, with secrets as references (for `rgd mcp`) |
| `flows/`, `knowledge/` | Workflow runs; knowledge bases pulled with the person's own Git access |
| `tmp/`, `logs/` | Working space; logs |

### Where the resources come from

`internal/desired` turns either source into the same `desired.State`:

- **A repository** (`--repo`, or `repo.url`): `internal/gitrepo` calls the system `git` (a bare clone and `git archive`, no work tree, line-ending conversion switched off), so the person's own credentials and SSH setup work and nothing is reimplemented. `internal/repoformat` reads `rigdepot.yaml` and every resource, validates them against the JSON Schemas in `schema/` and the kinds' own schemas, and applies the path safety rules (`internal/bundle`).
- **The server** (after `rgd login`): `internal/apiclient` fetches the desired state and the bundles it names, checks each bundle against its SHA-256, and keeps the state in `state/desired.json`, so that a sync while the server cannot be reached uses the last state and removes nothing.

Products, scopes (`org`, a product, `me`) and policies (required, recommended, optional) decide what is wanted; `rgd enable` and `rgd disable` record the person's choices.

### The sync engine

`internal/sync` runs one sync in the same steps every time; its package documentation has the full table of cases.

```
desired state ──┐
                ├─ Plan       detect each tool (adapters, through an Env that only reads);
manifest ───────┘             render each resource for it (the kind's forms, in order: the first
                              the tool can take, later ones are downgrades); let the adapter
                              plan the operations; compare each target three ways: what the
                              manifest recorded, what is on disk, what the plan wants
                 Preview      the same operations, executed in a sandbox copy of the touched
                              files (internal/sandbox), shown as a diff
                 Consent      changes to a tool's global configuration need the person's yes,
                              once per file (state/consent.json); --skip-global, --yes-global
                 Apply        the same operations on the real home directory, by the executor,
                              under the lock; each one recorded in the manifest
                 Report       to the server, when there is one: what was installed, no paths
```

The rules the engine never bends:

- **Unknown is not empty.** A tool, configuration file or target that cannot be read is left out of the run; nothing of it is written or undone.
- **Only what the manifest lists is ours.** A name that is taken is left alone and reported. A file the person changed is not overwritten unless they say `--overwrite`, and then their version goes to `kept/` first.
- **Undo restores bytes.** Uninstall undoes the manifest's entries newest first, and a config key that changes value is undone and set again, so that uninstalling returns every file byte for byte.
- Everything written carries the `rgd-` prefix (from `brand.json`), so it is recognisable in every tool.

### The executor and config edits

`internal/executor` is the only code that writes outside the local data directory. It knows four operations (`ext/adapter`'s `Op`): write a tree, write a file, set a config key, run the tool's own command. Before changing a file it backs it up once, writes atomically, verifies, and records an intent in `manifest.journal`, so that a crash halfway is settled at the next run (`rgd doctor` shows what is left).

Config keys are set by `internal/configedit`, which edits JSON, JSONC and TOML in place: comments, order and formatting of everything else stay as they were, and every change is a recorded byte range that can be put back. A file that does not parse is not touched.

### Other parts of the command line

- `internal/check` is the check engine for `rgd check`: it runs the registered rules (`ext/check`) over a repository. The server runs the same engine on import and upload, so the two always agree.
- `internal/doctor` holds the self-check items of `rgd doctor`, one file each.
- `internal/device` and `internal/keyring`: the device code sign-in, the device key pair and token, in the system keychain or in files only the user can read.
- `internal/flow`: `rgd flow next` hands out a workflow's steps one at a time and checks each before the next.
- `internal/localsecrets` keeps the personal secrets as the server sealed them for this device (HPKE to the device key) and opens one only at the moment it is used.
- `internal/mcprun` is `rgd mcp`: `run` starts one MCP server with the secrets in its environment or, for an HTTP server, bridges standard input and output to it with the secrets in the headers (nothing listens on a port); `serve` is the single entry, one MCP server offering the tools of all the others; `call` runs one tool, for servers delivered as a skill. A missing secret becomes a tool answer that says which one to set, not a server that fails to start. AI tools therefore only ever see a command, never a secret.

## The server

### Start-up

`rgd-server serve` (`internal/server`) loads the startup configuration (`internal/server/config`: flags > `RGD_*` > TOML file > defaults, see [deploy.md](deploy.md#configuration)), refuses to serve plain HTTP to the network, opens the database (`dbx`: SQLite or PostgreSQL behind one interface, the SQL dialect differences in one place), snapshots a SQLite database before running pending migrations (`migrations`, append-only, each with a SQLite and a PostgreSQL section), loads the master key (`masterkey`), builds the domain services, starts the event bus and the background jobs, and serves HTTP. `SIGINT` and `SIGTERM` stop it gracefully; `SIGHUP` reloads certificate files.

### Layers

```
HTTP        app.go, http.go: health, readiness, security headers, request ids, the web console (webui)
  │         httpapi: the strict server generated from api/openapi.yaml
  │         api.go + api_<domain>.go, httpapi/accessapi, httpapi/authnapi: one adapter per domain
  ▼
domains     access  authn  catalog  delivery  secrets  feedback  settings  audit  events  notify
  ▼
foundation  config  dbx  migrations  masterkey  jobs
```

The HTTP layer translates; the domain packages decide; the foundation stores. Between domain packages only these imports are allowed (rule R6):

| Package | May import | Does |
| --- | --- | --- |
| `access` | `audit`, `events` | Users, products, members, the three roles, invitations and join requests, and every permission decision |
| `authn` | `access`, `audit`, `events` | Local accounts, sessions, identity providers, access tokens, the device code flow |
| `catalog` | `access`, `audit`, `events` | Resources, drafts, versions, publishing, rollback, references, check results |
| `delivery` | `catalog`, `access`, `secrets`, `audit` | The desired state of each person, bundles, enable and disable, install reports |
| `settings` | `audit` | Run-time settings and the setup wizard's state |
| `notify` | `access`, `events` | In-app notifications and the mail outbox |
| `audit`, `events` | nothing | The hash-chained audit log; the in-process event bus |
| `secrets` | `access`, `audit` | Personal secret definitions and values, envelope-encrypted, sealed for each device on delivery |
| `feedback` | `access`, `catalog`, `audit`, `events` | The feedback queue, its states and ratings |

Nobody imports `notify`: a domain publishes an event (`catalog.published`, `access.join_requested` …) and the notification rules subscribe to it. Everything the domains share crosses these lines and no others.

Cross-cutting rules:

- **Permissions** are decided in one place, `access`, from a table of actions. An action missing from the table is refused, and a test makes sure every action has a row.
- **Audit**: every change of state is recorded in `audit`, an append-only log in which each entry carries the hash of the one before; `rgd-server audit verify` and a daily job check the chain.
- **Secrets at rest** are sealed with envelope encryption under the master key (`masterkey`); the key is never in the database or in a backup. `rgd-server keys rotate` re-wraps the data keys.
- **Background work** runs as jobs (`jobs`) with timeouts and leases, so that several instances on one PostgreSQL database do not run the same job twice: the outbox, the daily audit check, the daily backup and others.
- **Modules** (`ext/module`, `modhost`) are large optional features: compiled in through `modules/all/maybe_<id>.go` and a build tag, turned on in the settings, and given routes under `/api/v1/x/`, migrations, jobs and events. There are none yet.

### The web console

`web/` is a Preact and TypeScript application built with Vite into static files, embedded into `rgd-server` with `go build -tags webdist` (without the tag a one-page stand-in is embedded, so the Go side never needs Node). `internal/server/webui` serves it with a strict content security policy and fills the base path, language and nonce into `index.html` per request, so the console works under a sub-path. Its API types come from the same contract as the server's. [web/README.md](../web/README.md) is its guide.

## The HTTP API

[api/openapi.yaml](../api/openapi.yaml) (OpenAPI 3.0) is the contract between the server, `rgd` and the console, and the source of three generated pieces: the server interface (`internal/server/httpapi`), the Go client (`internal/apiclient`) and the console's TypeScript types (`web/src/api/schema.gen.ts`). Change the contract first, then `go run ./tools/dev gen`; the compiler then shows every place that has to follow.

- Every path is under `/api/v1/`. Errors are problem documents (`application/problem+json`) with a stable `code` such as `resource.check_blocked`.
- Three ways to authenticate: a browser session cookie (cross-site requests are refused by `Sec-Fetch-Site` and `Origin`), a device token (`rgd`), an access token with a scope (scripts).
- Editable objects carry an `ETag`; a write needs `If-Match` (`412` when it changed, `428` when it is missing). Unbounded lists are paged with `limit` and `cursor`.
- Operations the server does not implement yet answer `501` (`api_fallback.go`), so the contract can run ahead of the code.
- Contract tests check the server's real answers against the schema.

## Extension points

Everything under `ext/` is public API, versioned v0 until 1.0 (breaking changes are listed in the release notes). Each package's documentation is its reference.

| Package | What it extends | Built in | Registry |
| --- | --- | --- | --- |
| `ext/adapter` | An AI coding tool: detection, capabilities, where things go (a declarative profile plus code where needed) | `claudecode`, `codex`, `opencode` | `builtin/all/adapters.go` |
| `ext/kind` | A resource kind: layout in a repository, schema, parsing, forms in order of preference | `skill`, `mcp`, `workflow`, `agent`, `knowledge` | `builtin/all/kinds.go` |
| `ext/check` | A check rule: a pure function over one resource's files | 8 rules | `builtin/all/checks.go` |
| `ext/identity` | A sign-in method for the server | `oidc`, `gitlab` | `builtin/all/server/identity.go` |
| `ext/notify` | A notification channel | `email` | `builtin/all/server/notifiers.go` |
| `ext/module` | An optional module of the server | none yet | `modules/all/` |
| `ext/secretbackend` | What wraps the data keys of personal secrets (Vault, a cloud KMS …) | the master key, in the core (`internal/server/secrets`) | — |
| `ext/source` | Where the server gets resources from | not settled yet; the server pulls Git repositories itself | — |
| `ext/resource` | Shared types: references, metadata, bundles | — | — |

Each extensible package has a test suite in a sub-package whose name ends in `test` (`adaptertest`, `kindtest`, `checktest`); a built-in item's tests are a few lines that hand it to the suite.

## Names and translations

**Names.** The product name, the command names, the local directory, the prefix of everything written into other tools and the prefix of environment variables are defined once, in [brand.json](../brand.json). `go run ./tools/dev gen` turns them into `internal/brand` (Go) and `web/src/brand.gen.ts`, and `check` rejects string literals that spell them out. A fork renames the product by editing one file.

**Translations.** Every text shown to people is in `locales/<language>/<domain>.json`: flat JSON in the i18next conventions, `en` and `zh-CN` required and complete. Go code refers to keys through generated constants (`internal/i18n/msg`); built-in items, which cannot import `internal/`, return keys as strings, and the console uses `t('…')` with keys written out. `check` fails on a key that one required language lacks, on placeholders that differ, and on a key nothing uses. Command names, option names, `--json` fields, error codes and logs are not translated. [locales/README.md](../locales/README.md) has the conventions and the terms.

## Tests

| Layer | Where | What it proves |
| --- | --- | --- |
| Unit and package tests | next to the code | Everything; run by `check` with a temporary home directory and no network |
| Extension suites | `ext/*/…test` | That an adapter, kind or rule keeps the contract (golden files for adapters, hit and miss samples for rules) |
| Examples | `examples/` | The sample repository passes every rule; each broken one is hit by exactly its rule; `rgd check` output in both languages |
| Storage | `internal/server/dbx/dbxtest` | Each storage test on SQLite, and on PostgreSQL when `RGD_TEST_POSTGRES_DSN` names a local database |
| Contract | `internal/server` | The server's answers match `api/openapi.yaml` |
| End to end | `test/e2e` | The real `rgd` and `rgd-server` programs: sync, status, uninstall in temporary homes; sign-in and sync from a running server; nothing removed when it stops; a personal secret from the browser to an MCP server started by `rgd mcp` |
| Real tools (opt-in) | `test/realtools` | The installed Claude Code, Codex and OpenCode read what `rgd` wrote, in a temporary home: `RGD_TEST_REAL_TOOLS=claude,codex,opencode` |
| Web console | `web/` | Format, lint, type check, unit tests (Vitest) and a build; part of `check` when Node and pnpm are installed |

Every test package starts with `TestMain` handing over to `internal/testenv` (or a suite's `Main`, which does the same): it gives the test binary a temporary home directory, removes variables that would point at real configuration, switches the system keychain off, puts failing stand-ins in place of external commands such as the real AI tools, and blocks the network beyond loopback. `check` refuses a test package without it.

## Generated files

Generated files are committed, so that a clone builds without running generators, and `check` fails when one is stale. `go run ./tools/dev gen` rewrites them all:

| File | From |
| --- | --- |
| `internal/brand/brand_gen.go`, `web/src/brand.gen.ts` | `brand.json` |
| `internal/i18n/msg/keys_gen.go`, `web/src/i18n/keys.gen.ts` | `locales/` |
| `schema/repo/v1/kinds/*.schema.json` | each kind's `schema.json` |
| `internal/server/httpapi/api.gen.go`, `internal/apiclient/api.gen.go` | `api/openapi.yaml` |
| `web/src/api/schema.gen.ts` | `api/openapi.yaml` (needs Node and the console's dependencies; reported as not run without them) |
| `web/src/styles/tokens.gen.css` | `web/tokens/tokens.json` |
