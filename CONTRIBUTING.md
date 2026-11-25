<p align="right"><b>English</b> · <a href="CONTRIBUTING.zh-CN.md">简体中文</a></p>

# Contributing to Rigdepot

Thank you for considering a contribution. This page gets you from a clone to a passing check, and then through the changes people make most often, step by step.

Ideas and questions are contributions too, and you do not have to write code: open an issue, or talk to us on [Discord](https://discord.gg/feGwwqs7x). Issues and pull requests are welcome in English or Chinese.

For how the pieces fit together in more depth (the sync engine, the server's layers, the extension points, the tests), read [docs/architecture.md](docs/architecture.md) after this page.

## What this is

Rigdepot keeps a team's skills, MCP servers, workflows and agents in one Git repository and installs them into the AI coding tools on each person's computer. The repository holds two Go programs:

- **`rgd`**, the command-line program on each person's computer. It reads resources, works out what to write into each tool, shows you, writes after you agree, records every write and can undo all of it.
- **`rgd-server`**, the server a team hosts itself. It manages who may use what, releases, audit and notifications. The web console is embedded in it.

`rgd` works without the server, and most contributions only touch the `rgd` half.

## Ten minutes to a passing check

You need:

- **Go**, the version in [go.mod](go.mod), and **git**. That is all for the Go code. macOS, Linux and Windows all work; Windows needs neither WSL, bash nor make.
- **Node 22.12 or later and pnpm**, only if you change the web console in `web/`. Without them, `check` lists the console's step as "not run" and everything else works.
- **Docker**, only for `check-linux` and for trying the deployment files.

```sh
git clone https://github.com/code2rich/rigdepot.git
cd rigdepot
go run ./tools/dev deps      # download dependencies; the only step that needs the network
go run ./tools/dev check     # every check, ending in a result table
```

The first `check` compiles everything six times over and takes a few minutes; later runs are faster.

`go run ./tools/dev <task>` is the single entry point for everything:

| Task | What it does |
| --- | --- |
| `deps` | Downloads the modules of the main module and of the pinned development tools. |
| `check` | Every pre-merge check, in order: format, `go vet`, `staticcheck`, generated files up to date, structure rules, messages, tests, a build without optional modules, a cross-compile for six targets, and the web console's checks. Stops at the first failure and ends with a table of what passed, failed and did not run. |
| `test` | Runs tests in the same isolated environment as `check`. Takes packages and `go test` flags: `go run ./tools/dev test ./internal/cli -run TestVersion`. |
| `gen` | Regenerates generated files: the name constants from `brand.json`, the message key constants, the published copies of the resource kinds' schemas, the API code from `api/openapi.yaml`, and the web console's generated files. `gen --check` only reports what is stale. |
| `web-deps` | Installs the web console's dependencies from `web/pnpm-lock.yaml`. Needs Node, pnpm and the network. |
| `web` | Builds the web console into `web/dist`, for `go build -tags webdist`. |
| `check-linux` | Runs `check` in a local Linux container, so tests really run on Linux. Needs Docker. |
| `release-snapshot` | Builds every release artifact into `dist/` twice without publishing anything, and compares the two. |
| `release-files` | Writes the shell completions and third-party notices the release archives carry. |

The `Makefile` only holds aliases for these. CI runs the same commands. No check lives in the Makefile or in a workflow file, because a check that lives there cannot run on your machine.

`check` and `test` run every test with a temporary home directory, `GOFLAGS=-mod=readonly` and `GOPROXY=off`; the table's first line says which directory it used and that it was removed.

## Trying your change

### `rgd`

> **Never run an unreleased `rgd` against your real home directory.** It exists to edit the configuration of your AI tools. Give it an empty home directory first.

```sh
go build -o "$PWD/rgd" ./cmd/rgd
env -u CLAUDE_CONFIG_DIR -u CODEX_HOME -u XDG_CONFIG_HOME HOME="$(mktemp -d)" sh   # a shell with an empty home
./rgd sync --repo . --dir examples/resources --plan
./rgd sync --repo . --dir examples/resources
./rgd status --repo . --dir examples/resources
./rgd check examples/resources-broken/secrets
./rgd uninstall
exit
```

On Windows, set `USERPROFILE` to an empty directory instead of `HOME`. The variables taken out with `env -u` move a tool's configuration elsewhere; left in place, they would point back at your real one.

`examples/resources` is a sample resource repository that passes every check; `examples/resources-broken/<rule>` is one small repository per check rule that breaks exactly that rule. `--repo` reads what is committed, so commit (or stash into a branch) what you want it to see.

### `rgd-server`

The server runs on this machine without any setup:

```sh
RGD_BASE_URL=http://127.0.0.1:8080 RGD_DATA_DIR="$(mktemp -d)" go run ./cmd/rgd-server
```

It listens on `127.0.0.1:8080`, creates a SQLite database and a master key in the data directory and prints a one-time setup token in its log (the line with `setup_code`). Open <http://127.0.0.1:8080>, enter the token and create the first administrator. Then `rgd login http://127.0.0.1:8080` from the shell with the empty home signs that `rgd` in. All settings are in [docs/deploy.md](docs/deploy.md#configuration).

### The web console

Built this way, the server shows a one-page stand-in instead of the console. To work on the console, install Node and pnpm, then:

```sh
go run ./tools/dev web-deps                       # once, and after web/pnpm-lock.yaml changes
go run ./tools/dev web                            # build into web/dist
go build -tags webdist -o rgd-server ./cmd/rgd-server
```

While editing pages, run Vite's development server and let the Go server pass the console through to it, so that pages reload as you edit and are served with the production headers:

```sh
pnpm --dir web dev                                                        # http://127.0.0.1:5173
RGD_WEB_DEV_PROXY=http://127.0.0.1:5173 RGD_BASE_URL=http://127.0.0.1:8080 RGD_DATA_DIR="$(mktemp -d)" go run ./cmd/rgd-server
```

Open the server's address, not 5173. `VITE_API_MOCK=1 pnpm --dir web dev` answers the console's requests from `web/src/api/mock.ts` instead of a server; [web/README.md](web/README.md) has the details.

## A map of the repository

```
cmd/          entry points of the two programs, a few lines each
ext/          the public extension interfaces; what you write implements these
builtin/      built-in implementations, one directory each      <- most contributions go here
  adapters/     one per AI coding tool (claudecode, codex, opencode)
  kinds/        one per resource kind (skill, mcp, workflow, agent, knowledge)
  checks/       one per check rule
  identity/     one per sign-in method (oidc, gitlab)
  notifiers/    one per notification channel (email)
  all/          the registry: a new item is one more line here (server-only items: all/server/)
modules/      optional modules (large features that are off by default); none yet
internal/     the core: sync engine, executor, manifest, the server and its domains
locales/      translations, one directory per language          <- contributions without code
api/          the HTTP API contract (OpenAPI)
web/          the web console (Preact, TypeScript, Vite)
schema/       JSON Schemas: resource repository, local manifest, adapter profiles
examples/     a sample resource repository, and one broken one per check rule
test/         end-to-end tests, and the optional tests against real AI tools
deploy/       Compose files, systemd unit, packaging scripts, reverse proxy samples
scripts/      install.sh, install.ps1
docs/         the deployment guide and the architecture tour
tools/dev/    the one development command
brand.json    every name the code uses, in one file
```

Every package has a `doc.go` with one sentence saying what it is for. If you are looking for where something belongs, this prints them all:

```sh
go list -f '{{.ImportPath}}: {{.Doc}}' ./...
```

### Three rules to remember

`check` enforces these and tells you which rule a change breaks and which two packages are involved.

1. **Code in `builtin/` may only use `ext/`, the standard library and a short list of third-party modules**, never `internal/`. Built-in implementations are the examples of how to write an extension. If something you need is missing from `ext/`, the interface is incomplete: open an issue and say what you need.
2. **Only the executor writes the user's files.** An adapter says what should be written and where; it never writes itself.
3. **No `util`, `common` or `helpers` packages.** Code goes into the package of the domain it belongs to.

The full list:

| Rule | In short |
| --- | --- |
| R1 | `ext/**` imports only the standard library and `ext/**`. Two exceptions, both separate sub-packages: the test suites whose name ends in `test`, and `ext/adapter/profileyaml` (which may use the YAML library). |
| R2 | `builtin/**` imports only the standard library, `ext/**` and the allowed third-party modules (listed in `tools/dev/layout.go`). No `internal/**`, and no other built-in item; shared code goes in `builtin/<class>/internal/`. |
| R3 | `internal/**` does not import `builtin/**` or `modules/**`. The core compiles without any built-in item or module. |
| R4 | `modules/<id>/**` imports only the standard library, `ext/**` and its own directory. |
| R5 | The command-line side does not import `internal/server/**` and the server does not import the command-line side. `rgd` has no database driver and no sign-in protocol; `rgd-server` has no code that edits other tools' configuration. Neither program links `testing`. |
| R6 | Inside the server: HTTP layer → domain packages → foundation (`config`, `dbx`, `migrations`, `masterkey`, `jobs`), and between domain packages only in the directions listed in `tools/dev/layout.go`. |
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
manifest entry                               one entry in ~/.rigdepot/manifest.json; uninstall and
                                             drift detection look at nothing else
```

There are four kinds of artifact (skill directory, MCP entry, slash command, sub-agent) and four kinds of operation (write a tree, write a file, set a config key, run the tool's own command). The preview and the real write are the same operations with a different root directory, so they cannot disagree.

## Common contributions

Most of them are the same move: **add files in one directory, add one line to a registry.** `check` fails when a built-in item is not registered, and says which line to add.

### A new check rule

A rule is a pure function: it receives all files of one resource and returns what it found. It cannot read anything else from disk, reach the network or run a command. `go doc ./ext/check` and `go doc ./ext/check/checktest` are the reference; `builtin/checks/bundledenv` is the shortest real rule to copy from.

As an example, a rule `todo` that points out `TODO` and `FIXME` markers left in a skill:

1. **Create `builtin/checks/todo/`** (the directory name is the rule's ID) with `doc.go`:

   ```go
   // SPDX-License-Identifier: Apache-2.0
   // Copyright 2026 The Rigdepot Authors

   // Package todo is the check rule for TODO and FIXME markers left in a skill. Advisory.
   //
   // Message keys, all under check.todo.:
   //
   //	found  a marker in a file    {file} {line}
   package todo
   ```

   and `rule.go`:

   ```go
   // SPDX-License-Identifier: Apache-2.0
   // Copyright 2026 The Rigdepot Authors

   package todo

   import (
   	"bufio"
   	"bytes"
   	"context"
   	"io/fs"
   	"regexp"

   	"github.com/code2rich/rigdepot/ext/check"
   	"github.com/code2rich/rigdepot/ext/resource"
   )

   const id = "todo"

   type rule struct{}

   // New returns the rule.
   func New() check.Rule { return rule{} }

   func (rule) ID() string               { return id }
   func (rule) Severity() check.Severity { return check.Advisory }
   func (rule) Applies(kind string) bool { return kind == resource.KindSkill }

   var marker = regexp.MustCompile(`\b(TODO|FIXME)\b`)

   func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
   	var found []check.Finding
   	err := fs.WalkDir(in.Files, ".", func(p string, d fs.DirEntry, err error) error {
   		if err != nil {
   			return err // unreadable: the engine counts this as not passed, never as clean
   		}
   		if d.IsDir() {
   			return nil
   		}
   		data, err := fs.ReadFile(in.Files, p)
   		if err != nil {
   			return err
   		}
   		lines := bufio.NewScanner(bytes.NewReader(data))
   		for n := 1; lines.Scan(); n++ {
   			if marker.Match(lines.Bytes()) {
   				found = append(found, check.Finding{
   					Rule: id, Severity: check.Advisory, File: p, Line: n,
   					Message: "check." + id + ".found",
   					Params:  map[string]any{"file": p, "line": n},
   				})
   			}
   		}
   		return lines.Err()
   	})
   	if err != nil {
   		return nil, err
   	}
   	return found, nil
   }
   ```

2. **Add samples** under `builtin/checks/todo/testdata/`. Each sample is a small resource repository; at least one must be hit and one that **looks similar but must not be**:

   ```
   testdata/hit/marker-left/skills/demo/SKILL.md        "... TODO: write the steps ..."
   testdata/miss/word-in-text/skills/demo/SKILL.md      "... list the todos in the issue ..."
   ```

   A `SKILL.md` needs front matter with `name` (the directory name) and `description`. Files under `testdata/` carry no license header.

3. **Add the test**, `rule_test.go`:

   ```go
   // SPDX-License-Identifier: Apache-2.0
   // Copyright 2026 The Rigdepot Authors

   package todo

   import (
   	"testing"

   	"github.com/code2rich/rigdepot/ext/check/checktest"
   )

   func TestMain(m *testing.M) { checktest.Main(m) }

   func TestRule(t *testing.T) { checktest.Run(t, New(), "testdata") }
   ```

   The suite checks that every `hit/` sample has a finding and every `miss/` sample none, that findings point at real files and lines, that the rule gives the same result twice, and that an unreadable file is an error rather than "nothing found".

4. **Add the texts** to `locales/en/check.json` and `locales/zh-CN/check.json`, one line each, in sorted position:

   ```json
   "check.todo.found": "{{file}} line {{line}} still has a TODO or FIXME marker. Finish it, or move it to an issue.",
   ```

   ```json
   "check.todo.found": "{{file}} 第 {{line}} 行还留着 TODO 或 FIXME。写完它，或者挪进 issue。",
   ```

   Then run `go run ./tools/dev gen`: it sorts the files and regenerates the constants of the message keys, which `check` compares.

5. **Register it**: one line in `builtin/all/checks.go`, in alphabetical order, plus its import:

   ```go
   check.Register(todo.New())
   ```

6. **Add an example** to `examples/resources-broken/todo/`: a small resource repository that breaks this rule and no other (copy `examples/resources-broken/description/` and change the skill), and a row in that directory's `README.md`. Its `rigdepot.yaml` and `meta.yaml` carry the license header, as all YAML outside `testdata/` does. The example tests make sure every rule has one, and that `examples/resources` still passes every rule.

7. **Run `go run ./tools/dev check`**, and look at the rule from the user's side:

   ```sh
   go run ./cmd/rgd check examples/resources-broken/todo
   go run ./cmd/rgd check --lang zh-CN examples/resources-broken/todo
   ```

Two things to be careful with. Whether a rule blocks a release ("must fix") or only advises is a product decision: new rules start as `Advisory`, and the pull request says which level you suggest and why. And when a rule hits something sensitive such as a credential, the finding must not contain the matched text, not in `Params` and not in the name of a message key.

### A self-check item for `rgd doctor`

`rgd doctor` runs a list of items, each looking at one thing on this computer. An item only looks and never repairs, and reports what it cannot read as unknown, never as fine. They live in `internal/doctor`, one file per item; `git.go` is a short one to copy from.

1. **Write `internal/doctor/<item>.go`**: a type with `ID()` (stable lower-case words with hyphens; it is the `check` field of `rgd doctor --json`), `Title()` (the message key of its name, `msg.Doctor<Item>Title`) and `Run(ctx, env)`, which returns at least one `Result` with a `Status`, a stable `Code` and a `Message`, and a `Hint` when the status is not OK. Everything it needs from the computer comes from `Env`; if `Env` lacks something, add a field and fill it in where the command builds `Env` (`internal/cli/doctor.go`).
2. **Add one line** to the list in `Checks()` in `internal/doctor/doctor.go`, where it belongs in the order people read.
3. **Write the texts** under `doctor.<item>.*` in `locales/en/cli.json` and `locales/zh-CN/cli.json`, `doctor.<item>.title` among them, then `go run ./tools/dev gen` for the `msg` constants.
4. **Write `<item>_test.go`** with at least one case that passes and one that does not, each against a temporary home directory set up by the test (`newTestEnv` in `doctor_test.go`).
5. `go run ./tools/dev check`, then `rgd doctor` and `rgd doctor --json` in a shell with an empty home.

### A new language

No code needed.

1. Copy `locales/en/` to `locales/<tag>/`, using a BCP 47 tag such as `ja`, `de` or `zh-TW`.
2. Translate the **values**. Leave the keys and the `{{…}}` placeholders alone.
3. Fill in `meta.json`: the language's own name, its English name and the maintainers.
4. `go run ./tools/dev check` reports mismatched placeholders and broken JSON, and how complete the translation is.

An incomplete translation is fine; what is missing shows in English. Languages are discovered from the directory, there is nothing to register. Some words stay untranslated everywhere (Skill, MCP, Agent); the terms table in [locales/README.md](locales/README.md#terms) says how the others are translated.

### A page or a change in the web console

The console is in `web/`: Preact, TypeScript and Vite, built into static files the server embeds. Read [web/README.md](web/README.md) first; in short:

1. Make `web/src/pages/<id>/index.tsx`, whose default export is the page, starting with `<PageHeader page="<id>" … />`.
2. Register it in `web/src/pages/registry.ts`: a lazily loaded route, in the navigation or in `PUBLIC` for pages outside it.
3. Styles in `web/src/pages/<id>/<id>.css`, colours only through the design tokens (`var(--ink)` …).
4. Every string from `locales/en/web.json` and `locales/zh-CN/web.json`, keys prefixed with the page id and written out in full in `t('…')`; then `go run ./tools/dev gen`.
5. Data through `api` from `web/src/api/client.ts`; its types come from `api/openapi.yaml`. Show loading, failure and empty states.
6. `go run ./tools/dev check` (with Node and pnpm installed, so the console's step runs), then look at the page in both languages, both themes and at 390 px wide.

The server sends a strict content security policy: no inline scripts or styles added by hand, no `dangerouslySetInnerHTML`, nothing loaded from another address, and every link through `href()` because the console may be served under a sub-path.

### A new AI coding tool (an adapter)

An adapter tells `rgd` where one AI tool keeps skills, MCP servers, commands and agents, in which format, and which of its files are credentials. Most of it is a declarative profile; code is only for what the profile cannot say. `builtin/adapters/codex` and `builtin/adapters/opencode` are the ones to read. Please open an issue before starting, because the verification below takes the real tool.

1. **Find out where the tool really keeps things.** Do not guess from articles. Run the tool's own commands in a temporary home directory and look at what it wrote:

   ```sh
   tmp="$(mktemp -d)"
   HOME="$tmp" USERPROFILE="$tmp" XDG_CONFIG_HOME="$tmp/.config" <tool> mcp add probe -- echo hi
   find "$tmp" -type f
   ```

2. **Write the profile**, `builtin/adapters/<tool>/profile.yaml`: how to detect the tool (programs, directories, how to read its version), its configuration root and the variable that moves it, where skills, commands, agents and MCP entries go and in which format, and which files are credentials. The schema is `schema/adapter/v1`. The profile must carry a `verified` record: tool version, operating systems, date and how you verified it. A profile without one writes nothing.
3. **Write the adapter**, `adapter.go`: `New()` returns `adapter.FromProfile(profileyaml.MustParse(profileYAML))`, with `profile.yaml` embedded. Where the profile cannot express something (a version that moved a key, a secret that has to be referenced in the tool's own way), embed that adapter in your own type and override `Plan` or `Capabilities`, as the three existing adapters do.
4. **Add fixtures** under `testdata/homes/<case>/`: a `home/` made from files the real tool generated (account details removed), a `README.md` saying what the case is, and a `case.json` when it needs a system, environment variables or a version. At least: nothing configured, the user's own configuration (with comments, unknown keys and an entry of the same name), a broken config file, and a target location that is already taken.
5. **Run the contract suite**, `adaptertest.Run(t, New(), "testdata")`. Write the expected results once with `go test ./builtin/adapters/<tool> -run TestContract -update`, then **read every file** in `testdata/golden/`: is the user's existing content unchanged to the byte, and is what we added in the format the tool accepts?
6. **Register it**: one line in `builtin/all/adapters.go`, `adapter.Register(<tool>.New())`.
7. `go run ./tools/dev check`. With the tool installed, also run the tests against the real tool, which work in a temporary home directory; extend `test/realtools` for the new tool and paste the output into the pull request:

   ```sh
   RGD_TEST_REAL_TOOLS=claude,codex,opencode go test -count=1 -v ./test/realtools
   ```

In the pull request, state the tool version you verified against, how, which forms are supported natively and which are downgraded.

### A new resource kind

Ask first: can it be rendered as one of the four existing artifacts? If yes, it is one directory. If it needs a new kind of artifact, that affects every adapter and is no longer "adding a kind"; open an issue to discuss it. A new kind is also a product decision, so open an issue before the pull request in any case.

1. Create `builtin/kinds/<kind>/` with its JSON Schema (`schema.json`) and the code: `ID`, `Layout` (where it lives in a repository, a directory or a single file), `Schema`, `Parse`, and `Forms`, the forms it can be delivered in, in order of preference. `go doc ./ext/kind` is the reference; `builtin/kinds/skill` and `builtin/kinds/mcp` are the ones to read.
2. Add valid and invalid samples under `testdata/` and run the kind suite, `kindtest.Run`.
3. Register it: one line in `builtin/all/kinds.go`, `kind.Register(<kind>.New())`.
4. `go run ./tools/dev gen` (it copies the schema to `schema/repo/v1/kinds/`), then `check`.
5. For the console to show the kind by name rather than as a generic resource, add it to `KIND_NAME` and `KIND_ICON` in `web/src/ui/resource.tsx` and `res.kind.<kind>` to both `web.json` files.

### Notification channels and sign-in methods

Both are extension points of the server: `ext/notify` (built in: `builtin/notifiers/email`) and `ext/identity` (built in: `builtin/identity/oidc`, `builtin/identity/gitlab`), registered in `builtin/all/server/`. In this version the server sends notifications by email only, so a new channel would not be used yet. Please open an issue before writing either.

A channel or a sign-in method sends data out by nature, so a maintainer always reviews these pull requests in person. Hold to three things: send only to the addresses the deployer configured (or the recipient's own), never to one taken from a message; send only the fields you were given; keep configured tokens and secrets out of error messages.

## What gets stopped in review

This project edits other tools' configuration on people's machines, so some rules are stricter than usual.

| Doing this | Why not | Do this instead |
| --- | --- | --- |
| A test reads or writes the real home directory (`~/.claude`, `~/.codex`, …) | It can break the configuration of whoever runs it | Every test package starts with `func TestMain(m *testing.M) { testenv.Main(m) }` (or the suite's own `Main`); `check` fails without it |
| A test uses the network | Unstable, and it may send something out | A local fake server (`httptest`), with replies taken from saved real ones |
| Treating a config file that failed to parse as empty | It overwrites the user's configuration | Failed to parse means unknown means do not write; report why |
| Overwriting or deleting something that is not in the manifest | It belongs to the user | Touch only what the manifest lists; if a name is taken, skip and say so |
| Guessing another tool's config format from field names | A wrong type can stop the tool from starting | Read its documentation or source, verify in a temporary home directory, record it in the profile |
| A fix without a test that fails when the fix is removed | Nothing proves the fix | Write the failing test first; paste its failing output into the pull request |
| A refactoring or a feature "while I was there" | It cannot be reviewed | One pull request, one goal |

These changes are fine in principle, but **the review pauses until a maintainer has looked in person**. Mentioning them in an issue beforehand makes it much faster:

- A new third-party dependency (Go or the web console).
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

  The year is the year the file was created. Go, TypeScript, CSS, shell, PowerShell, YAML, TOML, SQL, systemd units, Dockerfiles and the Makefile carry it; Markdown, JSON, lock files, generated files and anything under `testdata/` do not. `check` lists the files that lack one.
- Never commit secrets, tokens, internal addresses, real people's names, `.env` files or local configuration. Examples use `example.com` and placeholders such as `<placeholder: token>`.
- Names (the product name, command names, prefixes, the local directory, the variable prefix) come from `brand.json` through the generated `internal/brand` package. `check` fails on a string literal that spells one out.

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
4. **Real use, where possible.** A change to a tool's adapter is verified against the real tool in a temporary home directory. A change to the console is looked at in a browser. If you could not do that, say so.
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
- A sample resource in `examples/resources`.
- A new fixture for an adapter: a new version of a tool, or a way of writing its configuration that is not covered.
- Documentation.

They are tagged `good first issue` on GitHub.

## Conduct and security

Everyone taking part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md). Please report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md), and not in a public issue.
