<p align="right"><b>English</b> · <a href="README.zh-CN.md">简体中文</a></p>

# Deployment files

The files for running `rgd-server`. How to use them, step by step, is in the deployment guide: **[docs/deploy.md](../docs/deploy.md)**.

| File | What it is | Guide |
| --- | --- | --- |
| [compose.yaml](compose.yaml) | Docker Compose: one container, SQLite in the volume `rigdepot-data`, listening on `127.0.0.1:8080` | [Compose with SQLite](../docs/deploy.md#docker-compose-with-sqlite) |
| [compose.postgres.yaml](compose.postgres.yaml) | Added on top of `compose.yaml`: PostgreSQL 17 in a second container | [Compose with PostgreSQL](../docs/deploy.md#docker-compose-with-postgresql) |
| [.env.example](.env.example) | The variables Compose reads; copy to `.env` and set `RGD_BASE_URL` | [Compose with SQLite](../docs/deploy.md#docker-compose-with-sqlite) |
| [secrets/](secrets/) | Where the PostgreSQL password and connection URL go; Git ignores the files | [Compose with PostgreSQL](../docs/deploy.md#docker-compose-with-postgresql) |
| [systemd/rigdepot-server.service](systemd/rigdepot-server.service) | The systemd unit the packages install | [Package with systemd](../docs/deploy.md#debian-or-rpm-package-with-systemd) |
| [packaging/](packaging/) | The scripts and the default `server.env` of the Debian and RPM packages | [Package with systemd](../docs/deploy.md#debian-or-rpm-package-with-systemd) |
| [proxy/Caddyfile](proxy/Caddyfile), [proxy/nginx.conf](proxy/nginx.conf) | Reverse proxy samples | [Reverse proxy](../docs/deploy.md#reverse-proxy) |
| [docker/Dockerfile](docker/Dockerfile) | The image the release builds from its packages; [../Dockerfile](../Dockerfile) builds the same image from source | [Other ways to run it](../docs/deploy.md#other-ways-to-run-it) |

The shortest path, with Docker:

```sh
cp .env.example .env                       # then set RGD_BASE_URL=https://hub.example.com
docker compose up -d
docker compose logs rigdepot | grep setup_code
```

Then put a reverse proxy with HTTPS in front of `127.0.0.1:8080`, open the address, enter the setup token, and back up the master key. All settings: [Configuration](../docs/deploy.md#configuration).
