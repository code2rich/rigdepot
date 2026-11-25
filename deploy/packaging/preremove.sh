#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# Before rigdepot-server is removed (deb: "remove"; rpm: 0). Not on an
# upgrade (deb: "upgrade"; rpm: 1), which restarts the service afterwards.
set -e

case "${1:-}" in
  remove | 0)
    if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
      systemctl disable --now rigdepot-server.service || true
    fi
    ;;
esac
