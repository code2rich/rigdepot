// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The server's container image and packages in the release rehearsal (docs: 08-deployment,
// 10-release). goreleaser builds the Debian and RPM packages and, from the Debian package of
// each architecture, the image (deploy/docker/Dockerfile); in a snapshot it loads the image of
// each architecture into the local Docker as <tag>-<arch>. The steps here:
//
//   - docker: whether Docker with buildx is there; without it goreleaser skips the image and
//     the image steps do not run.
//   - image exports: docker save of each architecture's image into
//     dist/<package>_<version>_linux_<arch>.docker.tar.gz, added to checksums.txt, for
//     networks that cannot reach a registry. The images are then removed from Docker.
//   - images: docker load of each export, and each architecture run (the other one emulated):
//     the version, then the server started with an empty data directory until /readyz answers
//     and it hands out the command line of this build.
//   - packages: the Debian package installed into a clean Debian container and the RPM into a
//     clean Rocky Linux container, for this machine's architecture: the files where they
//     belong, the system user, and the server started as that user until /readyz answers.

// serverPackage is the name of the server's packages, image and image exports.
func (r *rehearsal) serverPackage() string { return r.brand.CLILong + "-server" }

// dockerCheck finds Docker and buildx, and otherwise has goreleaser skip the image.
func (r *rehearsal) dockerCheck() (string, error) {
	for _, args := range [][]string{{"version", "--format", "{{.Server.Version}}"}, {"buildx", "version"}} {
		if _, err := output(r.root, nil, "docker", args...); err != nil {
			r.skip = append(r.skip, "--skip=docker")
			return "", errNotRun{"docker " + args[0] + " failed; the container image is not built: " + firstLine(err.Error())}
		}
	}
	r.docker = true
	v, _ := output(r.root, nil, "docker", "version", "--format", "{{.Server.Version}} {{.Server.Os}}/{{.Server.Arch}}")
	return "Docker " + v + " with buildx; goreleaser builds the image for linux/amd64 and linux/arm64", nil
}

// snapshotImages returns the image goreleaser built and loaded for each architecture, from
// dist/artifacts.json.
func (r *rehearsal) snapshotImages() (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(r.root, "dist", "artifacts.json"))
	if err != nil {
		return nil, err
	}
	var arts []struct{ Name, Type string }
	if err := json.Unmarshal(data, &arts); err != nil {
		return nil, fmt.Errorf("dist/artifacts.json: %w", err)
	}
	tag := strings.ReplaceAll(r.meta.Version, "+", "-")
	out := map[string]string{}
	for _, a := range arts {
		if a.Type != "Docker Image" {
			continue
		}
		for _, arch := range []string{"amd64", "arm64"} {
			if strings.HasSuffix(a.Name, ":"+tag+"-"+arch) {
				out[arch] = a.Name
			}
		}
	}
	if len(out) != 2 {
		return nil, fmt.Errorf("dist/artifacts.json names images %v; want the %s image for amd64 and arm64", out, tag)
	}
	return out, nil
}

func (r *rehearsal) imageExport(arch string) string {
	return filepath.Join(r.root, "dist", fmt.Sprintf("%s_%s_linux_%s.docker.tar.gz", r.serverPackage(), r.meta.Version, arch))
}

// imageExports saves each architecture's image into dist/ and adds the files to checksums.txt.
func (r *rehearsal) imageExports() (string, error) {
	if !r.docker {
		return "", errNotRun{"no Docker"}
	}
	images := r.images
	var names []string
	for _, arch := range []string{"amd64", "arm64"} {
		path := r.imageExport(arch)
		if err := dockerSave(images[arch], path); err != nil {
			return "", err
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return "", err
		}
		r.sums[filepath.Base(path)] = sum
		names = append(names, filepath.Base(path))
	}
	if err := writeChecksums(filepath.Join(r.root, "dist", "checksums.txt"), r.sums); err != nil {
		return "", err
	}
	// The exports are the images from here on: the next step loads them back.
	for _, img := range images {
		if _, err := output(r.root, nil, "docker", "image", "rm", img); err != nil {
			return "", err
		}
	}
	return strings.Join(names, ", ") + " (docker save, gzip), in checksums.txt", nil
}

// dockerSave writes docker save of image, gzipped, to path. args go before the image, such as
// --platform.
func dockerSave(image, path string, args ...string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	cmd := exec.Command("docker", append(append([]string{"save"}, args...), image)...)
	cmd.Stdout = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker save %s: %w\n%s", image, err, stderr.String())
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

// writeChecksums writes a checksums.txt the way goreleaser does: sorted by name.
func writeChecksums(path string, sums map[string]string) error {
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// images loads each export and runs it on its architecture.
func (r *rehearsal) runImages() (string, error) {
	if !r.docker || r.images == nil {
		return "", errNotRun{"no image exports"}
	}
	defer func() {
		for _, img := range r.images {
			exec.Command("docker", "image", "rm", "--force", img).Run()
		}
	}()
	var done []string
	for _, arch := range []string{"amd64", "arm64"} {
		out, err := output(r.root, nil, "docker", "load", "--input", r.imageExport(arch))
		if err != nil {
			return "", err
		}
		if !strings.Contains(out, r.images[arch]) {
			return "", fmt.Errorf("docker load of the %s export: %s; want %s", arch, out, r.images[arch])
		}
		if err := r.checkServer(arch, []string{"docker", "run", "--platform", "linux/" + arch}, r.images[arch], nil); err != nil {
			return "", fmt.Errorf("%s image: %w", arch, err)
		}
		done = append(done, arch)
	}
	return "loaded from the exports; " + strings.Join(done, " and ") + " report " + r.meta.Version +
		", start read-only on an empty volume, are ready and hand out the command line", nil
}

// checkServer starts the server with run (docker run … image [args]) on an empty data
// directory, waits until /readyz answers, and checks that it hands out the command line of
// this build. It runs the version command first.
func (r *rehearsal) checkServer(arch string, run []string, image string, serve []string) error {
	version, err := output(r.root, nil, run[0], append(append(append([]string{}, run[1:]...), "--rm", image), append(serve, "version")...)...)
	if err != nil {
		return err
	}
	if !strings.Contains(version, r.meta.Version) || !strings.Contains(version, "linux/"+arch) {
		return fmt.Errorf("version says %q; want %s on linux/%s", version, r.meta.Version, arch)
	}
	name := fmt.Sprintf("release-snapshot-%d", time.Now().UnixNano())
	env := r.brand.EnvPrefix
	args := append(append([]string{}, run[1:]...), "--detach", "--name", name, "--read-only", "--tmpfs", "/tmp",
		"--publish", "127.0.0.1::8080",
		"--env", env+"BASE_URL=http://localhost:8080", "--env", env+"LISTEN=0.0.0.0:8080", "--env", env+"TRUSTED_PROXIES=172.16.0.0/12",
		image)
	args = append(args, serve...)
	if _, err := output(r.root, nil, run[0], args...); err != nil {
		return err
	}
	defer exec.Command("docker", "rm", "--force", "--volumes", name).Run()
	port, err := output(r.root, nil, "docker", "port", name, "8080/tcp")
	if err != nil {
		return err
	}
	base := "http://" + strings.TrimSpace(strings.Split(port, "\n")[0])
	if err := waitReady(base+"/readyz", 2*time.Minute); err != nil {
		logs, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
		return fmt.Errorf("%w\n%s", err, logs)
	}
	var list struct {
		Version string
		Items   []struct{ File string }
	}
	if err := getJSON(base+"/api/v1/cli/downloads", &list); err != nil {
		return err
	}
	if want, _, _ := strings.Cut(r.meta.Version, "+"); list.Version != want || len(list.Items) != len(targets) {
		return fmt.Errorf("/api/v1/cli/downloads: version %s with %d files; want %s with %d", list.Version, len(list.Items), want, len(targets))
	}
	script, err := httpGet(base + "/install.sh")
	if err != nil {
		return err
	}
	if !strings.Contains(script, "served_version='"+r.meta.Version+"'") {
		return errors.New("/install.sh does not hand out the command line of this build")
	}
	return nil
}

func waitReady(url string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer 200 within %s (last: %v)", url, limit, err)
		}
		time.Sleep(time.Second)
	}
}

func httpGet(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return string(body), nil
}

func getJSON(url string, v any) error {
	body, err := httpGet(url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

// packageFiles returns the Debian and RPM package of each architecture, from dist/.
func (r *rehearsal) packageFiles() (map[string]string, error) {
	dist := filepath.Join(r.root, "dist")
	out := map[string]string{}
	for arch, rpmArch := range map[string]string{"amd64": "x86_64", "arm64": "aarch64"} {
		for _, pattern := range []string{r.serverPackage() + "_*_" + arch + ".deb", r.serverPackage() + "-*." + rpmArch + ".rpm"} {
			m, _ := filepath.Glob(filepath.Join(dist, pattern))
			if len(m) != 1 {
				return nil, fmt.Errorf("dist/ has %d files like %s, want 1", len(m), pattern)
			}
			out[arch+filepath.Ext(m[0])] = m[0]
		}
	}
	return out, nil
}

// The clean systems the packages are installed on.
const (
	debianImage = "debian:bookworm"
	rockyImage  = "rockylinux/rockylinux:9"
)

// installPackages installs the packages for this machine's architecture into clean
// containers and starts the server from them.
func (r *rehearsal) installPackages() (string, error) {
	if !r.docker {
		return "", errNotRun{"no Docker"}
	}
	pkgs, err := r.packageFiles()
	if err != nil {
		return "", err
	}
	arch, err := output(r.root, nil, "docker", "version", "--format", "{{.Server.Arch}}")
	if err != nil {
		return "", err
	}
	var done []string
	for _, p := range []struct{ image, file, install string }{
		{debianImage, pkgs[arch+".deb"], "apt-get update -qq && apt-get install -y -qq /pkg/" + filepath.Base(pkgs[arch+".deb"])},
		{rockyImage, pkgs[arch+".rpm"], "dnf install -y -q /pkg/" + filepath.Base(pkgs[arch+".rpm"])},
	} {
		if err := r.checkPackage(arch, p.image, p.file, p.install); err != nil {
			return "", fmt.Errorf("%s in %s: %w", filepath.Base(p.file), p.image, err)
		}
		done = append(done, filepath.Base(p.file)+" in "+p.image)
	}
	return strings.Join(done, ", ") + ": files, user and service unit in place; the server runs as its user and is ready", nil
}

// checkPackage installs one package into a clean container, checks what it put where and runs
// the server from it as its own user. systemd does not run in the container, so the unit's
// command runs directly.
func (r *rehearsal) checkPackage(arch, image, file, install string) error {
	name := fmt.Sprintf("release-snapshot-pkg-%d", time.Now().UnixNano())
	if _, err := output(r.root, nil, "docker", "run", "--detach", "--name", name, "--platform", "linux/"+arch,
		"--volume", filepath.Dir(file)+":/pkg:ro", "--publish", "127.0.0.1::8080", "--entrypoint", "sleep", image, "infinity"); err != nil {
		return err
	}
	defer exec.Command("docker", "rm", "--force", "--volumes", name).Run()
	exec := func(script string) (string, error) {
		return output(r.root, nil, "docker", "exec", name, "sh", "-c", script)
	}
	if out, err := exec(install); err != nil {
		return fmt.Errorf("install: %w\n%s", err, out)
	}
	data := "/var/lib/" + r.brand.CLILong
	layout, err := exec(fmt.Sprintf(`set -e
stat -c '%%n %%a %%U' /usr/bin/%[1]s /etc/%[2]s/server.env %[3]s /usr/lib/systemd/system/%[4]s.service
getent passwd %[2]s | cut -d: -f6,7
ls /usr/share/%[2]s/cli/ | wc -l`, r.brand.Server, r.brand.CLILong, data, r.serverPackage()))
	if err != nil {
		return err
	}
	for _, want := range []string{
		"/usr/bin/" + r.brand.Server + " 755 root", "/etc/" + r.brand.CLILong + "/server.env 600 root", data + " 700 " + r.brand.CLILong,
		r.serverPackage() + ".service 644 root", data + ":/usr/sbin/nologin", "\n" + fmt.Sprint(len(targets)),
	} {
		if !strings.Contains(layout, want) {
			return fmt.Errorf("after installing: want %q in\n%s", want, layout)
		}
	}
	// The unit's command, as the unit's user, with the unit's environment file plus the address.
	start := fmt.Sprintf(`set -a; . /etc/%[1]s/server.env; %[2]sBASE_URL=http://localhost:8080; %[2]sLISTEN=0.0.0.0:8080; %[2]sTRUSTED_PROXIES=172.16.0.0/12; set +a
nohup setpriv --reuid=%[1]s --regid=%[1]s --init-groups /usr/bin/%[3]s serve > /tmp/server.log 2>&1 &`, r.brand.CLILong, r.brand.EnvPrefix, r.brand.Server)
	if out, err := exec(start); err != nil {
		return fmt.Errorf("start: %w\n%s", err, out)
	}
	port, err := output(r.root, nil, "docker", "port", name, "8080/tcp")
	if err != nil {
		return err
	}
	base := "http://" + strings.TrimSpace(strings.Split(port, "\n")[0])
	if err := waitReady(base+"/readyz", 2*time.Minute); err != nil {
		logs, _ := exec("tail -20 /tmp/server.log")
		return fmt.Errorf("%w\n%s", err, logs)
	}
	owner, err := exec("stat -c '%U' " + data + "/master.key")
	if err != nil || owner != r.brand.CLILong {
		return fmt.Errorf("master.key belongs to %q (%v), want %s", owner, err, r.brand.CLILong)
	}
	script, err := httpGet(base + "/install.sh")
	if err != nil {
		return err
	}
	if !strings.Contains(script, "served_version='"+r.meta.Version+"'") {
		return errors.New("/install.sh does not hand out the command line of this build")
	}
	return nil
}

// checkPackagesListed checks that the Debian and RPM packages are in dist/ and in
// checksums.txt.
func (r *rehearsal) checkPackagesListed() []string {
	var problems []string
	pkgs, err := r.packageFiles()
	if err != nil {
		return []string{err.Error()}
	}
	keys := make([]string, 0, len(pkgs))
	for k := range pkgs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		name := filepath.Base(pkgs[k])
		sum, err := fileSHA256(pkgs[k])
		if err != nil || r.sums[name] != sum {
			problems = append(problems, name+": not in checksums.txt, or a different checksum")
		}
	}
	return problems
}
