#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# After rigdepot-server is installed or upgraded (deb: "configure" with the
# previous version on an upgrade; rpm: 1 on an install, 2 on an upgrade).
#
# A new installation is not started: the server needs RGD_BASE_URL first. An
# upgrade restarts a running server, which migrates its database on start.
set -e

upgrade=false
case "${1:-}" in
  configure) [ -n "${2:-}" ] && upgrade=true ;;
  [0-9]*) [ "$1" -ge 2 ] && upgrade=true ;;
esac

# The data directory belongs to the service's user alone. Not recursive: an
# upgrade does not touch what is inside.
install -d -o rigdepot -g rigdepot -m 0700 /var/lib/rigdepot
chown root:root /etc/rigdepot/server.env
chmod 0600 /etc/rigdepot/server.env

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  if $upgrade; then
    systemctl try-restart rigdepot-server.service || true
  fi
fi

if ! $upgrade; then
  cat <<'MESSAGE'

rigdepot-server is installed but not started. Next:

  1. Set RGD_BASE_URL in /etc/rigdepot/server.env (and RGD_TRUSTED_PROXIES
     when a reverse proxy on this machine terminates HTTPS).
  2. systemctl enable --now rigdepot-server
  3. journalctl -u rigdepot-server shows the one-time setup code for the
     setup wizard at the public address.

Back up /var/lib/rigdepot/master.key, apart from the database.

MESSAGE
fi
