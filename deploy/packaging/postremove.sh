#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# After rigdepot-server is removed. The data directory, the configuration and
# the system user stay: the database and the master key are not something a
# package manager should delete.
set -e

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
