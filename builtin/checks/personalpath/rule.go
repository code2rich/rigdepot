// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package personalpath

import (
	"context"
	"regexp"
	"strings"

	"github.com/code2rich/rigdepot/builtin/checks/internal/textlines"
	"github.com/code2rich/rigdepot/ext/check"
)

const id = "personalpath"

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.Advisory }

// Applies to every kind.
func (rule) Applies(string) bool { return true }

// homes are the home directories of the three systems. The first group is the account name.
// Windows comes first: "C:/Users/<name>" also looks like the macOS path, and a line gets at most
// one finding about a home directory.
var homes = []struct {
	system string
	re     *regexp.Regexp
}{
	{"windows", regexp.MustCompile(`(?i)\b[A-Z]:[\\/]+Users[\\/]+([A-Za-z0-9._-]+)`)},
	{"macos", regexp.MustCompile(`(?:^|[^A-Za-z0-9._/-])/Users/([A-Za-z0-9._-]+)`)},
	{"linux", regexp.MustCompile(`(?:^|[^A-Za-z0-9._/-])/home/([A-Za-z0-9._-]+)`)},
}

// notPersonal are names under the home directories that are not one person's account: shared
// folders and the usual stand-ins for "your name".
var notPersonal = []string{
	"shared", "public", "default", "default user", "all users", "guest",
	"user", "username", "yourname", "your-name", "your_name", "you", "me", "name", "example", "xxx",
}

var localPort = regexp.MustCompile(`(?i)\b(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])\s*:\s*(\d{2,5})\b`)

func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	var found []check.Finding
	add := func(file string, line int, problem string, params map[string]any) {
		found = append(found, check.Finding{
			Rule:     id,
			Severity: check.Advisory,
			File:     file,
			Line:     line,
			Message:  "check." + id + "." + problem,
			Params:   params,
		})
	}
	err := textlines.Each(in.Files, func(file string, line int, text string) {
	homes:
		for _, h := range homes {
			for _, m := range h.re.FindAllStringSubmatch(text, -1) {
				if isPersonal(m[1]) {
					add(file, line, "home_dir", map[string]any{"system": h.system})
					break homes
				}
			}
		}
		for _, m := range localPort.FindAllStringSubmatch(text, -1) {
			add(file, line, "local_port", map[string]any{"port": m[2]})
		}
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func isPersonal(name string) bool {
	lower := strings.ToLower(name)
	for _, n := range notPersonal {
		if lower == n {
			return false
		}
	}
	return true
}
