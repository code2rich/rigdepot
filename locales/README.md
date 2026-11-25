# Translations

Everything the programs show to people comes from the files in this directory. Command names, option names, the keys and values of `--json` output, error `code`s and logs are not translated: scripts and searches depend on them.

## Layout

One directory per language, named by its BCP 47 tag (`en`, `zh-CN`, later `zh-TW`, `ja`, `de` …). Which languages exist is read from the directory names; nothing has to be registered.

| File | Who shows it |
| --- | --- |
| `meta.json` | The language's own name (`name`), its English name (`englishName`) and who maintains it (`maintainers`). Not messages |
| `catalog.json` | What the resource kinds say about a resource, such as why it arrives in another form; the command line and the console show the same sentence |
| `check.json` | The findings of the check rules; `rgd check` and the console show the same sentence |
| `cli.json` | The command-line program |
| `server.json` | Notifications and emails the server sends |
| `web.json` | The web console |

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

How the product's words are translated. Use these, so that the command line, the console and the documentation say the same thing. A new term goes into this table in the same change that first uses it.

| Term | 中文 | Notes |
| --- | --- | --- |
| Skill, MCP, Agent | Skill、MCP、Agent | Industry names; not translated in any language. "MCP server" is 「MCP」 in Chinese text |
| AI coding tool | AI 编码工具 | Claude Code, Codex, OpenCode … |
| resource | 资源 | A Skill, MCP server, workflow, agent or knowledge base the team shares |
| resource kind | 资源类型 | `skill`, `mcp`, `workflow`, `agent`, `knowledge` |
| workflow | 工作流 | |
| knowledge base | 知识库 | |
| resource repository | 资源仓库 | The Git repository the resources come from |
| catalog | 资源目录 | The published resources a person can see (console page *Catalog*) |
| sync | 同步 | Writing the resources into the AI tools on this computer |
| plan | 计划 | What a sync would write; `--plan` only shows it |
| global configuration | 全局配置 | A tool's own configuration file, such as `~/.claude.json`; changing it needs consent |
| consent | 同意 | |
| local data directory | 本机目录 | `~/{{home}}` |
| local configuration | 本机配置 | `~/{{home}}/config.toml` |
| manifest | 清单 | The record of everything written; uninstalling relies on it |
| changed on this computer (drift) | 在本机被改过 | Avoid 「漂移」 in messages; say what happened |
| form; downgrade | 形态；降级 | A resource arrives in a form the tool supports; a later form is a downgrade |
| adapter | 适配器 | Console page *Tool adapters*: 「工具适配」 |
| check rule | 检查规则 | |
| finding (of a check) | 问题 | |
| must fix / advisory | 必须修复 / 建议修复 | The two levels of a finding |
| scope | 范围 | `org` 「全组织」, a product, `me` 「仅自己」 |
| install policy: required / recommended / optional | 安装策略：必装 / 推荐 / 可选 | |
| stage | 环节 | Engineering stages: requirement 需求, architecture 架构, design 设计, develop 开发, test 测试, scm 配管, release 发布, ops 运维 |
| product; product line | 产品；产品线 | |
| platform administrator | 平台管理员 | |
| product owner | 产品负责人 | 「负责人」 inside a product |
| member | 成员 | |
| maintainer | 维护人 | Of a resource |
| draft; publish; version | 草稿；发布；版本 | |
| roll back | 回滚 | |
| join request | 开通申请 | Console: 「申请加入」 |
| invitation | 邀请 | |
| sign-in method; identity provider | 登录方式；身份源 | |
| device code | 设备码 | The code `rgd login` shows is the 「确认码」 |
| access token | 访问令牌 | |
| personal secret | 个人密钥 | |
| master key | 主密钥 | |
| setup token | 初始化口令 | |
| setup wizard | 部署向导 | |
| audit log | 审计日志 | |
| notification | 通知 | |
| feedback | 问题反馈 | |
| option (command-line) | 参数 | `--json`, `--lang` … |
| exit code | 退出码 | |
