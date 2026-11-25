<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://rigdepot.code2rich.com/brand/logo-dark.svg">
    <img alt="Rigdepot" src="https://rigdepot.code2rich.com/brand/logo-light.svg" height="56">
  </picture>
</p>

# Rigdepot

**团队 AI 编码资源的中台：把好用的 Skill、MCP、工作流和环境资源统一登记，按研发环节下发到每个人手里的任何 AI 编码工具，再把用得好不好带回来。**

> 状态：**设计阶段。** 服务端和命令行（Go）还没有开始实现，这个仓库会随实现逐步填充。

## 为什么

团队用上 Claude Code、Codex、OpenCode 这些工具之后，会碰到同样的事：每个工具各配各的；好用的 Skill 散在个人电脑里沉淀不下来；团队的规范 AI 不知道；令牌明文贴得到处都是；推了一批资源不知道好不好用；换一个工具一切重来。

## 只做四件事

| | |
| --- | --- |
| **资源统一管理** | Skill、MCP、工作流、Agent、知识库放在一个 Git 仓库里，走代码评审、带版本发布；入库检查拦住密钥和危险命令；个人密钥只在本机注入 |
| **标准流程下发** | 按需求 → 架构 → 设计 → 开发 → 测试 → 配管 → 发布 → 运维组织；必装 / 推荐 / 可选三档；工作流让 AI 按团队的步骤走、在检查点停下 |
| **使用反馈闭环** | 只采元数据的使用洞察、摩擦率、按报错率自动生成的问题线索、质量分与达标状态 |
| **与 Agent 工具无关** | 资源写一次，适配器翻译成每个工具自己的格式；不支持的形态自动降级；换工具不丢沉淀 |

## 你的电脑是你的

- **写之前可预览**：`rgd sync --plan` 列出要写的每个文件、每处配置。
- **写过的都可撤**：每一处写入记在本机清单里，`rgd uninstall` 按清单干净撤销，清单以外的不碰。
- **不静默改全局配置**：只增删带前缀的条目，改之前先备份、先问你；你自己设的值永不覆盖。
- **平台不看内容**：不采提问、代码、调用参数和返回内容；不读其他工具的凭证来认人。

```text
$ rgd sync --plan
  将要写入（共 31 处，什么都还没改）
  + ~/.claude/skills/rgd-unit-testing/         新目录
  + ~/.zcode/skills/rgd-task-to-submit/        工作流以 Skill 下发（Zcode 没有斜杠命令）
  需要你同意：修改全局配置
  ~ ~/.claude.json   mcpServers 新增 1 项：rigdepot（单一入口，密钥在本机注入）
```

没有服务端也能用：命令行可以直接从一个 Git 仓库同步（`rgd sync --repo <地址>`）。服务端增加产品范围、个人密钥、审核、审计和洞察。

## 看一看

- **演示站**：<https://rigdepot.code2rich.com>，有在线原型（可切换三种身份和三种场景，演示数据全部虚构）和问题反馈入口。
- **社区**：[Discord](https://discord.gg/feGwwqs7x)，日常交流、提想法、看进展。

## 协议

完全开源，整个仓库采用 [Apache-2.0](LICENSE)：可以商用、修改、内部部署、fork、托管，带专利授权。没有企业版，没有收费功能。

- 贡献不需要签协议，提交时用 `git commit -s` 带上 `Signed-off-by` 即可，表示你同意 [DCO](DCO)。
- 「Rigdepot」名称和标志不随协议授权：可以说明你的项目基于 Rigdepot，但对外分发的 fork 需要换一个名字和标志。
