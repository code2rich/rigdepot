// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Credential is a read-only token for an https repository, sent as HTTP basic authentication.
//
// It is handed to git through its environment (GIT_CONFIG_COUNT and friends, git 2.31 or
// later) as an extra header scoped to the repository's host, never on the command line where
// other users of the computer could read it, and never stored in the cache repository.
type Credential struct {
	// Username goes with the token. Most hosts ignore it for tokens; empty means the address's
	// own user name ("https://oauth2@git.example.com/…") or else "oauth2".
	Username string
	// Password is the token.
	Password string
}

// defaultUsername is the user name sent with a token when the address names none. GitLab
// documents it for tokens; GitHub and most others ignore the name.
const defaultUsername = "oauth2"

// CheckAddress reports whether raw is an address a Fetcher with allowLocal would accept, and
// returns its transport: "https", "ssh" or "file". The error wraps ErrAddress.
func CheckAddress(raw string, allowLocal bool) (string, error) {
	addr, err := parseAddress(raw, allowLocal)
	if err != nil {
		return "", err
	}
	return string(addr.transport), nil
}

// authConfig returns the git configuration that sends c to the host of addr.
func authConfig(addr address, c *Credential) ([][2]string, error) {
	if c == nil {
		return nil, nil
	}
	if addr.transport != transportHTTPS {
		return nil, fmt.Errorf("%w: %q: a token can only be sent to an https address", ErrAddress, addr.shown)
	}
	if c.Password == "" || strings.ContainsAny(c.Password+c.Username, "\r\n\x00") || strings.Contains(c.Username, ":") {
		return nil, fmt.Errorf("%w: %q: the credential is empty or holds characters a header cannot carry", ErrAddress, addr.shown)
	}
	u, err := url.Parse(addr.arg)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: not a valid URL", ErrAddress, addr.shown)
	}
	user := c.Username
	if user == "" && u.User != nil {
		user = u.User.Username()
	}
	if user == "" {
		user = defaultUsername
	}
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + c.Password))
	// Scoped to the scheme and host, so that nothing else git talks to gets it.
	scope := "https://" + u.Host + "/"
	return [][2]string{{"http." + scope + ".extraHeader", "Authorization: Basic " + basic}}, nil
}

// withConfig adds configuration entries to an environment through GIT_CONFIG_COUNT, after the
// ones the environment already carries.
func withConfig(env []string, entries [][2]string) []string {
	if len(entries) == 0 {
		return env
	}
	n := 0
	out := env[:0:0]
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if strings.EqualFold(name, "GIT_CONFIG_COUNT") {
			if v, err := strconv.Atoi(value); err == nil && v > 0 {
				n = v
			}
			continue
		}
		out = append(out, kv)
	}
	for i, e := range entries {
		idx := strconv.Itoa(n + i)
		out = append(out, "GIT_CONFIG_KEY_"+idx+"="+e[0], "GIT_CONFIG_VALUE_"+idx+"="+e[1])
	}
	return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(n+len(entries)))
}
