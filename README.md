# Rigdepot

**团队 AI 编码资源的中台：把好用的 Skill、MCP、工作流和环境资源统一登记，按研发环节下发到每个人手里的任何 AI 编码工具，再把用得好不好带回来。**

> 状态：**设计阶段。** 仓库里是设计文档、可点击原型和产品站；服务端和命令行还没有实现。

![落地页](docs/screenshots/site-landing-light.png)

## 为什么

团队用上 Claude Code、Codex、OpenCode 这些工具之后，会碰到同样的事：每个工具各配各的；好用的 Skill 散在个人电脑里沉淀不下来；团队的规范 AI 不知道；令牌明文贴得到处都是；推了一批资源不知道好不好用；换一个工具一切重来。详见 [docs/positioning.md](docs/positioning.md)。

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

没有服务端也能用：开源命令行可以直接从一个 Git 仓库同步（`rgd sync --repo <地址>`）。服务端增加产品范围、个人密钥、审核、审计和洞察。

## 看一看

- **在线演示**：浏览器打开 [`prototype/index.html`](prototype/README.md)，顶部可以切换三种身份（平台管理员 / 产品负责人 / 成员）和三种场景（已接入 / 新成员未接入 / 刚部署没有资源）。演示数据全部虚构。
- **产品站**：打开 [`site/index.html`](site/README.md)；完整预览（含文档页）运行 `sh site/build.sh && cd dist && python3 -m http.server`。演示站 `https://rigdepot.code2rich.com`。
- **截图**：[docs/screenshots/](docs/screenshots/)。

## 设计文档

| | |
| --- | --- |
| [定位](docs/positioning.md) | 困境、目标用户、和现有方案的差异 |
| [总体设计](docs/design.md) | 非目标、架构、部署、开源与企业版边界、MVP 与路线图、待决定的问题 |
| [子系统](docs/subsystems/README.md) | 资源目录、同步命令行、工具适配器、Skill 检查与评分、密钥、反馈与摩擦、使用洞察、审计与确认、权限与角色、环境资源 |
| [扩展点](docs/extensibility.md) | 工具适配器、资源类型、身份源、密钥后端、审批流、审计与洞察导出、资源源、策略引擎 |
| [协议方案](docs/licensing.md) | 开放核心：Apache-2.0 + `ee/` 商业许可（待确认） |
| [参考与借鉴](docs/prior-art.md) | 学了什么、刻意不同在哪 |

## 这个仓库怎么维护

代码由编码 Agent 编写、评审、合并和发版，人负责产品方向、UI/UX 和拍板。Agent 从 [AGENTS.md](AGENTS.md) 开始读；标准在 [docs/code-standards.md](docs/code-standards.md)；教训在 [LESSONS.md](LESSONS.md)；开发循环的设计在 [docs/dev-loops.md](docs/dev-loops.md)。参与贡献见 [CONTRIBUTING.md](CONTRIBUTING.md)。

```
├── AGENTS.md  CLAUDE.md  LESSONS.md  CONTRIBUTING.md
├── LICENSE  NOTICE              # Apache-2.0
├── ee/                          # 企业版，单独许可（目前为空，许可为占位文本）
├── docs/                        # 设计文档与截图
├── prototype/                   # 可点击原型（零构建）
└── site/                        # 产品站：落地页、文档页、反馈页
```

## 协议

`ee/` 以外的内容采用 [Apache-2.0](LICENSE)；`ee/` 采用[单独的商业许可](ee/LICENSE)（占位文本，需正式法律文本）。贡献需要签 CLA。产品名称和标识不在协议授权范围内。
