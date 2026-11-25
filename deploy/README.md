<p align="right"><b>English</b> · <a href="README.zh-CN.md">简体中文</a></p>

# Deploying rgd-server

Three ways to run it. All of them are one process and one data directory, and all of them have been brought up from nothing to a signed-in administrator on a clean Linux machine.

| Form | Files | Database |
| --- | --- | --- |
| Debian or RPM package, with systemd | `rigdepot-server_<version>_<arch>.deb`, `rigdepot-server-<version>-1.<arch>.rpm`; [systemd/rigdepot-server.service](systemd/rigdepot-server.service) | SQLite (default) or PostgreSQL |
| Container image | `ghcr.io/code2rich/rigdepot-server`, or `rigdepot-server_<version>_linux_<arch>.docker.tar.gz` for `docker load`; [../Dockerfile](../Dockerfile) to build from source | SQLite on a volume |
| Docker Compose | [compose.yaml](compose.yaml) (SQLite), [compose.postgres.yaml](compose.postgres.yaml) (PostgreSQL on top), [.env.example](.env.example) | SQLite or PostgreSQL |

Reverse proxy samples: [proxy/Caddyfile](proxy/Caddyfile), [proxy/nginx.conf](proxy/nginx.conf).

The server targets Linux on amd64 and arm64. The only thing it needs on the machine is `git`, to fetch resource repositories; the packages depend on it and the image has it.

## What needs a backup

Two things, kept in **different places**:

- **The database.** With SQLite it is `rigdepot.db` in the data directory. Do not copy the file while the server is running: it may be half written. `rgd-server backup` makes a consistent snapshot while the server runs.
- **The master key**, `master.key` in the data directory unless you configured it elsewhere. If it is lost, everything encrypted in the database (everyone's personal secrets, the client secrets of identity sources, repository tokens, the mail password) can no longer be read and has to be entered again.

## Configuration

Startup configuration comes from `RGD_*` environment variables. Only one is required:

| Variable | Default | Meaning |
| --- | --- | --- |
| `RGD_BASE_URL` | **required** | The public address, such as `https://hub.example.com`. Sign-in callbacks, links in emails, cookie security and the install command depend on it. |
| `RGD_LISTEN` | `127.0.0.1:8080` (`0.0.0.0:8080` in the image) | Listen address. |
| `RGD_DATA_DIR` | `/var/lib/rigdepot` | Data directory. |
| `RGD_DATABASE_URL`, `RGD_DATABASE_URL_FILE` | SQLite in the data directory | `postgres://…` for PostgreSQL. The `_FILE` form reads the value from a file. |
| `RGD_TRUSTED_PROXIES` | none (Compose: the project's network) | Address ranges of your reverse proxy. `X-Forwarded-For` is believed only from these. |
| `RGD_CLI_DIST_DIR` | `<data dir>/cli`, then `/usr/share/rigdepot/cli` | Where the release archives of `rgd` are that the server hands out. |
| `RGD_CLI_DOWNLOAD_BASE` | none | A directory with a release's `checksums.txt` and archives (a mirror, an internal artifact store), for the install scripts when no archives are on the server. |

HTTPS is normally left to a reverse proxy in front of the server. Plain HTTP is refused unless the server only listens on this machine, has a certificate, or is told it sits behind a proxy (`RGD_TRUSTED_PROXIES`), because personal secrets travel over this connection. `RGD_INSECURE_HTTP=1` lifts the rule for an isolated trial.

## First start

On first start the server creates its database and master key and prints a one-time **setup code** in its log:

```json
{"level":"WARN","msg":"setup is not complete: open the server's address in a browser and enter this one-time setup code","base_url":"https://hub.example.com","setup_code":"…"}
```

Open the public address in a browser and enter it to create the first administrator. `rgd-server doctor` and `rgd-server config check` say what is missing when it does not start. Where to find the log: `journalctl -u rigdepot-server` (packages), `docker compose logs rigdepot` (Compose), `docker logs <container>` (image).

## Handing out the command line

Every server hands out `rgd`, the command line, on its own address, so that the team's computers do not need the internet to get it:

```sh
curl -fsSL https://hub.example.com/install.sh | sh          # macOS, Linux
irm https://hub.example.com/install.ps1 | iex               # Windows
```

The scripts are [../scripts/install.sh](../scripts/install.sh) and [../scripts/install.ps1](../scripts/install.ps1) with the server's address, the version and each binary's SHA-256 filled in; they download from `https://hub.example.com/downloads/rgd-<os>-<arch>` and refuse a file that does not match. The connection guide in the web console shows the same commands (`GET /api/v1/cli/downloads`).

The server hands out the release archives it finds in the first of these directories that has any:

1. `RGD_CLI_DIST_DIR`, when set; otherwise
2. `<data dir>/cli` — copy the `rgd_<version>_<os>_<arch>.tar.gz` / `.zip` files and `checksums.txt` of a release here to hand out another version, or when you run the single binary;
3. `/usr/share/rigdepot/cli` — the packages and the image put the archives of their own version here.

With `checksums.txt` next to them, every archive is checked against it, and one that does not match or is not listed is left out (the log says which). Of several versions, the newest is handed out. With no archives at all, the install scripts download from `RGD_CLI_DOWNLOAD_BASE` when it is set, and do not exist otherwise.

## Debian and RPM packages

```sh
sudo apt install ./rigdepot-server_0.4.0_amd64.deb        # Debian, Ubuntu
sudo dnf install ./rigdepot-server-0.4.0-1.x86_64.rpm     # Fedora, Rocky Linux, RHEL
```

The package installs `/usr/bin/rgd-server`, the systemd unit, a system user `rigdepot` that owns `/var/lib/rigdepot` (`0700`), `/etc/rigdepot/server.env` (`0600`, kept on upgrades) and the six `rgd` archives in `/usr/share/rigdepot/cli/`. It does not start the server, which needs its address first:

```sh
sudoedit /etc/rigdepot/server.env        # RGD_BASE_URL=https://hub.example.com, RGD_TRUSTED_PROXIES=127.0.0.1
sudo systemctl enable --now rigdepot-server
journalctl -u rigdepot-server            # the setup code
```

An upgrade restarts a running server; removing the package stops it and leaves the data directory, the configuration and the user in place.

The unit only lets the process write to the data directory and limits its memory; the numbers are starting points for a small machine.

## Docker Compose

```sh
cd deploy
cp .env.example .env      # then set RGD_BASE_URL
docker compose up -d
docker compose logs rigdepot              # the setup code
```

Until an image is published, or to try a change, build it from this checkout first: `docker compose build`.

The server listens on `127.0.0.1:8080` only. Point your reverse proxy at it. The container runs read-only, as a user without privileges, with all state in the `rigdepot-data` volume.

**Trusted proxies.** The project gets a network with a fixed address range, `RGD_SUBNET` (default `172.31.250.0/24`), and `RGD_TRUSTED_PROXIES` defaults to it. A reverse proxy on the host reaches the container through that network's gateway, and a proxy in another container of this project from inside it, so those are believed, and nothing else can reach the port. If the range overlaps a network you already have, `docker compose up` says so: pick another `RGD_SUBNET`. When the proxy is elsewhere, set `RGD_TRUSTED_PROXIES` to its address.

### PostgreSQL

Create two files in [secrets/](secrets/), which Git ignores:

```sh
(umask 077; openssl rand -hex 24 > secrets/postgres_password)
printf 'postgres://rigdepot:%s@postgres:5432/rigdepot\n' "$(cat secrets/postgres_password)" > secrets/database_url
```

The server in the container runs as user `10001` and has to be able to read `database_url`:

```sh
sudo chown 10001 secrets/database_url && sudo chmod 400 secrets/database_url
```

Then start both files together, or set `COMPOSE_FILE=compose.yaml:compose.postgres.yaml` in `.env`:

```sh
docker compose -f compose.yaml -f compose.postgres.yaml up -d
```

The data volume still holds the master key and the repository cache. Do not drop it.

## Container image

```sh
docker run -d --name rigdepot --read-only --tmpfs /tmp -p 127.0.0.1:8080:8080 \
  -e RGD_BASE_URL=https://hub.example.com -e RGD_TRUSTED_PROXIES=172.16.0.0/12 \
  -v rigdepot-data:/var/lib/rigdepot ghcr.io/code2rich/rigdepot-server:0.4.0
```

One tag holds `linux/amd64` and `linux/arm64`. Without access to a registry, download `rigdepot-server_<version>_linux_<arch>.docker.tar.gz` from the release and `docker load -i` it.

To build from source (the web console, the server and `rgd` for six platforms are all compiled inside the build):

```sh
docker build -t rigdepot-server .                 # from the repository root
docker buildx build --platform linux/amd64,linux/arm64 -t rigdepot-server .
```

The second command needs a builder that supports several platforms: turn on the containerd image store in Docker, or create a `docker-container` builder.

## Single binary without a package

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

To hand out the command line, copy a release's `rgd_*` archives and `checksums.txt` into `/var/lib/rigdepot/cli/`.
