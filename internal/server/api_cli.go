// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
	"github.com/code2rich/rigdepot/scripts"
)

// cliAPI implements the operations of the tag cli that hand out the command-line program on the
// organisation's own network (docs/architecture/08-deployment.md, 给内网分发命令行):
// listCLIDownloads, downloadCLI, getInstallScript and getInstallPowerShell. The policy
// operations of the same tag are toolsAPI's.
//
// What it hands out are the release archives in the first directory of config.CLIDistDirs that
// holds any (cliDist): the binary inside each archive, and the install scripts of scripts/ with
// this server's address, the version and each binary's checksum filled in, so that they
// download from here and verify against what this server read. With no archives on this
// machine and a download base configured, the install scripts download the release from there
// instead and verify against its checksums.txt; with neither, there is nothing to hand out.
type cliAPI struct {
	dist *cliDist
	// base is the public address, without a trailing slash.
	base string
	// downloadBase is config.CLIDownloadBase.
	downloadBase string
	// version is this server's version, the version listed when no archives are here.
	version string
	log     *slog.Logger
}

var errCLINotShipped = httpapi.NewProblem(http.StatusNotFound, "cli.not_shipped",
	"This server does not hand out the command line. An administrator can put the release archives into its "+
		"command-line directory, or set "+brand.EnvPrefix+"CLI_DOWNLOAD_BASE.")

func (c *cliAPI) shipped(ctx context.Context) (cliSet, error) {
	set, err := c.dist.scan()
	if err != nil {
		// A directory that cannot be read is not an empty one: say nothing rather than
		// something wrong.
		c.log.ErrorContext(ctx, "reading the command-line directories", "err", err)
		return cliSet{}, err
	}
	return set, nil
}

// ListCLIDownloads lists the binaries this server hands out and the install commands.
func (c *cliAPI) ListCLIDownloads(ctx context.Context, _ httpapi.ListCLIDownloadsRequestObject) (httpapi.ListCLIDownloadsResponseObject, error) {
	set, err := c.shipped(ctx)
	if err != nil {
		return nil, err
	}
	out := httpapi.CLIDownloadList{
		Version:    contractVersion(c.version),
		InstallSh:  c.base + "/install.sh",
		InstallPs1: c.base + "/install.ps1",
		Items:      []httpapi.CLIDownload{},
	}
	if len(set.binaries) > 0 {
		out.Version = contractVersion(set.version)
	}
	for _, b := range set.binaries {
		out.Items = append(out.Items, httpapi.CLIDownload{
			Os: httpapi.CLIDownloadOs(b.os), Arch: httpapi.CLIDownloadArch(b.arch), File: b.file,
			Url: c.base + "/downloads/" + b.file, Sha256: b.sha256, Size: int(b.size),
		})
	}
	return httpapi.ListCLIDownloads200JSONResponse(out), nil
}

// contractVersion is a version as the contract's SemVer has it: without build metadata, and
// 0.0.0 for one that does not parse.
func contractVersion(v string) string {
	p, ok := settings.ParseVersion(v)
	if !ok {
		return "0.0.0"
	}
	return p.String()
}

// DownloadCLI sends one binary, read out of its release archive.
func (c *cliAPI) DownloadCLI(ctx context.Context, req httpapi.DownloadCLIRequestObject) (httpapi.DownloadCLIResponseObject, error) {
	set, err := c.shipped(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range set.binaries {
		if b.file == req.File {
			rc, err := b.open()
			if err != nil {
				return nil, err
			}
			return cliBinaryResponse{binary: b, body: rc}, nil
		}
	}
	return nil, errCLINotShipped
}

// cliBinaryResponse streams a binary. The install scripts check its SHA-256; a browser saves it
// under the program's own name.
type cliBinaryResponse struct {
	binary cliBinary
	body   io.ReadCloser
}

func (r cliBinaryResponse) VisitDownloadCLIResponse(w http.ResponseWriter) error {
	defer r.body.Close()
	name := brand.CLI
	if r.binary.os == "windows" {
		name += ".exe"
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", fmt.Sprint(r.binary.size))
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("ETag", `"`+r.binary.sha256+`"`)
	// The address does not name the version: ask again after an upgrade.
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, err := io.CopyN(w, r.body, r.binary.size)
	return err
}

// GetInstallScript hands out scripts/install.sh with this server filled in.
func (c *cliAPI) GetInstallScript(ctx context.Context, _ httpapi.GetInstallScriptRequestObject) (httpapi.GetInstallScriptResponseObject, error) {
	script, err := c.script(ctx, scripts.InstallSh, shQuote, map[string]string{
		"from": "served_from", "version": "served_version", "sums": "served_sums", "base": "served_download_base",
	})
	if err != nil {
		return nil, err
	}
	return scriptResponse(script), nil
}

// GetInstallPowerShell hands out scripts/install.ps1 with this server filled in.
func (c *cliAPI) GetInstallPowerShell(ctx context.Context, _ httpapi.GetInstallPowerShellRequestObject) (httpapi.GetInstallPowerShellResponseObject, error) {
	script, err := c.script(ctx, scripts.InstallPs1, psQuote, map[string]string{
		"from": "$ServedFrom", "version": "$ServedVersion", "sums": "$ServedSums", "base": "$ServedDownloadBase",
	})
	if err != nil {
		return nil, err
	}
	return scriptResponse(script), nil
}

// script fills in an install script: the server's address, the version and the checksum of
// each binary when the archives are here; otherwise the download base. vars names the
// script's variable for each value. The script has a line `<variable> = ”` (PowerShell) or
// `<variable>=”` (sh) for each, which is replaced whole.
func (c *cliAPI) script(ctx context.Context, src string, quote func(string) string, vars map[string]string) (string, error) {
	set, err := c.shipped(ctx)
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	switch {
	case len(set.binaries) > 0:
		var sums strings.Builder
		for _, b := range set.binaries {
			fmt.Fprintf(&sums, "%s-%s %s\n", b.os, b.arch, b.sha256)
		}
		values["from"], values["version"], values["sums"] = c.base, set.version, sums.String()
	case c.downloadBase != "":
		values["base"] = c.downloadBase
	default:
		return "", errCLINotShipped
	}
	return fillScript(src, quote, vars, values)
}

// fillScript replaces the line that assigns the empty string to each variable in vars with
// one that assigns its value. Every variable's line has to be there exactly once.
func fillScript(src string, quote func(string) string, vars, values map[string]string) (string, error) {
	lines := strings.SplitAfter(src, "\n")
	for key, name := range vars {
		found := 0
		for i, line := range lines {
			body := strings.TrimRight(line, "\r\n")
			assign, ok := strings.CutSuffix(body, "''")
			if !ok || strings.ReplaceAll(assign, " ", "") != name+"=" {
				continue
			}
			found++
			lines[i] = assign + quote(values[key]) + line[len(body):]
		}
		if found != 1 {
			return "", fmt.Errorf("the install script assigns %s %d times, want once", name, found)
		}
	}
	return strings.Join(lines, ""), nil
}

// shQuote quotes s for a POSIX shell: in single quotes, where nothing is special but the
// quote itself.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// psQuote quotes s for PowerShell: in single quotes, where a quote is doubled.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// scriptResponse is an install script. It names this server's current files, so it is not
// cached.
type scriptResponse string

func (s scriptResponse) VisitGetInstallScriptResponse(w http.ResponseWriter) error {
	return s.write(w)
}

func (s scriptResponse) VisitGetInstallPowerShellResponse(w http.ResponseWriter) error {
	return s.write(w)
}

func (s scriptResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, err := io.WriteString(w, string(s))
	return err
}

// publicBase is the public address without a trailing slash.
func publicBase(u *url.URL) string { return strings.TrimRight(u.String(), "/") }
