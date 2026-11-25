# 示例资源仓库

「示例科技」的虚构资源，三个用途共用这一份：开发环境默认连它、文档里的例子指向它、端到端测试用它。格式见资源仓库格式 v1。

| 资源 | 类型 | 看点 |
| --- | --- | --- |
| `skills/unit-testing` | Skill | 带 `.env.example`（本机配置项，其中一项引用个人密钥）和 `references/` |
| `skills/code-review` | Skill | 最简单的形态：`SKILL.md` 加 `meta.yaml` |
| `skills/release-notes` | Skill | 带脚本 `scripts/`，英文的 `i18n` |
| `mcp/jira` | MCP（http） | 密钥在 `headers` 里用 `${secret:JIRA_API_TOKEN}` 引用 |
| `mcp/gitlab` | MCP（stdio） | 密钥在 `env` 里用 `${secret:GITLAB_TOKEN}` 引用；包写死版本号 |

密钥的值永远不在仓库里：仓库只写名字（`rigdepot.yaml` 的 `secrets`），资源只引用名字。没有服务端时（M0），命令行把引用写成各工具原生的「从环境变量取」，使用者在自己的环境里设好同名变量即可。

这个仓库对全部检查规则零问题；故意犯错的那一份在 `../resources-broken/`。
