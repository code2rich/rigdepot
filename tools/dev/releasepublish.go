// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// runRelease publishes the release of the version tag on HEAD (docs: 10-release, 发版步骤). The
// same command runs on the owner's machine and in .github/workflows/release.yml:
//
//	go run ./tools/dev release            build, check and publish the tag on HEAD
//	go run ./tools/dev release --dry-run  build and check it, publish nothing
//	go run ./tools/dev release --dry-run --tag v0.4.0
//	                                      the same before tagging: in a temporary clone with a
//	                                      local tag that is never pushed
//
// Steps, each stopping the run when it fails:
//
//  1. preflight: a clean work tree; exactly one version tag (vX.Y.Z or vX.Y.Z-rc.N) on HEAD;
//     for a real release, HEAD on origin/main and the tag pushed with the same commit; release
//     notes for the version in CHANGELOG.md.
//  2. publishing: what this machine can publish with. Without GITHUB_TOKEN the run is a dry run
//     and says so. A real release needs PACKAGES_GITHUB_TOKEN (Homebrew tap and Scoop bucket;
//     not for a pre-release), Docker with a buildx builder that can push a multi-architecture
//     image (docker-container driver, both platforms) and credentials for ghcr.io (GHCR_TOKEN,
//     or an earlier docker login ghcr.io).
//  3. checks: check, and check-linux where this is not Linux; check-windows does not exist yet
//     and is listed as not run. --no-check skips them (they ran separately).
//  4. goreleaser, the web console and the SBOM switch, as in release-snapshot.
//  5. confirmation: the version, the commit and what will be published; type the version to go
//     on (--yes skips this, for CI).
//  6. goreleaser release: archives, packages, checksums, install scripts, the multi-architecture
//     image pushed to ghcr.io, the Homebrew cask and the Scoop manifest, and the GitHub release
//     as a draft (.goreleaser.yaml release.draft). A dry run passes --skip=publish.
//  7. the artifacts in dist/ checked like release-snapshot does, and rgd installed from dist/
//     with install.sh.
//  8. image exports: each architecture of the image just pushed pulled back, started until it is
//     ready, saved into dist/<package>_<version>_linux_<arch>.docker.tar.gz and added to
//     checksums.txt (goreleaser does not do docker save).
//  9. upload: the exports and the new checksums.txt onto the draft.
//  10. verify: every file of checksums.txt is on the draft with its size, and the draft's
//     checksums.txt is the local one.
//  11. publish: the draft becomes the release (latest, unless it is a pre-release).
//  12. installed from GitHub with install.sh into a temporary directory; rgd version must be
//     the version. Only after this step is the version announced.
//
// The tag is created and pushed by a person, never by this task: it is the one step that cannot
// be taken back.
func runRelease(root string, args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "build and check everything, publish nothing")
	tagFlag := fs.String("tag", "", "with --dry-run: the version tag to rehearse before it exists (vX.Y.Z or vX.Y.Z-rc.N)")
	yes := fs.Bool("yes", false, "do not ask before publishing (for CI)")
	noCheck := fs.Bool("no-check", false, "skip check and check-linux (they ran separately on this commit)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("release takes no arguments besides flags: %v", fs.Args())
	}
	if *tagFlag != "" && !*dryRun {
		return errors.New("--tag only goes with --dry-run: a release publishes the tag on HEAD, which a person creates and pushes")
	}
	b, err := readBrand(root)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	rel := &release{
		rehearsal: &rehearsal{root: root, brand: b, tmp: tmp, env: map[string]string{}},
		src:       root,
		dryRun:    *dryRun,
		tag:       *tagFlag,
		yes:       *yes,
		noCheck:   *noCheck,
	}
	steps := []struct {
		name string
		run  func() (string, error)
	}{
		{"preflight", rel.preflight},
		{"publishing", rel.publishing},
		{"check", rel.check},
		{"check-linux", rel.checkLinux},
		{"check-windows", func() (string, error) {
			return "", errNotRun{"there is no check-windows task yet: this release is not verified on Windows"}
		}},
		{"goreleaser", rel.buildGoreleaser},
		{"web console", rel.webConsole},
		{"SBOM", rel.sbom},
		{"confirmation", rel.confirm},
		{"build and publish", rel.goreleaserRelease},
		{"artifacts", rel.artifacts},
		{"install.sh", rel.installSh},
		{"image exports", rel.imageExports},
		{"upload", rel.upload},
		{"verify release", rel.verifyRelease},
		{"publish", rel.publish},
		{"install from GitHub", rel.installFromGitHub},
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
	title := "release results"
	if rel.dryRun {
		title = "release --dry-run results"
	}
	printResults(title, results)
	if rel.src != root && rel.meta.Version != "" {
		// The dry run built in a clone; keep its dist/ where people look for it.
		if err := os.RemoveAll(filepath.Join(root, "dist")); err == nil {
			if err := copyDir(filepath.Join(rel.src, "dist"), filepath.Join(root, "dist")); err == nil {
				fmt.Println("The artifacts of the dry run are in dist/.")
			}
		}
	}
	if failed {
		if rel.draftID != 0 {
			fmt.Printf("The release is still a draft on GitHub (%s); fix the problem and run release again, or delete the draft.\n", rel.tag)
		}
		return errors.New("failed")
	}
	if rel.dryRun {
		fmt.Println("Nothing was published or tagged.")
		for _, m := range rel.missing {
			fmt.Println("  To publish: " + m)
		}
		return nil
	}
	fmt.Printf("%s is released: https://github.com/%s/releases/tag/%s\n", rel.tag, rel.repo, rel.tag)
	return nil
}

// release is the state of one release run. It reuses the rehearsal's steps, with root set to
// the directory goreleaser builds in.
type release struct {
	*rehearsal
	src     string // where goreleaser runs: the repository, or for a dry run of a future tag a clone
	head    string // the commit
	dryRun  bool
	tag     string // v0.4.0
	version string // 0.4.0
	pre     bool   // a pre-release (-rc.N)
	yes     bool
	noCheck bool
	repo    string // owner/name on GitHub, from the module path
	notes   string // the release notes file
	missing []string
	gh      *githubClient
	draftID int64
	image   string // the image name without a tag
}

// versionTag is the form of a release tag: vX.Y.Z, or vX.Y.Z-rc.N for a release candidate.
var versionTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$`)

func (r *release) preflight() (string, error) {
	status, err := output(r.root, nil, "git", "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("the work tree has uncommitted changes; a release is built from a commit:\n%s", status)
	}
	head, err := output(r.root, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	tagsOut, err := output(r.root, nil, "git", "tag", "--points-at", "HEAD")
	if err != nil {
		return "", err
	}
	var tags []string
	for _, t := range strings.Fields(tagsOut) {
		if versionTag.MatchString(t) {
			tags = append(tags, t)
		}
	}
	var detail []string
	switch {
	case r.tag != "":
		if !versionTag.MatchString(r.tag) {
			return "", fmt.Errorf("--tag %s is not a version tag (vX.Y.Z or vX.Y.Z-rc.N)", r.tag)
		}
		if at, err := output(r.root, nil, "git", "rev-parse", "--verify", "--quiet", "refs/tags/"+r.tag+"^{commit}"); err == nil && at != "" {
			if at != head {
				return "", fmt.Errorf("the tag %s already exists on another commit (%s)", r.tag, at[:12])
			}
			detail = append(detail, r.tag+" on HEAD")
		} else {
			if len(tags) > 0 {
				return "", fmt.Errorf("HEAD is already tagged %s", strings.Join(tags, ", "))
			}
			detail = append(detail, r.tag+" not tagged yet: rehearsed in a temporary clone with a local tag")
			r.src = filepath.Join(r.tmp, "clone")
		}
	case len(tags) == 1:
		r.tag = tags[0]
		detail = append(detail, r.tag+" on HEAD")
	case len(tags) == 0:
		return "", errors.New("HEAD has no version tag. Tag it and push the tag (git tag -a vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z), or rehearse first with --dry-run --tag vX.Y.Z")
	default:
		return "", fmt.Errorf("HEAD has several version tags: %s", strings.Join(tags, ", "))
	}
	r.head = head
	r.version = strings.TrimPrefix(r.tag, "v")
	r.pre = strings.Contains(r.version, "-")

	module, err := moduleOf(r.root)
	if err != nil {
		return "", err
	}
	r.repo = strings.TrimPrefix(module, "github.com/")
	if r.repo == module || strings.Count(r.repo, "/") != 1 {
		return "", fmt.Errorf("module path %s is not github.com/<owner>/<name>", module)
	}
	r.image = "ghcr.io/" + strings.Split(r.repo, "/")[0] + "/" + r.serverPackage()

	notes, err := changelogSection(filepath.Join(r.root, "CHANGELOG.md"), r.version)
	if err != nil {
		return "", err
	}
	r.notes = filepath.Join(r.tmp, "release-notes.md")
	if err := os.WriteFile(r.notes, []byte(notes), 0o644); err != nil {
		return "", err
	}
	detail = append(detail, "release notes from CHANGELOG.md")

	if r.dryRun {
		detail = append(detail, "origin not consulted (dry run)")
	} else {
		remote, err := r.checkRemote(head)
		if err != nil {
			return "", err
		}
		detail = append(detail, remote)
	}
	if r.src != r.root {
		if err := r.cloneWithTag(head); err != nil {
			return "", err
		}
	}
	return head[:12] + ", clean; " + strings.Join(detail, "; "), nil
}

// checkRemote checks that HEAD is on origin/main and that the tag on origin is this commit. In
// GitHub Actions the run was started by pushing the tag, and the checkout has the branches.
func (r *release) checkRemote(head string) (string, error) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		if ref := os.Getenv("GITHUB_REF"); ref != "refs/tags/"+r.tag {
			return "", fmt.Errorf("GitHub Actions started this run for %s, not for refs/tags/%s", ref, r.tag)
		}
	} else if _, err := output(r.root, nil, "git", "fetch", "--quiet", "origin", "main"); err != nil {
		return "", err
	}
	if _, err := output(r.root, nil, "git", "merge-base", "--is-ancestor", "HEAD", "origin/main"); err != nil {
		return "", fmt.Errorf("HEAD is not on origin/main; a release is tagged on the main branch")
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return "on origin/main; started by the tag", nil
	}
	refs, err := output(r.root, nil, "git", "ls-remote", "--tags", "origin", "refs/tags/"+r.tag, "refs/tags/"+r.tag+"^{}")
	if err != nil {
		return "", err
	}
	at := ""
	for _, line := range strings.Split(refs, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		// An annotated tag is listed twice; the peeled line (^{}) names the commit.
		if f[1] == "refs/tags/"+r.tag+"^{}" || at == "" {
			at = f[0]
		}
	}
	if at == "" {
		return "", fmt.Errorf("the tag %s is not on origin; push it first: git push origin %s", r.tag, r.tag)
	}
	if at != head {
		return "", fmt.Errorf("the tag %s on origin is %s, not HEAD", r.tag, at[:12])
	}
	return "on origin/main; the tag on origin is HEAD", nil
}

// cloneWithTag makes the clone a dry run of a future tag builds in: HEAD, the built console and
// a local tag that exists only there.
func (r *release) cloneWithTag(head string) error {
	if _, err := output(r.root, nil, "git", "clone", "--quiet", "--no-hardlinks", r.root, r.src); err != nil {
		return err
	}
	if _, err := output(r.src, nil, "git", "checkout", "--quiet", "--detach", head); err != nil {
		return err
	}
	_, err := output(r.src, nil, "git", "tag", r.tag)
	return err
}

// changelogSection returns the notes of version from a changelog with one "## <version>"
// section per release; a release candidate uses the section of the version it leads to.
func changelogSection(path, version string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("release notes: %w", err)
	}
	want, _, _ := strings.Cut(version, "-")
	var out []string
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			heading := strings.Fields(strings.TrimPrefix(line, "## "))
			in = len(heading) > 0 && strings.TrimPrefix(heading[0], "v") == want
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	notes := strings.TrimSpace(strings.Join(out, "\n"))
	if notes == "" {
		return "", fmt.Errorf("%s has no \"## %s\" section with the release notes", filepath.Base(path), want)
	}
	return notes + "\n", nil
}

// publishing finds out what this machine can publish with.
func (r *release) publishing() (string, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" && !r.dryRun {
		r.dryRun = true
		r.missing = append(r.missing, "GITHUB_TOKEN, a token that can write the contents of "+r.repo)
		fmt.Println("No GITHUB_TOKEN: this run builds and checks everything and publishes nothing.")
	}
	if r.dryRun {
		if token == "" && !slices.ContainsFunc(r.missing, func(m string) bool { return strings.HasPrefix(m, "GITHUB_TOKEN") }) {
			r.missing = append(r.missing, "GITHUB_TOKEN, a token that can write the contents of "+r.repo)
		}
		if !r.pre && os.Getenv("PACKAGES_GITHUB_TOKEN") == "" {
			r.missing = append(r.missing, "PACKAGES_GITHUB_TOKEN, a token that can push to the Homebrew tap and the Scoop bucket")
		}
		if err := r.dockerPublishing(); err != nil {
			r.missing = append(r.missing, err.Error())
		}
		return "dry run: nothing is published; the image is not built (only a release pushes it; release-snapshot builds and runs it)", nil
	}
	r.gh = newGitHubClient(r.repo, token)
	var detail []string
	if r.pre {
		detail = append(detail, "a pre-release: Homebrew and Scoop are not updated")
	} else if os.Getenv("PACKAGES_GITHUB_TOKEN") == "" {
		return "", errors.New("PACKAGES_GITHUB_TOKEN is not set: a release updates the Homebrew tap and the Scoop bucket with it")
	} else {
		detail = append(detail, "PACKAGES_GITHUB_TOKEN for the Homebrew tap and the Scoop bucket")
	}
	if err := r.dockerPublishing(); err != nil {
		return "", err
	}
	r.docker = true
	detail = append(detail, "Docker with a docker-container buildx builder for linux/amd64 and linux/arm64, logged in to ghcr.io")
	return "GITHUB_TOKEN for " + r.repo + "; " + strings.Join(detail, "; "), nil
}

// dockerPublishing checks that Docker can build and push the multi-architecture image, and logs
// in to ghcr.io with GHCR_TOKEN when it is set.
func (r *release) dockerPublishing() error {
	if _, err := output(r.root, nil, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		return errors.New("Docker, to build and push the server image: " + firstLine(err.Error()))
	}
	inspect, err := output(r.root, nil, "docker", "buildx", "inspect", "--bootstrap")
	if err != nil {
		return errors.New("docker buildx, to build the image for two architectures: " + firstLine(err.Error()))
	}
	driver, platforms := "", ""
	for _, line := range strings.Split(inspect, "\n") {
		k, v, _ := strings.Cut(line, ":")
		switch strings.TrimSpace(k) {
		case "Driver":
			if driver == "" {
				driver = strings.TrimSpace(v)
			}
		case "Platforms":
			platforms += strings.TrimSpace(v) + ","
		}
	}
	if driver != "docker-container" {
		return fmt.Errorf("a buildx builder with the docker-container driver (the current one is %q, which cannot push a multi-architecture image): docker buildx create --name release --driver docker-container --use", driver)
	}
	for _, p := range []string{"linux/amd64", "linux/arm64"} {
		if !strings.Contains(platforms, p) {
			return fmt.Errorf("the buildx builder cannot build %s; install QEMU (docker run --privileged --rm tonistiigi/binfmt --install all)", p)
		}
	}
	if token := os.Getenv("GHCR_TOKEN"); token != "" {
		user := os.Getenv("GHCR_USER")
		if user == "" {
			user = strings.Split(r.repo, "/")[0]
		}
		cmd := exec.Command("docker", "login", "ghcr.io", "--username", user, "--password-stdin")
		cmd.Stdin = strings.NewReader(token)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("docker login ghcr.io with GHCR_TOKEN: %v\n%s", err, out)
		}
		return nil
	}
	if !dockerLoggedIn("ghcr.io") {
		return errors.New("credentials for ghcr.io: set GHCR_TOKEN (a classic token with write:packages) or run docker login ghcr.io first")
	}
	return nil
}

// dockerLoggedIn reports whether Docker's configuration holds credentials for a registry.
func dockerLoggedIn(registry string) bool {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".docker")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Auths       map[string]json.RawMessage `json:"auths"`
		CredHelpers map[string]string          `json:"credHelpers"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return false
	}
	_, auth := cfg.Auths[registry]
	_, helper := cfg.CredHelpers[registry]
	return auth || helper
}

func (r *release) check() (string, error) {
	if r.noCheck {
		return "", errNotRun{"--no-check: check ran separately on this commit"}
	}
	if err := runCheck(r.root, nil); err != nil {
		return "", err
	}
	return "go run ./tools/dev check passed (its table is above)", nil
}

func (r *release) checkLinux() (string, error) {
	if r.noCheck {
		return "", errNotRun{"--no-check: check-linux ran separately on this commit"}
	}
	if runtime.GOOS == "linux" {
		return "", errNotRun{"this is Linux: check above ran here"}
	}
	if err := runCheckLinux(r.root, nil); err != nil {
		return "", err
	}
	return "go run ./tools/dev check-linux passed", nil
}

// webConsole builds the console; a release without it is not a release.
func (r *release) webConsole() (string, error) {
	detail, err := r.buildWeb()
	var skipped errNotRun
	if errors.As(err, &skipped) {
		return "", errors.New("a release embeds the web console: " + skipped.reason)
	}
	if err != nil {
		return "", err
	}
	if r.src != r.root {
		if err := copyDir(filepath.Join(r.root, webDir, "dist"), filepath.Join(r.src, webDir, "dist")); err != nil {
			return "", err
		}
	}
	return detail, nil
}

func (r *release) confirm() (string, error) {
	if r.dryRun {
		return "", errNotRun{"dry run"}
	}
	if r.yes {
		return "--yes", nil
	}
	if info, err := os.Stdin.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", errors.New("nobody to ask: run it in a terminal, or pass --yes")
	}
	what := "archives, packages, install scripts, checksums and the image " + r.image + ":" + r.version
	if !r.pre {
		what += " (also " + majorMinor(r.version) + " and latest), the Homebrew cask and the Scoop manifest"
	}
	fmt.Printf("\nAbout to publish %s of %s from commit %s:\n  %s\n\nType %s to go on: ", r.tag, r.repo, r.head, what, r.version)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(line) != r.version {
		return "", errors.New("not confirmed")
	}
	return "confirmed", nil
}

func majorMinor(v string) string {
	parts := strings.SplitN(v, ".", 3)
	return parts[0] + "." + parts[1]
}

// goreleaserRelease runs goreleaser release on the tag. A dry run skips publishing (and with it
// the image, which goreleaser builds only to push it) and keeps every token from goreleaser.
//
// From here on the rehearsal's steps work in that directory: dist/ is there.
func (r *release) goreleaserRelease() (string, error) {
	r.root = r.src
	args := []string{"release", "--clean", "--release-notes", r.notes}
	vars := map[string]string{"GORELEASER_CURRENT_TAG": r.tag}
	for k, v := range r.env {
		vars[k] = v
	}
	env := mergeEnv(os.Environ(), vars)
	// An empty secret in GitHub Actions arrives as a variable that is set but empty; to
	// .goreleaser.yaml (isEnvSet) it must look unset.
	env = slices.DeleteFunc(env, func(kv string) bool { return kv == "PACKAGES_GITHUB_TOKEN=" })
	if r.dryRun {
		args = append(args, "--skip=publish,docker")
		env = slices.DeleteFunc(env, func(kv string) bool {
			name, _, _ := strings.Cut(kv, "=")
			return name == "PACKAGES_GITHUB_TOKEN" || name == "GITHUB_TOKEN" || name == "GORELEASER_KEY"
		})
	}
	cmd := exec.Command(r.goreleaserBin, args...)
	cmd.Dir = r.src
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("goreleaser %s: %w", strings.Join(args, " "), err)
	}
	if r.dryRun {
		return "goreleaser release --skip=publish,docker: built " + r.tag + ", published nothing", nil
	}
	return "goreleaser release: image pushed, Homebrew and Scoop updated (unless a pre-release), GitHub release created as a draft", nil
}

// artifacts checks dist/ the way release-snapshot does, for the version of the tag.
func (r *release) artifacts() (string, error) {
	if !r.dryRun {
		r.pushedImage = r.image + ":" + r.version
	}
	detail, err := r.verifyDist()
	if err != nil {
		return "", err
	}
	if r.meta.Version != r.version {
		return "", fmt.Errorf("dist/metadata.json says version %s; the tag is %s", r.meta.Version, r.tag)
	}
	return detail, nil
}

// imageExports pulls back each architecture of the image that was pushed, runs it, and saves it
// into dist/ next to the other files, adding it to checksums.txt.
func (r *release) imageExports() (string, error) {
	if r.dryRun {
		return "", errNotRun{"dry run: no image was pushed"}
	}
	ref := r.image + ":" + r.version
	_, inspectErr := output(r.root, nil, "docker", "image", "inspect", ref)
	hadImage := inspectErr == nil
	var names []string
	for _, arch := range []string{"amd64", "arm64"} {
		platform := "linux/" + arch
		if _, err := output(r.root, nil, "docker", "pull", "--quiet", "--platform", platform, ref); err != nil {
			return "", err
		}
		if err := r.checkServer(arch, []string{"docker", "run", "--platform", platform}, ref, nil); err != nil {
			return "", fmt.Errorf("%s image: %w", arch, err)
		}
		path := r.imageExport(arch)
		if err := dockerSave(ref, path, "--platform", platform); err != nil {
			return "", err
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return "", err
		}
		r.sums[filepath.Base(path)] = sum
		names = append(names, filepath.Base(path))
	}
	if !hadImage {
		// Only the image this step pulled.
		exec.Command("docker", "image", "rm", ref).Run()
	}
	if err := writeChecksums(filepath.Join(r.root, "dist", "checksums.txt"), r.sums); err != nil {
		return "", err
	}
	return "pulled " + ref + " per architecture from ghcr.io, ran both until ready, saved " + strings.Join(names, ", ") + " and added them to checksums.txt", nil
}

// upload puts the image exports and the new checksums.txt onto the draft goreleaser created.
func (r *release) upload() (string, error) {
	if r.dryRun {
		return "", errNotRun{"dry run"}
	}
	rel, err := r.gh.draftByTag(r.tag)
	if err != nil {
		return "", err
	}
	r.draftID = rel.ID
	dist := filepath.Join(r.root, "dist")
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" || strings.HasSuffix(a.Name, ".docker.tar.gz") {
			if err := r.gh.deleteAsset(a.ID); err != nil {
				return "", err
			}
		}
	}
	var uploaded []string
	for _, arch := range []string{"amd64", "arm64"} {
		if err := r.gh.uploadAsset(rel.ID, r.imageExport(arch)); err != nil {
			return "", err
		}
		uploaded = append(uploaded, filepath.Base(r.imageExport(arch)))
	}
	if err := r.gh.uploadAsset(rel.ID, filepath.Join(dist, "checksums.txt")); err != nil {
		return "", err
	}
	return "onto the draft " + r.tag + ": " + strings.Join(uploaded, ", ") + " and checksums.txt (replacing goreleaser's)", nil
}

// verifyRelease checks the draft against dist/: every file of checksums.txt and checksums.txt
// itself with their sizes, and checksums.txt byte for byte.
func (r *release) verifyRelease() (string, error) {
	if r.dryRun {
		return "", errNotRun{"dry run"}
	}
	rel, err := r.gh.release(r.draftID)
	if err != nil {
		return "", err
	}
	assets := map[string]githubAsset{}
	for _, a := range rel.Assets {
		assets[a.Name] = a
	}
	dist := filepath.Join(r.root, "dist")
	var problems []string
	names := append([]string{"checksums.txt"}, sortedKeys(r.sums)...)
	for _, name := range names {
		info, err := os.Stat(filepath.Join(dist, name))
		if err != nil {
			// install.sh and install.ps1 are listed by name, from scripts/.
			info, err = os.Stat(filepath.Join(r.root, "scripts", name))
		}
		if err != nil {
			problems = append(problems, name+": not in dist/")
			continue
		}
		a, ok := assets[name]
		switch {
		case !ok:
			problems = append(problems, name+": not on the release")
		case a.Size != info.Size():
			problems = append(problems, fmt.Sprintf("%s: %d bytes on the release, %d here", name, a.Size, info.Size()))
		}
	}
	if a, ok := assets["checksums.txt"]; ok {
		remote, err := r.gh.downloadAsset(a.ID)
		if err != nil {
			return "", err
		}
		local, err := os.ReadFile(filepath.Join(dist, "checksums.txt"))
		if err != nil {
			return "", err
		}
		if string(remote) != string(local) {
			problems = append(problems, "checksums.txt on the release differs from dist/checksums.txt")
		}
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%d problem(s) on the draft:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
	return fmt.Sprintf("all %d files of checksums.txt and checksums.txt itself are on the draft with their sizes; checksums.txt identical", len(r.sums)), nil
}

func (r *release) publish() (string, error) {
	if r.dryRun {
		return "", errNotRun{"dry run"}
	}
	latest := "true"
	if r.pre {
		latest = "false"
	}
	if err := r.gh.publishDraft(r.draftID, latest); err != nil {
		return "", err
	}
	r.draftID = 0
	if r.pre {
		return r.tag + " published as a pre-release", nil
	}
	return r.tag + " published and marked latest", nil
}

// installFromGitHub installs rgd of this version from the release with install.sh, the way
// people will.
func (r *release) installFromGitHub() (string, error) {
	if r.dryRun {
		return "", errNotRun{"dry run"}
	}
	if runtime.GOOS == "windows" {
		return "", errNotRun{"install.sh is for macOS and Linux"}
	}
	home := filepath.Join(r.tmp, "install-github")
	dir := filepath.Join(home, "bin")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	env := mergeEnv(withoutPrefix(os.Environ(), r.brand.EnvPrefix), map[string]string{
		"HOME":                            home,
		r.brand.EnvPrefix + "VERSION":     r.version,
		r.brand.EnvPrefix + "INSTALL_DIR": dir,
	})
	if _, err := output(home, env, "sh", filepath.Join(r.root, "scripts", "install.sh")); err != nil {
		return "", fmt.Errorf("%w (is the repository public?)", err)
	}
	detail, err := r.checkInstalled(filepath.Join(dir, r.brand.CLI), home)
	if err != nil {
		return "", err
	}
	return strings.Replace(detail, "from file://", "from github.com", 1), nil
}
