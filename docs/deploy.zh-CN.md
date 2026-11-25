<p align="right"><a href="deploy.md">English</a> · <b>简体中文</b></p>

# 部署与升级 rgd-server

`rgd-server` 是一个程序加一个数据目录。这份指南带你从一台空机器走到管理员登录，再讲 HTTPS、配置、分发命令行、备份、升级和日常运维。部署过 Web 服务的人，一天之内应该能做完。

文中提到的文件都在 [deploy/](../deploy/)。示例里服务端的对外地址写成 `hub.example.com`，换成你们自己的。

## 目录

- [开始之前](#开始之前)
- [选一种形态](#选一种形态)
- [Docker Compose + SQLite](#docker-compose--sqlite)
- [Docker Compose + PostgreSQL](#docker-compose--postgresql)
- [Debian 或 RPM 包 + systemd](#debian-或-rpm-包--systemd)
- [其他运行方式](#其他运行方式)：单独用容器镜像、单个可执行文件
- [反向代理](#反向代理)
- [HTTPS](#https)
- [首次启动和部署向导](#首次启动和部署向导)
- [配置](#配置)
- [给内网分发命令行](#给内网分发命令行)
- [备份与恢复](#备份与恢复)
- [升级](#升级)
- [运维命令](#运维命令)
- [健康检查和指标](#健康检查和指标)
- [最低配置](#最低配置)
- [出了问题](#出了问题)

## 开始之前

需要：

- **一台 Linux 机器**，amd64 或 arm64。macOS 上也能跑起来试用；Windows 不作为服务端的支持平台。
- **一个地址**，比如 `hub.example.com`，大家的浏览器和命令行都能访问到。登录回调、邮件里的链接、Cookie、安装命令都依赖这个地址，所以第一次启动之前就定下来。
- **HTTPS。** 个人凭据要经过这条连接，所以服务端拒绝在本机以外用明文 HTTP。通常由你们已有的反向代理做 HTTPS；服务端自己也能做（见 [HTTPS](#https)）。
- **能访问它要连的东西**：你们的资源仓库、身份源（OIDC 或 GitLab）和邮件服务器。服务端不会主动访问项目方或任何别的地址。
- **`git`**，2.31 或更新，用来拉资源仓库。镜像里有，安装包把它列为依赖。

要备份的有两样：**数据库**和**主密钥**。这两样故意分开放，见[备份与恢复](#备份与恢复)。

## 选一种形态

| 形态 | 什么时候选 | 数据库 | 章节 |
| --- | --- | --- | --- |
| Docker Compose | 想一条命令起来，机器上有 Docker | SQLite，放在一个卷里 | [Compose + SQLite](#docker-compose--sqlite) |
| Docker Compose + PostgreSQL | 组织要求数据放在 PostgreSQL 里 | 另一个容器里的 PostgreSQL | [Compose + PostgreSQL](#docker-compose--postgresql) |
| Debian 或 RPM 包 | 一台 Linux 机器，不用容器 | `/var/lib/rigdepot` 里的 SQLite，或外部的 PostgreSQL | [安装包 + systemd](#debian-或-rpm-包--systemd) |
| 容器镜像 | 按自己的方式跑容器（Kubernetes、Nomad、直接 `docker run`） | 挂一个卷放 SQLite，或外部的 PostgreSQL | [其他运行方式](#其他运行方式) |

SQLite 是默认的，一台机器上几百人的团队够用。要跑多个实例，或者组织要求用托管数据库时，选 PostgreSQL。

每种形态的数据目录里放的东西都一样：

| 数据目录里的路径 | 是什么 | 要备份吗 |
| --- | --- | --- |
| `rigdepot.db`（运行时旁边还有 `-wal`、`-shm`） | SQLite 数据库 | 要，用 `rgd-server backup` |
| `master.key` | 主密钥（没有另外指定时） | 要，单独保管 |
| `cache/repos/` | 资源仓库的裸克隆 | 不用，会重新拉 |
| `acme/` | 服务端自己申请的证书（开了才有） | 不用，会重新申请 |
| `backups/` | `rgd-server backup` 和迁移前的快照（没设 `backup.dir` 时） | 复制到别的机器上 |
| `cli/` | 可选：要分发的 `rgd` 发布压缩包（见[给内网分发命令行](#给内网分发命令行)） | 不用 |

## Docker Compose + SQLite

需要装了 Compose 插件的 Docker（`docker compose version` 有输出），以及一份 [deploy/](../deploy/) 目录：克隆仓库，或者从发布页下载。

**1. 配置。** 在 `deploy/` 里：

```sh
cp .env.example .env
```

编辑 `.env`，至少写上对外地址：

```sh
RGD_BASE_URL=https://hub.example.com
```

`.env.example` 里其余几行是可选的：

| 变量 | 默认 | 什么时候设 |
| --- | --- | --- |
| `RGD_VERSION` | `latest` | 生产环境一定要设：写死完整版本号，比如 `0.4.0`。 |
| `RGD_PORT` | `8080` | 本机 8080 端口被占用时。服务端只发布在 `127.0.0.1:<端口>` 上。 |
| `RGD_SUBNET` | `172.31.250.0/24` | 这个网段和你们已有的网络重叠时；`docker compose up` 会报出来。 |
| `RGD_TRUSTED_PROXIES` | `RGD_SUBNET` 的值 | 反向代理不在这台机器上时：填代理的地址。 |

**2. 启动。**

```sh
docker compose up -d
```

Docker 会拉取 `ghcr.io/code2rich/rigdepot-server`。访问不了这个镜像仓库时，先从发布页下载镜像文件并 `docker load -i rigdepot-server_<版本>_linux_<架构>.docker.tar.gz`，或者用 `docker compose build` 从仓库源码构建镜像（全部在 Docker 里编译，要几分钟）。

**3. 确认它在跑。**

```sh
docker compose ps                          # 几秒后容器变成 healthy
curl -s http://127.0.0.1:8080/readyz       # {"status":"ok",…}；邮件没配置时是 degraded
docker compose logs rigdepot | grep setup_code
```

最后一条命令显示一次性的**初始化口令**：

```json
{"level":"WARN","msg":"setup is not complete: open the server's address in a browser and enter this one-time setup code","base_url":"https://hub.example.com","setup_code":"<占位：初始化口令>"}
```

**4. 在前面放上反向代理**，转发到 `127.0.0.1:8080`，见[反向代理](#反向代理)。代理在这台机器上时，`RGD_TRUSTED_PROXIES` 已经覆盖了它。

**5. 打开 `https://hub.example.com`**，用初始化口令走完[部署向导](#首次启动和部署向导)。你就以第一个管理员的身份登录了。

**6. 马上备份主密钥**，放到这台机器的备份以外的地方，并安排好数据库的备份（见[备份与恢复](#备份与恢复)）：

```sh
docker compose cp rigdepot:/var/lib/rigdepot/master.key ./master.key   # 然后挪到安全的地方
```

Compose 建了这些：一个容器，以无特权用户（10001）运行，根文件系统只读，没有任何 capability，带 `no-new-privileges`；一个卷 `rigdepot-data`，要留的东西都在里面；一个固定网段 `RGD_SUBNET` 的网络，好让 `RGD_TRUSTED_PROXIES` 能指名它。主机上的反向代理经这个网络的网关访问容器，别的东西访问不到这个端口。

在容器里运行命令：`docker compose exec rigdepot rgd-server <命令>`，比如 `docker compose exec rigdepot rgd-server doctor`。在容器里服务端只能监听 `0.0.0.0:8080`；Compose 只把端口发布在 `127.0.0.1` 上，`config check` 知道自己在容器里，不会就监听地址提示。

> **在自己的电脑上试一试。** 设 `RGD_BASE_URL=http://localhost:8080`，打开这个地址就行，不需要代理。`config check` 会给一条提示：声明了反向代理，但地址是本机的 `http://`。这是试用，不算问题；换成正式的 `https://` 地址、加上代理后就没有了。

**其他配置项。** `compose.yaml` 只传上面这几个变量。要用[配置](#配置)里的其他项，在旁边建一个 `compose.override.yaml`，Compose 会自动读它：

```yaml
services:
  rigdepot:
    environment:
      RGD_BACKUP_DIR: /var/lib/rigdepot/backups
      RGD_DEFAULT_LOCALE: zh-CN
```

`.env` 里设了 `COMPOSE_FILE` 时（比如用 PostgreSQL），要把它加进那个列表：`COMPOSE_FILE=compose.yaml:compose.postgres.yaml:compose.override.yaml`。

## Docker Compose + PostgreSQL

和上面一样，再加一个 PostgreSQL 17 容器。`docker compose up` 之前，在 `deploy/secrets/` 里建两个文件（Git 忽略这个目录）：

```sh
(umask 077; openssl rand -hex 24 > secrets/postgres_password)
printf 'postgres://rigdepot:%s@postgres:5432/rigdepot\n' "$(cat secrets/postgres_password)" > secrets/database_url
sudo chown 10001 secrets/database_url && sudo chmod 400 secrets/database_url
```

容器里的服务端以用户 10001 运行，要能读 `database_url`。然后两个文件一起启动：

```sh
docker compose -f compose.yaml -f compose.postgres.yaml up -d
```

或者在 `.env` 里写 `COMPOSE_FILE=compose.yaml:compose.postgres.yaml`，再 `docker compose up -d`。数据卷 `rigdepot-data` 仍然需要：主密钥和仓库缓存在里面。数据库在 `postgres-data` 卷里。

要用你们已有的 PostgreSQL：不要 `compose.postgres.yaml`，通过 `compose.override.yaml` 传 `RGD_DATABASE_URL_FILE`（或 `RGD_DATABASE_URL`）。服务端需要一个归它所有的数据库，表由它自己建。

## Debian 或 RPM 包 + systemd

从发布页下载对应架构的安装包：

```sh
sudo apt install ./rigdepot-server_0.4.0_amd64.deb        # Debian、Ubuntu
sudo dnf install ./rigdepot-server-0.4.0-1.x86_64.rpm     # Fedora、Rocky Linux、RHEL
```

安装包装这些：

| 路径 | 是什么 |
| --- | --- |
| `/usr/bin/rgd-server` | 程序 |
| `/usr/lib/systemd/system/rigdepot-server.service` | systemd 单元 |
| `/etc/rigdepot/server.env` | 启动配置，`0600`，升级时保留 |
| `/var/lib/rigdepot/` | 数据目录，`0700`，属于系统用户 `rigdepot` |
| `/usr/share/rigdepot/cli/` | 同一版本的六个 `rgd` 压缩包，用来分发 |

它依赖 `git`，装完不会启动服务端，因为得先有地址：

```sh
sudoedit /etc/rigdepot/server.env
```

```sh
RGD_BASE_URL=https://hub.example.com
RGD_TRUSTED_PROXIES=127.0.0.1           # 反向代理在这台机器上
```

```sh
sudo systemctl enable --now rigdepot-server
journalctl -u rigdepot-server | grep setup_code
```

然后和 Compose 一样：反向代理、[部署向导](#首次启动和部署向导)、备份主密钥。运维命令以服务用户、用同一份配置运行：

```sh
sudo -u rigdepot sh -c 'set -a; . /etc/rigdepot/server.env; exec rgd-server doctor'
```

升级安装包会重启正在运行的服务端。卸载会停掉服务端，数据目录、配置和用户都留着。

单元只允许进程写数据目录（`ProtectSystem=strict`、`ReadWritePaths=/var/lib/rigdepot`），用 `GOMEMLIMIT=384MiB` 和 `MemoryMax=512M` 限制内存；这些数字是给小机器的起点。要把 `backup.dir` 放在数据目录以外，用 `systemctl edit rigdepot-server` 把它加进 `ReadWritePaths`。

用 PostgreSQL 时，在 `server.env` 里写 `RGD_DATABASE_URL=postgres://…`（或 `RGD_DATABASE_URL_FILE=<文件>`）。

## 其他运行方式

### 单独用容器镜像

```sh
docker run -d --name rigdepot --read-only --tmpfs /tmp -p 127.0.0.1:8080:8080 \
  -e RGD_BASE_URL=https://hub.example.com -e RGD_TRUSTED_PROXIES=172.16.0.0/12 \
  -v rigdepot-data:/var/lib/rigdepot ghcr.io/code2rich/rigdepot-server:0.4.0
```

一个标签里同时有 `linux/amd64` 和 `linux/arm64`。镜像里的服务端监听 `0.0.0.0:8080`，以用户 10001 运行，数据放在 `/var/lib/rigdepot`，带健康检查（`rgd-server healthcheck`）。因为它监听容器的所有网卡，需要 `RGD_TRUSTED_PROXIES`（代理的请求从哪个网段来）、证书，或者隔离试用时的 `RGD_INSECURE_HTTP=1` 三者之一。访问不了镜像仓库时，从发布页下载镜像文件 `docker load -i`。从仓库源码构建：`docker build -t rigdepot-server .`（网页控制台、服务端和六个平台的 `rgd` 都在构建过程里编译）。

### 不用安装包，单个可执行文件

用发布页的压缩包 `rgd-server_<版本>_linux_<架构>.tar.gz`：

```sh
sudo useradd --system --home-dir /var/lib/rigdepot --shell /usr/sbin/nologin rigdepot
sudo install -d -o rigdepot -g rigdepot -m 0700 /var/lib/rigdepot
sudo install -d -m 0755 /etc/rigdepot
printf 'RGD_BASE_URL=https://hub.example.com\nRGD_TRUSTED_PROXIES=127.0.0.1\n' | sudo install -m 0600 /dev/stdin /etc/rigdepot/server.env
sudo install -m 0755 rgd-server /usr/bin/rgd-server
sudo install -m 0644 deploy/systemd/rigdepot-server.service /etc/systemd/system/rigdepot-server.service
sudo systemctl daemon-reload
sudo systemctl enable --now rigdepot-server
```

机器上要装 `git`。要分发命令行，把某次发布的 `rgd_*` 压缩包和 `checksums.txt` 复制进 `/var/lib/rigdepot/cli/`。

## 反向代理

代理负责 HTTPS，把请求转给 `127.0.0.1:8080` 上的服务端。样例：[deploy/proxy/Caddyfile](../deploy/proxy/Caddyfile) 和 [deploy/proxy/nginx.conf](../deploy/proxy/nginx.conf)。

代理要做的：

- 传 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto`。Caddy 自己会传；nginx 要样例里那几行 `proxy_set_header`。
- 请求体上限不低于 `RGD_MAX_BUNDLE_MB`（默认 5 MB）；样例给的是 16 MB。
- 别的都不用：没有 WebSocket，也没有长连接。

服务端要知道的：`RGD_TRUSTED_PROXIES`，代理的请求从哪个地址或网段来。服务端只相信来自那里的 `X-Forwarded-For`，限流和审计日志都依赖它；设了它也就声明了 HTTPS 由代理负责，所以代理和服务端之间才可以用明文 HTTP。

| 情况 | `RGD_TRUSTED_PROXIES` |
| --- | --- |
| 安装包或可执行文件，代理在同一台机器上 | `127.0.0.1` |
| Compose，代理在主机上或同一个项目的容器里 | 不用设：默认就是 `RGD_SUBNET` |
| 代理在另一台机器上 | 那台机器的地址 |

用 Caddy 且地址是公网域名时，Caddy 会自己申请证书。在内网，用 site 块里的 `tls <证书文件> <私钥文件>` 给它你们自己的证书，或者用 nginx 样例并填上证书路径。

服务端可以挂在一个子路径下，比如 `https://www.example.com/rigdepot`：把路径写进 `RGD_BASE_URL`，代理原样转发这个路径。`/healthz` 和 `/readyz` 在根路径上也会回答。

## HTTPS

三种做法，按推荐顺序：

| 做法 | 什么时候用 | 配置 |
| --- | --- | --- |
| **交给反向代理** | 多数情况。内网部署通常用组织自己的 CA，多数团队本来就有代理 | `RGD_TRUSTED_PROXIES` |
| **证书文件** | 没有代理，证书由别的系统管 | `RGD_TLS_CERT_FILE`、`RGD_TLS_KEY_FILE` |
| **自动申请证书（ACME）** | 没有代理，有公网域名，443 端口能从公网访问到 | `RGD_TLS_ACME_DOMAINS`、`RGD_TLS_ACME_EMAIL` |

用证书文件时，服务端在 `RGD_LISTEN` 上提供 HTTPS（设成 `:443`，或者把 443 转发给它）。收到 `SIGHUP`（`systemctl kill -s HUP rigdepot-server`、`docker compose kill -s HUP rigdepot`）时，以及发现文件变了时（每分钟查一次），它重新加载证书，所以续期之后不用重启。加载不了的一对证书会记进日志，继续用旧的。

自动申请证书时，服务端用 TLS-ALPN-01 验证（在 443 端口上）从 Let's Encrypt（或 `RGD_TLS_ACME_DIRECTORY_URL` 指定的 CA）申请和续期，证书放在数据目录的 `acme/` 里。`RGD_TLS_ACME_DOMAINS` 必须包含 `RGD_BASE_URL` 的主机名；不能是 IP 地址，也不能是通配符。`RGD_HTTP_REDIRECT_LISTEN=:80` 再开一个明文端口，只做跳转到 HTTPS，同时回答 HTTP-01 验证。

`rgd-server doctor` 在证书文件到期前 14 天、自动申请的证书到期前 20 天（正常情况下到期前 30 天就会续期）报警告。

## 首次启动和部署向导

数据目录是空的时候第一次启动，服务端会建库、执行迁移、生成主密钥（`master.key`，除非你给了），并打印两条警告：备份主密钥，以及初始化口令。部署向导完成之前，每次启动都会再打印同一个口令。

在浏览器里打开对外地址。向导有五步：

1. **创建管理员**：先填初始化口令，再填一个本地管理员的账号和密码。身份源出问题时，这个账号是你进得来的那扇门。
2. **登录方式**：本地账号、OIDC（公司的单点登录）或 GitLab。以后都可以在「用户与登录」里设置或修改；要在身份源那边登记的回调地址也显示在那里。
3. **资源仓库**：服务端从哪个 Git 仓库导入资源，填 `https://` 或 `ssh` 地址和分支；需要的话给 `https://` 仓库一个只读令牌。服务端马上拉一次，之后每隔 `RGD_SOURCE_SYNC_INTERVAL`（10 分钟）拉一次；新的和改过的资源以草稿进来，发不发布由人决定。这一步可以跳过：资源仓库也可以在「平台设置」里管理；在「资源管理」里，单个资源还能以压缩包上传，MCP 还能在网页上登记。
4. **数据与隐私**：可选模块，默认全关。这一版还没有可选模块。
5. **第一个产品**：产品决定谁能看到哪些资源。现在建一个，或者以后在「产品与成员」里建。

向导完成后，初始化口令永久作废。想自己定口令（自动化部署时），在第一次启动前设 `RGD_SETUP_TOKEN` 或 `RGD_SETUP_TOKEN_FILE`，这样服务端就不会打印它。

以后忘了管理员密码？在服务器上运行 `rgd-server reset-admin`（见[运维命令](#运维命令)）。

向导之后，在控制台里：

- **平台设置 → 邮件**：填一个 SMTP 服务器，邀请和通知才能通过邮件发出。在那之前，邀请链接会显示出来供复制。
- **用户与登录**：邀请成员，或者让大家用身份源登录、再审核他们的开通申请。
- **接入指南**（每个成员都能看到）：安装 `rgd` 和登录的命令。

## 配置

配置分两类，分开放：

- **启动配置**：进程打开数据库之前就要知道的。来自环境变量、一个 TOML 文件和命令行参数，下面是全部。
- **运行期设置**：登录方式、邮件服务器、资源仓库、工具适配、最低命令行版本、可选模块。存在数据库里，在控制台（部署向导和「平台设置」）里改，每次修改都进审计，所有实例共享。

优先级从高到低：**命令行参数 > 环境变量 > 配置文件 > 默认值。** 值为空的环境变量算没设。`rgd-server config check` 打印每一项、它的值、值从哪来（密钥只显示「已设置」），再列出互相矛盾的地方。

配置文件由 `RGD_CONFIG` 或 `--config` 指定，是 TOML 格式；键里的点表示表：

```toml
base_url = "https://hub.example.com"
trusted_proxies = ["127.0.0.1"]

[log]
level = "info"

[backup]
dir = "/srv/rigdepot-backups"
keep = 30
```

配置文件里出现不认识的键会报错，免得写错一个字却悄悄用了默认值。密钥可以用文件给：`RGD_DATABASE_URL_FILE`、`RGD_SETUP_TOKEN_FILE` 从它们指向的文件里读值（Docker secrets、systemd 凭据）。密钥永远不能用命令行参数给，因为参数会出现在进程列表里。

| 环境变量 | 配置文件里的键 | 命令行参数 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `RGD_CONFIG` | — | `--config` | 无 | 配置文件。 |
| `RGD_BASE_URL` | `base_url` | `--base-url` | **必填** | 对外地址，`https://`（只有本机时可以是 `http://`），可以带子路径。登录回调、设备确认页、邮件里的链接、Cookie 是否带 `Secure`、安装命令都依赖它。 |
| `RGD_LISTEN` | `listen` | `--listen` | `127.0.0.1:8080`（镜像里是 `0.0.0.0:8080`） | 监听的地址和端口。 |
| `RGD_DATA_DIR` | `data_dir` | `--data-dir` | `/var/lib/rigdepot` | 数据目录；不存在时以 `0700` 新建。 |
| `RGD_DATABASE_URL`、`RGD_DATABASE_URL_FILE` | `database_url` | — | `sqlite://<数据目录>/rigdepot.db` | `sqlite://<路径>` 或 `postgres://<用户>:<密码>@<主机>:<端口>/<数据库>`。密钥。 |
| `RGD_MASTER_KEY_FILE` | `master_key_file` | `--master-key-file` | `<数据目录>/master.key` | 主密钥文件。不存在、且库里还没有任何加密内容时，第一次启动会生成。 |
| `RGD_MASTER_KEY` | — | — | 无 | 直接给主密钥，标准 base64 的 32 字节。和文件二选一。密钥。 |
| `RGD_SETUP_TOKEN`、`RGD_SETUP_TOKEN_FILE` | — | — | 第一次启动时生成并打印 | 部署向导的一次性口令。密钥。 |
| `RGD_AUTO_MIGRATE` | `auto_migrate` | `--auto-migrate` | `true` | 启动时执行数据库迁移。设为 `false` 时，有待执行迁移的服务端不会启动，直到你运行 `rgd-server migrate`。 |
| `RGD_TLS_CERT_FILE`、`RGD_TLS_KEY_FILE` | `tls.cert_file`、`tls.key_file` | `--tls-cert-file`、`--tls-key-file` | 无 | 用这对证书和私钥提供 HTTPS；要么都给，要么都不给。`SIGHUP` 和文件变化时重新加载。 |
| `RGD_TLS_ACME_DOMAINS` | `tls.acme_domains` | `--tls-acme-domains` | 无 | 为这些域名自动申请证书（逗号分隔；必须包含对外地址的主机名）。和证书文件互斥。 |
| `RGD_TLS_ACME_EMAIL` | `tls.acme_email` | `--tls-acme-email` | 无 | 留给 CA 的联系邮箱。 |
| `RGD_TLS_ACME_DIRECTORY_URL` | `tls.acme_directory_url` | `--tls-acme-directory-url` | Let's Encrypt | 换一个 CA 的 ACME 目录地址。 |
| `RGD_HTTP_REDIRECT_LISTEN` | `tls.http_redirect_listen` | `--http-redirect-listen` | 无 | 一个明文 HTTP 地址，比如 `:80`，只做跳转到 HTTPS（并回答 ACME 的 HTTP-01）。需要证书文件或 ACME。 |
| `RGD_TRUSTED_PROXIES` | `trusted_proxies` | `--trusted-proxies` | 无（Compose 里是 `RGD_SUBNET`） | 反向代理的地址或网段，逗号分隔。同时声明 HTTPS 由代理负责。 |
| `RGD_INSECURE_HTTP` | — | `--insecure-http` | `false` | 允许在本机以外用明文 HTTP。只给隔离的试用环境。 |
| `RGD_DEFAULT_LOCALE` | `default_locale` | `--default-locale` | `en` | 用户和浏览器都没说用什么语言时用的语言：`en` 或 `zh-CN`。测试邮件也用它。 |
| `RGD_LOG_LEVEL` | `log.level` | `--log-level` | `info` | `debug`、`info`、`warn` 或 `error`。 |
| `RGD_LOG_FORMAT` | `log.format` | `--log-format` | `json` | `json` 或 `text`。日志写到标准输出，密钥会被遮住。 |
| `RGD_METRICS_ADDR` | `metrics_addr` | `--metrics-addr` | 不开 | `GET /metrics` 的地址，比如 `127.0.0.1:9464`。不能是对外的端口。 |
| `RGD_GIT` | `git` | `--git` | `git` | git 程序。 |
| `RGD_SOURCE_SYNC_INTERVAL` | `source_sync_interval` | `--source-sync-interval` | `10m` | 拉资源仓库的间隔，至少 `1m`。 |
| `RGD_MAX_BUNDLE_MB` | `max_bundle_mb` | `--max-bundle-mb` | `5` | 接受的资源压缩包上限，单位 MB（1–1024）。 |
| `RGD_CLI_DIST_DIR` | `cli_dist_dir` | `--cli-dist-dir` | `<数据目录>/cli`，再 `/usr/share/rigdepot/cli` | 要分发的 `rgd` 发布压缩包放在哪。设了它就只找这一个目录。 |
| `RGD_CLI_DOWNLOAD_BASE` | `cli_download_base` | `--cli-download-base` | 无 | 一个 `https://` 目录，里面放着某次发布的 `checksums.txt` 和压缩包（镜像站、内部制品库）；服务端上没有压缩包时，安装脚本从这里下载。 |
| `RGD_BACKUP_DIR` | `backup.dir` | `--backup-dir` | 不开 | 每天在这里做一份 SQLite 数据库的快照。`backup` 和迁移前的快照也放这里。 |
| `RGD_BACKUP_KEEP` | `backup.keep` | `--backup-keep` | `14` | 每天的快照留几份。 |
| `RGD_MODULES_DISABLED` | `modules_disabled` | `--modules-disabled` | 无 | 即使在设置里打开了也不加载的可选模块，逗号分隔。 |
| `RGD_WEB_DEV_PROXY` | — | `--web-dev-proxy` | 无 | 只用于开发：控制台从本机的开发服务器取。 |

关于明文 HTTP 的规则：只有监听本机回环地址、设了 `RGD_TRUSTED_PROXIES`、或者 `RGD_INSECURE_HTTP=1` 时，服务端才会不带 TLS 启动。对外地址是 `http://` 而主机不是本机时，无论如何都拒绝。

## 给内网分发命令行

每个服务端都在自己的地址上分发 `rgd`，所以团队的电脑不用访问外网，装上的版本也天然和服务端对得上：

```sh
curl -fsSL https://hub.example.com/install.sh | sh          # macOS、Linux
irm https://hub.example.com/install.ps1 | iex               # Windows
```

脚本就是 [scripts/install.sh](../scripts/install.sh) 和 [scripts/install.ps1](../scripts/install.ps1)，由服务端填上自己的地址、版本和每个文件的 SHA-256。它们从 `https://hub.example.com/downloads/rgd-<系统>-<架构>` 下载，校验和对不上就拒绝。控制台的「接入指南」里显示的也是这两条命令。

服务端按顺序在这几个目录里找发布压缩包（`rgd_<版本>_<系统>_<架构>.tar.gz` 或 `.zip`，加上那次发布的 `checksums.txt`），用第一个有压缩包的：

1. `RGD_CLI_DIST_DIR`，设了的话；否则
2. `<数据目录>/cli`：想分发另一个版本，或者用的是单个可执行文件时，把某次发布的压缩包和 `checksums.txt` 复制到这里；
3. `/usr/share/rigdepot/cli`：安装包和镜像把自己这个版本的压缩包放在这里。

每个压缩包都按 `checksums.txt` 核对；对不上或者没列在里面的会被排除，日志里会写是哪个。有多个版本时分发最新的；启动时日志里 `handing out the command line` 那一行写着是哪个版本。哪里都没有压缩包时，设了 `RGD_CLI_DOWNLOAD_BASE` 的话安装脚本从那里下载，没设就不提供安装脚本。

## 备份与恢复

两样东西，放在**不同的地方**：

| 什么 | 怎么备份 | 说明 |
| --- | --- | --- |
| 数据库（SQLite） | `rgd-server backup`，或者设 `RGD_BACKUP_DIR` 每天自动做一份 | 服务运行时也能做出一致的快照。**服务运行时不要直接复制 `rigdepot.db`**：可能复制到写了一半的状态。 |
| 数据库（PostgreSQL） | `pg_dump`，或者托管服务的快照 | `rgd-server backup` 会拒绝并说明。 |
| 主密钥 | 复制 `master.key`（或者保管好你在 `RGD_MASTER_KEY` 里给的值） | 做一次，`keys rotate` 之后再做一次。绝不和数据库的备份放在一起。 |

数据库备份里故意不含主密钥：没有密钥，偷走的备份里看不到任何密钥内容。**主密钥丢了，库里所有加密的内容（每个人的个人密钥、身份源的 Client Secret、仓库令牌、邮件密码）都再也读不出来**，只能重新填；其余数据不受影响。有密钥再加上备份，就什么都能读，所以备份要按敏感数据保管。

```sh
rgd-server backup                     # 放进 backup.dir，没设时放进 <数据目录>/backups
rgd-server backup --out /srv/backups  # 放到别处
```

它写出 `rigdepot-<时间>.db`，说明主密钥在哪，并打印核对这份快照的命令。设了 `RGD_BACKUP_DIR` 时，服务端每天做一份，保留最新的 `RGD_BACKUP_KEEP` 份；它只删自己写的文件。用你们平常的工具把快照复制到别的机器上。每天的备份超过 50 小时没更新时 `doctor` 会警告，最近一次失败时 `/readyz` 会报出来。

**定期核对备份**，不碰正在用的数据：

```sh
rgd-server restore --check /srv/backups/rigdepot-20261125T030000Z.db
```

它以只读方式打开快照，检查：是不是一个完好的 SQLite 数据库，迁移版本是多少（这个服务端会不会迁移它），配置的主密钥能不能打开它，审计链是否完整。最后打印恢复它的步骤。

**恢复**要在服务端停掉的时候手工做，步骤和 `restore --check` 打印的一样：

1. 停掉服务端（`systemctl stop rigdepot-server`、`docker compose stop rigdepot`）。
2. 把快照复制成 `<数据目录>/rigdepot.db`。
3. 删掉旁边的 `rigdepot.db-wal` 和 `rigdepot.db-shm`（如果有）。
4. 主密钥丢了的话，把它放回原处。
5. 启动服务端。

用 Compose 时数据目录在卷里，要通过一个挂着这个卷的容器把快照复制进去，比如 `docker compose run --rm --no-deps -v /srv/backups:/restore:ro --entrypoint sh rigdepot -c 'cp /restore/<快照> /var/lib/rigdepot/rigdepot.db && rm -f /var/lib/rigdepot/rigdepot.db-wal /var/lib/rigdepot/rigdepot.db-shm'`。

用 PostgreSQL 时，在服务端停掉的时候用 `pg_restore`（或托管服务的工具）恢复，并确认服务端拿着配套的主密钥。

## 升级

**升级就是换程序（或镜像标签）再重启。** 启动时服务端按顺序执行还没执行过的迁移，从任何更早的版本都可以，然后开始服务。

- **Compose**：把 `.env` 里的 `RGD_VERSION` 改成新版本，然后 `docker compose pull && docker compose up -d`。
- **安装包**：`apt install ./rigdepot-server_<新版本>_<架构>.deb` 或 `dnf install ./rigdepot-server-<新版本>-1.<架构>.rpm`；安装包会重启服务端。
- **镜像或可执行文件**：换掉，重启。

开始之前读一遍发布说明，手边准备好一份新的备份和主密钥。

**有什么保护你。** 用 SQLite 时，在对一个有数据的库执行迁移之前，服务端先写一份快照 `pre-migrate-<前>-<后>-<时间>.db`（数字是迁移版本号），放进 `backup.dir`，没设时放进数据目录的 `backups/`。快照写不出来，迁移就不执行，服务端也不启动。用 PostgreSQL 时服务端不替你备份，只在日志里警告你先备份。想自己掌握这一步：设 `RGD_AUTO_MIGRATE=false`，备份，运行 `rgd-server migrate`，再启动。

**退回去。** 迁移只往前。比数据库旧的程序会拒绝启动（「the database was migrated by a newer version of this program」），不会带着不认识的表结构继续跑。要回到上一个版本：

1. 停掉服务端。
2. 按[备份与恢复](#备份与恢复)恢复 `pre-migrate-…` 快照（或者你在升级前做的备份）。
3. 换回上一个版本的程序或镜像标签。
4. 启动。

升级之后到退回之前做的改动会丢失。

**多个实例（PostgreSQL）。** 迁移带锁：先启动的实例执行迁移，其余的等着。新旧实例不打算同时对外服务，所以升级期间只留一个实例。

**命令行。** 先升级服务端。服务端接受和它同一版本、以及上一个小版本的 `rgd`；更旧的 `rgd` 在同步时被拒绝（退出码 8）并提示升级（查看状态和卸载照常能用）。控制台的「工具适配」可以调高最低版本，但不能调到这个下限以下。大家重新运行一遍安装命令就能升级 `rgd`；它不会自己升级。

## 运维命令

每条命令读的配置和 `serve` 一样（环境变量、文件、参数），所以要以服务用户、带着服务的环境变量运行：`docker compose exec rigdepot rgd-server <命令>`，安装包则是 `sudo -u rigdepot sh -c 'set -a; . /etc/rigdepot/server.env; exec rgd-server <命令>'`。`rgd-server <命令> -h` 列出一条命令的参数。

| 命令 | 做什么 |
| --- | --- |
| `serve` | 启动服务端（不给命令时的默认）。 |
| `migrate` | 只执行数据库迁移；用 SQLite 时之前先做快照。 |
| `config check` | 打印每一项配置、它的值和来源，再列出矛盾的地方。有问题时退出码 1，配置加载不了时 2。 |
| `doctor` | 逐项检查环境：配置、数据目录、数据库、迁移、主密钥、审计日志、邮件、证书、磁盘空间、git、备份。只看不修。有一项失败时退出码 1；警告不影响退出码。 |
| `backup [--out <目录>]` | 服务运行时给 SQLite 数据库做一份一致的快照。 |
| `restore --check <快照>` | 核对一份快照，并打印怎么恢复。从不改数据。 |
| `reset-admin [--user <账号>] [--password-stdin]` | 直接在数据库里给一个平台管理员设新密码：忘了密码或身份源宕机时进得来的办法。生成的新密码只打印一次（或者从标准输入读一个），解除账号锁定，结束它的浏览器会话，并记进审计日志。 |
| `keys rotate [--out <文件>]` | 让一把新的主密钥成为当前密钥，用它重新封装每一条加密记录；旧密钥作为退役密钥留在文件里，正在运行的服务端照常工作。谁都不用重新填任何东西。主密钥放在 `RGD_MASTER_KEY` 里时必须给 `--out`，之后改用 `RGD_MASTER_KEY_FILE` 指向新文件重启服务端。做完要备份密钥文件。 |
| `audit verify` | 校验审计日志的整条哈希链。有条目被改、被删或被插入时退出码 1。服务端每天也会自己查一次，并通知管理员。 |
| `healthcheck [--ready]` | 问本机的服务端 `/healthz`（或 `/readyz`），给容器的健康检查用。 |
| `version` | 打印版本。 |
| `setup --from <文件>` | 无人值守的初始化。这一版还没有实现。 |

## 健康检查和指标

- `GET /healthz`：进程在服务就回答 200。
- `GET /readyz`：回答一个 JSON，每项检查一条。`database`、`migrations`、`master_key` 失败时返回 503（`unavailable`）；`mail`、`audit_chain`、`sources`（某个资源仓库上一次拉取失败）以及开了每日备份时的 `backup` 失败只会让它 `degraded`，仍是 200；服务端继续提供上一次成功拉取导入的内容。邮件没配置导致的 degraded 是正常的，配好就没了。

设了 `RGD_METRICS_ADDR` 时，那个单独的地址上的 `GET /metrics` 以 Prometheus 文本格式提供指标。名字都以 `rgd_server_` 开头：

| 指标 | 类型 | 内容 |
| --- | --- | --- |
| `build_info` | gauge | 版本号，在标签里。 |
| `http_requests_total`、`http_request_duration_seconds` | counter、histogram | 按路由模板和状态码统计的请求。 |
| `job_runs_total`、`job_run_seconds_total` | counter | 按任务和结果统计的后台任务。 |
| `mail_outbox_messages`、`mail_sent_last_hour` | gauge | 等待发送、重试中、已放弃的邮件；已发出的邮件。 |
| `db_open_connections`、`db_in_use_connections`、`db_idle_connections`、`db_max_open_connections`、`db_waits_total`、`db_wait_seconds_total` | gauge、counter | 数据库连接池。 |

标签里只有路由模板、状态码、任务名、结果和连接池名：没有任何关于人、资源或内容的信息。

日志是写到标准输出的 JSON 行（`journalctl -u rigdepot-server`、`docker compose logs rigdepot`）。每个请求有一个 `request_id`，错误响应里也带着它，所以能按用户看到的错误在日志里找到那一次请求。

## 最低配置

> **下面的数字是按设计估算的，不是实测。** 以后会换成压测结果，并写明压测方法。

目标：**2 核 2 GB 内存**的机器，支撑几百人以内的团队。

| | 估算 | 依据 |
| --- | --- | --- |
| 平时的内存 | 几十 MB | 一个 Go 进程加 SQLite |
| 峰值内存 | 不超过单元里 512 MB 的上限 | 密码校验（每次约 19 MB，有并发上限）、`git` 子进程、上传的压缩包（默认最大 5 MB） |
| 处理器 | 平时接近空闲 | 200 人每 10 分钟同步一次，每秒不到一个请求；状态没变时只是一次缓存校验 |
| 磁盘 | 数据库几十到几百 MB，再加上仓库缓存和备份 | 至少留 1 GB 空闲：低于 1 GiB `doctor` 警告，低于 100 MiB 失败 |
| 网络 | 你们的身份源、邮件服务器、资源仓库 | 不需要别的 |

## 出了问题

先跑 `rgd-server doctor` 和 `rgd-server config check`，大部分问题和该怎么办它们都会说出来。

| 现象 | 原因和办法 |
| --- | --- |
| `refusing to serve plain HTTP beyond this machine` | 服务端会对网络提供明文 HTTP。在代理后面就设 `RGD_TRUSTED_PROXIES`，否则给证书。除非主机是本机，`RGD_BASE_URL` 必须是 `https://`。 |
| `base_url is required` | 没设 `RGD_BASE_URL`（安装包的话在 `/etc/rigdepot/server.env`）。 |
| `docker compose up` 报网段重叠（pool overlaps） | `RGD_SUBNET` 和已有的网络重叠：在 `.env` 里换一个网段。 |
| `docker compose up` 报 address already in use | 8080 端口被占用：设 `RGD_PORT`。 |
| 初始化口令找不到了 | 重启服务端：向导完成之前，每次启动都会打印同一个口令。 |
| 管理员忘了密码 | `rgd-server reset-admin`。 |
| 服务端起不来：主密钥打不开数据库 | 库里已经有加密内容，而密钥文件丢了或不对。把正确的 `master.key` 放回去；服务端绝不会替换一把它需要的密钥。 |
| `the database was migrated by a newer version of this program` | 旧程序遇到了新数据库：运行新版本，或者按[升级](#升级)里的办法退回去。 |
| `rgd` 说它的版本太旧（退出码 8） | 重新运行「接入指南」里的安装命令。 |
| 用身份源登录，跳回来之后失败 | 在身份源那边登记的回调地址必须和「用户与登录」里显示的一致，它跟着 `RGD_BASE_URL` 走。 |
| 资源仓库没有导入 | `/readyz` 里 `sources` 是 degraded，日志里写着原因。在「平台设置」里核对地址、分支和只读令牌，并确认服务器上 `git --version` 是 2.31 或更新。 |
| 大家收不到邮件 | 在「平台设置」里配好邮件服务器，发一封测试邮件，再看 `rgd_server_mail_outbox_messages` 指标或日志。 |
