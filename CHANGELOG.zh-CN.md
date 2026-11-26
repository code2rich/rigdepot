<p align="right"><a href="CHANGELOG.md">English</a> · <b>简体中文</b></p>

# 更新记录

每个版本一节，新的在上。GitHub 发布页上的发版说明取自 [CHANGELOG.md](CHANGELOG.md)（英文）里该版本标题下的文字，由 `go run ./tools/dev release` 发布；候选版本（`v0.2.0-rc.1`）用它对应的那个版本的一节。这一份是它的中文版，两份一起改。1.0 之前，次版本号（`0.Y.0`）的变化可以包含不兼容的改动，写在「需要注意」里。

## 0.1.0（2026-11-25）

Rigdepot 的第一个版本：命令行 `rgd`，以及团队服务端 `rgd-server` 和它的网页控制台。两者出自同一个仓库，版本号相同。

### 包含什么

- **不连服务端的 `rgd`。** `sync --repo` 把一个 Git 仓库里的 Skill、MCP、工作流、Agent 和知识库装进 Claude Code、Codex 和 OpenCode。`sync --plan` 先列出要动的每个文件和每条配置；不经同意不改工具的全局配置。`status`（带 `--diff`）找出之后被改过的，`uninstall` 只撤销本机清单里记着的，`check` 跑八条入库检查规则，`doctor` 检查本机环境。另有 `enable` / `disable`、`flow next`、`config` 和命令补全。
- **连服务端的 `rgd`。** `login`（在浏览器里确认一个码）、从服务端目录 `sync` 并上报安装结果、按设备加密下发的个人密钥（`secret`）、填好密钥再启动的 MCP（`mcp run`，以及把它们合成一个入口的 `mcp serve`）、`feedback`。
- **`rgd-server`。** 初始化向导、本地账号、OIDC 和 GitLab 登录、邀请和开通申请、产品及其成员和角色、访问令牌、定时拉取的资源仓库、目录（草稿、入库检查、版本、发布和回滚）、上传资源包和在网页上登记 MCP、向 `rgd` 下发、静态加密的个人密钥、反馈、站内和邮件通知、带哈希链的审计日志、HTTPS（证书文件或 ACME）、备份、`doctor`、`config check`、指标，以及从自己的地址分发 `rgd`，给不能上外网的网络用。
- **网页控制台**，嵌在 `rgd-server` 里：M1 的全部页面，中英文、亮色暗色。
- **怎么获取。** `rgd` 在 macOS、Linux、Windows 的 amd64 和 arm64 上的压缩包；`install.sh` 和 `install.ps1`（下载、核对 SHA-256、放到位，不做别的）；Homebrew cask（`brew install --cask code2rich/tap/rigdepot`）和 Scoop 清单（`code2rich/scoop-bucket`）。服务端有 Linux 压缩包、带 systemd 单元的 Debian 和 RPM 包，以及 linux/amd64 和 linux/arm64 的容器镜像（`ghcr.io/code2rich/rigdepot-server`），镜像另有一份给 `docker load` 用的文件，供拉不到镜像仓库的网络。所有文件都列在 `checksums.txt` 里。第三方许可在每个压缩包和安装包的 `THIRD_PARTY_NOTICES` 里。

### 已知限制

- **没有在 Windows 机器上验证过。** `rgd` 交叉编译了 Windows 的 amd64 和 arm64 版本，但程序和 `install.ps1` 都没在 Windows 上跑过。Linux 只在容器里验证过，没在 Linux 真机上验证。发布之前，发版工作流在 GitHub Actions 的 Linux 上跑了完整的 `go run ./tools/dev check`，macOS 上也跑过；浏览器测试（`tools/dev e2e-web`）只在 macOS 上跑过。
- **可执行文件没有签名，也没有公证。** 在 macOS 上用浏览器下载的 `rgd` 第一次运行会被 Gatekeeper 拦下；安装脚本和 Homebrew 不受影响（cask 会去掉它装的那一个文件的隔离标记）。在 Windows 上用浏览器下载可能出现 SmartScreen 警告；Scoop 和安装脚本不会。`checksums.txt` 也没有签名：下载后用 SHA-256 对照它核对。
- 发版的机器上装了 `syft` 才附带 **SBOM**。
- 单一入口的 MCP（`rgd mcp serve`）在 Claude Code 和 Codex 上验证过，OpenCode 上还没有。
- 没有无人值守的服务端初始化（`rgd-server setup --from`），请用初始化向导。
- 服务端有些文字只有英文：接口返回的错误说明、审计动作名、检查规则的名字。

### 升级

这是第一个版本，没有可以升级的旧版本。本机清单、资源仓库格式（`schema: 1`）和接口（`/api/v1`）从这里开始；以后的版本都能读这个版本写下的东西。
