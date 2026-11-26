// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

// runReleaseSnapshot is the release rehearsal (docs: 10-release). It builds every release
// artifact into dist/ with the pinned goreleaser, publishing and tagging nothing, then:
//
//   - checks that dist/ holds every file a release has, and that checksums.txt is right;
//   - builds everything a second time, in a fresh clone at another path with an empty build
//     cache, and fails unless the archives have the same checksums (reproducible builds);
//   - installs rgd from dist/ into a temporary directory with scripts/install.sh, offline,
//     and checks that it reports the version that was built;
//   - exports the server's image for both architectures into dist/, loads and runs them, and
//     installs the Debian and RPM packages into clean containers (releaseimages.go).
//
// It ends with a table of what passed, failed or did not run. The first run compiles
// goreleaser, which takes a while; the second build compiles everything from scratch.
func runReleaseSnapshot(root string, args []string) error {
	if len(args) != 0 {
		return errors.New("release-snapshot takes no arguments")
	}
	b, err := readBrand(root)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "release-snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	r := &rehearsal{root: root, brand: b, tmp: tmp, env: map[string]string{}}
	steps := []struct {
		name string
		run  func() (string, error)
	}{
		{"goreleaser", r.buildGoreleaser},
		{"web console", r.buildWeb},
		{"SBOM", r.sbom},
		{"docker", r.dockerCheck},
		{"build", func() (string, error) { return r.goreleaser(root, nil, r.skip...) }},
		{"artifacts", r.verifyDist},
		{"reproducible", r.rebuild},
		{"install.sh", r.installSh},
		{"install.ps1", r.installPs1},
		{"image exports", r.imageExports},
		{"images", r.runImages},
		{"packages", r.installPackages},
	}
	var results []result
	failed := false
	for _, st := range steps {
		if failed {
			results = append(results, result{name: st.name, status: "not run", detail: "an earlier step failed"})
			continue
		}
		fmt.Printf("==> %s\n", st.name)
		start := time.Now()
		detail, err := st.run()
		res := result{name: st.name, status: "pass", detail: detail, elapsed: time.Since(start)}
		var skipped errNotRun
		switch {
		case errors.As(err, &skipped):
			res.status, res.detail = "not run", skipped.reason
		case err != nil:
			res.status, res.detail = "FAIL", firstLine(err.Error())
			fmt.Fprintf(os.Stderr, "%s: %v\n", st.name, err)
			failed = true
		}
		results = append(results, res)
	}
	printResults("release-snapshot results", results)
	if failed {
		return errors.New("failed")
	}
	fmt.Println("The artifacts are in dist/. Nothing was published or tagged.")
	return nil
}

// rehearsal is the state of one release-snapshot run.
type rehearsal struct {
	root  string
	brand brandNames
	tmp   string
	env   map[string]string // the switches .goreleaser.yaml reads

	goreleaserBin string
	webdist       bool
	sboms         bool
	meta          releaseMeta
	sums          map[string]string // archive name -> sha256, from the first build

	docker bool              // Docker with buildx is there: goreleaser builds the image
	skip   []string          // goreleaser --skip flags, for what this machine cannot build
	images map[string]string // architecture -> the image goreleaser loaded
	// pushedImage is set by a real release: the image goreleaser pushed (ghcr.io/…:0.4.0). The
	// image is then one multi-architecture manifest in the registry, not one image per
	// architecture in Docker, so verifyDist only checks that dist/artifacts.json names it; the
	// release's image exports step pulls and runs each architecture.
	pushedImage string
}

// releaseMeta is what goreleaser writes into dist/metadata.json.
type releaseMeta struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// buildGoreleaser compiles the pinned goreleaser once, so that both builds use the same program
// and the second one's empty build cache does not mean compiling goreleaser again.
func (r *rehearsal) buildGoreleaser() (string, error) {
	r.goreleaserBin = filepath.Join(r.tmp, "bin", exe("goreleaser"))
	out, err := output(r.root, nil, "go", "build", "-modfile="+releaseModfile, "-o", r.goreleaserBin, "github.com/goreleaser/goreleaser/v2")
	if err != nil {
		return "", fmt.Errorf("%w (if a module is missing, run first: go run ./tools/dev deps)", err)
	}
	_ = out
	v, err := output(r.root, nil, r.goreleaserBin, "--version")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(v, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "GitVersion:" {
			return "goreleaser " + f[1] + " from " + releaseModfile, nil
		}
	}
	return "pinned in " + releaseModfile, nil
}

// buildWeb builds the web console into web/dist for rgd-server. Without Node, pnpm or the
// installed dependencies the server is built with the stub page, and the table says so.
func (r *rehearsal) buildWeb() (string, error) {
	if err := webToolchain(r.root, true); err != nil {
		r.env["RELEASE_WEBDIST"] = "0"
		return "", errNotRun{err.Error() + "; " + r.brand.Server + " built with the stub page instead of the console"}
	}
	if err := runWeb(r.root, nil); err != nil {
		return "", err
	}
	index, err := os.ReadFile(filepath.Join(r.root, webDir, "dist", "index.html"))
	if err != nil {
		return "", err
	}
	for _, m := range webMarkers {
		if !bytes.Contains(index, []byte(m)) {
			return "", fmt.Errorf("the built index.html lacks %s, which the server fills in", m)
		}
	}
	r.env["RELEASE_WEBDIST"] = "1"
	r.webdist = true
	return "web/dist built; " + r.brand.Server + " built with -tags webdist", nil
}

// sbom turns the SBOMs on when syft is installed.
func (r *rehearsal) sbom() (string, error) {
	if _, err := exec.LookPath("syft"); err != nil {
		r.env["RELEASE_SBOM"] = "0"
		return "", errNotRun{"syft is not on PATH; a release needs it for the SPDX SBOMs"}
	}
	r.env["RELEASE_SBOM"] = "1"
	r.sboms = true
	return "syft found; an SPDX SBOM per archive", nil
}

// goreleaser runs goreleaser release --snapshot --clean in dir with the switches, extra
// variables and extra arguments.
func (r *rehearsal) goreleaser(dir string, extra map[string]string, args ...string) (string, error) {
	vars := map[string]string{}
	for k, v := range r.env {
		vars[k] = v
	}
	for k, v := range extra {
		vars[k] = v
	}
	// A token in the developer's environment must not reach a rehearsal.
	env := mergeEnv(os.Environ(), vars)
	env = slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "PACKAGES_GITHUB_TOKEN" || name == "GITHUB_TOKEN" || name == "GORELEASER_KEY"
	})
	cmd := exec.Command(r.goreleaserBin, append([]string{"release", "--snapshot", "--clean"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("goreleaser release --snapshot in %s: %w", dir, err)
	}
	return strings.Join(append([]string{"goreleaser release --snapshot --clean"}, args...), " "), nil
}

// expectedArchives lists the archive names of a release of version v.
func (r *rehearsal) expectedArchives(v string) map[string][]string {
	cli, server := r.brand.CLI, r.brand.Server
	out := map[string][]string{}
	var completions []string
	for _, c := range completionFiles {
		completions = append(completions, "completions/"+fmt.Sprintf(c.file, cli))
	}
	for _, t := range targets {
		name, bin := fmt.Sprintf("%s_%s_%s_%s.tar.gz", cli, v, t.os, t.arch), cli
		if t.os == "windows" {
			name, bin = strings.TrimSuffix(name, ".tar.gz")+".zip", cli+".exe"
		}
		out[name] = append([]string{bin, "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES"}, completions...)
		if t.os == "linux" {
			out[fmt.Sprintf("%s_%s_%s_%s.tar.gz", server, v, t.os, t.arch)] = []string{
				server, "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES", "deploy/systemd/" + r.brand.CLILong + "-server.service",
			}
		}
	}
	return out
}

// verifyDist checks that dist/ holds every artifact with the right contents and checksums.
func (r *rehearsal) verifyDist() (string, error) {
	dist := filepath.Join(r.root, "dist")
	data, err := os.ReadFile(filepath.Join(dist, "metadata.json"))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(data, &r.meta); err != nil {
		return "", fmt.Errorf("dist/metadata.json: %w", err)
	}
	sums, err := readChecksums(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		return "", err
	}
	r.sums = sums
	var problems []string
	archives := r.expectedArchives(r.meta.Version)
	names := make([]string, 0, len(archives))
	for name := range archives {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := filepath.Join(dist, name)
		got, err := fileSHA256(p)
		if err != nil {
			problems = append(problems, name+": missing")
			continue
		}
		if sums[name] != got {
			problems = append(problems, name+": not in checksums.txt, or a different checksum")
		}
		entries, err := archiveEntries(p)
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		for _, want := range archives[name] {
			if !slices.Contains(entries, want) {
				problems = append(problems, name+": has no "+want)
			}
		}
		if r.sboms && !isFile(p+".sbom.json") {
			problems = append(problems, name+": no SBOM next to it")
		}
	}
	for _, f := range []string{"homebrew/Casks/" + r.brand.CLILong + ".rb", "scoop/" + r.brand.CLILong + ".json"} {
		if !isFile(filepath.Join(dist, filepath.FromSlash(f))) {
			problems = append(problems, "dist/"+f+": missing")
		}
	}
	problems = append(problems, r.checkPackagesListed()...)
	// The images goreleaser loaded, read now: a second build in place replaces dist/.
	switch {
	case r.pushedImage != "":
		if err := pushedImageListed(filepath.Join(dist, "artifacts.json"), r.pushedImage); err != nil {
			problems = append(problems, err.Error())
		}
	case r.docker:
		images, err := r.snapshotImages()
		if err != nil {
			problems = append(problems, err.Error())
		}
		r.images = images
	}
	// The server was built with the console exactly when web/dist was built.
	for _, t := range targets {
		if t.os != "linux" {
			continue
		}
		bin := filepath.Join(dist, fmt.Sprintf("%s_%s_%s", r.brand.Server, t.os, t.arch))
		matches, _ := filepath.Glob(bin + "*" + string(filepath.Separator) + r.brand.Server)
		if len(matches) != 1 {
			problems = append(problems, "the "+t.arch+" server binary is not in dist/")
			continue
		}
		info, err := output(r.root, nil, "go", "version", "-m", matches[0])
		if err != nil {
			return "", err
		}
		if has := strings.Contains(info, "-tags=webdist"); has != r.webdist {
			problems = append(problems, fmt.Sprintf("%s: built with webdist %v, want %v", matches[0], has, r.webdist))
		}
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%d problem(s) in dist/:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	detail := fmt.Sprintf("%s: %d archives with their files, Debian and RPM packages, checksums.txt, Homebrew cask, Scoop manifest", r.meta.Version, len(archives))
	if r.sboms {
		detail += ", SBOMs"
	}
	return detail, nil
}

// rebuild builds everything again and compares the archives' checksums with the first build.
// A clean work tree is cloned to another path, so the comparison also covers where the source
// is; with uncommitted changes the second build runs in place. Either way the build cache is
// empty, so nothing is reused from the first build.
func (r *rehearsal) rebuild() (string, error) {
	status, err := output(r.root, nil, "git", "status", "--porcelain")
	if err != nil {
		return "", err
	}
	dir, where := r.root, "in place (uncommitted changes)"
	if status == "" {
		dir, where = filepath.Join(r.tmp, "clone"), "in a clone at another path"
		if _, err := output(r.root, nil, "git", "clone", "--quiet", "--no-hardlinks", r.root, dir); err != nil {
			return "", err
		}
		if _, err := output(dir, nil, "git", "checkout", "--quiet", "--detach", r.meta.Commit); err != nil {
			return "", err
		}
		if r.webdist {
			if err := copyDir(filepath.Join(r.root, webDir, "dist"), filepath.Join(dir, webDir, "dist")); err != nil {
				return "", err
			}
		}
	}
	cache := filepath.Join(r.tmp, "gocache")
	// The image is built from the packages, which are compared here; building it again would
	// only replace the one the first build loaded.
	if _, err := r.goreleaser(dir, map[string]string{"GOCACHE": cache}, "--skip=docker"); err != nil {
		return "", err
	}
	second, err := readChecksums(filepath.Join(dir, "dist", "checksums.txt"))
	if err != nil {
		return "", err
	}
	var differ []string
	for name, sum := range r.sums {
		if strings.HasSuffix(name, ".sbom.json") {
			continue // an SBOM records when it was made
		}
		if second[name] != sum {
			differ = append(differ, name)
		}
	}
	sort.Strings(differ)
	if len(differ) > 0 {
		return "", fmt.Errorf("the second build differs (%s): %s", where, strings.Join(differ, ", "))
	}
	if dir == r.root {
		// The second build replaced dist/; it is identical, but read it again for the steps after.
		r.sums = second
	}
	compared := 0
	for name := range r.sums {
		if !strings.HasSuffix(name, ".sbom.json") {
			compared++
		}
	}
	return fmt.Sprintf("second build %s with an empty build cache: all %d files of checksums.txt identical (archives, packages, install scripts)", where, compared), nil
}

// installSh installs rgd from dist/ with scripts/install.sh, offline (file://), into a
// temporary directory, and runs the installed program.
func (r *rehearsal) installSh() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errNotRun{"install.sh is for macOS and Linux"}
	}
	for _, tool := range []string{"sh", "curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			return "", errNotRun{tool + " is not on PATH"}
		}
	}
	home := filepath.Join(r.tmp, "install-sh")
	dir := filepath.Join(home, "bin")
	env := mergeEnv(withoutPrefix(os.Environ(), r.brand.EnvPrefix), map[string]string{
		"HOME":                              home,
		r.brand.EnvPrefix + "DOWNLOAD_BASE": fileURL(filepath.Join(r.root, "dist")),
		r.brand.EnvPrefix + "INSTALL_DIR":   dir,
	})
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	if _, err := output(home, env, "sh", filepath.Join(r.root, "scripts", "install.sh")); err != nil {
		return "", err
	}
	return r.checkInstalled(filepath.Join(dir, r.brand.CLI), home)
}

// installPs1 does the same with scripts/install.ps1 where PowerShell is installed. Outside
// Windows the script installs the Windows program for this architecture, which cannot run here:
// the step then checks only that it was put in place.
func (r *rehearsal) installPs1() (string, error) {
	shell := ""
	for _, name := range []string{"pwsh", "powershell"} {
		if _, err := exec.LookPath(name); err == nil {
			shell = name
			break
		}
	}
	if shell == "" {
		return "", errNotRun{"neither pwsh nor powershell is on PATH"}
	}
	home := filepath.Join(r.tmp, "install-ps1")
	dir := filepath.Join(home, "bin")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	env := mergeEnv(withoutPrefix(os.Environ(), r.brand.EnvPrefix), map[string]string{
		"HOME":                              home,
		"USERPROFILE":                       home,
		r.brand.EnvPrefix + "DOWNLOAD_BASE": fileURL(filepath.Join(r.root, "dist")),
		r.brand.EnvPrefix + "INSTALL_DIR":   dir,
	})
	if _, err := output(home, env, shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", filepath.Join(r.root, "scripts", "install.ps1")); err != nil {
		return "", err
	}
	installed := filepath.Join(dir, r.brand.CLI+".exe")
	if runtime.GOOS != "windows" {
		if !isFile(installed) {
			return "", fmt.Errorf("install.ps1 did not install %s", installed)
		}
		return shell + ": downloaded, verified and unpacked " + r.brand.CLI + ".exe (it cannot run on " + runtime.GOOS + ")", nil
	}
	return r.checkInstalled(installed, home)
}

// checkInstalled runs an installed program's version command and compares it with the build.
func (r *rehearsal) checkInstalled(bin, home string) (string, error) {
	env := mergeEnv(withoutPrefix(os.Environ(), r.brand.EnvPrefix), map[string]string{"HOME": home, "USERPROFILE": home})
	out, err := output(home, env, bin, "--json", "version")
	if err != nil {
		return "", err
	}
	var v struct {
		Version, Commit string
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", fmt.Errorf("%s --json version: %w\n%s", bin, err, out)
	}
	if v.Version != r.meta.Version || v.Commit != r.meta.Commit {
		return "", fmt.Errorf("the installed %s says version %s, commit %s; the build is %s, %s", r.brand.CLI, v.Version, v.Commit, r.meta.Version, r.meta.Commit)
	}
	return fmt.Sprintf("installed from file:// into a temporary directory; %s version = %s", r.brand.CLI, v.Version), nil
}

func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/… on Windows
	}
	return "file://" + p
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// readChecksums reads a checksums.txt: "<sha256>  <file name>" per line.
func readChecksums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s: malformed line %q", path, sc.Text())
		}
		out[fields[1]] = fields[0]
	}
	return out, sc.Err()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// archiveEntries lists the files in a .tar.gz or .zip.
func archiveEntries(path string) ([]string, error) {
	var names []string
	if strings.HasSuffix(path, ".zip") {
		z, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer z.Close()
		for _, f := range z.File {
			names = append(names, f.Name)
		}
		return names, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		names = append(names, h.Name)
	}
}

// copyDir copies a directory of regular files, keeping their permissions (dist/ holds
// programs).
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
