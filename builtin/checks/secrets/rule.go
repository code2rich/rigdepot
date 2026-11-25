// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package secrets

import (
	"context"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/code2rich/rigdepot/builtin/checks/internal/textlines"
	"github.com/code2rich/rigdepot/ext/check"
)

const id = "secrets"

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.MustFix }

// Applies to every kind: a credential does not belong in any of them.
func (rule) Applies(string) bool { return true }

// format is a credential with a recognisable shape. prefix is the fixed, public part of the shape;
// it is the only part of a match a finding may show.
type format struct {
	kind   string
	prefix string
	re     *regexp.Regexp
}

var formats = []format{
	{"private_key", "-----BEGIN", regexp.MustCompile(`-----BEGIN ((RSA|DSA|EC|OPENSSH|PGP|ENCRYPTED) )?PRIVATE KEY( BLOCK)?-----`)},
	{"aws_access_key", "AKIA", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github_token", "gh*_", regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}\b|\bgithub_pat_[A-Za-z0-9_]{60,}`)},
	{"gitlab_token", "glpat-", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	{"slack_token", "xox*-", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"api_key", "sk-", regexp.MustCompile(`\bsk-(ant-|proj-)?[A-Za-z0-9_-]{20,}`)},
	{"google_api_key", "AIza", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"jwt", "eyJ", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
}

// assignment is a setting whose name ends in a word that says its value is a credential:
// PASSWORD=, db_password:, "api_key": and the like. The value is the second group.
var assignment = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(password|passwd|secret|api_?key|access_?key|private_?key|token))["']?\s*[:=]\s*["']?([A-Za-z0-9_+/=.@!%^&*~-]{4,})`)

// placeholders are values that stand for a credential without being one.
var placeholders = []string{
	"changeme", "change-me", "password", "passwd", "secret", "example", "placeholder", "none", "null",
	"true", "false", "required", "optional", "todo", "xxxx", "****",
}

// candidate is a long run of the characters credentials are made of. The slash of base64 is left
// out: with it, every long file path with a capital letter and a digit looks random.
var candidate = regexp.MustCompile(`[A-Za-z0-9+=_-]{32,}`)

// minEntropy is the Shannon entropy, in bits per character, above which a candidate counts as
// random. English words and identifiers stay well below; hexadecimal digests cannot exceed 4.
const minEntropy = 4.2

func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	var found []check.Finding
	add := func(file string, line int, problem string, params map[string]any) {
		found = append(found, check.Finding{
			Rule:     id,
			Severity: check.MustFix,
			File:     file,
			Line:     line,
			Message:  "check." + id + "." + problem,
			Params:   params,
		})
	}
	err := textlines.Each(in.Files, func(file string, line int, text string) {
		var taken [][2]int // spans already reported on this line
		for _, f := range formats {
			for _, m := range f.re.FindAllStringIndex(text, -1) {
				taken = append(taken, [2]int{m[0], m[1]})
				add(file, line, "token_like", map[string]any{"kind": f.kind, "masked": f.prefix + "…", "length": m[1] - m[0]})
			}
		}
		for _, m := range assignment.FindAllStringSubmatchIndex(text, -1) {
			if m[0] > 0 && strings.ContainsRune("{$:", rune(text[m[0]-1])) {
				continue // ${secret:NAME} is a reference, not a value
			}
			value := text[m[6]:m[7]]
			if overlaps(taken, m[6], m[7]) || isPlaceholder(value) {
				continue
			}
			taken = append(taken, [2]int{m[6], m[7]})
			add(file, line, "assignment", map[string]any{"key": text[m[2]:m[3]], "length": len(value)})
		}
		for _, m := range candidate.FindAllStringIndex(text, -1) {
			token := text[m[0]:m[1]]
			if overlaps(taken, m[0], m[1]) || !looksRandom(token) || isEncodedContent(text[:m[0]], token) {
				continue
			}
			add(file, line, "high_entropy", map[string]any{"length": len(token)})
		}
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func overlaps(spans [][2]int, start, end int) bool {
	for _, s := range spans {
		if start < s[1] && s[0] < end {
			return true
		}
	}
	return false
}

func isPlaceholder(v string) bool {
	lower := strings.ToLower(strings.Trim(v, ".-_"))
	if lower == "" || strings.HasPrefix(lower, "your") || strings.HasPrefix(lower, "my_") {
		return true
	}
	if strings.Trim(lower, "x*.") == "" {
		return true
	}
	for _, p := range placeholders {
		if lower == p {
			return true
		}
	}
	if strings.Trim(lower, "0123456789") == "" {
		return true // a number, such as a token limit
	}
	// Code that reads the value from the environment.
	return strings.Contains(lower, "env.") || strings.Contains(lower, "getenv") || strings.Contains(lower, "environ")
}

// looksRandom reports whether a token has upper- and lower-case letters and digits and the
// entropy of random text.
func looksRandom(token string) bool {
	var upper, lower, digit bool
	counts := map[rune]float64{}
	for _, r := range token {
		upper = upper || unicode.IsUpper(r)
		lower = lower || unicode.IsLower(r)
		digit = digit || unicode.IsDigit(r)
		counts[r]++
	}
	if !upper || !lower || !digit {
		return false
	}
	n := float64(len(token))
	entropy := 0.0
	for _, c := range counts {
		p := c / n
		entropy -= p * math.Log2(p)
	}
	return entropy >= minEntropy
}

// isEncodedContent reports whether a token is encoded content or an integrity hash, which look
// random and are not credentials: a data URI ("base64,…") or a hash such as "sha512-…".
func isEncodedContent(before, token string) bool {
	if strings.HasSuffix(before, "base64,") {
		return true
	}
	for _, marker := range []string{"sha256-", "sha384-", "sha512-"} {
		if strings.HasPrefix(token, marker) || strings.HasSuffix(before, marker) {
			return true
		}
	}
	return false
}
