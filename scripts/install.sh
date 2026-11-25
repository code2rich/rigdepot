#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# Installs rgd, the Rigdepot command-line program, on macOS and Linux.
#
# It does one thing: download the release archive for this platform, verify
# its SHA-256 checksum against the published checksums.txt, put the binary in
# place and tell you how to add it to PATH. It does not sign you in, sync
# anything, install a background job or ask for root.
#
# Read it before you run it:
#
#   curl -fsSLO https://raw.githubusercontent.com/code2rich/rigdepot/main/scripts/install.sh
#   sh install.sh
#
# Environment variables:
#
#   RGD_VERSION        version to install, such as 0.4.0 (default: the latest release)
#   RGD_INSTALL_DIR    directory to install into (default: $HOME/.local/bin)
#   RGD_DOWNLOAD_BASE  URL of a directory that holds checksums.txt and the
#                      archives, for a mirror or a network that cannot reach
#                      GitHub; https:// and file:// both work

set -eu

REPO="code2rich/rigdepot"

say() { printf '%s\n' "$*"; }
fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# fetch URL FILE: download quietly, fail on an HTTP error.
fetch() {
  if have curl; then
    curl -fsSL --retry 2 -o "$2" "$1"
  elif have wget; then
    wget -q -O "$2" "$1"
  else
    fail "curl or wget is required"
  fi
}

sha256_of() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required to verify the download"
  fi
}

main() {
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "unsupported operating system: $(uname -s). On Windows, use install.ps1." ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m). Releases exist for amd64 and arm64." ;;
  esac

  version="${RGD_VERSION:-}"
  version="${version#v}"
  if [ -n "${RGD_DOWNLOAD_BASE:-}" ]; then
    base="${RGD_DOWNLOAD_BASE%/}"
  elif [ -n "$version" ]; then
    base="https://github.com/$REPO/releases/download/v$version"
  else
    base="https://github.com/$REPO/releases/latest/download"
  fi
  install_dir="${RGD_INSTALL_DIR:-$HOME/.local/bin}"

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT INT TERM

  if ! fetch "$base/checksums.txt" "$tmp/checksums.txt"; then
    cat >&2 <<MESSAGE
install.sh: could not download $base/checksums.txt

Either no release of rgd has been published yet, the version you asked for
does not exist, or this network cannot reach the download location.

  - Until the first release, build from source (needs Go and git):
      git clone https://github.com/$REPO.git && cd rigdepot && go build -o rgd ./cmd/rgd
  - To download from a mirror, set RGD_DOWNLOAD_BASE to the URL of a directory
    that holds checksums.txt and the archives.
MESSAGE
    exit 1
  fi

  # A line of checksums.txt is "<sha256>  <file name>". The archive for this
  # platform is rgd_<version>_<os>_<arch>.tar.gz; the version is whatever the
  # release says it is, so it is read from here and not guessed.
  suffix="_${os}_${arch}.tar.gz"
  match="$(awk -v suffix="$suffix" '
    {
      name = $2
      at = length(name) - length(suffix) + 1
      if (substr(name, 1, 4) == "rgd_" && at > 0 && substr(name, at) == suffix) print $1 " " name
    }' "$tmp/checksums.txt")"
  [ -n "$match" ] || fail "checksums.txt at $base lists no rgd archive for $os/$arch"
  [ "$(printf '%s\n' "$match" | wc -l)" -eq 1 ] || fail "checksums.txt at $base lists more than one rgd archive for $os/$arch"
  expected="${match%% *}"
  archive="${match#* }"

  say "Downloading $archive"
  fetch "$base/$archive" "$tmp/$archive" || fail "could not download $base/$archive"

  actual="$(sha256_of "$tmp/$archive")"
  if [ "$actual" != "$expected" ]; then
    fail "checksum mismatch for $archive
  expected $expected
  got      $actual
Nothing was installed. Do not use this file."
  fi
  say "Checksum verified (SHA-256 $expected)"

  mkdir "$tmp/unpacked"
  tar -xzf "$tmp/$archive" -C "$tmp/unpacked" rgd || fail "$archive does not contain rgd"

  mkdir -p "$install_dir"
  # Copy next to the destination and rename, so that a running rgd is replaced
  # in one step and never left half written.
  cp "$tmp/unpacked/rgd" "$install_dir/.rgd.new.$$"
  chmod 755 "$install_dir/.rgd.new.$$"
  mv -f "$install_dir/.rgd.new.$$" "$install_dir/rgd"

  say "Installed $install_dir/rgd"
  say "  $("$install_dir/rgd" --version)"

  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *)
      say ""
      say "$install_dir is not on your PATH. Add this line to your shell profile:"
      say "  export PATH=\"$install_dir:\$PATH\""
      ;;
  esac
}

# Everything runs from main, so a download that was cut off half way cannot
# execute a partial script.
main "$@"
