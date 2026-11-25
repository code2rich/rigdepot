<p align="right"><a href="CONTRIBUTING.md">English</a> · <b>简体中文</b></p>

# 参与贡献 Rigdepot

感谢你愿意贡献。这一页带你从克隆走到检查通过，再一步一步走过最常见的几种改动。

提想法和问题同样是贡献，不一定要写代码：开 issue，或者到 [Discord](https://discord.gg/feGwwqs7x) 找我们。Issue 和 PR 用中文或英文都可以。

各部分怎么拼在一起（同步引擎、服务端的分层、扩展点、测试），读完这一页再读 [docs/architecture.zh-CN.md](docs/architecture.zh-CN.md)。

## 这是什么

Rigdepot 把团队的 Skill、MCP、工作流和 Agent 放在一个 Git 仓库里，再装进每个人电脑上的各种 AI 编码工具。仓库里是两个 Go 程序：

- **`rgd`**：命令行，装在每个人的电脑上。它读资源，算出要往每个工具里写什么，给你看，征得同意后写入，记下写过的每一处，并且能原样撤掉。
- **`rgd-server`**：服务端，团队自己部署。它管谁能用什么、版本发布、个人密钥、审计、问题反馈和通知。网页控制台嵌在它里面。

`rgd` 不需要服务端也能用，大部分贡献只碰 `rgd` 这一半。

## 十分钟跑通检查

需要：

- **Go**（版本看 [go.mod](go.mod)）和 **git**。Go 代码只需要这两样。macOS、Linux、Windows 都行；Windows 上不需要 WSL、bash 和 make。
- **Node 22.12 或更新、pnpm**：只有改 `web/` 里的网页控制台才需要。没有它们时，`check` 把控制台那一步列为「没跑」，其余照常。
- **Docker**：只有 `check-linux` 和试部署文件时需要。

```sh
git clone https://github.com/code2rich/rigdepot.git
cd rigdepot
go run ./tools/dev deps      # 下载依赖。只有这一步联网
go run ./tools/dev check     # 全部检查，结尾打印一张结果表
```

第一次 `check` 要把所有代码编译六遍，需要几分钟；之后会快很多。

`go run ./tools/dev <任务>` 是所有开发任务的唯一入口：

| 任务 | 做什么 |
| --- | --- |
| `deps` | 下载主模块和锁定版本的开发工具所需的模块。 |
| `check` | 合并前的全部检查，按顺序：格式、`go vet`、`staticcheck`、生成文件是否最新、结构规则、文案、测试、不带可选模块的构建、六个目标的交叉编译、网页控制台的检查。遇到第一个失败就停，最后打印一张表：哪些通过、哪些失败、哪些没跑。 |
| `test` | 在和 `check` 相同的隔离环境里跑测试。可以带包和 `go test` 的参数：`go run ./tools/dev test ./internal/cli -run TestVersion`。 |
| `gen` | 重新生成生成文件：`brand.json` 里的名字常量、文案键的常量、各资源类型 Schema 的发布副本、`api/openapi.yaml` 生成的接口代码、网页控制台的生成文件。`gen --check` 只报告哪些过期了。 |
| `web-deps` | 按 `web/pnpm-lock.yaml` 安装网页控制台的依赖。需要 Node、pnpm 和网络。 |
| `web` | 把网页控制台构建到 `web/dist`，给 `go build -tags webdist` 用，同时写出它打包的依赖和字体的许可（`web/dist/third-party-notices.txt`）。 |
| `check-linux` | 在本机的 Linux 容器里跑 `check`，让测试真的在 Linux 上执行。需要 Docker。 |
| `release-snapshot` | 把全部发布产物构建两遍到 `dist/`，不发布任何东西，并比较两遍是否一致。 |
| `release-files` | 生成发布压缩包里带的命令补全脚本和第三方声明：Go 模块的许可，加上 `web/dist` 里网页控制台的许可。许可不在 Apache-2.0、MIT、BSD、ISC、OFL 之内就停下；`check` 对控制台的依赖做同样的核对。 |

`Makefile` 里只是这些任务的别名。CI 跑的也是同样的命令。没有任何检查写在 Makefile 或工作流文件里，因为写在那里的检查没法在你自己的电脑上跑。

`check` 和 `test` 跑每个测试时都用临时主目录、`GOFLAGS=-mod=readonly` 和 `GOPROXY=off`；结果表的第一行写着用了哪个目录、是否已经删掉。

## 试一试你的改动

### `rgd`

> **永远不要对着你真实的主目录跑还没发布的 `rgd`。** 它本来就是用来改你的 AI 工具配置的。先给它一个空的主目录。

```sh
go build -o "$PWD/rgd" ./cmd/rgd
env -u CLAUDE_CONFIG_DIR -u CODEX_HOME -u XDG_CONFIG_HOME HOME="$(mktemp -d)" RGD_KEYRING=file sh   # 一个主目录是空的 shell
./rgd sync --repo . --dir examples/resources --plan
./rgd sync --repo . --dir examples/resources
./rgd status --repo . --dir examples/resources
./rgd check examples/resources-broken/secrets
./rgd uninstall
exit
```

在 Windows 上，把 `USERPROFILE` 而不是 `HOME` 指向一个空目录。用 `env -u` 去掉的那几个变量会把某个工具的配置挪到别处；留着的话，它们会指回你真实的配置。`RGD_KEYRING=file` 让试验性的登录不进你的系统钥匙串，钥匙串不会跟着 `HOME` 换。

`examples/resources` 是一个示例资源仓库，能通过全部检查；`examples/resources-broken/<规则>` 是每条检查规则一个的小仓库，恰好违反那一条。`--repo` 读的是已提交的内容，所以想让它看到的改动要先提交（或者先提交到一个分支）。

### `rgd-server`

服务端在本机直接就能跑起来：

```sh
RGD_BASE_URL=http://127.0.0.1:8080 RGD_DATA_DIR="$(mktemp -d)" go run ./cmd/rgd-server
```

它监听 `127.0.0.1:8080`，在数据目录里建 SQLite 数据库和主密钥，并在日志里打印一个一次性的初始化口令（带 `setup_code` 的那一行）。打开 <http://127.0.0.1:8080>，填入口令，创建第一个管理员。之后在那个主目录是空的 shell 里运行 `rgd login http://127.0.0.1:8080`，就能让那个 `rgd` 登录上。全部配置项见 [docs/deploy.zh-CN.md](docs/deploy.zh-CN.md#配置)。

### 网页控制台

这样构建出来的服务端里是一个占位页，不是控制台。要改控制台，先装好 Node 和 pnpm，然后：

```sh
go run ./tools/dev web-deps                       # 第一次，以及 web/pnpm-lock.yaml 变了之后
go run ./tools/dev web                            # 构建到 web/dist
go build -tags webdist -o rgd-server ./cmd/rgd-server
```

改页面的时候，跑 Vite 的开发服务器，让 Go 服务端把控制台转给它，这样改了即时生效，响应头也和生产环境一样：

```sh
pnpm --dir web dev                                                        # http://127.0.0.1:5173
RGD_WEB_DEV_PROXY=http://127.0.0.1:5173 RGD_BASE_URL=http://127.0.0.1:8080 RGD_DATA_DIR="$(mktemp -d)" go run ./cmd/rgd-server
```

打开服务端的地址，不是 5173。`VITE_API_MOCK=1 pnpm --dir web dev` 让控制台的请求由 `web/src/api/mock.ts` 回答，不需要服务端；细节见 [web/README.md](web/README.md)。

## 一张地图

```
cmd/          两个程序的入口，各几行
ext/          公开的扩展接口。你写的东西实现这里的接口
builtin/      内置的实现，一项一个目录                ← 大多数贡献在这里
  adapters/     每个 AI 编码工具一个（claudecode、codex、opencode）
  kinds/        每种资源类型一个（skill、mcp、workflow、agent、knowledge）
  checks/       每条检查规则一个
  identity/     每种登录方式一个（oidc、gitlab）
  notifiers/    每个通知通道一个（email）
  all/          登记处：新增一项，在这里加一行（只有服务端用的在 all/server/）
modules/      可选模块（默认关闭的大功能）；现在还没有
internal/     核心。同步引擎、执行器、清单、服务端和它的各个领域
locales/      翻译，一种语言一个目录                    ← 不用写代码的贡献在这里
api/          HTTP 接口契约（OpenAPI）
web/          网页控制台（Preact、TypeScript、Vite）
schema/       JSON Schema：资源仓库、本机清单、适配器档案
examples/     示例资源仓库，以及每条检查规则一个的「故意犯错」的仓库
test/         端到端测试，以及对着真实 AI 工具的可选测试
deploy/       Compose 文件、systemd 单元、打包脚本、反向代理样例
scripts/      install.sh、install.ps1
docs/         部署指南和架构导览
tools/dev/    开发用的那一条命令
brand.json    代码用到的所有名字，都在这一个文件里
```

每个包都有一个 `doc.go`，用一句话说它是干什么的。不知道某样东西该放哪时，这条命令把它们全列出来：

```sh
go list -f '{{.ImportPath}}: {{.Doc}}' ./...
```

### 三条要记住的规则

`check` 会检查这些规则；违反了，它会告诉你是哪一条、涉及哪两个包。

1. **`builtin/` 里的代码只能用 `ext/`、标准库和一小组第三方模块**，不能引用 `internal/`。内置实现就是写扩展的样例。你需要的东西 `ext/` 里没有，说明接口缺了：开个 issue 说你要什么。
2. **只有执行器写使用者的文件。** 适配器只说「该写什么、写到哪」，不自己动手。
3. **没有 `util`、`common`、`helpers` 这类包。** 一段代码属于哪个领域，就放进哪个领域的包。

完整的规则：

| 规则 | 简述 |
| --- | --- |
| R1 | `ext/**` 只引用标准库和 `ext/**`。两个例外，都是单独的子包：名字以 `test` 结尾的测试套件，以及 `ext/adapter/profileyaml`（可以用 YAML 库）。 |
| R2 | `builtin/**` 只引用标准库、`ext/**` 和允许的第三方模块（列在 `tools/dev/layout.go`）。不引用 `internal/**`，也不引用别的内置项；共用的代码放 `builtin/<类别>/internal/`。 |
| R3 | `internal/**` 不引用 `builtin/**` 和 `modules/**`。核心不带任何内置项和模块也能编译。 |
| R4 | `modules/<标识>/**` 只引用标准库、`ext/**` 和它自己的目录。 |
| R5 | 命令行这边不引用 `internal/server/**`，服务端也不引用命令行这边。`rgd` 不带数据库驱动和登录协议；`rgd-server` 不带改其他工具配置的代码。两个程序都不链接 `testing`。 |
| R6 | 服务端内部：HTTP 层 → 领域包 → 基础层（`config`、`dbx`、`migrations`、`masterkey`、`jobs`）；领域包之间只能按 `tools/dev/layout.go` 列出的方向引用。 |
| R7 | 在使用者的主目录里，除了 Rigdepot 自己的数据目录，只有 `internal/executor` 写文件。 |
| R8 | 没有 `util`、`common`、`helpers`、`misc`、`base` 这类包。 |

`internal/` 下新增的包要登记到 `tools/dev/layout.go`，那是用数据写成的目录结构。

### 一个资源是怎么到工具里的

读懂这条链，就读懂了 `rgd` 的大半：

```
资源仓库里的一个目录             skills/unit-testing/
  │ 资源类型把它解析出来，按工具的能力选一种形态
  ▼
制品                            一个名为 rgd-unit-testing 的技能目录
  │ 适配器知道这个工具把技能放在哪
  ▼
操作                            「把这棵目录树写到 ~/.claude/skills/rgd-unit-testing」
  │ 执行器先在一份临时副本里执行一遍，给你看差异，
  │ 你同意后再真正执行
  ▼
清单条目                        ~/.rigdepot/manifest.json 里的一条；卸载和漂移检测只认它
```

制品只有四种（技能目录、MCP 条目、斜杠命令、子 Agent），操作也只有四种（写目录树、写文件、设配置键、运行工具自己的命令）。预览和真正写入是同一组操作，只是根目录不同，所以两者不会不一致。

## 常见的贡献

大多是同一个动作：**在一个目录里加文件，在登记处加一行。** 内置项没登记时 `check` 会失败，并告诉你要加哪一行。

### 新增一条检查规则

规则是一个纯函数：给它一个资源的全部文件，它返回发现的问题。它读不了磁盘上别的东西，联不了网，也不能运行命令。参考文档是 `go doc ./ext/check` 和 `go doc ./ext/check/checktest`；最短的真实规则是 `builtin/checks/bundledenv`，照着抄。

举个例子，一条叫 `todo` 的规则，指出 Skill 里遗留的 `TODO`、`FIXME` 标记：

1. **建目录 `builtin/checks/todo/`**（目录名就是规则的标识），写 `doc.go`：

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

   和 `rule.go`：

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

   代码里的注释和文档用英文写，和仓库里其他代码一致。

2. **准备样例**，放在 `builtin/checks/todo/testdata/`。每个样例是一个很小的资源仓库；至少一个该命中的，一个**看起来很像但不该命中**的：

   ```
   testdata/hit/marker-left/skills/demo/SKILL.md        「…… TODO: write the steps ……」
   testdata/miss/word-in-text/skills/demo/SKILL.md      「…… list the todos in the issue ……」
   ```

   `SKILL.md` 要有带 `name`（等于目录名）和 `description` 的 frontmatter。`testdata/` 下的文件不带协议头。

3. **写测试** `rule_test.go`：

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

   测试套件检查：每个 `hit/` 样例至少有一条发现，每个 `miss/` 样例一条都没有；发现指向真实存在的文件和行；跑两遍结果相同；文件读不了时返回错误，而不是「什么都没发现」。

4. **写文案**：在 `locales/en/check.json` 和 `locales/zh-CN/check.json` 里各加一行，放在按字母排序的位置：

   ```json
   "check.todo.found": "{{file}} line {{line}} still has a TODO or FIXME marker. Finish it, or move it to an issue.",
   ```

   ```json
   "check.todo.found": "{{file}} 第 {{line}} 行还留着 TODO 或 FIXME。写完它，或者挪进 issue。",
   ```

   然后跑 `go run ./tools/dev gen`：它把文件排好序，并重新生成文案键的常量，`check` 会核对这些常量。

5. **登记**：在 `builtin/all/checks.go` 里按字母顺序加一行，并加上 import：

   ```go
   check.Register(todo.New())
   ```

6. **加一个示例**：`examples/resources-broken/todo/`，一个恰好违反这条规则、不违反别的规则的小资源仓库（复制 `examples/resources-broken/description/`，改一下那个 Skill），并在那个目录的 `README.md` 里加一行。它的 `rigdepot.yaml` 和 `meta.yaml` 要带协议头，`testdata/` 以外的 YAML 都要带。示例的测试会确认每条规则都有这样一个目录，并且 `examples/resources` 仍然通过全部规则。

7. **跑 `go run ./tools/dev check`**，再站在使用者的角度看一眼：

   ```sh
   go run ./cmd/rgd check examples/resources-broken/todo
   go run ./cmd/rgd check --lang zh-CN examples/resources-broken/todo
   ```

两件要小心的事。一条规则是拦发布（「必须修复」）还是只提醒，是产品决定：新规则一律先做成 `Advisory`，在 PR 里说明你建议的档位和理由。规则命中了密钥之类的敏感内容时，发现里不能带命中的原文，`Params` 里不能带，文案键的名字里也不能带。

### 给 `rgd doctor` 加一个自检项

`rgd doctor` 依次运行一组自检项，每项看这台电脑上的一样东西。自检项只看不修；读不了的报「未知」，绝不报「正常」。它们在 `internal/doctor`，一项一个文件；`git.go` 比较短，可以照着抄。

1. **写 `internal/doctor/<项>.go`**：一个类型，带 `ID()`（稳定的小写英文单词，用短横线连接；它是 `rgd doctor --json` 里的 `check` 字段）、`Title()`（名字的文案键，`msg.Doctor<项>Title`）和 `Run(ctx, env)`。`Run` 至少返回一个 `Result`，带 `Status`、稳定的 `Code` 和 `Message`；状态不是正常时再带一个 `Hint`。它从电脑上要的东西都来自 `Env`；`Env` 里没有的，加一个字段，并在命令构造 `Env` 的地方（`internal/cli/doctor.go`）填上。
2. **加一行**：在 `internal/doctor/doctor.go` 的 `Checks()` 列表里，按人读的顺序放到合适的位置。
3. **写文案**：在 `locales/en/cli.json` 和 `locales/zh-CN/cli.json` 里加 `doctor.<项>.*`，其中要有 `doctor.<项>.title`，然后 `go run ./tools/dev gen` 生成 `msg` 常量。
4. **写 `<项>_test.go`**：至少一个通过的用例和一个不通过的用例，各自用测试自己建的临时主目录（`doctor_test.go` 里的 `newTestEnv`）。
5. `go run ./tools/dev check`，再在主目录是空的 shell 里跑 `rgd doctor` 和 `rgd doctor --json`。

### 新增一种语言

不用写代码。

1. 复制 `locales/en/` 为 `locales/<语言标签>/`，标签用 BCP 47，比如 `ja`、`de`、`zh-TW`。
2. 翻译**值**。键不动，`{{…}}` 里的占位符不动。
3. 改 `meta.json`：这种语言自己的名字、英文名、维护人。
4. `go run ./tools/dev check` 会报占位符不一致和 JSON 写错的地方，并告诉你完成了多少。

没译完也可以提交，缺的会显示英文。有哪些语言是从目录里读出来的，不需要在任何地方登记。有些词在所有语言里都不译（Skill、MCP、Agent）；其他术语怎么译，看 [locales/README.md](locales/README.md#terms) 的术语表。

### 网页控制台里的页面或改动

控制台在 `web/`：Preact、TypeScript、Vite，构建成静态文件嵌进服务端。先读 [web/README.md](web/README.md)，简单说：

1. 建 `web/src/pages/<标识>/index.tsx`，默认导出这个页面，开头用 `<PageHeader page="<标识>" … />`。
2. 在 `web/src/pages/registry.ts` 里登记：一个按需加载的路由，放进导航；导航以外的页面放进 `PUBLIC`。
3. 样式写在 `web/src/pages/<标识>/<标识>.css`，颜色只用设计令牌（`var(--ink)` 等）。
4. 每一句文字都来自 `locales/en/web.json` 和 `locales/zh-CN/web.json`，键以页面标识开头，在 `t('…')` 里写全；然后 `go run ./tools/dev gen`。
5. 数据通过 `web/src/api/client.ts` 里的 `api` 取，类型来自 `api/openapi.yaml`。加载中、失败、空列表三种状态都要有。
6. `go run ./tools/dev check`（装好 Node 和 pnpm，控制台那一步才会跑），再在中英文、浅色深色、390 像素宽下各看一遍。

服务端会发一个严格的内容安全策略：不手写内联脚本和样式，不用 `dangerouslySetInnerHTML`，不从别的地址加载任何东西；所有链接都经过 `href()`，因为控制台可能挂在子路径下。

### 新增一个 AI 编码工具（适配器）

适配器告诉 `rgd`：一个 AI 工具把 Skill、MCP、命令、Agent 放在哪、是什么格式，哪些文件是凭据。大部分写在一份声明式的档案里，档案表达不了的才写代码。可以读 `builtin/adapters/codex` 和 `builtin/adapters/opencode`。下面的核对要用到真实的工具，所以开始之前请先开个 issue。

1. **弄清楚这个工具真正把东西放在哪。** 不要照着文章猜。在一个临时主目录里运行工具自己的命令，看它写了什么：

   ```sh
   tmp="$(mktemp -d)"
   HOME="$tmp" USERPROFILE="$tmp" XDG_CONFIG_HOME="$tmp/.config" <工具> mcp add probe -- echo hi
   find "$tmp" -type f
   ```

2. **写档案** `builtin/adapters/<工具>/profile.yaml`：怎么检测这个工具（程序、目录、怎么读版本号），它的配置根目录和挪动它的环境变量，Skill、命令、Agent、MCP 条目写到哪、用什么格式，哪些文件是凭据。Schema 是 `schema/adapter/v1`。档案必须带 `verified` 记录：工具版本、操作系统、日期、怎么核对的。没有这条记录的档案不会写任何东西。
3. **写适配器** `adapter.go`：`New()` 返回 `adapter.FromProfile(profileyaml.MustParse(profileYAML))`，`profile.yaml` 用 embed 嵌进来。档案表达不了的地方（某个版本挪了键的位置、密钥要按工具自己的方式引用），把它嵌进你自己的类型，覆盖 `Plan` 或 `Capabilities`，现有的三个适配器都是这么做的。
4. **准备夹具** `testdata/homes/<情形>/`：一个 `home/`，用真实工具生成的文件做（去掉账号信息）；一个 `README.md` 说明这是什么情形；需要指定系统、环境变量或版本时再加一个 `case.json`。至少要有：什么都没配的、有使用者自己的配置的（带注释、带不认识的键、带同名条目）、配置文件坏了的、目标位置被占用的。
5. **跑契约测试套件** `adaptertest.Run(t, New(), "testdata")`。用 `go test ./builtin/adapters/<工具> -run TestContract -update` 生成一次期望结果，然后**逐个读一遍** `testdata/golden/` 里的文件：使用者原有的内容是不是一个字节都没变，我们加的是不是工具认的格式。
6. **登记**：在 `builtin/all/adapters.go` 加一行 `adapter.Register(<工具>.New())`。
7. `go run ./tools/dev check`。机器上装着这个工具的话，再跑对着真实工具的测试（它们在临时主目录里跑）；给新工具扩展 `test/realtools`，把输出贴进 PR：

   ```sh
   RGD_TEST_REAL_TOOLS=claude,codex,opencode go test -count=1 -v ./test/realtools
   ```

PR 里写明：核对的工具版本、怎么核对的、哪些形态原生支持、哪些降级。

### 新增一种资源类型

先问一句：它能不能渲染成已有的四种制品之一？能的话只动一个目录。需要新的制品形状就不是「加一种类型」了，那会牵动每个适配器，先开 issue 讨论。加一种类型同时是产品决定，所以无论如何，PR 之前先开 issue。

1. 建目录 `builtin/kinds/<类型>/`，放它的 JSON Schema（`schema.json`）和代码：`ID`、`Layout`（在仓库里放在哪，是目录还是单个文件）、`Schema`、`Parse`，以及 `Forms`，即它能下发成的形态，按优先顺序。参考文档是 `go doc ./ext/kind`；可以读 `builtin/kinds/skill` 和 `builtin/kinds/mcp`。
2. 在 `testdata/` 里放正反样例，跑类型的测试套件 `kindtest.Run`。
3. 登记：在 `builtin/all/kinds.go` 加一行 `kind.Register(<类型>.New())`。
4. `go run ./tools/dev gen`（它把 Schema 复制到 `schema/repo/v1/kinds/`），再 `check`。
5. 想让控制台显示这种类型的名字，而不是当成通用资源，在 `web/src/ui/resource.tsx` 的 `KIND_NAME`、`KIND_ICON` 里加上它，并在两个 `web.json` 里加 `res.kind.<类型>`。

### 通知通道和登录方式

这两个是服务端的扩展点：`ext/notify`（内置的是 `builtin/notifiers/email`）和 `ext/identity`（内置的是 `builtin/identity/oidc`、`builtin/identity/gitlab`），登记在 `builtin/all/server/`。这一版的服务端只通过邮件发通知，所以新的通道暂时用不上。写这两类之前请先开 issue。

通知通道和登录方式天生要往外发数据，所以这类 PR 一定由维护者亲自评审。写的时候守住三条：只发到部署方配置的地址（或收件人自己的地址），地址不能取自消息内容；只发给你的那些字段；错误信息里不要带配置里的令牌和密钥。

## 会被拦下的做法

这个项目要改别人电脑上其他工具的配置，所以有几条比一般项目严的规矩。

| 做法 | 为什么不行 | 应该怎么做 |
| --- | --- | --- |
| 测试读写真实的主目录（`~/.claude`、`~/.codex`…） | 会改坏跑测试的人的配置 | 每个测试包都以 `func TestMain(m *testing.M) { testenv.Main(m) }`（或测试套件自己的 `Main`）开头；没有的话 `check` 会失败 |
| 测试联网 | 不稳定，还可能把东西发出去 | 用本地的假服务器（`httptest`），回复内容取自保存下来的真实回复 |
| 配置文件解析失败时当成空文件 | 会覆盖使用者的配置 | 解析失败就是未知，未知就不写，并说明原因 |
| 覆盖或删除不在清单里的东西 | 那是使用者的 | 只动清单里的；名字被占用就跳过并说明 |
| 照着字段名猜别的工具的配置格式 | 类型写错可能让工具启动失败 | 看它的文档或源码，在临时主目录里实测，写进档案 |
| 修了一个缺陷，却没有「去掉修复就失败」的测试 | 没法证明修好了 | 先写出会失败的测试，把它失败时的输出贴进 PR |
| 顺手重构、顺手加功能 | 没法评审 | 一个 PR 一个目标 |

下面这些改动原则上可以，但**评审会暂停，等维护者亲自看过**。提前在 issue 里说一声会快很多：

- 新增第三方依赖（Go 或网页控制台的）。
- 新增网络地址、任何上报，或者把数据发到以前不去的地方。
- 读写凭据文件或钥匙串，或者让适配器写到新的位置。
- 改 `.github/`、发布配置或安装脚本。
- 放宽检查规则，或者多采集字段。

## 提交

- **一个提交只做一件事。** 文档和代码在同一个提交里改。无关的重构单独提交。
- 第一行说做了什么。正文说为什么，以及跑了什么、没跑什么。超过 400 行的提交必须有正文。中文英文都可以；最好带上历史里用的类型前缀（`feat:`、`fix:`、`docs:`、`test:`、`build:`、`ci:`、`chore:`）。
- 每个新的源文件开头都带协议头，用这个文件自己的注释语法：

  ```
  SPDX-License-Identifier: Apache-2.0
  Copyright 2026 The Rigdepot Authors
  ```

  年份是文件创建的那一年。Go、TypeScript、CSS、shell、PowerShell、YAML、TOML、SQL、systemd 单元、Dockerfile 和 Makefile 要带；Markdown、JSON、锁文件、生成文件和 `testdata/` 下的任何文件不带。缺协议头的文件 `check` 会列出来。
- 不要提交密钥、令牌、内部地址、真实的人名、`.env` 文件和本机配置。示例一律用 `example.com` 和 `<占位：令牌>` 这样的占位符。
- 名字（产品名、命令名、前缀、本机目录、环境变量前缀）都来自 `brand.json`，经生成的 `internal/brand` 包引用。把它们写成字符串字面量，`check` 会失败。

### 开发者原创声明（DCO）

不需要签贡献者协议。取而代之的是：每个提交带一行 `Signed-off-by`，表示这个改动是你写的，或者你有权按本项目的协议提交它。全文见 [DCO](DCO)。

```sh
git commit -s -m "fix: keep a user's own MCP entry when the name is taken"
```

`-s` 会用你 Git 里的名字和邮箱加上这一行。名字请用真名或大家认识你的名字，邮箱要能联系到你。

忘了加？

```sh
git commit --amend -s --no-edit          # 最后一个提交
git rebase --signoff main                # 你分支上的每个提交
```

没有签名的提交不会被合并。

## PR 流程

1. **比小修复大的改动，先开 issue** 说你想做什么。免得做完了合不进来。
2. 在你 fork 的分支上做。一个 PR 一个目标。
3. 跑 `go run ./tools/dev check`，把结果表贴进 PR。没跑的步骤列为没跑，不要省略。
4. 填 PR 模板。模板就是下面的验收清单。
5. 评审者会拉你最新的提交，合到当前的主分支上，对合并结果跑检查。他会试着把它弄坏：去掉你的修复，看你的测试能不能发现。评审中途你又推了新提交，评审从头开始。
6. 草稿状态的 PR 不合并。

### 验收清单

下面每一条都给出了证据，改动才会被合并：

1. **一个不带这个改动就失败的测试。** 把旧代码放回去，跑新测试，看到真正的断言失败，而不是编译错误或测试被跳过。说明是哪个测试、它打印了什么。
2. **测试在沙箱里跑。** `check` 和 `test` 就是这么做的：临时主目录，不联网。
3. **全部测试和跨平台构建。** 就是 `check` 的结果表。为一个平台交叉编译不等于在那个平台上测过；说明测试真正在哪些平台上跑过。
4. **尽可能真实地用一下。** 改了某个工具的适配器，就在临时主目录里对着真实工具核对。改了控制台，就在浏览器里看一遍。做不到的，写明没做。
5. **文档同步更新**，或者说明为什么不需要。

## 在 Windows 上开发

- 不需要 WSL、bash 和 make。`go run ./tools/dev check` 直接在 PowerShell 或 cmd 里跑。
- 克隆之前不用改 Git 的换行设置。`.gitattributes` 已经处理好了，测试夹具按二进制处理。
- 有些测试只在 Windows 上跑（文件名以 `_windows_test.go` 结尾）。Windows 上的测试结果对我们很有价值，在真实的 Windows 机器上验证是项目现在最缺的。

## 从哪开始

适合作为第一个贡献的，每个都小、边界清楚：

- 一条检查规则。
- `rgd doctor` 的一个自检项。
- 一种语言的翻译。
- `examples/resources` 里的一个示例资源。
- 适配器的一个新夹具：工具的新版本，或者还没覆盖到的一种配置写法。
- 文档。

它们在 GitHub 上标着 `good first issue`。

## 行为准则和安全

参与的每个人都应遵守[行为准则](CODE_OF_CONDUCT.zh-CN.md)。发现漏洞请按 [SECURITY.zh-CN.md](SECURITY.zh-CN.md) 私下报告，不要开公开的 issue。
