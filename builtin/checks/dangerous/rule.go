// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package dangerous

import (
	"context"
	"regexp"

	"github.com/code2rich/rigdepot/builtin/checks/internal/textlines"
	"github.com/code2rich/rigdepot/ext/check"
)

const id = "dangerous"

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.MustFix }

// Applies to every kind: a dangerous command is dangerous wherever it is written.
func (rule) Applies(string) bool { return true }

// pattern is one kind of dangerous command. A line matches when it matches any of the regular
// expressions and, if set, the extra test.
type pattern struct {
	problem string
	res     []*regexp.Regexp
	also    func(m []string) bool
}

var patterns = []pattern{
	{
		problem: "delete_root",
		res:     []*regexp.Regexp{regexp.MustCompile(`\brm((?:\s+-{1,2}[A-Za-z-]+)+)\s+["']?(/\*?|~/?\*?|\$\{?HOME\}?/?\*?)["']?(?:\s|$|[;&|)])`)},
		also: func(m []string) bool {
			return recursive.MatchString(m[1]) && force.MatchString(m[1])
		},
	},
	{
		problem: "pipe_to_shell",
		res: []*regexp.Regexp{
			regexp.MustCompile(`(?i)\b(curl|wget|iwr|irm|Invoke-WebRequest|Invoke-RestMethod)\b[^|\n]*\|\s*(sudo\s+)?(-\S+\s+)*((ba|z|k|da|fi)?sh|iex|Invoke-Expression)\b`),
			regexp.MustCompile(`\b(ba|z|k|da)?sh\s+(-c\s+)?["']?(\$\(|<\()\s*(curl|wget)\b`),
		},
	},
	{
		problem: "world_writable",
		res:     []*regexp.Regexp{regexp.MustCompile(`\bchmod\s+(-R\s+|--recursive\s+)?(0?777|a\+rwx|ugo\+rwx|o\+w)(\s|$)`)},
	},
	{
		problem: "tls_disabled",
		res: []*regexp.Regexp{
			regexp.MustCompile(`\bcurl\b[^|;\n]*\s(-[A-Za-z]*k[A-Za-z]*|--insecure)(\s|$)`),
			regexp.MustCompile(`--no-check-certificate\b`),
			regexp.MustCompile(`\bNODE_TLS_REJECT_UNAUTHORIZED["']?\s*[:=]\s*["']?0\b`),
			regexp.MustCompile(`\bGIT_SSL_NO_VERIFY["']?\s*[:=]\s*["']?(1|true)\b`),
			regexp.MustCompile(`(?i)\bPYTHONHTTPSVERIFY["']?\s*[:=]\s*["']?0\b`),
			regexp.MustCompile(`(?i)\bhttp\.sslVerify\s*(=\s*)?["']?false\b`),
			regexp.MustCompile(`\bverify\s*=\s*False\b`),
			regexp.MustCompile(`\bInsecureSkipVerify\s*:\s*true\b`),
			regexp.MustCompile(`\brejectUnauthorized\s*:\s*false\b`),
			regexp.MustCompile(`\bstrict-ssl\s*(=\s*|\s+)false\b`),
		},
	},
	{
		problem: "shell_startup",
		res: []*regexp.Regexp{
			regexp.MustCompile(`(>>?|\btee\s+(-a\s+|--append\s+)?)\s*["']?(~|\$HOME|\$\{HOME\})/\.(bashrc|bash_profile|bash_login|zshrc|zprofile|zshenv|profile|config/fish/config\.fish)\b`),
			regexp.MustCompile(`\bsed\s+-i\S*\s.*(~|\$HOME|\$\{HOME\})/\.(bashrc|bash_profile|zshrc|zprofile|zshenv|profile)\b`),
			regexp.MustCompile(`(?i)(\bAdd-Content\b[^|\n]*|>>?\s*)\$PROFILE\b`),
		},
	},
}

var (
	recursive = regexp.MustCompile(`(^|\s)(-[A-Za-z]*[rR][A-Za-z]*|--recursive)\b`)
	force     = regexp.MustCompile(`(^|\s)(-[A-Za-z]*f[A-Za-z]*|--force)\b`)
)

func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	var found []check.Finding
	err := textlines.Each(in.Files, func(file string, line int, text string) {
		for _, p := range patterns {
			if p.matches(text) {
				found = append(found, check.Finding{
					Rule:     id,
					Severity: check.MustFix,
					File:     file,
					Line:     line,
					Message:  "check." + id + "." + p.problem,
				})
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func (p pattern) matches(text string) bool {
	for _, re := range p.res {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if p.also == nil || p.also(m) {
				return true
			}
		}
	}
	return false
}
