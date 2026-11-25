// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// transport is how git reaches a repository, as named in GIT_ALLOW_PROTOCOL.
type transport string

const (
	transportHTTPS transport = "https"
	transportSSH   transport = "ssh"
	transportFile  transport = "file"
)

// address is a repository address that passed the checks.
type address struct {
	// arg is what is passed to git: the address as given, or the absolute path for a local
	// path.
	arg string
	// shown is the address as it may appear in messages, without a password.
	shown     string
	transport transport
}

// parseAddress checks a repository address and tells how git will reach it. Only https and ssh
// are accepted, plus local paths and file:// when allowLocal is set.
func parseAddress(raw string, allowLocal bool) (address, error) {
	refuse := func(why string) (address, error) {
		return address{}, fmt.Errorf("%w: %q: %s", ErrAddress, redact(raw), why)
	}
	if raw == "" {
		return refuse("the address is empty")
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return refuse("the address contains control characters")
	}
	if strings.HasPrefix(raw, "-") {
		return refuse("an address cannot start with a hyphen")
	}
	// "<transport>::<address>" makes git run a remote helper of that name; ext:: runs an
	// arbitrary command. None of them is allowed.
	if i := strings.Index(raw, "::"); i > 0 && isSchemeName(raw[:i]) {
		return refuse("remote helper addresses (" + raw[:i] + "::) are not allowed")
	}

	if scheme, _, ok := strings.Cut(raw, "://"); ok && isSchemeName(scheme) {
		u, err := url.Parse(raw)
		if err != nil {
			return refuse("not a valid URL")
		}
		switch strings.ToLower(scheme) {
		case "https":
			if u.Host == "" {
				return refuse("the URL has no host")
			}
			return address{arg: raw, shown: redact(raw), transport: transportHTTPS}, nil
		case "ssh", "git+ssh", "ssh+git":
			if u.Host == "" || strings.HasPrefix(u.Host, "-") {
				return refuse("the URL has no usable host")
			}
			return address{arg: raw, shown: redact(raw), transport: transportSSH}, nil
		case "file":
			if !allowLocal {
				return refuse("local repositories are only read when given explicitly")
			}
			return address{arg: raw, shown: raw, transport: transportFile}, nil
		default:
			return refuse("only https and ssh addresses are allowed")
		}
	}

	if host, ok := scpHost(raw); ok {
		if host == "" || strings.HasPrefix(host, "-") {
			return refuse("the address has no usable host")
		}
		return address{arg: raw, shown: raw, transport: transportSSH}, nil
	}

	if !allowLocal {
		return refuse("not an https or ssh address; local repositories are only read when given explicitly")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return refuse(err.Error())
	}
	return address{arg: abs, shown: abs, transport: transportFile}, nil
}

// isSchemeName reports whether s can be a URL scheme or a remote helper name.
func isSchemeName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.'):
		default:
			return false
		}
	}
	return true
}

// scpHost recognises the scp-like ssh syntax "[user@]host:path" the way git does: a colon that
// comes before the first slash. On Windows a single letter before the colon is a drive.
func scpHost(raw string) (string, bool) {
	colon := strings.Index(raw, ":")
	if colon < 0 {
		return "", false
	}
	if slash := strings.Index(raw, "/"); slash >= 0 && slash < colon {
		return "", false
	}
	if runtime.GOOS == "windows" {
		if colon == 1 || strings.Contains(raw[:colon], `\`) {
			return "", false
		}
	}
	host := raw[:colon]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return host, true
}

// redact removes a password from an address that is a URL, for messages. It works on the text,
// so that an address too malformed to parse does not leak one either.
func redact(raw string) string {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	at := strings.LastIndex(rest[:end], "@")
	if at < 0 {
		return raw
	}
	user, _, hasPassword := strings.Cut(rest[:at], ":")
	if !hasPassword {
		return raw
	}
	return scheme + "://" + user + ":xxxxx" + rest[at:]
}
