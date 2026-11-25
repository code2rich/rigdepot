# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# Aliases for people who type "make". Every target is one line that calls the
# real entry point, which needs neither make nor bash:
#
#     go run ./tools/dev <task>
#
# Do not put any logic here: what lives in this file cannot run on Windows.

.PHONY: deps check test gen check-linux release-snapshot release

deps:
	go run ./tools/dev deps

check:
	go run ./tools/dev check

test:
	go run ./tools/dev test

gen:
	go run ./tools/dev gen

check-linux:
	go run ./tools/dev check-linux

release-snapshot:
	go run ./tools/dev release-snapshot

release:
	go run ./tools/dev release
