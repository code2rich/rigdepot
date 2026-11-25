# Translations

Everything the programs show to people comes from the files in this directory. Command names, option names, the keys and values of `--json` output, error `code`s and logs are not translated: scripts and searches depend on them.

## Layout

One directory per language, named by its BCP 47 tag (`en`, `zh-CN`, later `zh-TW`, `ja`, `de` …). Which languages exist is read from the directory names; nothing has to be registered.

| File | Who shows it |
| --- | --- |
| `meta.json` | The language's own name (`name`), its English name (`englishName`) and who maintains it (`maintainers`). Not messages |
| `catalog.json` | Nouns both the command line and the web console show: resource kinds, stages, states, tool names |
| `check.json` | Names and findings of the checks; `rgd check` and the console show the same sentence |
| `cli.json` | The command-line program |
| `server.json` | API errors, notifications and emails |
| `web.json` | The web console |

A file that has no messages yet does not exist yet.

`en` and `zh-CN` are required and must be complete: the same keys, and for every key the same placeholders. Every other language may be incomplete; what it lacks is shown in English.

## Writing messages

The files are flat JSON objects in the i18next v4 conventions, which translation platforms read directly:

```json
{
  "config.set.done": "Set {{key}} to {{value}} in {{path}}",
  "sync.plan.total_one": "{{count}} change · nothing has been changed yet",
  "sync.plan.total_other": "{{count}} changes · nothing has been changed yet"
}
```

- **Keys** are dotted lower-case English; the first part is the feature. A key is a stable identifier: changing the text keeps the key, changing the meaning gets a new key. Keys are sorted (`go run ./tools/dev gen` sorts them).
- **Placeholders** are `{{name}}`. They are replaced, never formatted: numbers and dates arrive formatted. `{{cli}}`, `{{product}}`, `{{server}}` and `{{home}}` are filled in everywhere with the names from `brand.json`; write them instead of the product's names.
- **Plurals**: the key gets a CLDR category suffix (`_zero`, `_one`, `_two`, `_few`, `_many`, `_other`), chosen by `{{count}}`. Every language needs `_other`. Chinese has only `other`, but `zh-CN` repeats the English categories so that the two required languages have the same keys. A plural form may leave out `{{count}}` ("one file").
- **Whole sentences.** One sentence is one message. Code never glues two messages together or puts a word from one message into another: word order differs between languages.
- Go code refers to keys through generated constants (`msg.ConfigSetDone` in `internal/i18n/msg`), so a misspelt key does not compile. Built-in implementations, which cannot import `internal/`, return keys as strings.

After adding or removing a key, run `go run ./tools/dev gen` (sorts the files, regenerates the constants) and `go run ./tools/dev check`. The check fails when a required language lacks a key or has one too many, when placeholders differ from English, when a key is not used by any code, and when a file is not a flat, sorted JSON object; it names the file and the key.

## Adding a language

1. Copy `en/` to `<tag>/`, for example `ja/`.
2. Translate the values. Leave the keys and the `{{…}}` placeholders as they are.
3. Change `meta.json`: the language's name in itself, its English name, the maintainers.
4. Run `go run ./tools/dev check`. It reports placeholder mismatches and broken JSON, and how complete the language is.

An incomplete translation can be merged; the rest shows in English.

## Terms

| Term | 中文 | Notes |
| --- | --- | --- |
| Skill, MCP, Agent | Skill、MCP、Agent | Industry names; not translated in any language |
| resource | 资源 | A Skill, MCP, workflow, Agent or knowledge base the team shares |
| resource repository | 资源仓库 | The Git repository the resources come from |
| sync | 同步 | Writing the resources into the AI tools on this computer |
| global configuration | 全局配置 | A tool's own configuration file, such as `~/.claude.json`; changing it needs consent |
| local data directory | 本机目录 | `~/{{home}}` |
| local configuration | 本机配置 | `~/{{home}}/config.toml` |
| manifest | 清单 | The record of everything written; uninstalling relies on it |
| option (command-line) | 参数 | `--json`, `--lang` … |
