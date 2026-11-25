// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/scripts"
)

// cliRelease writes the command-line archives of one release into dir, the way a release
// names and packs them, and returns each binary's contents by download name. The binary of
// a platform is a shell script that prints its version, so that the one for this machine
// runs where sh does.
func cliRelease(t *testing.T, dir, version string, checksums bool) map[string][]byte {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bins := map[string][]byte{}
	var sums strings.Builder
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			body := fmt.Appendf(nil, "#!/bin/sh\necho '%s version %s %s/%s'\n", brand.CLI, version, goos, arch)
			entry, file := brand.CLI, brand.CLI+"-"+goos+"-"+arch
			name := fmt.Sprintf("%s_%s_%s_%s.tar.gz", brand.CLI, version, goos, arch)
			var data []byte
			if goos == "windows" {
				entry, file = entry+".exe", file+".exe"
				name = strings.TrimSuffix(name, ".tar.gz") + ".zip"
				data = zipOf(t, map[string][]byte{"LICENSE": []byte("license"), entry: body})
			} else {
				data = tarGzOf(t, map[string][]byte{"LICENSE": []byte("license"), entry: body})
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
			bins[file] = body
		}
	}
	if checksums {
		if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bins
}

func tarGzOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(files[n])
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, data := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withCLIDirs makes the fixture hand out the command line from dirs, and with a download base.
func withCLIDirs(downloadBase string, dirs ...string) func(*App) {
	return func(a *App) {
		a.cli = newCLIDist(dirs, logging.Discard())
		a.cfg.CLIDownloadBase = downloadBase
	}
}

func listCLI(t *testing.T, f *apiFixture) httpapi.CLIDownloadList {
	t.Helper()
	rec, _ := f.call(t, http.MethodGet, "/api/v1/cli/downloads", "", "", nil, http.StatusOK)
	var list httpapi.CLIDownloadList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestCLIDownloadsFromTheReleaseArchives: with a release's archives in its directory, the
// server lists and hands out the six binaries out of them, signed out, with each binary's
// checksum; and its install scripts download from it and carry those checksums.
func TestCLIDownloadsFromTheReleaseArchives(t *testing.T) {
	dir := t.TempDir()
	bins := cliRelease(t, dir, "1.2.3", true)
	f := newAPIWith(t, fixtureOptions{version: "9.9.9", setup: withCLIDirs("", filepath.Join(t.TempDir(), "absent"), dir)})

	list := listCLI(t, f)
	if list.Version != "1.2.3" || list.InstallSh != "http://127.0.0.1:8080/install.sh" || list.InstallPs1 != "http://127.0.0.1:8080/install.ps1" || len(list.Items) != 6 {
		t.Fatalf("list: %+v", list)
	}
	for _, it := range list.Items {
		body, ok := bins[it.File]
		if !ok || it.Url != "http://127.0.0.1:8080/downloads/"+it.File || it.Sha256 != sha256Hex(body) || it.Size != len(body) ||
			!strings.HasPrefix(it.File, brand.CLI+"-"+string(it.Os)+"-"+string(it.Arch)) {
			t.Errorf("item %+v", it)
		}
		rec, _ := f.call(t, http.MethodGet, "/downloads/"+it.File, "", "", nil, http.StatusOK)
		if !bytes.Equal(rec.Body.Bytes(), body) || rec.Header().Get("Content-Length") != fmt.Sprint(len(body)) {
			t.Errorf("%s: %q", it.File, rec.Body)
		}
	}
	if rec, p := f.do(t, http.MethodGet, "/downloads/"+brand.CLI+"-plan9-amd64", "", "", nil); rec.Code != http.StatusNotFound || p.Code != "cli.not_shipped" {
		t.Errorf("unknown platform: %d %+v", rec.Code, p)
	}

	rec, _ := f.call(t, http.MethodGet, "/install.sh", "", "", nil, http.StatusOK)
	sh := rec.Body.String()
	for _, want := range []string{
		"served_from='http://127.0.0.1:8080'\n",
		"served_version='1.2.3'\n",
		"served_download_base=''\n",
		"linux-amd64 " + sha256Hex(bins[brand.CLI+"-linux-amd64"]) + "\n",
		"windows-arm64 " + sha256Hex(bins[brand.CLI+"-windows-arm64.exe"]) + "\n",
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	if rec.Header().Get("Cache-Control") != "no-cache" || len(sh) < len(scripts.InstallSh) {
		t.Errorf("install.sh: %v", rec.Header())
	}
	rec, _ = f.call(t, http.MethodGet, "/install.ps1", "", "", nil, http.StatusOK)
	for _, want := range []string{
		"$ServedFrom = 'http://127.0.0.1:8080'\n",
		"$ServedVersion = '1.2.3'\n",
		"windows-amd64 " + sha256Hex(bins[brand.CLI+"-windows-amd64.exe"]) + "\n",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("install.ps1 lacks %q", want)
		}
	}
}

// TestCLIArchivesThatDoNotCheckOut: an archive that does not match the release's
// checksums.txt, or is not listed there, is not handed out; the others are.
func TestCLIArchivesThatDoNotCheckOut(t *testing.T) {
	dir := t.TempDir()
	cliRelease(t, dir, "1.2.3", true)
	tampered := filepath.Join(dir, brand.CLI+"_1.2.3_linux_amd64.tar.gz")
	if err := os.WriteFile(tampered, tarGzOf(t, map[string][]byte{brand.CLI: []byte("#!/bin/sh\necho tampered\n")}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, brand.CLI+"_1.2.3_linux_riscv.tar.gz"), nil, 0o644); err != nil {
		t.Fatal(err) // not a platform the server knows: ignored
	}
	f := newAPIWith(t, fixtureOptions{setup: withCLIDirs("", dir)})
	var files []string
	for _, it := range listCLI(t, f).Items {
		files = append(files, it.File)
	}
	if len(files) != 5 || slices.Contains(files, brand.CLI+"-linux-amd64") {
		t.Errorf("handed out %v, want all but linux-amd64", files)
	}
	if rec, p := f.do(t, http.MethodGet, "/downloads/"+brand.CLI+"-linux-amd64", "", "", nil); rec.Code != http.StatusNotFound || p.Code != "cli.not_shipped" {
		t.Errorf("tampered: %d %+v", rec.Code, p)
	}

	// Without checksums.txt nothing is compared, and an archive without the binary is left out.
	if err := os.Remove(filepath.Join(dir, "checksums.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, brand.CLI+"_1.2.3_darwin_arm64.tar.gz"), tarGzOf(t, map[string][]byte{"LICENSE": nil}), 0o644); err != nil {
		t.Fatal(err)
	}
	files = files[:0]
	for _, it := range listCLI(t, f).Items {
		files = append(files, it.File)
	}
	if len(files) != 5 || slices.Contains(files, brand.CLI+"-darwin-arm64") || !slices.Contains(files, brand.CLI+"-linux-amd64") {
		t.Errorf("without checksums.txt: %v", files)
	}
}

// TestCLINewestVersionAndFirstDirectory: of several releases in one directory the newest is
// handed out; the first directory with archives wins over the ones after it.
func TestCLINewestVersionAndFirstDirectory(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	cliRelease(t, first, "1.10.0", false)
	newest := cliRelease(t, first, "1.10.1-rc.1+abc", false)
	cliRelease(t, first, "1.9.9", false)
	cliRelease(t, second, "2.0.0", true)
	f := newAPIWith(t, fixtureOptions{setup: withCLIDirs("", first, second)})
	list := listCLI(t, f)
	if list.Version != "1.10.1-rc.1" || len(list.Items) != 6 || list.Items[0].Sha256 != sha256Hex(newest[list.Items[0].File]) {
		t.Errorf("list: %+v", list)
	}
	rec, _ := f.call(t, http.MethodGet, "/install.sh", "", "", nil, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "served_version='1.10.1-rc.1+abc'\n") {
		t.Error("install.sh does not name the version of the archives")
	}
}

// TestCLINothingToHandOut: without archives the list is empty and says the server's version;
// the install scripts download from the download base when there is one and do not exist
// otherwise.
func TestCLINothingToHandOut(t *testing.T) {
	f := newAPIWith(t, fixtureOptions{version: "0.5.0+abc", setup: withCLIDirs("", t.TempDir())})
	list := listCLI(t, f)
	if list.Version != "0.5.0" || len(list.Items) != 0 {
		t.Errorf("list: %+v", list)
	}
	for _, path := range []string{"/install.sh", "/install.ps1", "/downloads/" + brand.CLI + "-linux-amd64"} {
		if rec, p := f.do(t, http.MethodGet, path, "", "", nil); rec.Code != http.StatusNotFound || p.Code != "cli.not_shipped" {
			t.Errorf("%s: %d %+v", path, rec.Code, p)
		}
	}

	const base = "https://artifacts.example.com/rigdepot/v0.5.0"
	f = newAPIWith(t, fixtureOptions{setup: withCLIDirs(base, t.TempDir())})
	rec, _ := f.call(t, http.MethodGet, "/install.sh", "", "", nil, http.StatusOK)
	if sh := rec.Body.String(); !strings.Contains(sh, "served_download_base='"+base+"'\n") || !strings.Contains(sh, "served_from=''\n") {
		t.Errorf("install.sh with a download base:\n%s", sh)
	}
	rec, _ = f.call(t, http.MethodGet, "/install.ps1", "", "", nil, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "$ServedDownloadBase = '"+base+"'\n") {
		t.Error("install.ps1 lacks the download base")
	}
	if rec, p := f.do(t, http.MethodGet, "/downloads/"+brand.CLI+"-linux-amd64", "", "", nil); rec.Code != http.StatusNotFound || p.Code != "cli.not_shipped" {
		t.Errorf("download: %d %+v", rec.Code, p)
	}
}

// TestCLIUnreadableDirectory: a directory that cannot be read is not an empty one.
func TestCLIUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user cannot read")
	}
	dir := t.TempDir()
	cliRelease(t, dir, "1.2.3", false)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	f := newAPIWith(t, fixtureOptions{setup: withCLIDirs("https://artifacts.example.com/rigdepot", dir)})
	for _, path := range []string{"/api/v1/cli/downloads", "/install.sh", "/downloads/" + brand.CLI + "-linux-amd64"} {
		if rec, p := f.do(t, http.MethodGet, path, "", "", nil); rec.Code != http.StatusInternalServerError || p.Code != httpapi.CodeServerError {
			t.Errorf("%s: %d %+v", path, rec.Code, p)
		}
	}
}

// TestFillScript: the values replace the lines that assign them, quoted so that nothing in
// them is code; a script without such a line is an error, not a script that downloads from
// somewhere else.
func TestFillScript(t *testing.T) {
	vars := map[string]string{"from": "served_from"}
	got, err := fillScript("a=1\nserved_from=''\r\nb=2\n", shQuote, vars, map[string]string{"from": "https://hub.example.com/it's"})
	if err != nil || got != "a=1\nserved_from='https://hub.example.com/it'\\''s'\r\nb=2\n" {
		t.Errorf("sh: %q, %v", got, err)
	}
	got, err = fillScript("$ServedFrom = ''\n", psQuote, map[string]string{"from": "$ServedFrom"}, map[string]string{"from": "it's"})
	if err != nil || got != "$ServedFrom = 'it''s'\n" {
		t.Errorf("ps: %q, %v", got, err)
	}
	for _, src := range []string{"", "served_from=''\nserved_from=''\n", "# served_from=''\n"} {
		if _, err := fillScript(src, shQuote, vars, map[string]string{}); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
	// The published scripts have every line the server fills in.
	if _, err := fillScript(scripts.InstallSh, shQuote, map[string]string{"a": "served_from", "b": "served_version", "c": "served_sums", "d": "served_download_base"}, nil); err != nil {
		t.Error(err)
	}
	if _, err := fillScript(scripts.InstallPs1, psQuote, map[string]string{"a": "$ServedFrom", "b": "$ServedVersion", "c": "$ServedSums", "d": "$ServedDownloadBase"}, nil); err != nil {
		t.Error(err)
	}
}

// TestServedInstallScriptInstalls runs what a person on the internal network runs,
// curl <server>/install.sh | sh, against a server on this machine: it installs the binary
// this server hands out for this platform, and refuses one that does not match the checksum
// the script carries.
func TestServedInstallScriptInstalls(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh is for macOS and Linux")
	}
	for _, tool := range []string{"sh", "curl", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not on PATH")
		}
	}
	dir := t.TempDir()
	bins := cliRelease(t, dir, "1.2.3", true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String() + "/rd" // under a path, as a base URL can be
	f := newAPIWith(t, fixtureOptions{setup: func(a *App) {
		withCLIDirs("", dir)(a)
		a.cfg.BaseURL, _ = url.Parse(base)
	}})
	srv := httptest.NewUnstartedServer(f.app.Handler())
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	install := func() (string, error) {
		home := t.TempDir()
		cmd := exec.Command("sh", "-c", "curl -fsSL "+base+"/install.sh | sh")
		cmd.Env = append(os.Environ(), "HOME="+home, brand.EnvPrefix+"INSTALL_DIR="+filepath.Join(home, "bin"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := install()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	want := fmt.Sprintf("%s version 1.2.3 %s/%s", brand.CLI, runtime.GOOS, runtime.GOARCH)
	if !strings.Contains(out, "Checksum verified") || !strings.Contains(out, want) {
		t.Errorf("install output lacks the check or %q:\n%s", want, out)
	}

	// The binary changes after the script was written: the script refuses it. The archive
	// is replaced with a new modification time, so the server reads it again.
	file := brand.CLI + "-" + runtime.GOOS + "-" + runtime.GOARCH
	script, _ := f.do(t, http.MethodGet, "/rd/install.sh", "", "", nil)
	stale := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(stale, script.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, fmt.Sprintf("%s_1.2.3_%s_%s.tar.gz", brand.CLI, runtime.GOOS, runtime.GOARCH))
	os.Remove(filepath.Join(dir, "checksums.txt"))
	if err := os.WriteFile(archive, tarGzOf(t, map[string][]byte{brand.CLI: append(bins[file], '#')}), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cmd := exec.Command("sh", stale)
	cmd.Env = append(os.Environ(), "HOME="+home, brand.EnvPrefix+"INSTALL_DIR="+filepath.Join(home, "bin"))
	outb, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(outb), "checksum mismatch") {
		t.Errorf("a changed binary was not refused: %v\n%s", err, outb)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", brand.CLI)); err == nil {
		t.Error("the changed binary was installed")
	}
}
