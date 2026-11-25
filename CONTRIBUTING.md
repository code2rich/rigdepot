<p align="right"><b>English</b> · <a href="CONTRIBUTING.zh-CN.md">简体中文</a></p>

# Contributing to Rigdepot

Thank you for considering a contribution. This page takes about ten minutes to read and gets you from a clone to a passing check.

Ideas and questions are contributions too, and you do not have to write code: open an issue, or talk to us on [Discord](https://discord.gg/feGwwqs7x). Issues and pull requests are welcome in English or Chinese.

> **Where the project is.** The design is complete and M0, the command-line core, is in development. The repository skeleton, the development entry point and the checks described here work today. The extension interfaces for tools, resource kinds and check rules are defined in `ext/` (read them with `go doc ./ext/adapter`, `./ext/kind` and `./ext/check`), and so are the JSON Schemas of the file formats in `schema/`. Nothing implements them yet, so the five kinds of contribution described [below](#the-five-most-common-contributions) are the shape things will have, not something you can copy from yet. Each section says what exists.

## What this is

Rigdepot keeps a team's skills, MCP servers and workflows in one Git repository and installs them into the AI coding tools on each person's computer. The repository holds two Go programs:

- **`rgd`**, the command-line program on each person's computer. It reads resources, works out what to write into each tool, shows you, writes after you agree, records every write and can undo all of it.
- **`rgd-server`**, the server a team hosts itself. It manages who may use what, releases, personal secrets, audit and feedback. The web console is embedded in it.

`rgd` works without the server, and most contributions only touch the `rgd` half.

## Ten minutes to a passing check

You need Go (the version in `go.mod`) and git. macOS, Linux and Windows all work; Windows needs neither WSL, bash nor make.

```sh
git clone https://github.com/code2rich/rigdepot.git
cd rigdepot
go run ./tools/dev deps      # download dependencies; the only step that needs the network
go run ./tools/dev check     # every check, ending in a result table
```

`go run ./tools/dev <task>` is the single entry point for everything:

| Task | What it does |
| --- | --- |
| `deps` | Downloads the modules of the main module and of the pinned development tools. |
| `check` | Every pre-merge check: format, `go vet`, `staticcheck`, generated files, structure rules, tests, a build without optional modules, and a cross-compile for six targets. Ends with a table of what passed, failed and did not run. |
| `test` | Runs tests in the same isolated environment as `check`. Takes packages and `go test` flags: `go run ./tools/dev test ./internal/cli -run TestVersion`. |
| `gen` | Regenerates generated files (today: the name constants from `brand.json`). |
| `check-linux` | Runs `check` in a local Linux container, so tests are really executed on Linux. Needs Docker. |
| `release-snapshot` | Builds all release artifacts into `dist/` without publishing anything. |

The `Makefile` only holds aliases for these. CI runs the same commands. No check lives in the Makefile or in a workflow file, because a check that lives there cannot run on your machine.

A `dev` task that starts a sandboxed local environment (a sample resource repository and a fake home directory with realistic tool configuration) arrives with M0. Until then:

> **Never run an unreleased `rgd` against your real home directory.** It exists to edit the configuration of your AI tools. Point `HOME` (and `USERPROFILE` on Windows) at a temporary directory first.

## A map of the repository

```
cmd/         entry points of the two programs, a few lines each
ext/         the public extension interfaces; what you write implements these
builtin/     built-in implementations, one directory each      <- most contributions go here
  adapters/    one per AI coding tool
  kinds/       one per resource kind
  checks/      one per check rule
  identity/    one per sign-in method
  notifiers/   one per notification channel
  all/         the registry: a new item is one more line here (server-only items: all/server/)
modules/     optional modules (large features that are off by default)
internal/    the core: sync engine, executor, manifest, the server's domains
schema/      JSON Schema of the resource repository, the local manifest and adapter profiles
tools/dev/   the one development command
scripts/     install.sh, install.ps1
deploy/      Compose file, systemd unit
brand.json   every name the code uses, in one file
```

Still to come as M0 and M1 progress: `locales/` (translations, one directory per language; contributions that need no code will go there), `api/` (the OpenAPI contract), `web/` sources (the web console) and `examples/` (a sample resource repository).

Every package has a `doc.go` with one sentence saying what it is for. If you are looking for where something belongs, this prints them all:

```sh
go list -f '{{.ImportPath}}: {{.Doc}}' ./...
```

### Three rules to remember

`check` enforces these and tells you which rule a change breaks and which two packages are involved.

1. **Code in `builtin/` may only use `ext/` and the standard library**, never `internal/`. Built-in implementations are the examples of how to write an extension. If something you need is missing from `ext/`, the interface is incomplete: open an issue and say what you need.
2. **Only the executor writes the user's files.** An adapter says what should be written and where; it never writes itself.
3. **No `util`, `common` or `helpers` packages.** Code goes into the package of the domain it belongs to.

The full list:

| Rule | In short |
| --- | --- |
| R1 | `ext/**` imports only the standard library and `ext/**`. Two exceptions, both separate sub-packages: the test suites whose name ends in `test`, and `ext/adapter/profileyaml`. |
| R2 | `builtin/**` imports only the standard library, `ext/**` and the allowed third-party modules. No `internal/**`, and no other built-in item; shared code goes in `builtin/<class>/internal/`. |
| R3 | `internal/**` does not import `builtin/**` or `modules/**`. The core compiles without any built-in item or module. |
| R4 | `modules/<id>/**` imports only the standard library, `ext/**` and its own directory. |
| R5 | The command-line side does not import `internal/server/**` and the server does not import the command-line side. `rgd` has no database driver; `rgd-server` has no code that edits other tools' configuration. |
| R6 | Inside the server: HTTP layer → domain packages → storage, and between domain packages only in the documented direction. |
| R7 | Only `internal/executor` writes files in the user's home directory outside Rigdepot's own data directory. |
| R8 | No `util`, `common`, `helpers`, `misc` or `base` packages. |

A new package under `internal/` has to be added to `tools/dev/layout.go`, which is the layout as data.

### How a resource reaches a tool

Understand this chain and you understand most of `rgd`:

```
a directory in the resource repository      skills/unit-testing/
  │ the resource kind parses it and picks a form the tool supports
  ▼
artifact                                     a skill directory named rgd-unit-testing
  │ the adapter knows where this tool keeps skills
  ▼
operation                                    "write this tree to ~/.claude/skills/rgd-unit-testing"
  │ the executor runs it in a temporary copy first to show you the diff,
  │ then for real after you agree
  ▼
manifest entry                               one line in the local manifest; uninstall and drift
                                             detection look at nothing else
```

There are only four kinds of artifact (skill directory, MCP entry, slash command, sub-agent) and four kinds of operation (write a tree, write a file, set a config key, run the tool's own command). The preview and the real write are the same operations with a different root directory, so they cannot disagree.

## The five most common contributions

Each of them is the same move: **add files in one directory, add one line to the registry.**

> The interfaces for the first three are defined: `ext/adapter`, `ext/kind` and `ext/check`. Their test suites (`adaptertest`, `kindtest`, `checktest`) have their entry points but no assertions yet, and the interface for notification channels comes with M1. Until the first built-in item of a kind has landed there is nothing to copy from, so please open an issue before starting one of these; we will tell you whether the ground is ready.

### A new AI coding tool (an adapter)

1. **Find out where the tool really keeps things.** Do not guess from articles. Run the tool's own commands in a temporary home directory and look at what it wrote:

   ```sh
   tmp="$(mktemp -d)"
   HOME="$tmp" USERPROFILE="$tmp" XDG_CONFIG_HOME="$tmp/.config" <tool> mcp add probe -- echo hi
   find "$tmp" -type f
   ```

2. Create `builtin/adapters/<tool>/` with a profile (`profile.yaml`): how to detect the tool, where its configuration root is, where skills and MCP entries go and in which format, and which files are credentials. The profile must carry a `verified` record: tool version, operating system, date and how you verified it. A profile without one writes nothing.
3. Write the adapter. When the tool is shaped like the others, it is a few lines that load the profile. Write code only for what the profile cannot express.
4. Add fixtures under `testdata/homes/`: several "user home directories" made from files the real tool generated, with account details removed. At least: nothing configured, the user's own configuration (with comments, unknown keys and an entry of the same name), a broken config file, and a target location that is already taken.
5. Run the adapter contract test suite against them. Generate the expected results once, then **read every file**: is the user's existing content unchanged to the byte, and is what we added in the format the tool accepts?
6. Register it: one line in `builtin/all/adapters.go`.
7. `go run ./tools/dev check`. If the tool is installed on your machine, also run the real-tool verification and paste its output into the pull request.

In the pull request, state the tool version you verified against, how, which forms are supported natively and which are downgraded.

### A new check rule

A rule is a pure function: it receives all files of one resource and returns what it found. It cannot read anything else from disk and cannot reach the network.

1. Create `builtin/checks/<rule>/` with the rule.
2. Add samples: at least one that must be hit and one that **looks similar but must not be hit** (`testdata/hit/`, `testdata/miss/`).
3. Run the rule test suite against them.
4. Add the texts, the rule's name and the description of the problem, in both English and Chinese.
5. Register it: one line in `builtin/all/checks.go`.
6. `go run ./tools/dev check`.

Two things to be careful with. Whether a rule blocks a release ("must fix") or only advises is a product decision: new rules start as advisory, and the pull request says which level you suggest and why. And when a rule hits something sensitive such as a credential, the finding must not contain the matched text.

### A new resource kind

Ask first: can it be rendered as one of the four existing artifacts? If yes, you only touch one directory. If it needs a new kind of artifact, that affects every adapter and is no longer "adding a kind"; open an issue to discuss it.

1. Create `builtin/kinds/<kind>/` with its JSON Schema and the code that parses it and renders it into artifacts, listing the forms it can take in order of preference.
2. Add valid and invalid samples and run the kind test suite.
3. Add the kind's display name and a one-line description in both languages.
4. Register it: one line in `builtin/all/kinds.go`.
5. `go run ./tools/dev gen`, then `check`.

A new kind is also a product decision, so open an issue before the pull request.

### A new language

No code needed.

1. Copy `locales/en/` to `locales/<tag>/`, using a BCP 47 tag such as `ja`, `de` or `zh-TW`.
2. Translate the **values**. Leave the keys and the `{{…}}` placeholders alone.
3. Fill in the language's own name, its English name and the maintainer.
4. `go run ./tools/dev check` reports mismatched placeholders and broken JSON, and how complete the translation is.

An incomplete translation is fine; what is missing shows in English. Languages are discovered from the directory, there is nothing to register. Some words stay untranslated everywhere: Skill, MCP, Agent.

### A new notification channel

A channel does one thing: send a message that is already rendered. Who receives it, in which language and whether a failure is retried is not its business.

1. Create `builtin/notifiers/<channel>/` with the channel and the schema of its configuration.
2. Test it against a local fake server (`httptest`): what is sent, and that an error reply becomes an error.
3. Add the channel's name in both languages.
4. Register it: one line in `builtin/all/server/notifiers.go`. Channels and sign-in methods are used by the server only, so their registry is `builtin/all/server`.

A channel that is compiled in does nothing until whoever deploys the server fills in its configuration.

**A notification channel sends data out by nature**, so a maintainer always reviews these pull requests in person. Hold to three things: send only to the address the deployer configured, never to one taken from the message; send only the fields of the message; keep configured tokens out of error messages.

## What gets stopped in review

This project edits other tools' configuration on people's machines, so some rules are stricter than usual.

| Doing this | Why not | Do this instead |
| --- | --- | --- |
| A test reads or writes the real home directory (`~/.claude`, `~/.codex`, …) | It can break the configuration of whoever runs it | Every test package starts with `func TestMain(m *testing.M) { testenv.Main(m) }`; `check` fails without it |
| A test uses the network | Unstable, and it may send something out | A local fake server, with replies taken from saved real ones |
| Treating a config file that failed to parse as empty | It overwrites the user's configuration | Failed to parse means unknown means do not write; report why |
| Overwriting or deleting something that is not in the manifest | It belongs to the user | Touch only what the manifest lists; if a name is taken, skip and say so |
| Guessing another tool's config format from field names | A wrong type can stop the tool from starting | Read its documentation or source, verify in a temporary home directory, record it |
| A fix without a test that fails when the fix is removed | Nothing proves the fix | Write the failing test first; paste its failing output into the pull request |
| A refactoring or a feature "while I was there" | It cannot be reviewed | One pull request, one goal |

These changes are fine in principle, but **the review pauses until a maintainer has looked in person**. Mentioning them in an issue beforehand makes it much faster:

- A new third-party dependency.
- A new network address, any reporting, or data going somewhere it did not go before.
- Reading or writing credential files or the keychain, or an adapter writing to a new location.
- Changes to `.github/`, the release configuration or the install scripts.
- Relaxing a check rule or collecting more fields.

## Commits

- **One commit, one goal.** Documentation and code change in the same commit. Unrelated refactoring goes into its own commit.
- The first line says what was done. The body says why, and what you ran and did not run. A commit of more than 400 lines needs a body. English and Chinese are both fine, and a short type prefix as used in the history (`feat:`, `fix:`, `docs:`, `test:`, `build:`, `ci:`, `chore:`) is appreciated.
- Every new source file starts with the license header, as comments in the file's own syntax:

  ```
  SPDX-License-Identifier: Apache-2.0
  Copyright 2026 The Rigdepot Authors
  ```

  The year is the year the file was created. Markdown, JSON, lock files, generated files and test fixtures do not carry one.
- Never commit secrets, tokens, internal addresses, real people's names, `.env` files or local configuration.
- Names (the product name, command names, prefixes, the local directory) come from `brand.json` through the generated `internal/brand` package. `check` fails on a string literal that spells one out.

### Developer Certificate of Origin (DCO)

There is no contributor agreement to sign. Instead, every commit carries a `Signed-off-by` line, with which you state that you wrote the change or otherwise have the right to submit it under the project's license. The full text is in [DCO](DCO).

```sh
git commit -s -m "fix: keep a user's own MCP entry when the name is taken"
```

`-s` adds the line from your Git name and email. Use your real name or the name you are known by, and an email address you can be reached at.

Forgot it?

```sh
git commit --amend -s --no-edit          # the last commit
git rebase --signoff main                # every commit on your branch
```

Commits without a sign-off are not merged.

## Pull requests

1. **For anything larger than a small fix, open an issue first** and say what you want to do. This saves you from building something that cannot be merged.
2. Work on a branch of your fork. Keep the pull request to one goal.
3. Run `go run ./tools/dev check` and paste the result table into the pull request. Steps that did not run are listed as not run, not left out.
4. Fill in the pull request template. It is the acceptance checklist below.
5. A reviewer pulls your latest commit, merges it onto the current main branch and runs the checks on the result. Expect them to try to break it: remove your fix and see whether your test catches it. If you push again during review, the review starts over.
6. Draft pull requests are not merged.

### The acceptance checklist

A change is merged only after each of these has been shown:

1. **A test that fails without the change.** Put the old code back, run the new test, and see a real assertion failure, not a compile error or a skipped test. Say which test and what it printed.
2. **Tests ran in a sandbox.** That is what `check` and `test` do: temporary home directory, no network.
3. **The full test suite and the cross-platform build.** That is the result table of `check`. Cross-compiling for a platform is not the same as testing on it; say which platforms the tests really ran on.
4. **Real use, where possible.** A change to a tool's adapter is verified against the real tool in a temporary home directory. If you could not do that, write "not verified against the real tool".
5. **Documentation is in sync**, or you say why none is needed.

## Developing on Windows

- No WSL, bash or make needed. `go run ./tools/dev check` runs directly in PowerShell or cmd.
- You do not need to change Git's line-ending settings before cloning. `.gitattributes` takes care of it, and test fixtures are treated as binary.
- Some tests only run on Windows (files ending in `_windows_test.go`). Test results from Windows are very valuable to us; verification on real Windows machines is what the project lacks most right now.

## Where to start

Good first contributions, each small and with clear boundaries:

- A check rule.
- A self-check item for `rgd doctor`.
- A translation.
- A sample resource.
- A new fixture for an adapter: a new version of a tool, or a way of writing its configuration that is not covered.
- Documentation.

They are tagged `good first issue` as the ground for each becomes ready.

## Conduct and security

Everyone taking part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md). Please report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md), and not in a public issue.
