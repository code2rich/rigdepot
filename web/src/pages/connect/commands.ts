// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// The commands the connection guide and the home page's first steps show. The installer comes from this
// server (GET /api/v1/cli/downloads), so it works on an internal network; the package managers are the
// other way (docs/architecture/13-open-questions.md D10).

import type { Schemas } from '../../api/client'
import { BRAND } from '../../brand.gen'
import { basePath } from '../../router/paths'

/** serverAddress is this server as `rgd login` wants it: the console's own address. */
export function serverAddress(): string {
  return location.origin + basePath()
}

/** installScript is where the installer for macOS and Linux is. */
export function installScript(downloads?: Schemas['CLIDownloadList'] | null): string {
  return downloads?.install_sh ?? serverAddress() + '/install.sh'
}

/** installCommand downloads the installer, shows it, then runs it: look before running. */
export function installCommand(downloads?: Schemas['CLIDownloadList'] | null): string {
  return `curl -fsSL ${installScript(downloads)} -o install.sh && less install.sh && sh install.sh`
}

/** quickInstall is the one-liner for the home page's guide: install, then sign in. */
export function quickInstall(downloads?: Schemas['CLIDownloadList'] | null): string {
  return `curl -fsSL ${installScript(downloads)} | sh && ${BRAND.cli} login ${serverAddress()}`
}

export function loginCommand(): string {
  return `${BRAND.cli} login ${serverAddress()}`
}
