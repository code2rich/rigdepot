<p align="right"><a href="README.md">English</a> · <b>简体中文</b></p>

# 部署文件

运行 `rgd-server` 用的文件。一步一步怎么用，见部署指南：**[docs/deploy.zh-CN.md](../docs/deploy.zh-CN.md)**。

| 文件 | 是什么 | 指南 |
| --- | --- | --- |
| [compose.yaml](compose.yaml) | Docker Compose：一个容器，SQLite 放在卷 `rigdepot-data` 里，监听 `127.0.0.1:8080` | [Compose + SQLite](../docs/deploy.zh-CN.md#docker-compose--sqlite) |
| [compose.postgres.yaml](compose.postgres.yaml) | 叠加在 `compose.yaml` 上：另一个容器里的 PostgreSQL 17 | [Compose + PostgreSQL](../docs/deploy.zh-CN.md#docker-compose--postgresql) |
| [.env.example](.env.example) | Compose 读的变量；复制成 `.env`，设好 `RGD_BASE_URL` | [Compose + SQLite](../docs/deploy.zh-CN.md#docker-compose--sqlite) |
| [secrets/](secrets/) | 放 PostgreSQL 口令和连接地址的地方；Git 忽略里面的文件 | [Compose + PostgreSQL](../docs/deploy.zh-CN.md#docker-compose--postgresql) |
| [systemd/rigdepot-server.service](systemd/rigdepot-server.service) | 安装包装的 systemd 单元 | [安装包 + systemd](../docs/deploy.zh-CN.md#debian-或-rpm-包--systemd) |
| [packaging/](packaging/) | Debian 和 RPM 包的脚本和默认的 `server.env` | [安装包 + systemd](../docs/deploy.zh-CN.md#debian-或-rpm-包--systemd) |
| [proxy/Caddyfile](proxy/Caddyfile)、[proxy/nginx.conf](proxy/nginx.conf) | 反向代理样例 | [反向代理](../docs/deploy.zh-CN.md#反向代理) |
| [docker/Dockerfile](docker/Dockerfile) | 发布时用安装包构建镜像的那份；[../Dockerfile](../Dockerfile) 从源码构建同样的镜像 | [其他运行方式](../docs/deploy.zh-CN.md#其他运行方式) |

用 Docker 的最短路径：

```sh
cp .env.example .env                       # 然后设 RGD_BASE_URL=https://hub.example.com
docker compose up -d
docker compose logs rigdepot | grep setup_code
```

然后在 `127.0.0.1:8080` 前面放一个带 HTTPS 的反向代理，打开地址，填入初始化口令，并备份主密钥。全部配置项见[配置](../docs/deploy.zh-CN.md#配置)。
