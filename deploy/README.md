<p align="right"><b>English</b> · <a href="README.zh-CN.md">简体中文</a></p>

# Deploying rgd-server

> **The server arrives with M1.** The files here are in place and build, but today `rgd-server` only answers `--version`: `serve`, `healthcheck` and the other subcommands mentioned below do not exist yet, so a container started from this image exits right away. Nothing on this page has been exercised end to end. It describes the shape a deployment will have, so that the files can be reviewed early.

Three ways to run it. All of them are one process and one data directory.

| Form | Files | Database |
| --- | --- | --- |
| Single binary + systemd | [systemd/rigdepot-server.service](systemd/rigdepot-server.service) | SQLite (default) |
| Container image | [../Dockerfile](../Dockerfile) | SQLite on a volume |
| Docker Compose | [compose.yaml](compose.yaml), [.env.example](.env.example) | SQLite, or PostgreSQL through the `postgres` profile |

The server targets Linux on amd64 and arm64. The only thing it needs on the machine is `git`, to fetch resource repositories; the image already has it.

## What needs a backup

Two things, kept in **different places**:

- **The database.** With SQLite it is `rigdepot.db` in the data directory. Do not copy the file while the server is running; use the server's own backup command.
- **The master key**, `master.key` in the data directory unless you configured it elsewhere. If it is lost, everything encrypted in the database (everyone's personal secrets, the client secrets of identity sources, repository tokens, the mail password) can no longer be read and has to be entered again.

## Configuration

Startup configuration comes from `RGD_*` environment variables. Only one is required:

| Variable | Default | Meaning |
| --- | --- | --- |
| `RGD_BASE_URL` | **required** | The public address, such as `https://hub.example.com`. Sign-in callbacks, links in emails and cookie security depend on it. |
| `RGD_LISTEN` | `127.0.0.1:8080` (`0.0.0.0:8080` in the image) | Listen address. |
| `RGD_DATA_DIR` | `/var/lib/rigdepot` | Data directory. |
| `RGD_DATABASE_URL`, `RGD_DATABASE_URL_FILE` | SQLite in the data directory | `postgres://…` for PostgreSQL. The `_FILE` form reads the value from a file. |
| `RGD_TRUSTED_PROXIES` | none | Address ranges of your reverse proxy. `X-Forwarded-*` is trusted only from these. |

HTTPS is normally left to a reverse proxy in front of the server. Plain HTTP on a non-loopback address is refused by default, because personal secrets travel over this connection.

## Docker Compose

```sh
cd deploy
cp .env.example .env      # then set RGD_BASE_URL
docker compose build      # until an image is published
docker compose up -d
```

The server listens on `127.0.0.1:8080` only. Point your reverse proxy at it.

### PostgreSQL

Create two files in [secrets/](secrets/), which Git ignores:

```sh
openssl rand -hex 24 > secrets/postgres_password
printf 'postgres://rigdepot:%s@postgres:5432/rigdepot\n' "$(cat secrets/postgres_password)" > secrets/database_url
```

The server in the container runs as user `10001` and has to be able to read `database_url`:

```sh
sudo chown 10001 secrets/database_url && sudo chmod 400 secrets/database_url
```

Then uncomment the two PostgreSQL lines in `.env` and start as before:

```sh
docker compose up -d
```

The data volume still holds the master key and the repository cache. Do not drop it.

## Container image

```sh
docker build -t rigdepot-server .                 # from the repository root
docker run --rm rigdepot-server --version
docker buildx build --platform linux/amd64,linux/arm64 -t rigdepot-server .
```

The last command builds both architectures at once. It needs a builder that supports several platforms: turn on the containerd image store in Docker, or create a `docker-container` builder. With the default builder, build one platform at a time with `docker build --platform linux/amd64 .`.

The image runs as a non-root user, keeps all state in one volume (`/var/lib/rigdepot`) and listens on port 8080.

## Single binary and systemd

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

The unit only lets the process write to the data directory and limits its memory; the numbers are starting points for a small machine.
