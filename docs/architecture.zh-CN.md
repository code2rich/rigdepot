<p align="right"><a href="architecture.md">English</a> · <b>简体中文</b></p>

# 给贡献者的架构导览

这一页讲 Rigdepot 是怎么搭起来的，写给想改它的人。先读 [CONTRIBUTING.zh-CN.md](../CONTRIBUTING.zh-CN.md)：它带你跑通检查，并一步步走过常见的改动；这一页是那些步骤背后的地图。每个包也有自己的文档（`go doc ./internal/sync`、`go doc ./ext/adapter` 等）；这一页和包的文档对不上时，以包为准，也欢迎提 PR 改这一页。

## 目录

- [两个程序，一个仓库](#两个程序一个仓库)
- [包的分组和依赖规则](#包的分组和依赖规则)
- [命令行](#命令行)
- [服务端](#服务端)
- [HTTP 接口](#http-接口)
- [扩展点](#扩展点)
- [名字和翻译](#名字和翻译)
- [测试](#测试)
- [生成文件](#生成文件)

## 两个程序，一个仓库

| | `rgd` | `rgd-server` |
| --- | --- | --- |
| 跑在哪 | 每个人的电脑：macOS、Linux、Windows | 团队的一台 Linux 机器（开发时可以是 macOS） |
| 干什么 | 读资源，算出每个 AI 工具需要什么，给人看，同意后写入，记下来，能撤掉 | 谁能用什么、资源目录和版本、下发、审计、通知、网页控制台 |
| 入口 | [cmd/rgd/main.go](../cmd/rgd/main.go) → `internal/cli` | [cmd/rgd-server/main.go](../cmd/rgd-server/main.go) → `internal/server` |
| 带着什么 | 没有数据库驱动、没有登录协议、没有网页资源 | 没有改其他工具配置的代码 |

两个程序出自同一个 Go 模块 `github.com/code2rich/rigdepot`，同一个版本号一起发布。两者都是纯 Go（`CGO_ENABLED=0`）：SQLite 用的是 `modernc.org/sqlite`，所以在任何机器上都能交叉编译出全部目标。`rgd` 不需要服务端也能用；有服务端时，它登录后从服务端而不是从仓库取资源。

入口只有几行：引入内置项的登记处（`builtin/all`，服务端再加 `builtin/all/server` 和 `modules/all`），然后调用核心。核心因此不认识任何具体的工具、类型或规则。

## 包的分组和依赖规则

```
ext/            公开的接口和共用类型；只用标准库
builtin/        ext/ 的实现（适配器、资源类型、检查规则、身份源、通知通道）
modules/        可选模块，用构建标签编进或编出
internal/       核心
  共用            brand、buildinfo、i18n、logging、repoformat、bundle、check、gitrepo、testenv
  命令行          cli、cliout、localconfig、home、manifest、configedit、executor、sandbox、
                  desired、sync、flow、apiclient、keyring、device、localsecrets、mcprun、doctor
  服务端          internal/server 及其下所有包
```

分组以数据的形式写在 [tools/dev/layout.go](../tools/dev/layout.go)，`go run ./tools/dev check` 按它检查 [CONTRIBUTING.zh-CN.md](../CONTRIBUTING.zh-CN.md#三条要记住的规则) 里列的 R1–R8。这些规则背后的理由：

- **`builtin/` 只看得见 `ext/`**（R2）：内置项的写法和外部贡献者的写法完全一样；缺了什么能力，会表现为 `ext/` 的缺口，而不是一条通向核心的捷径。
- **核心不认识内置项**（R3）。`go build -tags rgd_core_only` 在一个内置项都没有的情况下构建并测试核心；`check` 会跑它。
- **两个程序不共享各自的那一半**（R5）。`rgd` 保持小巧，和数据库无关；`rgd-server` 想改工具配置也改不了。两者都不链接 `testing` 包。
- **只有执行器写使用者主目录里的文件**（R7）。备份、原子写入、「解析不了的文件不写」、校验和清单只实现一次，也只评审一次。

新增的 internal 包要加进 `layout.go`，不加 `check` 就不通过。

## 命令行

### 从命令到引擎

`internal/cli` 用 cobra 建命令树。建之前先宽松地预读全局参数确定语言，所以连「配置文件坏了」这种错误也能用对的语言说出来。配置由 `internal/localconfig` 按优先级解析（参数 > `RGD_*` 环境变量 > `~/.rigdepot/config.toml` > 默认值；`rgd config list` 显示每个值从哪来）。给人看的输出和 `--json`、颜色、宽度、退出码归 `internal/cliout` 管；每条命令的 `--json` 都是一个带 `schema` 字段的文档，比如 `rgd.check/v1`。

### 本机目录

`rgd` 给自己存的东西都在 `~/.rigdepot`（`--home` 或 `RGD_HOME` 可以挪走），结构由 `internal/home` 定：

| 路径 | 是什么 |
| --- | --- |
| `config.toml` | 本机配置 |
| `manifest.json`、`manifest.journal`、`lock` | 写过的一切的清单、它的意图日志、跨进程的锁 |
| `state/` | `consent.json`（本人同意过改哪些全局配置文件）、`desired.json`（上一次从服务端拿到的期望状态） |
| `cache/` | `repos/`（资源仓库的裸克隆）、`bundles/`（从服务端取的资源包） |
| `backup/` | 其他工具的配置文件在第一次被改之前的副本 |
| `kept/` | 被 `--overwrite` 替换掉的、本人改过的版本 |
| `secrets/` | 没有系统钥匙串时，设备密钥和令牌放这里 |
| `flows/`、`knowledge/` | 工作流的运行记录；用本人的 Git 权限拉下来的知识库 |
| `tmp/`、`logs/` | 临时空间；日志 |

### 资源从哪来

`internal/desired` 把两种来源变成同一个 `desired.State`：

- **仓库**（`--repo` 或 `repo.url`）：`internal/gitrepo` 调用系统的 `git`（裸克隆加 `git archive`，没有工作区，关掉换行转换），所以本人的凭据和 SSH 配置直接可用，什么都不用重新实现。`internal/repoformat` 读 `rigdepot.yaml` 和每个资源，按 `schema/` 里的 JSON Schema 和各资源类型自己的 Schema 校验，并套用路径安全规则（`internal/bundle`）。
- **服务端**（`rgd login` 之后）：`internal/apiclient` 取回期望状态和它点名的资源包，按 SHA-256 核对每个包，并把状态存进 `state/desired.json`；这样服务端连不上时，同步按上一次的状态进行，什么都不删。

产品、范围（`org`、某个产品、`me`）和安装策略（必装、推荐、可选）决定要装什么；`rgd enable`、`rgd disable` 记下本人的选择。

### 同步引擎

`internal/sync` 每次同步都走同样的几步；完整的情形表在它的包文档里。

```
期望状态 ──┐
           ├─ 计划       检测每个工具（适配器，经一个只读的 Env）；为它渲染每个资源（资源类型的
清单 ──────┘             各种形态按顺序取第一个工具能接收的，后面的是降级）；让适配器算出操作；
                         对每个目标做三方比较：清单里记的、磁盘上现在的、计划想要的
              预览       同一组操作，在涉及到的文件的沙箱副本里执行（internal/sandbox），显示差异
              同意       改某个工具的全局配置要本人点头，每个文件一次（state/consent.json）；
                         --skip-global、--yes-global
              写入       同一组操作，由执行器在锁里对真实主目录执行；每一个都记进清单
              上报       有服务端时：装了什么，不带路径
```

引擎绝不变通的几条：

- **未知不等于空。** 读不了的工具、配置文件或目标，整个排除在这次运行之外；它的东西既不写也不撤。
- **只有清单里的才是我们的。** 名字被占用就不动，并报出来。本人改过的文件不覆盖，除非他说 `--overwrite`，那时先把他的版本放进 `kept/`。
- **撤销还原字节。** 卸载按从新到旧撤销清单里的条目；配置键换值时先撤再写，所以卸载能把每个文件逐字节还原。
- 写进去的东西都带 `rgd-` 前缀（来自 `brand.json`），在每个工具里都认得出来。

### 执行器和配置编辑

`internal/executor` 是唯一在本机目录以外写文件的代码。它只认四种操作（`ext/adapter` 的 `Op`）：写目录树、写文件、设配置键、运行工具自己的命令。改一个文件之前先备份一次，原子写入，写完校验，并在 `manifest.journal` 里记一条意图，所以中途崩溃的话，下次运行时会收尾（`rgd doctor` 会显示剩下的）。

配置键由 `internal/configedit` 设：它就地编辑 JSON、JSONC 和 TOML，其余内容的注释、顺序和格式都保持原样；每次改动都是一段有记录、可以放回去的字节范围。解析不了的文件不碰。

### 命令行的其他部分

- `internal/check` 是 `rgd check` 的检查引擎：在一个仓库上跑已登记的规则（`ext/check`）。服务端导入和上传时跑的是同一个引擎，所以两边的结论总是一致。
- `internal/doctor` 是 `rgd doctor` 的自检项，一项一个文件。
- `internal/device` 和 `internal/keyring`：设备码登录、设备的密钥对和令牌，放在系统钥匙串里，或者只有本人能读的文件里。
- `internal/flow`：`rgd flow next` 一步一步发放工作流的步骤，每一步检查过了才发下一步。
- `internal/localsecrets` 和 `internal/mcprun` 留给设备上的个人密钥和 `rgd mcp` 这组命令；这一版里它们只有文档。

## 服务端

### 启动

`rgd-server serve`（`internal/server`）依次：加载启动配置（`internal/server/config`：参数 > `RGD_*` > TOML 文件 > 默认值，见 [deploy.zh-CN.md](deploy.zh-CN.md#配置)），拒绝对网络提供明文 HTTP，打开数据库（`dbx`：SQLite 和 PostgreSQL 在同一个接口后面，SQL 方言的差异集中在一处），对 SQLite 数据库在执行待执行的迁移之前做快照（`migrations`，只增不改，每个都有 SQLite 和 PostgreSQL 两段），加载主密钥（`masterkey`），构造各领域服务，启动事件总线和后台任务，然后提供 HTTP 服务。`SIGINT`、`SIGTERM` 让它优雅退出；`SIGHUP` 重新加载证书文件。

### 分层

```
HTTP 层   app.go、http.go：健康检查、就绪检查、安全响应头、请求标识、网页控制台（webui）
  │       httpapi：由 api/openapi.yaml 生成的严格服务端接口
  │       api.go + api_<领域>.go、httpapi/accessapi、httpapi/authnapi：每个领域一个适配层
  ▼
领域层    access  authn  catalog  delivery  settings  audit  events  notify  （secrets  feedback）
  ▼
基础层    config  dbx  migrations  masterkey  jobs
```

HTTP 层负责翻译，领域层负责判断，基础层负责存。领域包之间只允许这些引用（规则 R6）：

| 包 | 可以引用 | 做什么 |
| --- | --- | --- |
| `access` | `audit`、`events` | 用户、产品、成员、三种角色、邀请和开通申请，以及所有的权限判定 |
| `authn` | `access`、`audit`、`events` | 本地账号、会话、身份源、访问令牌、设备码流程 |
| `catalog` | `access`、`audit`、`events` | 资源、草稿、版本、发布、回滚、引用、检查结果 |
| `delivery` | `catalog`、`access`、`secrets`、`audit` | 每个人的期望状态、资源包、启用和停用、安装上报 |
| `settings` | `audit` | 运行期设置和部署向导的状态 |
| `notify` | `access`、`events` | 站内通知和邮件发件箱 |
| `audit`、`events` | 什么都不引用 | 带哈希链的审计日志；进程内的事件总线 |
| `secrets`、`feedback` | 见 `layout.go` | 预留；这一版还没有实现 |

谁都不引用 `notify`：领域包发出一个事件（`catalog.published`、`access.join_requested` 等），通知规则订阅它。领域之间共享的东西都走这几条线，不走别的。

贯穿各层的规矩：

- **权限**只在一个地方判定：`access`，依据一张动作表。表里没有的动作一律拒绝，有测试保证每个动作都有一行。
- **审计**：每一次状态变化都记进 `audit`。它是只能追加的日志，每一条带着前一条的哈希；`rgd-server audit verify` 和每天的后台任务都会核对这条链。
- **静态存放的密钥**用主密钥做信封加密（`masterkey`）；主密钥不进数据库，也不进备份。`rgd-server keys rotate` 重新封装数据密钥。
- **后台工作**以任务的形式运行（`jobs`），带超时和租约，所以同一个 PostgreSQL 上的多个实例不会把同一个任务跑两遍：发件箱、每天的审计链核对、每天的备份等。
- **模块**（`ext/module`、`modhost`）是大的可选功能：经 `modules/all/maybe_<标识>.go` 和构建标签编进来，在设置里打开，拿到 `/api/v1/x/` 下的路由、迁移、任务和事件。现在还没有任何模块。

### 网页控制台

`web/` 是一个 Preact 加 TypeScript 的应用，用 Vite 构建成静态文件，通过 `go build -tags webdist` 嵌进 `rgd-server`（不带这个标签时嵌的是一个占位页，所以 Go 这边永远不需要 Node）。`internal/server/webui` 用严格的内容安全策略提供它，并在每个请求里往 `index.html` 填入基础路径、语言和 nonce，所以控制台挂在子路径下也能用。它的接口类型和服务端来自同一份契约。它的说明是 [web/README.md](../web/README.md)。

## HTTP 接口

[api/openapi.yaml](../api/openapi.yaml)（OpenAPI 3.0）是服务端、`rgd` 和控制台之间的契约，也是三份生成代码的来源：服务端接口（`internal/server/httpapi`）、Go 客户端（`internal/apiclient`）、控制台的 TypeScript 类型（`web/src/api/schema.gen.ts`）。先改契约，再 `go run ./tools/dev gen`；编译器会指出每一处要跟着改的地方。

- 所有路径都在 `/api/v1/` 下。错误是 problem 文档（`application/problem+json`），带稳定的 `code`，比如 `resource.check_blocked`。
- 三种认证方式：浏览器会话 Cookie（跨站请求按 `Sec-Fetch-Site` 和 `Origin` 拒绝）、设备令牌（`rgd`）、带权限范围的访问令牌（脚本）。
- 可编辑的对象带 `ETag`；写入要带 `If-Match`（对象变了返回 `412`，没带返回 `428`）。没有上限的列表用 `limit` 和 `cursor` 分页。
- 服务端还没实现的操作回答 `501`（`api_fallback.go`），所以契约可以走在代码前面。
- 契约测试用 Schema 核对服务端真实的回答。

## 扩展点

`ext/` 下的一切都是公开接口，1.0 之前版本为 v0（不兼容的改动会写进发布说明）。每个包的文档就是它的参考。

| 包 | 扩展什么 | 内置的 | 登记处 |
| --- | --- | --- | --- |
| `ext/adapter` | 一个 AI 编码工具：检测、能力、东西放在哪（一份声明式的档案，必要时加代码） | `claudecode`、`codex`、`opencode` | `builtin/all/adapters.go` |
| `ext/kind` | 一种资源类型：在仓库里怎么放、Schema、解析、按优先顺序的形态 | `skill`、`mcp`、`workflow`、`agent`、`knowledge` | `builtin/all/kinds.go` |
| `ext/check` | 一条检查规则：对一个资源的文件求值的纯函数 | 8 条 | `builtin/all/checks.go` |
| `ext/identity` | 服务端的一种登录方式 | `oidc`、`gitlab` | `builtin/all/server/identity.go` |
| `ext/notify` | 一个通知通道 | `email` | `builtin/all/server/notifiers.go` |
| `ext/module` | 服务端的一个可选模块 | 还没有 | `modules/all/` |
| `ext/source`、`ext/secretbackend` | 服务端从哪取资源、把密钥存在哪 | 还没有 | — |
| `ext/resource` | 共用类型：引用、元数据、资源包 | — | — |

每个可扩展的包都有一个名字以 `test` 结尾的子包作为测试套件（`adaptertest`、`kindtest`、`checktest`）；内置项的测试只有几行，把它交给套件。

## 名字和翻译

**名字。** 产品名、命令名、本机目录、写进其他工具的东西的前缀、环境变量的前缀，都只在 [brand.json](../brand.json) 里定义一次。`go run ./tools/dev gen` 把它们生成到 `internal/brand`（Go）和 `web/src/brand.gen.ts`，`check` 拒绝把它们写成字符串字面量。fork 改名只改这一个文件。

**翻译。** 给人看的每一句文字都在 `locales/<语言>/<领域>.json`：按 i18next 约定的扁平 JSON，`en` 和 `zh-CN` 必须有且完整。Go 代码通过生成的常量引用键（`internal/i18n/msg`）；内置项不能引用 `internal/`，以字符串返回键；控制台用 `t('…')`，键要写全。某个必需语言缺键、占位符不一致、键没人用，`check` 都会失败。命令名、参数名、`--json` 的字段、错误码和日志不翻译。约定和术语见 [locales/README.md](../locales/README.md)。

## 测试

| 层次 | 在哪 | 证明什么 |
| --- | --- | --- |
| 单元和包测试 | 和代码放在一起 | 一切；由 `check` 在临时主目录、不联网的环境里跑 |
| 扩展套件 | `ext/*/…test` | 适配器、资源类型或规则守住了契约（适配器用期望文件，规则用命中和不命中的样例） |
| 示例 | `examples/` | 示例仓库通过全部规则；每个故意犯错的仓库恰好被它那条规则命中；两种语言下 `rgd check` 的输出 |
| 存储 | `internal/server/dbx/dbxtest` | 每个存储测试都在 SQLite 上跑；`RGD_TEST_POSTGRES_DSN` 指向本机的数据库时也在 PostgreSQL 上跑 |
| 契约 | `internal/server` | 服务端的回答符合 `api/openapi.yaml` |
| 端到端 | `test/e2e` | 真实的 `rgd` 和 `rgd-server` 程序：在临时主目录里同步、查看状态、卸载；登录正在运行的服务端并从它同步；服务端停掉后什么都不删 |
| 真实工具（按需） | `test/realtools` | 装在机器上的 Claude Code、Codex、OpenCode 在临时主目录里能读到 `rgd` 写的东西：`RGD_TEST_REAL_TOOLS=claude,codex,opencode` |
| 网页控制台 | `web/` | 格式、lint、类型检查、单元测试（Vitest）和构建；装了 Node 和 pnpm 时是 `check` 的一部分 |

每个测试包都以 `TestMain` 开头，把控制权交给 `internal/testenv`（或者套件自己的 `Main`，做的是同一件事）：它给测试程序一个临时主目录，去掉会指向真实配置的环境变量，关掉系统钥匙串，用会失败的替身顶替外部命令（比如真实的 AI 工具），并阻断回环以外的网络。没有它的测试包 `check` 不放行。

## 生成文件

生成文件都提交进仓库，这样克隆下来不跑生成器也能构建；某个文件过期时 `check` 会失败。`go run ./tools/dev gen` 重写全部：

| 文件 | 来自 |
| --- | --- |
| `internal/brand/brand_gen.go`、`web/src/brand.gen.ts` | `brand.json` |
| `internal/i18n/msg/keys_gen.go`、`web/src/i18n/keys.gen.ts` | `locales/` |
| `schema/repo/v1/kinds/*.schema.json` | 各资源类型的 `schema.json` |
| `internal/server/httpapi/api.gen.go`、`internal/apiclient/api.gen.go` | `api/openapi.yaml` |
| `web/src/api/schema.gen.ts` | `api/openapi.yaml`（需要 Node 和控制台的依赖；没有时报为没跑） |
| `web/src/styles/tokens.gen.css` | `web/tokens/tokens.json` |
