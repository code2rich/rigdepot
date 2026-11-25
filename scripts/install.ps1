# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors

<#
.SYNOPSIS
Installs rgd, the Rigdepot command-line program, on Windows.

.DESCRIPTION
It does one thing: download rgd.exe for this platform, verify its SHA-256
checksum, put it in place and tell you how to add it to PATH. It does not sign
you in, sync anything, install a background task or ask for administrator
rights.

Read it before you run it:

    irm https://raw.githubusercontent.com/code2rich/rigdepot/main/scripts/install.ps1 -OutFile install.ps1
    .\install.ps1

Your organisation's server hands out a copy of this script with its own
address filled in (https://<server>/install.ps1). That copy downloads rgd.exe
from the server and checks it against the checksums the server wrote into the
script, so it works on a network that cannot reach the internet.

Environment variables:

    RGD_VERSION        version to install, such as 0.4.0 (default: the latest
                       release; a server hands out one version)
    RGD_INSTALL_DIR    directory to install into (default: %LOCALAPPDATA%\Programs\rigdepot)
    RGD_DOWNLOAD_BASE  URL of a directory that holds checksums.txt and the
                       archives, for a mirror or a network that cannot reach GitHub
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Repo = 'code2rich/rigdepot'

# Filled in by the server that hands out this script (GET /install.ps1); empty
# in the published file. The server replaces these lines whole.
$ServedFrom = ''
$ServedVersion = ''
$ServedSums = ''
$ServedDownloadBase = ''

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

function Test-Checksum([string]$Path, [string]$Expected, [string]$Name) {
    $actual = (Get-FileHash -Algorithm SHA256 -Path $Path).Hash.ToLowerInvariant()
    if ($actual -ne $Expected) {
        throw "Checksum mismatch for $Name`n  expected $Expected`n  got      $actual`nNothing was installed. Do not use this file."
    }
    Write-Host "Checksum verified (SHA-256 $Expected)"
}

# Downloads rgd.exe from the server that handed out this script, verified
# against the checksum it wrote in. Returns the path of rgd.exe.
function Get-FromServer([string]$Arch, [string]$Version, [string]$Tmp) {
    if ($Version -and $Version -ne $ServedVersion) {
        throw "$ServedFrom hands out rgd $ServedVersion, not $Version. To install another version, set RGD_DOWNLOAD_BASE to a directory that holds that release."
    }
    $expected = $null
    foreach ($line in ($ServedSums -split "`n")) {
        $parts = $line.Trim() -split '\s+'
        if ($parts.Count -eq 2 -and $parts[0] -eq "windows-$Arch") { $expected = $parts[1] }
    }
    if (-not $expected) { throw "$ServedFrom does not hand out rgd for windows/$Arch" }
    $file = "rgd-windows-$Arch.exe"
    Write-Host "Downloading rgd $ServedVersion from $ServedFrom"
    $exe = Join-Path $Tmp 'rgd.exe'
    Get-File "$ServedFrom/downloads/$file" $exe
    Test-Checksum $exe $expected $file
    return $exe
}

# Downloads the release archive from Base, verified against the release's
# checksums.txt. Returns the path of rgd.exe.
function Get-FromRelease([string]$Arch, [string]$Base, [string]$Tmp) {
    $checksums = Join-Path $Tmp 'checksums.txt'
    try {
        Get-File "$Base/checksums.txt" $checksums
    } catch {
        throw @"
Could not download $Base/checksums.txt
($($_.Exception.Message))

Either no release of rgd has been published yet, the version you asked for
does not exist, or this network cannot reach the download location.

  - If your organisation runs a Rigdepot server, install from it instead:
      irm https://<server>/install.ps1 | iex
  - Until the first release, build from source (needs Go and git):
      git clone https://github.com/$Repo.git; cd rigdepot; go build -o rgd.exe ./cmd/rgd
  - To download from a mirror, set RGD_DOWNLOAD_BASE to the URL of a directory
    that holds checksums.txt and the archives.
"@
    }

    # A line of checksums.txt is "<sha256>  <file name>". The archive for
    # this platform is rgd_<version>_windows_<arch>.zip; the version is
    # whatever the release says it is, so it is read from here.
    $suffix = "_windows_$Arch.zip"
    $found = @(Get-Content $checksums | ForEach-Object {
            $parts = $_ -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].StartsWith('rgd_') -and $parts[1].EndsWith($suffix)) {
                [pscustomobject]@{ Hash = $parts[0].ToLowerInvariant(); Name = $parts[1].Trim() }
            }
        })
    if ($found.Count -eq 0) { throw "checksums.txt at $Base lists no rgd archive for windows/$Arch" }
    if ($found.Count -gt 1) { throw "checksums.txt at $Base lists more than one rgd archive for windows/$Arch" }
    $archive = $found[0].Name

    Write-Host "Downloading $archive"
    $zip = Join-Path $Tmp $archive
    Get-File "$Base/$archive" $zip
    Test-Checksum $zip $found[0].Hash $archive

    $unpacked = Join-Path $Tmp 'unpacked'
    Expand-Archive -Path $zip -DestinationPath $unpacked -Force
    $exe = Join-Path $unpacked 'rgd.exe'
    if (-not (Test-Path $exe)) { throw "$archive does not contain rgd.exe" }
    return $exe
}

function Install-Rgd {
    $arch = Get-Arch

    $version = "$env:RGD_VERSION".TrimStart('v')
    $base = ''
    if ($env:RGD_DOWNLOAD_BASE) {
        $base = $env:RGD_DOWNLOAD_BASE.TrimEnd('/')
    } elseif ($ServedFrom) {
        # from the server
    } elseif ($ServedDownloadBase) {
        $base = $ServedDownloadBase.TrimEnd('/')
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
        if ($base) {
            $exe = Get-FromRelease $arch $base $tmp
        } else {
            $exe = Get-FromServer $arch $version $tmp
        }

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
