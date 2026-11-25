// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package buildinfo

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// These are set at link time by a release build:
//
//	-ldflags "-X github.com/code2rich/rigdepot/internal/buildinfo.Version=0.4.0 ..."
//
// A build without them (go build, go run, go install) falls back to what the
// Go toolchain recorded about the source it was built from.
var (
	// Version is the semantic version without the leading "v".
	Version = ""
	// Commit is the full hash of the commit the binary was built from.
	Commit = ""
	// Date is the time of that commit in RFC 3339. It is the commit time and
	// not the build time, so that two builds of one commit are identical.
	Date = ""
)

// devVersion marks a build that is not a release, so that it cannot be
// mistaken for one.
const devVersion = "0.0.0-dev"

// Info describes the build of the running binary.
type Info struct {
	Version   string // "0.4.0", or "0.0.0-dev+<commit>" for a development build
	Commit    string // full commit hash; empty when unknown
	Date      string // commit time in RFC 3339; empty when unknown
	Modified  bool   // built from a work tree with uncommitted changes
	GoVersion string
	OS        string
	Arch      string
}

// Get returns the build information of the running binary.
func Get() Info {
	var settings map[string]string
	moduleVersion := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = bi.Main.Version
		settings = make(map[string]string, len(bi.Settings))
		for _, s := range bi.Settings {
			settings[s.Key] = s.Value
		}
	}
	return resolve(Version, Commit, Date, moduleVersion, settings)
}

// resolve combines the link-time values with what the toolchain recorded.
// Link-time values win; they are what a release build sets.
func resolve(version, commit, date, moduleVersion string, settings map[string]string) Info {
	info := Info{
		Version:   version,
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if info.Commit == "" {
		info.Commit = settings["vcs.revision"]
		info.Modified = settings["vcs.modified"] == "true"
	}
	if info.Date == "" {
		info.Date = settings["vcs.time"]
	}
	if info.Version != "" {
		return info
	}
	// "go install module@v0.4.0" records the module version and nothing else.
	// A pseudo-version is what the toolchain makes up for an untagged commit;
	// it is not a release.
	if v := strings.TrimPrefix(moduleVersion, "v"); v != "" && v != "(devel)" && !pseudoVersion.MatchString(v) {
		info.Version = v
		return info
	}
	info.Version = devVersion
	if info.Commit != "" {
		info.Version += "+" + short(info.Commit)
		if info.Modified {
			info.Version += ".dirty"
		}
	}
	return info
}

// pseudoVersion matches the timestamp and commit that end a pseudo-version,
// as in v0.4.1-0.20261125080000-1a2b3c4d5e6f.
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}`)

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// String formats the build information on one line for the given program:
//
//	rgd 0.4.0 (commit 1a2b3c4d5e6f, 2026-11-25T08:00:00Z, linux/amd64, go1.26.0)
func (i Info) String(program string) string {
	var details []string
	if i.Commit != "" {
		details = append(details, "commit "+short(i.Commit))
	}
	if i.Date != "" {
		details = append(details, i.Date)
	}
	details = append(details, i.OS+"/"+i.Arch, i.GoVersion)
	return fmt.Sprintf("%s %s (%s)", program, i.Version, strings.Join(details, ", "))
}
