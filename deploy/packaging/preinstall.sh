#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# Before the files of rigdepot-server are unpacked (deb and rpm): the system
# user the service runs as. It owns /var/lib/rigdepot and nothing else.
set -e

if ! getent group rigdepot >/dev/null 2>&1; then
  groupadd --system rigdepot
fi
if ! getent passwd rigdepot >/dev/null 2>&1; then
  nologin=/usr/sbin/nologin
  [ -x "$nologin" ] || nologin=/sbin/nologin
  useradd --system --gid rigdepot --home-dir /var/lib/rigdepot --no-create-home \
    --shell "$nologin" --comment "Rigdepot server" rigdepot
fi
