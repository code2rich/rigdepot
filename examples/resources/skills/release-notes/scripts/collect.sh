#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# Lists the commits between two tags, one per line: short hash and subject.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: collect.sh <previous tag> <this tag>" >&2
	exit 2
fi
git log --no-merges --format='%h %s' "$1..$2"
