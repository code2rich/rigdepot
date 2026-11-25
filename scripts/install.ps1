# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors

<#
.SYNOPSIS
Installs rgd, the Rigdepot command-line program, on Windows.

.DESCRIPTION
It does one thing: download the release archive for this platform, verify its
SHA-256 checksum against the published checksums.txt, put rgd.exe in place and
tell you how to add it to PATH. It does not sign you in, sync anything,
install a background task or ask for administrator rights.

Read it before you run it:

    irm https://raw.githubusercontent.com/code2rich/rigdepot/main/scripts/install.ps1 -OutFile install.ps1
    .\install.ps1

Environment variables:

    RGD_VERSION        version to install, such as 0.4.0 (default: the latest release)
    RGD_INSTALL_DIR    directory to install into (default: %LOCALAPPDATA%\Programs\rigdepot)
    RGD_DOWNLOAD_BASE  URL of a directory that holds checksums.txt and the
                       archives, for a mirror or a network that cannot reach GitHub
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Repo = 'code2rich/rigdepot'

function Get-Arch {
    # OSArchitecture is the architecture of Windows itself, also when this
    # PowerShell runs under emulation.
    $arch = $null
    try { $arch = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch { }
    if (-not $arch) {
        $arch = $env:PROCESSOR_ARCHITEW6432
        if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    }
    switch -Regex ($arch) {
        '^(X64|AMD64)$' { return 'amd64' }
        '^ARM64$' { return 'arm64' }
        default { throw "Unsupported architecture: $arch. Releases exist for amd64 and arm64." }
    }
}

function Get-File([string]$Url, [string]$Path) {
    # Windows PowerShell 5.1 does not enable TLS 1.2 on its own.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $Url -OutFile $Path -UseBasicParsing
}

function Install-Rgd {
    $arch = Get-Arch

    $version = "$env:RGD_VERSION".TrimStart('v')
    if ($env:RGD_DOWNLOAD_BASE) {
        $base = $env:RGD_DOWNLOAD_BASE.TrimEnd('/')
    } elseif ($version) {
        $base = "https://github.com/$Repo/releases/download/v$version"
    } else {
        $base = "https://github.com/$Repo/releases/latest/download"
    }

    $installDir = $env:RGD_INSTALL_DIR
    if (-not $installDir) {
        if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; set RGD_INSTALL_DIR to the directory to install into.' }
        $installDir = Join-Path $env:LOCALAPPDATA 'Programs\rigdepot'
    }

    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("rgd-install-" + [System.Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $checksums = Join-Path $tmp 'checksums.txt'
        try {
            Get-File "$base/checksums.txt" $checksums
        } catch {
            throw @"
Could not download $base/checksums.txt
($($_.Exception.Message))

Either no release of rgd has been published yet, the version you asked for
does not exist, or this network cannot reach the download location.

  - Until the first release, build from source (needs Go and git):
      git clone https://github.com/$Repo.git; cd rigdepot; go build -o rgd.exe ./cmd/rgd
  - To download from a mirror, set RGD_DOWNLOAD_BASE to the URL of a directory
    that holds checksums.txt and the archives.
"@
        }

        # A line of checksums.txt is "<sha256>  <file name>". The archive for
        # this platform is rgd_<version>_windows_<arch>.zip; the version is
        # whatever the release says it is, so it is read from here.
        $suffix = "_windows_$arch.zip"
        $found = @(Get-Content $checksums | ForEach-Object {
                $parts = $_ -split '\s+', 2
                if ($parts.Count -eq 2 -and $parts[1].StartsWith('rgd_') -and $parts[1].EndsWith($suffix)) {
                    [pscustomobject]@{ Hash = $parts[0].ToLowerInvariant(); Name = $parts[1].Trim() }
                }
            })
        if ($found.Count -eq 0) { throw "checksums.txt at $base lists no rgd archive for windows/$arch" }
        if ($found.Count -gt 1) { throw "checksums.txt at $base lists more than one rgd archive for windows/$arch" }
        $archive = $found[0].Name
        $expected = $found[0].Hash

        Write-Host "Downloading $archive"
        $zip = Join-Path $tmp $archive
        Get-File "$base/$archive" $zip

        $actual = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLowerInvariant()
        if ($actual -ne $expected) {
            throw "Checksum mismatch for $archive`n  expected $expected`n  got      $actual`nNothing was installed. Do not use this file."
        }
        Write-Host "Checksum verified (SHA-256 $expected)"

        $unpacked = Join-Path $tmp 'unpacked'
        Expand-Archive -Path $zip -DestinationPath $unpacked -Force
        $exe = Join-Path $unpacked 'rgd.exe'
        if (-not (Test-Path $exe)) { throw "$archive does not contain rgd.exe" }

        New-Item -ItemType Directory -Path $installDir -Force | Out-Null
        $target = Join-Path $installDir 'rgd.exe'
        Copy-Item -Path $exe -Destination $target -Force
        Write-Host "Installed $target"
        # Show what was installed. Only where it can run: PowerShell also
        # exists on macOS and Linux, and a Windows program does not run there.
        if ([System.Environment]::OSVersion.Platform -eq 'Win32NT') {
            try { Write-Host "  $(& $target --version)" } catch { }
        }

        $onPath = @("$env:Path" -split [System.IO.Path]::PathSeparator) -contains $installDir
        if (-not $onPath) {
            Write-Host ''
            Write-Host "$installDir is not on your PATH. To add it for your user, run:"
            Write-Host "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';$installDir', 'User')"
            Write-Host 'and open a new terminal.'
        }
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}

Install-Rgd
