<p align="right"><a href="README.md">English</a> · <b>简体中文</b></p>

# 部署 rgd-server

> **服务端在 M1 实现。** 这里的文件已经就位并且能构建，但现在的 `rgd-server` 只会回答 `--version`：下文提到的 `serve`、`healthcheck` 等子命令都还没有，所以用这个镜像起的容器会立刻退出。这一页写的东西都还没有从头到尾跑通过。它讲的是部署将来的样子，好让这些文件能早一点被评审。

三种形态，都是一个进程加一个数据目录。

| 形态 | 文件 | 数据库 |
| --- | --- | --- |
| 单个可执行文件 + systemd | [systemd/rigdepot-server.service](systemd/rigdepot-server.service) | SQLite（默认） |
| 容器镜像 | [../Dockerfile](../Dockerfile) | SQLite，挂一个卷 |
| Docker Compose | [compose.yaml](compose.yaml)、[.env.example](.env.example) | SQLite，或者用 `postgres` profile 接 PostgreSQL |

服务端的发布目标是 Linux 的 amd64 和 arm64。机器上唯一需要的是 `git`（拉资源仓库用），镜像里已经带了。

## 要备份什么

两样东西，**分开保管**：

- **数据库。** 用 SQLite 时是数据目录里的 `rigdepot.db`。不要在服务运行时直接复制这个文件，用服务端自带的备份命令。
- **主密钥。** 没有另外指定时是数据目录里的 `master.key`。它丢了，库里所有加密的内容（每个人的个人密钥、身份源的 Client Secret、仓库令牌、邮件密码）都解不开，只能重新填。

## 配置

启动配置来自 `RGD_*` 环境变量，只有一个是必填的：

| 变量 | 默认 | 含义 |
| --- | --- | --- |
| `RGD_BASE_URL` | **必填** | 对外的地址，如 `https://hub.example.com`。登录回调、邮件里的链接、Cookie 的安全属性都由它决定。 |
| `RGD_LISTEN` | `127.0.0.1:8080`（镜像里是 `0.0.0.0:8080`） | 监听地址。 |
| `RGD_DATA_DIR` | `/var/lib/rigdepot` | 数据目录。 |
| `RGD_DATABASE_URL`、`RGD_DATABASE_URL_FILE` | 数据目录里的 SQLite | 用 PostgreSQL 时填 `postgres://…`。`_FILE` 的形式从文件里读值。 |
| `RGD_TRUSTED_PROXIES` | 无 | 反向代理的地址段。只信任来自这些地址的 `X-Forwarded-*`。 |

HTTPS 通常交给服务端前面的反向代理。在非本机地址上用明文 HTTP 默认会被拒绝，因为个人密钥是经这条线传的。

## Docker Compose

```sh
cd deploy
cp .env.example .env      # 然后设好 RGD_BASE_URL
docker compose build      # 镜像发布之前，先从源码构建
docker compose up -d
```

服务端只监听 `127.0.0.1:8080`，把你们的反向代理指向它。

### PostgreSQL

在 [secrets/](secrets/) 里建两个文件（这个目录里的内容不进 Git）：

```sh
openssl rand -hex 24 > secrets/postgres_password
printf 'postgres://rigdepot:%s@postgres:5432/rigdepot\n' "$(cat secrets/postgres_password)" > secrets/database_url
```

容器里的服务端以用户 `10001` 运行，它要能读到 `database_url`：

```sh
sudo chown 10001 secrets/database_url && sudo chmod 400 secrets/database_url
```

然后把 `.env` 里 PostgreSQL 的那两行取消注释，照旧启动：

```sh
docker compose up -d
```

数据卷里仍然有主密钥和仓库缓存，不能删。

## 容器镜像

```sh
docker build -t rigdepot-server .                 # 在仓库根目录
docker run --rm rigdepot-server --version
docker buildx build --platform linux/amd64,linux/arm64 -t rigdepot-server .
```

最后一条命令一次构建两种架构。它需要支持多平台的构建器：在 Docker 里打开 containerd 镜像存储，或者建一个 `docker-container` 构建器。用默认构建器时，一次构建一个平台：`docker build --platform linux/amd64 .`。

镜像以非 root 用户运行，状态全在一个卷里（`/var/lib/rigdepot`），监听 8080 端口。

## 单个可执行文件加 systemd

```sh
sudo useradd --system --home-dir /var/lib/rigdepot --shell /usr/sbin/nologin rigdepot
sudo install -d -o rigdepot -g rigdepot -m 0700 /var/lib/rigdepot
sudo install -d -m 0755 /etc/rigdepot
printf 'RGD_BASE_URL=https://hub.example.com\n' | sudo install -m 0600 /dev/stdin /etc/rigdepot/server.env
sudo install -m 0755 rgd-server /usr/bin/rgd-server
sudo install -m 0644 systemd/rigdepot-server.service /etc/systemd/system/rigdepot-server.service
sudo systemctl daemon-reload
sudo systemctl enable --now rigdepot-server
```

这个单元只允许进程写数据目录，并限制了内存；数字是给小机器的起点。
