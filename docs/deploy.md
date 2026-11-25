<p align="right"><b>English</b> · <a href="deploy.zh-CN.md">简体中文</a></p>

# Deploying and upgrading rgd-server

`rgd-server` is one program with one data directory. This guide takes you from an empty machine to a signed-in administrator, then covers HTTPS, configuration, handing out the command line, backups, upgrades and day-to-day operation. Someone who has run a web service before should be done within a day.

The files it refers to are in [deploy/](../deploy/). Examples use `hub.example.com` for the server's public address; put your own in its place.

## Contents

- [Before you start](#before-you-start)
- [Choose a form](#choose-a-form)
- [Docker Compose with SQLite](#docker-compose-with-sqlite)
- [Docker Compose with PostgreSQL](#docker-compose-with-postgresql)
- [Debian or RPM package with systemd](#debian-or-rpm-package-with-systemd)
- [Other ways to run it](#other-ways-to-run-it): the container image on its own, a single binary
- [Reverse proxy](#reverse-proxy)
- [HTTPS](#https)
- [First start and the setup wizard](#first-start-and-the-setup-wizard)
- [Configuration](#configuration)
- [Handing out the command line](#handing-out-the-command-line)
- [Backup and restore](#backup-and-restore)
- [Upgrading](#upgrading)
- [Operations commands](#operations-commands)
- [Health checks and metrics](#health-checks-and-metrics)
- [Minimum resources](#minimum-resources)
- [When something goes wrong](#when-something-goes-wrong)

## Before you start

You need:

- **A Linux machine**, amd64 or arm64. The server also runs on macOS for a trial; Windows is not supported as a server.
- **A name for it**, such as `hub.example.com`, that people's browsers and the command line reach. Sign-in callbacks, links in emails, cookies and the install command all depend on this address, so pick it before the first start.
- **HTTPS.** Personal credentials travel over this connection, so the server refuses plain HTTP beyond the machine it runs on. Normally a reverse proxy you already run terminates HTTPS; the server can also do it itself (see [HTTPS](#https)).
- **Outgoing access** to what you connect it to: your resource repositories, your identity provider (OIDC or GitLab) and your mail server. The server never contacts the project or any other address of its own accord.
- **`git`**, 2.31 or later, to pull resource repositories. The image has it and the packages depend on it.

Two things will need a backup: the **database** and the **master key**. They are kept apart on purpose; see [Backup and restore](#backup-and-restore).

## Choose a form

| Form | Pick it when | Database | Section |
| --- | --- | --- | --- |
| Docker Compose | You want one command to start, and Docker is available | SQLite in a volume | [Compose with SQLite](#docker-compose-with-sqlite) |
| Docker Compose with PostgreSQL | Your organisation wants the data in PostgreSQL | PostgreSQL in a second container | [Compose with PostgreSQL](#docker-compose-with-postgresql) |
| Debian or RPM package | One Linux machine, no containers | SQLite in `/var/lib/rigdepot`, or an external PostgreSQL | [Package with systemd](#debian-or-rpm-package-with-systemd) |
| Container image | You run containers your own way (Kubernetes, Nomad, plain `docker run`) | SQLite on a volume, or an external PostgreSQL | [Other ways](#other-ways-to-run-it) |

SQLite is the default and is enough for a team of a few hundred on one machine. Choose PostgreSQL when you want several instances, or when your organisation requires a managed database.

Every form keeps the same things in its data directory:

| Path in the data directory | What | Back up? |
| --- | --- | --- |
| `rigdepot.db` (with `-wal` and `-shm` next to it while running) | The SQLite database | Yes, with `rgd-server backup` |
| `master.key` | The master key, unless configured elsewhere | Yes, separately |
| `cache/repos/` | Bare clones of the resource repositories | No; pulled again |
| `acme/` | Certificates the server obtained itself, when it does | No; obtained again |
| `backups/` | Snapshots from `rgd-server backup` and before migrations, unless `backup.dir` says otherwise | Copy them off the machine |
| `cli/` | Optional: `rgd` release archives to hand out (see [Handing out the command line](#handing-out-the-command-line)) | No |

## Docker Compose with SQLite

You need Docker with the Compose plugin (`docker compose version` answers), and a copy of the [deploy/](../deploy/) directory: clone the repository, or download it from the release.

**1. Configure.** In `deploy/`:

```sh
cp .env.example .env
```

Edit `.env` and set at least the public address:

```sh
RGD_BASE_URL=https://hub.example.com
```

The other lines of `.env.example` are optional:

| Variable | Default | When to set it |
| --- | --- | --- |
| `RGD_VERSION` | `latest` | Always in production: pin the full version, such as `0.4.0`. |
| `RGD_PORT` | `8080` | When port 8080 on this machine is taken. The server is published on `127.0.0.1:<port>` only. |
| `RGD_SUBNET` | `172.31.250.0/24` | When this range overlaps a network you already have; `docker compose up` says so. |
| `RGD_TRUSTED_PROXIES` | the value of `RGD_SUBNET` | When the reverse proxy is not on this machine: its address. |

**2. Start.**

```sh
docker compose up -d
```

Docker pulls `ghcr.io/code2rich/rigdepot-server`. Without access to that registry, load the image file from the release first (`docker load -i rigdepot-server_<version>_linux_<arch>.docker.tar.gz`), or build the image from the repository with `docker compose build` (it compiles everything inside Docker and takes a few minutes).

**3. Check that it runs.**

```sh
docker compose ps                          # the container is "healthy" after a few seconds
curl -s http://127.0.0.1:8080/readyz       # {"status":"ok",…} or "degraded" while mail is not set up
docker compose logs rigdepot | grep setup_code
```

The last line shows the one-time **setup token**:

```json
{"level":"WARN","msg":"setup is not complete: open the server's address in a browser and enter this one-time setup code","base_url":"https://hub.example.com","setup_code":"<placeholder: setup token>"}
```

**4. Put the reverse proxy in front**, pointing at `127.0.0.1:8080`: see [Reverse proxy](#reverse-proxy). `RGD_TRUSTED_PROXIES` already covers a proxy on this machine.

**5. Open `https://hub.example.com`** and complete the [setup wizard](#first-start-and-the-setup-wizard) with the setup token. You are signed in as the first administrator.

**6. Back up the master key now**, to somewhere other than this machine's backups, and set up backups of the database (see [Backup and restore](#backup-and-restore)):

```sh
docker compose cp rigdepot:/var/lib/rigdepot/master.key ./master.key   # then move it somewhere safe
```

What Compose sets up: one container, running as an unprivileged user (10001) with a read-only root file system, no capabilities and `no-new-privileges`; a volume `rigdepot-data` with everything that needs keeping; a network with the fixed range `RGD_SUBNET`, so that `RGD_TRUSTED_PROXIES` can name it. A reverse proxy on the host reaches the container through that network's gateway, and nothing else can reach the port.

Commands inside the container run as `docker compose exec rigdepot rgd-server <command>`, for example `docker compose exec rigdepot rgd-server doctor`. `config check` notes that the server listens on `0.0.0.0:8080` behind a proxy: inside the container it has to, and Compose publishes the port on `127.0.0.1` only, so the note does not apply here.

> **Trying it on your own computer.** Set `RGD_BASE_URL=http://localhost:8080` and open that address; no proxy is needed. `doctor` and `config check` will report that a proxy is declared while the address is `http://`. That is expected for a trial, and goes away with the real address and the proxy.

**Other settings.** `compose.yaml` passes only the variables above. For any other setting from [Configuration](#configuration), create `compose.override.yaml` next to it, which Compose reads automatically:

```yaml
services:
  rigdepot:
    environment:
      RGD_BACKUP_DIR: /var/lib/rigdepot/backups
      RGD_DEFAULT_LOCALE: zh-CN
```

When `COMPOSE_FILE` is set in `.env` (as for PostgreSQL), add the override to that list: `COMPOSE_FILE=compose.yaml:compose.postgres.yaml:compose.override.yaml`.

## Docker Compose with PostgreSQL

The same as above, plus a PostgreSQL 17 container. Before `docker compose up`, create two files in `deploy/secrets/` (Git ignores that directory):

```sh
(umask 077; openssl rand -hex 24 > secrets/postgres_password)
printf 'postgres://rigdepot:%s@postgres:5432/rigdepot\n' "$(cat secrets/postgres_password)" > secrets/database_url
sudo chown 10001 secrets/database_url && sudo chmod 400 secrets/database_url
```

The server in the container runs as user 10001 and has to be able to read `database_url`. Then start both files together:

```sh
docker compose -f compose.yaml -f compose.postgres.yaml up -d
```

or put `COMPOSE_FILE=compose.yaml:compose.postgres.yaml` in `.env` and run `docker compose up -d`. The data volume `rigdepot-data` is still needed: it holds the master key and the repository cache. The database is in the `postgres-data` volume.

To use a PostgreSQL you already run instead, leave out `compose.postgres.yaml` and pass `RGD_DATABASE_URL_FILE` (or `RGD_DATABASE_URL`) through `compose.override.yaml`. The server needs a database it owns; it creates its tables itself.

## Debian or RPM package with systemd

Download the package for your architecture from the release:

```sh
sudo apt install ./rigdepot-server_0.4.0_amd64.deb        # Debian, Ubuntu
sudo dnf install ./rigdepot-server-0.4.0-1.x86_64.rpm     # Fedora, Rocky Linux, RHEL
```

The package installs:

| Path | What |
| --- | --- |
| `/usr/bin/rgd-server` | The program |
| `/usr/lib/systemd/system/rigdepot-server.service` | The systemd unit |
| `/etc/rigdepot/server.env` | Startup configuration, `0600`, kept on upgrades |
| `/var/lib/rigdepot/` | The data directory, `0700`, owned by the system user `rigdepot` |
| `/usr/share/rigdepot/cli/` | The six `rgd` archives of the same version, to hand out |

It depends on `git` and does not start the server, which needs its address first:

```sh
sudoedit /etc/rigdepot/server.env
```

```sh
RGD_BASE_URL=https://hub.example.com
RGD_TRUSTED_PROXIES=127.0.0.1           # the reverse proxy runs on this machine
```

```sh
sudo systemctl enable --now rigdepot-server
journalctl -u rigdepot-server | grep setup_code
```

Then the reverse proxy, the [setup wizard](#first-start-and-the-setup-wizard) and the master key backup, as for Compose. Operations commands run as the service user, with the same configuration:

```sh
sudo -u rigdepot sh -c 'set -a; . /etc/rigdepot/server.env; exec rgd-server doctor'
```

An upgrade of the package restarts a running server. Removing it stops the server and leaves the data directory, the configuration and the user in place.

The unit lets the process write only to the data directory (`ProtectSystem=strict`, `ReadWritePaths=/var/lib/rigdepot`), and limits memory with `GOMEMLIMIT=384MiB` and `MemoryMax=512M`; these are starting points for a small machine. To put `backup.dir` outside the data directory, add it to `ReadWritePaths` with `systemctl edit rigdepot-server`.

For PostgreSQL, put `RGD_DATABASE_URL=postgres://…` (or `RGD_DATABASE_URL_FILE=<file>`) into `server.env`.

## Other ways to run it

### The container image on its own

```sh
docker run -d --name rigdepot --read-only --tmpfs /tmp -p 127.0.0.1:8080:8080 \
  -e RGD_BASE_URL=https://hub.example.com -e RGD_TRUSTED_PROXIES=172.16.0.0/12 \
  -v rigdepot-data:/var/lib/rigdepot ghcr.io/code2rich/rigdepot-server:0.4.0
```

One tag holds `linux/amd64` and `linux/arm64`. In the image, the server listens on `0.0.0.0:8080`, runs as user 10001, keeps its data in `/var/lib/rigdepot` and has a health check (`rgd-server healthcheck`). Because it listens on every interface of the container, it needs `RGD_TRUSTED_PROXIES` (the range the proxy's requests come from), a certificate, or `RGD_INSECURE_HTTP=1` for an isolated trial. Without access to a registry, `docker load -i` the image file from the release. To build it from the repository: `docker build -t rigdepot-server .` (the web console, the server and `rgd` for six platforms are compiled inside the build).

### A single binary without a package

From the release archive `rgd-server_<version>_linux_<arch>.tar.gz`:

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

`git` must be installed. To hand out the command line, copy a release's `rgd_*` archives and `checksums.txt` into `/var/lib/rigdepot/cli/`.

## Reverse proxy

The proxy terminates HTTPS and forwards to the server on `127.0.0.1:8080`. Samples: [deploy/proxy/Caddyfile](../deploy/proxy/Caddyfile) and [deploy/proxy/nginx.conf](../deploy/proxy/nginx.conf).

What the proxy has to do:

- Pass `Host`, `X-Forwarded-For` and `X-Forwarded-Proto`. Caddy does it on its own; nginx needs the `proxy_set_header` lines of the sample.
- Allow request bodies at least as large as `RGD_MAX_BUNDLE_MB` (5 MB by default); the samples allow 16 MB.
- Nothing else: there are no WebSockets and no long-lived connections.

What the server has to know: `RGD_TRUSTED_PROXIES`, the address or range the proxy's requests come from. The server believes `X-Forwarded-For` only from there, which matters for rate limits and the audit log, and setting it declares that a proxy terminates HTTPS, which is what allows plain HTTP between the proxy and the server.

| Setup | `RGD_TRUSTED_PROXIES` |
| --- | --- |
| Package or binary, proxy on the same machine | `127.0.0.1` |
| Compose, proxy on the host or in a container of the same project | Nothing to do: it defaults to `RGD_SUBNET` |
| Proxy on another machine | That machine's address |

With Caddy and a public name, Caddy obtains the certificate itself. On an internal network, give it your organisation's certificate with `tls <cert file> <key file>` in the site block, or use the nginx sample with your certificate paths.

The server can live under a path, such as `https://www.example.com/rigdepot`: put the path in `RGD_BASE_URL` and forward that path unchanged. `/healthz` and `/readyz` answer at the root as well.

## HTTPS

Three ways, in order of preference:

| Way | When | Settings |
| --- | --- | --- |
| **A reverse proxy** | Most of the time. Internal deployments usually have their own certificate authority, and most teams already run a proxy | `RGD_TRUSTED_PROXIES` |
| **Certificate files** | No proxy; certificates come from somewhere else | `RGD_TLS_CERT_FILE`, `RGD_TLS_KEY_FILE` |
| **Automatic certificates (ACME)** | No proxy, a public name, and port 443 reachable from the internet | `RGD_TLS_ACME_DOMAINS`, `RGD_TLS_ACME_EMAIL` |

With certificate files, the server serves HTTPS on `RGD_LISTEN` (set it to `:443`, or forward 443 to it). It reloads the files when it receives `SIGHUP` (`systemctl kill -s HUP rigdepot-server`, `docker compose kill -s HUP rigdepot`) and when it notices they changed, checked every minute, so a renewed certificate needs no restart. A pair that does not load is reported in the log and the old certificate stays in use.

With automatic certificates, the server obtains and renews them from Let's Encrypt (or the authority in `RGD_TLS_ACME_DIRECTORY_URL`) with the TLS-ALPN-01 challenge on port 443, and keeps them in `acme/` in the data directory. `RGD_TLS_ACME_DOMAINS` must include the host of `RGD_BASE_URL`; addresses and wildcards are not possible. `RGD_HTTP_REDIRECT_LISTEN=:80` adds a plain listener that redirects to HTTPS and also answers the HTTP-01 challenge.

`rgd-server doctor` warns 14 days before a certificate file runs out and 20 days before an automatic one does (they are normally renewed 30 days before).

## First start and the setup wizard

On the first start with an empty data directory the server creates the database and runs its migrations, generates the master key (`master.key`, unless you gave one) and prints two warnings: back up the master key, and the setup token. Until setup is complete, every start prints the same token again.

Open the public address in a browser. The wizard has five steps:

1. **Create administrator**: the setup token, then the account name and password of a local administrator. This account stays your way in when an identity provider has trouble.
2. **Sign-in methods**: local accounts, OIDC (your company's single sign-on) or GitLab. Each can be set up or changed later under *Users & sign-in*. The callback address to register with the identity provider is shown there.
3. **Resource repository**: the Git repository the server imports resources from, by `https://` or `ssh` address and branch, with a read-only token for an `https://` repository that needs one. The server pulls it now and every `RGD_SOURCE_SYNC_INTERVAL` (10 minutes); new and changed resources arrive as drafts, and people decide what is published. The step can be skipped: repositories are also managed under *Settings*, and under *Resources* single resources can be uploaded as an archive or, for MCP servers, registered on the web.
4. **Data & privacy**: optional modules, all off by default. This version has none.
5. **First product**: a product decides who sees which resources. Create one now, or later under *Products & members*.

Finishing the wizard invalidates the setup token for good. If you would rather choose the token yourself (for automated deployments), set `RGD_SETUP_TOKEN` or `RGD_SETUP_TOKEN_FILE` before the first start; the server then does not print it.

Lost the administrator's password later? Run `rgd-server reset-admin` on the server (see [Operations commands](#operations-commands)).

After the wizard, in the console:

- **Settings → Mail**: an SMTP server, so that invitations and notifications go out by mail. Until then, invitation links are shown to copy.
- **Users & sign-in**: invite people, or let them sign in with the identity provider and approve their join requests.
- **Connect** (every member sees it): the commands to install `rgd` and sign in.

## Configuration

There are two kinds of configuration, kept apart:

- **Startup configuration**: what the process needs before it can open its database. It comes from environment variables, a TOML file and command-line flags, and is listed below.
- **Run-time settings**: sign-in methods, the mail server, resource repositories, tool adapters, the minimum command-line version, optional modules. They live in the database, are changed in the console (setup wizard and *Settings*), are audited, and are shared by every instance.

Precedence, highest first: **command-line flags > environment variables > the configuration file > defaults.** An empty variable counts as not set. `rgd-server config check` prints every setting, its value and where the value came from (secrets only as "set"), then what does not fit together.

The configuration file is named by `RGD_CONFIG` or `--config`. It is TOML; a dot in a key is a table:

```toml
base_url = "https://hub.example.com"
trusted_proxies = ["127.0.0.1"]

[log]
level = "info"

[backup]
dir = "/srv/rigdepot-backups"
keep = 30
```

An unknown key in the file is an error, so that a typo does not silently leave a default in place. Secrets can be given as a file: `RGD_DATABASE_URL_FILE` and `RGD_SETUP_TOKEN_FILE` read the value from the file they name (Docker secrets, systemd credentials). Secrets are never flags, because flags show in the process list.

| Variable | File key | Flag | Default | Meaning |
| --- | --- | --- | --- | --- |
| `RGD_CONFIG` | — | `--config` | none | The configuration file. |
| `RGD_BASE_URL` | `base_url` | `--base-url` | **required** | The public address, `https://` (or `http://` on this machine only), optionally with a path. Sign-in callbacks, the device confirmation page, links in emails, whether cookies are `Secure`, and the install command depend on it. |
| `RGD_LISTEN` | `listen` | `--listen` | `127.0.0.1:8080` (`0.0.0.0:8080` in the image) | Address and port to listen on. |
| `RGD_DATA_DIR` | `data_dir` | `--data-dir` | `/var/lib/rigdepot` | The data directory; created `0700` when missing. |
| `RGD_DATABASE_URL`, `RGD_DATABASE_URL_FILE` | `database_url` | — | `sqlite://<data dir>/rigdepot.db` | `sqlite://<path>` or `postgres://<user>:<password>@<host>:<port>/<database>`. Secret. |
| `RGD_MASTER_KEY_FILE` | `master_key_file` | `--master-key-file` | `<data dir>/master.key` | The master key file. Generated on first start when missing and the database holds nothing encrypted yet. |
| `RGD_MASTER_KEY` | — | — | none | The master key itself, 32 bytes in standard base64. Instead of the file, not both. Secret. |
| `RGD_SETUP_TOKEN`, `RGD_SETUP_TOKEN_FILE` | — | — | generated and printed on first start | The one-time token of the setup wizard. Secret. |
| `RGD_AUTO_MIGRATE` | `auto_migrate` | `--auto-migrate` | `true` | Run database migrations on start. With `false`, a server with pending migrations does not start until you run `rgd-server migrate`. |
| `RGD_TLS_CERT_FILE`, `RGD_TLS_KEY_FILE` | `tls.cert_file`, `tls.key_file` | `--tls-cert-file`, `--tls-key-file` | none | Serve HTTPS with this certificate and key; both or neither. Reloaded on `SIGHUP` and when they change. |
| `RGD_TLS_ACME_DOMAINS` | `tls.acme_domains` | `--tls-acme-domains` | none | Obtain certificates automatically for these names (comma-separated; must include the host of the base URL). Excludes certificate files. |
| `RGD_TLS_ACME_EMAIL` | `tls.acme_email` | `--tls-acme-email` | none | Contact address given to the certificate authority. |
| `RGD_TLS_ACME_DIRECTORY_URL` | `tls.acme_directory_url` | `--tls-acme-directory-url` | Let's Encrypt | The ACME directory of another certificate authority. |
| `RGD_HTTP_REDIRECT_LISTEN` | `tls.http_redirect_listen` | `--http-redirect-listen` | none | A plain HTTP address, such as `:80`, that only redirects to HTTPS (and answers ACME's HTTP-01). Needs certificate files or ACME. |
| `RGD_TRUSTED_PROXIES` | `trusted_proxies` | `--trusted-proxies` | none (Compose: `RGD_SUBNET`) | Addresses or ranges of the reverse proxy, comma-separated. Declares that a proxy terminates HTTPS. |
| `RGD_INSECURE_HTTP` | — | `--insecure-http` | `false` | Allow plain HTTP beyond this machine. Only for an isolated trial. |
| `RGD_DEFAULT_LOCALE` | `default_locale` | `--default-locale` | `en` | The language when neither the user nor the browser says: `en` or `zh-CN`. Also the language of test mails. |
| `RGD_LOG_LEVEL` | `log.level` | `--log-level` | `info` | `debug`, `info`, `warn` or `error`. |
| `RGD_LOG_FORMAT` | `log.format` | `--log-format` | `json` | `json` or `text`. Logs go to standard output; secrets are masked. |
| `RGD_METRICS_ADDR` | `metrics_addr` | `--metrics-addr` | off | Address for `GET /metrics`, such as `127.0.0.1:9464`. Never the public port. |
| `RGD_GIT` | `git` | `--git` | `git` | The git program. |
| `RGD_SOURCE_SYNC_INTERVAL` | `source_sync_interval` | `--source-sync-interval` | `10m` | How often to pull the resource repositories, at least `1m`. |
| `RGD_MAX_BUNDLE_MB` | `max_bundle_mb` | `--max-bundle-mb` | `5` | The largest resource archive accepted, in MB (1–1024). |
| `RGD_CLI_DIST_DIR` | `cli_dist_dir` | `--cli-dist-dir` | `<data dir>/cli`, then `/usr/share/rigdepot/cli` | Where the `rgd` release archives to hand out are. When set, only this directory is searched. |
| `RGD_CLI_DOWNLOAD_BASE` | `cli_download_base` | `--cli-download-base` | none | An `https://` directory holding a release's `checksums.txt` and archives (a mirror, an internal artifact store), for the install scripts when no archives are on this server. |
| `RGD_BACKUP_DIR` | `backup.dir` | `--backup-dir` | off | Make a snapshot of the SQLite database here every day. Also where `backup` and pre-migration snapshots go. |
| `RGD_BACKUP_KEEP` | `backup.keep` | `--backup-keep` | `14` | How many daily snapshots to keep. |
| `RGD_MODULES_DISABLED` | `modules_disabled` | `--modules-disabled` | none | Optional modules never to load, even when turned on in the settings; comma-separated. |
| `RGD_WEB_DEV_PROXY` | — | `--web-dev-proxy` | none | Development only: serve the console from a development server on this machine. |

The rule about plain HTTP: the server starts without TLS only when it listens on loopback, or `RGD_TRUSTED_PROXIES` is set, or `RGD_INSECURE_HTTP=1`. A base URL of `http://` with a host other than this machine is refused in any case.

## Handing out the command line

Every server hands out `rgd` on its own address, so the team's computers install the version that fits the server without reaching the internet:

```sh
curl -fsSL https://hub.example.com/install.sh | sh          # macOS, Linux
irm https://hub.example.com/install.ps1 | iex               # Windows
```

The scripts are [scripts/install.sh](../scripts/install.sh) and [scripts/install.ps1](../scripts/install.ps1) with the server's address, the version and each file's SHA-256 filled in. They download from `https://hub.example.com/downloads/rgd-<os>-<arch>` and refuse a file that does not match. The console's *Connect* page shows the same commands.

The server looks for release archives (`rgd_<version>_<os>_<arch>.tar.gz` or `.zip`, with the release's `checksums.txt`) in the first of these that has any:

1. `RGD_CLI_DIST_DIR`, when set; otherwise
2. `<data dir>/cli`: copy a release's archives and `checksums.txt` here to hand out another version, or when you run a single binary;
3. `/usr/share/rigdepot/cli`: the packages and the image put the archives of their own version here.

Each archive is checked against `checksums.txt`; one that does not match or is not listed is left out, and the log says which. Of several versions, the newest is handed out; the log line `handing out the command line` at startup names it. With no archives anywhere, the install scripts download from `RGD_CLI_DOWNLOAD_BASE` when it is set, and are not offered otherwise.

## Backup and restore

Two things, kept in **different places**:

| What | How to back it up | Notes |
| --- | --- | --- |
| The database (SQLite) | `rgd-server backup`, or `RGD_BACKUP_DIR` for a daily snapshot | Consistent while the server runs. **Do not copy `rigdepot.db` itself while the server runs**: the copy may be half written. |
| The database (PostgreSQL) | `pg_dump`, or your provider's snapshots | `rgd-server backup` refuses and says so. |
| The master key | Copy `master.key` (or keep the value you gave in `RGD_MASTER_KEY`) | Once, and again after `keys rotate`. Never into the same place as the database backups. |

The master key is never in a database backup, on purpose: a stolen backup without the key reveals no secret. **If the master key is lost, everything encrypted in the database (people's personal secrets, the client secrets of identity providers, repository tokens, the mail password) cannot be read again** and has to be entered again; everything else is unaffected. With the key and a backup, everything can be read, so keep backups like other sensitive data.

```sh
rgd-server backup                     # into backup.dir, else <data dir>/backups
rgd-server backup --out /srv/backups  # somewhere else
```

It writes `rigdepot-<time>.db`, says where the master key is, and prints the command to check the snapshot. With `RGD_BACKUP_DIR` set, the server makes one every day and keeps the newest `RGD_BACKUP_KEEP`; it deletes only files it wrote itself. Copy the snapshots off the machine with your usual tools. `doctor` warns when the daily backup is more than 50 hours old, and `/readyz` reports when the last one failed.

**Check a backup regularly**, without touching the data in use:

```sh
rgd-server restore --check /srv/backups/rigdepot-20261125T030000Z.db
```

It opens the snapshot read-only and checks that it is a sound SQLite database, which schema version it has (and whether this server would migrate it), that the configured master key opens it, and that its audit chain is intact. It ends with the steps to restore it.

**Restoring** is done by hand with the server stopped, as `restore --check` prints:

1. Stop the server (`systemctl stop rigdepot-server`, `docker compose stop rigdepot`).
2. Copy the snapshot to `<data dir>/rigdepot.db`.
3. Delete `rigdepot.db-wal` and `rigdepot.db-shm` next to it, if they are there.
4. Put the master key back in place, if it was lost.
5. Start the server.

With Compose, the data directory is in the volume; copy the snapshot in through a container that mounts it, for example `docker compose run --rm --no-deps -v /srv/backups:/restore:ro --entrypoint sh rigdepot -c 'cp /restore/<snapshot> /var/lib/rigdepot/rigdepot.db && rm -f /var/lib/rigdepot/rigdepot.db-wal /var/lib/rigdepot/rigdepot.db-shm'`.

With PostgreSQL, restore with `pg_restore` (or your provider's tools) while the server is stopped, and make sure the server has the master key that matches.

## Upgrading

**An upgrade is a new program (or image tag) and a restart.** On start the server runs the migrations it has not run yet, from any earlier version, in order; then it serves.

- **Compose:** set `RGD_VERSION` to the new version in `.env`, then `docker compose pull && docker compose up -d`.
- **Package:** `apt install ./rigdepot-server_<new>_<arch>.deb` or `dnf install ./rigdepot-server-<new>-1.<arch>.rpm`; the package restarts the server.
- **Image or binary:** replace it and restart.

Before you start, read the release notes, and have a fresh backup and the master key at hand.

**What protects you.** With SQLite, before running migrations on a database that has data, the server writes a snapshot `pre-migrate-<from>-<to>-<time>.db` (the numbers are schema versions) to `backup.dir`, or to `backups/` in the data directory. If the snapshot cannot be written, the migrations do not run and the server does not start. With PostgreSQL, the server does not make a backup: it logs a warning to take one. To control that step yourself, set `RGD_AUTO_MIGRATE=false`, back up, run `rgd-server migrate`, then start.

**Going back.** Migrations only go forward. A program older than the database refuses to start ("the database was migrated by a newer version of this program") rather than run against tables it does not know. To return to the previous version:

1. Stop the server.
2. Restore the `pre-migrate-…` snapshot (or your backup from before the upgrade) as in [Backup and restore](#backup-and-restore).
3. Put the previous program or image tag back.
4. Start it.

Changes made between the upgrade and going back are lost.

**Several instances (PostgreSQL).** Migrations take a lock: the first instance to start runs them and the others wait. Newer and older instances are not meant to serve at the same time, so during an upgrade keep one instance running.

**The command line.** Upgrade the server first. The server accepts `rgd` of its own version and of the previous minor version; an older `rgd` is refused on sync with exit code 8 and told to upgrade (status and uninstall keep working). *Tool adapters* in the console can raise the minimum version, not lower it below that. People upgrade `rgd` by running the install command again; it never upgrades itself.

## Operations commands

Every command reads the same configuration as `serve` (environment, file, flags), so run it as the service user with the service's environment: `docker compose exec rigdepot rgd-server <command>`, or for the package `sudo -u rigdepot sh -c 'set -a; . /etc/rigdepot/server.env; exec rgd-server <command>'`. `rgd-server <command> -h` lists a command's flags.

| Command | What it does |
| --- | --- |
| `serve` | Start the server (the default when no command is given). |
| `migrate` | Only run the database migrations, with the snapshot before them on SQLite. |
| `config check` | Print every setting, its value and where it came from, then contradictions. Exit 1 on a problem, 2 when the configuration does not load. |
| `doctor` | Check the environment item by item: configuration, data directory, database, migrations, master key, audit log, mail, certificate, disk space, git, backups. Looks and never repairs. Exit 1 when an item fails; warnings do not change the exit code. |
| `backup [--out <dir>]` | A consistent snapshot of the SQLite database while it runs. |
| `restore --check <snapshot>` | Check a snapshot and print how to restore it. Never changes data. |
| `reset-admin [--user <name>] [--password-stdin]` | Set a new password for a platform administrator, straight in the database: the way back in when the password is forgotten or the identity provider is down. Prints a generated password once (or reads one from standard input), lifts the account's lock, ends its browser sessions and records it in the audit log. |
| `keys rotate [--out <file>]` | Make a new master key current and re-wrap every encrypted record with it; the old key stays in the file as retired, so a running server keeps working. Nobody has to enter anything again. With the key in `RGD_MASTER_KEY`, `--out` is required, and the server is restarted with `RGD_MASTER_KEY_FILE` pointing at the new file. Back up the key file afterwards. |
| `audit verify` | Check the whole hash chain of the audit log. Exit 1 when an entry was changed, removed or inserted. The server also checks it every day and notifies the administrators. |
| `healthcheck [--ready]` | Ask the server on this machine for `/healthz` (or `/readyz`); for container health checks. |
| `version` | Print the version. |
| `setup --from <file>` | Unattended setup. Not implemented in this version. |

## Health checks and metrics

- `GET /healthz` answers 200 while the process serves.
- `GET /readyz` answers a JSON document with one entry per check: `database`, `migrations` and `master_key` make it 503 (`unavailable`) when they fail; `mail`, `audit_chain`, `sources` (a resource repository whose last pull failed) and, with daily backups, `backup` only make it `degraded` with 200; the server keeps serving what the last good pull imported. "Degraded" because mail is not set up is normal until you set it up.

With `RGD_METRICS_ADDR` set, `GET /metrics` on that separate address serves metrics in the Prometheus text format. All names start with `rgd_server_`:

| Metric | Type | What |
| --- | --- | --- |
| `build_info` | gauge | The version, as a label. |
| `http_requests_total`, `http_request_duration_seconds` | counter, histogram | Requests by route template and status code. |
| `job_runs_total`, `job_run_seconds_total` | counter | Background jobs by job and result. |
| `mail_outbox_messages`, `mail_sent_last_hour` | gauge | Mail waiting, retrying or given up; mail sent. |
| `db_open_connections`, `db_in_use_connections`, `db_idle_connections`, `db_max_open_connections`, `db_waits_total`, `db_wait_seconds_total` | gauge, counter | The database connection pools. |

Labels carry only route templates, status codes, job names, results and pool names: nothing about people, resources or content.

Logs are JSON lines on standard output (`journalctl -u rigdepot-server`, `docker compose logs rigdepot`). Every request has a `request_id`, which also appears in error responses, so a user's error can be found in the log.

## Minimum resources

A machine with **2 CPU cores and 2 GB of memory** carries an organisation of **500 people** with a wide margin, using SQLite. These are measurements, made with the method below.

| | Measured (500 people, 130 resources, two AI tools each) |
| --- | --- |
| Syncs | Up to **40 syncs a second** with p95 under 0.35 s (25 a second: p95 0.08 s); the limit is about 50 a second, where answers start to queue. People who sync every 10 minutes make N/600 syncs a second, so 500 people make under one. No request failed in any phase (77,841 requests). |
| Everyone's first sync | All 500 computers at once, each downloading all 130 resource bundles: done in 44 s, no errors. 100 at a time: 48 s for all, 12 s each (p95). |
| Onboarding | 500 people accepting their invitation and signing a computer in: 9 s in all (accept p95 0.13 s, device login p95 0.06 s). |
| Publishing | A repository of 100 skills imported over HTTPS in 1.8 s; uploading and publishing a resource p95 under 0.05 s. |
| Console | 10 people and an administrator each loading five API calls a second: every page p95 0.6 s or less, except the administrator's home page figures (*installs summary*, *tool adapters*, *resources*) at about 1 s. Beyond about 25 busy browsers at once the catalog pages slow to over 1 s. |
| Memory | 50–70 MB at rest; up to about 340 MB at the limit of 50 syncs a second (the container's peak, page cache included, 441 MB). Far past the limit (100 syncs a second) waiting requests pile up and memory reached about 1 GB. |
| Disk | Tens to hundreds of MB for the database, plus the repository cache and backups. Keep at least 1 GB free: `doctor` warns below 1 GiB and fails below 100 MiB. |
| Network | Your identity provider, mail server and resource repositories; nothing else. |

The systemd unit limits the process to 512 MB (`MemoryMax`) and sets `GOMEMLIMIT=384MiB`: enough for the load above, not for a sustained overload. Change to PostgreSQL when you run several instances, or when your organisation requires a managed database; the number of people is not the reason at this size.

**How it was measured** (2026-11-25): `go run ./tools/dev load` from the source tree. It builds the server image, runs it with `docker run --cpus 2 --memory 2g --memory-swap 2g` and SQLite, and plays the organisation from outside the container: 500 people accept invitations and sign one computer in each with the device code flow; 30 skills are uploaded and a repository of 100 skills is imported over HTTPS and everything is published; every computer does a first sync (desired state, every bundle, report), 100 at a time; 10 members' browsers and an administrator's read the common pages; then computers sync at a fixed rate (5, 10, 25, 40, 50 a second, 30 s each; desired state with `If-None-Match`, then the report) while the administrator's console is read twice a second. Each request carries the client address of its own person behind the trusted proxy. The host was a Mac with Apple M1 Ultra cores under Docker Desktop; a 2-vCPU cloud machine with slower cores carries proportionally less. `go run ./tools/dev load -h` lists the options to repeat it with other numbers or against a fresh server of your own (`--url`, `--setup-token`).

## When something goes wrong

Start with `rgd-server doctor` and `rgd-server config check`; between them they name most problems and what to do.

| Symptom | Cause and fix |
| --- | --- |
| `refusing to serve plain HTTP beyond this machine` | The server would serve plain HTTP to the network. Behind a proxy, set `RGD_TRUSTED_PROXIES`; otherwise give a certificate. `RGD_BASE_URL` must be `https://` unless the host is this machine. |
| `base_url is required` | `RGD_BASE_URL` is not set (in a package: `/etc/rigdepot/server.env`). |
| `docker compose up` fails with a pool overlap | `RGD_SUBNET` overlaps an existing network: pick another range in `.env`. |
| `docker compose up` fails with "address already in use" | Port 8080 is taken: set `RGD_PORT`. |
| The setup token is lost | Restart the server: until setup is complete it prints the same token on every start. |
| The administrator's password is forgotten | `rgd-server reset-admin`. |
| The server does not start: the master key does not open the database | The key file is missing or wrong while the database holds encrypted data. Put the right `master.key` back; the server never replaces a key it needs. |
| `the database was migrated by a newer version of this program` | An older program meets a newer database: run the newer version, or go back as described in [Upgrading](#upgrading). |
| `rgd` says its version is too old (exit code 8) | Run the install command from the *Connect* page again. |
| Sign-in with the identity provider fails after the redirect | The callback address registered with the identity provider must match the one shown under *Users & sign-in*, which follows `RGD_BASE_URL`. |
| A resource repository is not imported | `/readyz` shows `sources` as degraded and the log says why. Check the address, the branch and the read-only token under *Settings*, and that `git --version` on the server is 2.31 or later. |
| People are not getting mail | Set up the mail server under *Settings*, use its test mail, and look at `rgd_server_mail_outbox_messages` or the log. |
