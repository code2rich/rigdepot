<p align="right"><a href="README.md">English</a> · <b>简体中文</b></p>

# 部署 rgd-server

三种形态，都是一个进程加一个数据目录；每一种都在干净的 Linux 上从零起到管理员登录成功验证过。

| 形态 | 文件 | 数据库 |
| --- | --- | --- |
| Debian 或 RPM 包，配 systemd | `rigdepot-server_<版本>_<架构>.deb`、`rigdepot-server-<版本>-1.<架构>.rpm`；[systemd/rigdepot-server.service](systemd/rigdepot-server.service) | SQLite（默认）或 PostgreSQL |
| 容器镜像 | `ghcr.io/code2rich/rigdepot-server`，或给 `docker load` 用的 `rigdepot-server_<版本>_linux_<架构>.docker.tar.gz`；从源码构建用 [../Dockerfile](../Dockerfile) | SQLite，挂一个卷 |
| Docker Compose | [compose.yaml](compose.yaml)（SQLite）、[compose.postgres.yaml](compose.postgres.yaml)（叠加 PostgreSQL）、[.env.example](.env.example) | SQLite 或 PostgreSQL |

反向代理的样例：[proxy/Caddyfile](proxy/Caddyfile)、[proxy/nginx.conf](proxy/nginx.conf)。

服务端的发布目标是 Linux 的 amd64 和 arm64。机器上唯一需要的是 `git`（拉资源仓库用）：包把它列为依赖，镜像里已经带了。

## 要备份什么

两样东西，**分开保管**：

- **数据库。** 用 SQLite 时是数据目录里的 `rigdepot.db`。不要在服务运行时直接复制这个文件，可能复制到写了一半的状态。
- **主密钥。** 没有另外指定时是数据目录里的 `master.key`。它丢了，库里所有加密的内容（每个人的个人密钥、身份源的 Client Secret、仓库令牌、邮件密码）都解不开，只能重新填。

## 配置

启动配置来自 `RGD_*` 环境变量，只有一个是必填的：

| 变量 | 默认 | 含义 |
| --- | --- | --- |
| `RGD_BASE_URL` | **必填** | 对外的地址，如 `https://hub.example.com`。登录回调、邮件里的链接、Cookie 的安全属性、安装命令都由它决定。 |
| `RGD_LISTEN` | `127.0.0.1:8080`（镜像里是 `0.0.0.0:8080`） | 监听地址。 |
| `RGD_DATA_DIR` | `/var/lib/rigdepot` | 数据目录。 |
| `RGD_DATABASE_URL`、`RGD_DATABASE_URL_FILE` | 数据目录里的 SQLite | 用 PostgreSQL 时填 `postgres://…`。`_FILE` 形式从文件读值。 |
| `RGD_TRUSTED_PROXIES` | 无（Compose 里是项目网络的网段） | 反向代理的地址段。只相信来自这些地址的 `X-Forwarded-For`。 |
| `RGD_CLI_DIST_DIR` | 先 `<数据目录>/cli`，再 `/usr/share/rigdepot/cli` | 服务端分发的 `rgd` 发布归档放在哪。 |
| `RGD_CLI_DOWNLOAD_BASE` | 无 | 一个放着某次发布的 `checksums.txt` 和归档的目录（镜像站、内部制品库）。服务端上没有归档时，安装脚本从这里下载。 |

HTTPS 通常交给前面的反向代理。除非只监听本机、有证书、或者声明了在代理后面（`RGD_TRUSTED_PROXIES`），服务端拒绝用明文 HTTP，因为个人密钥要经这条线传。隔离的试用环境可以设 `RGD_INSECURE_HTTP=1` 放开。

## 首次启动

第一次启动时服务端建库、生成主密钥，并在日志里打印一次性的**初始化口令**：

```json
{"level":"WARN","msg":"setup is not complete: open the server's address in a browser and enter this one-time setup code","base_url":"https://hub.example.com","setup_code":"…"}
```

浏览器打开对外地址，输入它，创建第一个管理员。日志在哪：`journalctl -u rigdepot-server`（包）、`docker compose logs rigdepot`（Compose）、`docker logs <容器>`（镜像）。

## 给内网分发命令行

每个服务端都在自己的地址上分发命令行 `rgd`，团队的电脑不需要访问公网：

```sh
curl -fsSL https://hub.example.com/install.sh | sh          # macOS、Linux
irm https://hub.example.com/install.ps1 | iex               # Windows
```

这两个脚本就是 [../scripts/install.sh](../scripts/install.sh) 和 [../scripts/install.ps1](../scripts/install.ps1)，由服务端填好自己的地址、版本和每个可执行文件的 SHA-256；它们从 `https://hub.example.com/downloads/rgd-<系统>-<架构>` 下载，对不上就拒绝。控制台的接入指南显示的是同样的命令（`GET /api/v1/cli/downloads`）。

服务端分发它在下面几个目录里找到的第一组发布归档：

1. 设了 `RGD_CLI_DIST_DIR` 就只看它；否则
2. `<数据目录>/cli`：把某次发布的 `rgd_<版本>_<系统>_<架构>.tar.gz` / `.zip` 和 `checksums.txt` 复制到这里，就能分发另一个版本；只用单个可执行文件部署时也放这里；
3. `/usr/share/rigdepot/cli`：包和镜像把它们自己那个版本的归档放在这里。

旁边有 `checksums.txt` 时，每个归档都要和它对得上，对不上或没列出的不分发（日志里说是哪个）。有几个版本时分发最新的。一个归档都没有时，设了 `RGD_CLI_DOWNLOAD_BASE` 就让安装脚本从那里下载，没设就没有安装脚本。

## Debian 和 RPM 包

```sh
sudo apt install ./rigdepot-server_0.4.0_amd64.deb        # Debian、Ubuntu
sudo dnf install ./rigdepot-server-0.4.0-1.x86_64.rpm     # Fedora、Rocky Linux、RHEL
```

包里有 `/usr/bin/rgd-server`、systemd 单元、系统用户 `rigdepot`（它拥有 `/var/lib/rigdepot`，`0700`）、`/etc/rigdepot/server.env`（`0600`，升级时保留）和 `/usr/share/rigdepot/cli/` 里六个平台的 `rgd` 归档。装完不启动：服务端要先知道自己的地址。

```sh
sudoedit /etc/rigdepot/server.env        # RGD_BASE_URL=https://hub.example.com、RGD_TRUSTED_PROXIES=127.0.0.1
sudo systemctl enable --now rigdepot-server
journalctl -u rigdepot-server            # 初始化口令
```

升级时正在运行的服务会重启；卸载时停掉服务，数据目录、配置和用户都留着。

单元只允许进程写数据目录，并限制了内存；数字是给小机器的起点。

## Docker Compose

```sh
cd deploy
cp .env.example .env      # 然后设好 RGD_BASE_URL
docker compose up -d
docker compose logs rigdepot              # 初始化口令
```

还没有发布镜像，或者想试一个改动时，先从当前代码构建：`docker compose build`。

服务端只监听本机的 `127.0.0.1:8080`，让反向代理指向它。容器以只读根文件系统、无特权用户运行，状态全在 `rigdepot-data` 卷里。

**受信任的代理。** 项目有一个固定网段的网络 `RGD_SUBNET`（默认 `172.31.250.0/24`），`RGD_TRUSTED_PROXIES` 默认就是它。宿主机上的反向代理经这个网络的网关连到容器，本项目里另一个容器中的代理从网络内部连过来，所以这两种都被相信，别的地方连不到这个端口。网段和已有的网络重叠时，`docker compose up` 会报错：换一个 `RGD_SUBNET`。代理在别处时，把 `RGD_TRUSTED_PROXIES` 设成它的地址。

### PostgreSQL

在 [secrets/](secrets/) 里建两个文件（Git 忽略这个目录）：

```sh
(umask 077; openssl rand -hex 24 > secrets/postgres_password)
printf 'postgres://rigdepot:%s@postgres:5432/rigdepot\n' "$(cat secrets/postgres_password)" > secrets/database_url
```

容器里的服务端以用户 `10001` 运行，要能读 `database_url`：

```sh
sudo chown 10001 secrets/database_url && sudo chmod 400 secrets/database_url
```

然后两个文件一起起，或者在 `.env` 里设 `COMPOSE_FILE=compose.yaml:compose.postgres.yaml`：

```sh
docker compose -f compose.yaml -f compose.postgres.yaml up -d
```

数据卷里仍然有主密钥和仓库缓存，不能省。

## 容器镜像

```sh
docker run -d --name rigdepot --read-only --tmpfs /tmp -p 127.0.0.1:8080:8080 \
  -e RGD_BASE_URL=https://hub.example.com -e RGD_TRUSTED_PROXIES=172.16.0.0/12 \
  -v rigdepot-data:/var/lib/rigdepot ghcr.io/code2rich/rigdepot-server:0.4.0
```

同一个标签里有 `linux/amd64` 和 `linux/arm64`。访问不了镜像仓库时，从发布页下载 `rigdepot-server_<版本>_linux_<架构>.docker.tar.gz`，`docker load -i` 导入。

从源码构建（控制台、服务端、六个平台的 `rgd` 都在构建里编译）：

```sh
docker build -t rigdepot-server .                 # 在仓库根目录
docker buildx build --platform linux/amd64,linux/arm64 -t rigdepot-server .
```

第二条要一个支持多平台的构建器：打开 Docker 的 containerd 镜像存储，或者建一个 `docker-container` 构建器。

## 不用包，只用可执行文件

```sh
sudo useradd --system --home-dir /var/lib/rigdepot --shell /usr/sbin/nologin rigdepot
sudo install -d -o rigdepot -g rigdepot -m 0700 /var/lib/rigdepot
sudo install -d -m 0755 /etc/rigdepot
printf 'RGD_BASE_URL=https://hub.example.com\n' | sudo install -m 0600 /dev/stdin /etc/rigdepot/server.env
sudo install -m 0755 rgd-server /usr/bin/rgd-server
sudo install -m 0644 deploy/systemd/rigdepot-server.service /etc/systemd/system/rigdepot-server.service
sudo systemctl daemon-reload
sudo systemctl enable --now rigdepot-server
```

要分发命令行，把某次发布的 `rgd_*` 归档和 `checksums.txt` 复制到 `/var/lib/rigdepot/cli/`。
