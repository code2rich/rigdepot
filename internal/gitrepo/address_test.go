// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package gitrepo

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAddress(t *testing.T) {
	cases := []struct {
		raw        string
		allowLocal bool
		want       transport // "" means refused
	}{
		{"https://git.example.com/platform/resources.git", false, transportHTTPS},
		{"HTTPS://git.example.com/platform/resources.git", false, transportHTTPS},
		{"ssh://git@git.example.com/platform/resources.git", false, transportSSH},
		{"ssh://git@git.example.com:2222/platform/resources.git", false, transportSSH},
		{"git+ssh://git.example.com/platform/resources.git", false, transportSSH},
		{"git@git.example.com:platform/resources.git", false, transportSSH},
		{"git.example.com:platform/resources.git", false, transportSSH},

		// Transports that are not allowed.
		{"http://git.example.com/platform/resources.git", false, ""},
		{"git://git.example.com/platform/resources.git", false, ""},
		{"ftp://git.example.com/platform/resources.git", false, ""},
		{"ext::sh -c touch% /tmp/pwned", false, ""},
		{"ext::sh -c touch% /tmp/pwned", true, ""},
		{"fd::17", true, ""},
		{"https::git.example.com/resources.git", false, ""},
		{"persistent-https::git.example.com/resources.git", false, ""},

		// Option injection through the address or the ssh host.
		{"-uploadpack=touch /tmp/pwned", true, ""},
		{"--upload-pack=x", true, ""},
		{"ssh://-oProxyCommand=touch%20/tmp/pwned/x", false, ""},
		{"-oProxyCommand=x:path", false, ""},
		{"git@-oProxyCommand=x:path", false, ""},
		{"https://git.example.com/a\nb", false, ""},
		{"", true, ""},
		{"https:///resources.git", false, ""},

		// Local repositories only when allowed.
		{"file:///srv/example/resources.git", false, ""},
		{"file:///srv/example/resources.git", true, transportFile},
		{"/srv/example/resources.git", false, ""},
		{"/srv/example/resources.git", true, transportFile},
		{"./resources", true, transportFile},
		{"resources", true, transportFile},
		{"resources", false, ""},
	}
	for _, c := range cases {
		got, err := parseAddress(c.raw, c.allowLocal)
		switch {
		case c.want == "" && err == nil:
			t.Errorf("parseAddress(%q, %v) accepted it as %s, want refused", c.raw, c.allowLocal, got.transport)
		case c.want == "" && !errors.Is(err, ErrAddress):
			t.Errorf("parseAddress(%q, %v) = %v, want ErrAddress", c.raw, c.allowLocal, err)
		case c.want != "" && err != nil:
			t.Errorf("parseAddress(%q, %v) = %v, want %s", c.raw, c.allowLocal, err, c.want)
		case c.want != "" && got.transport != c.want:
			t.Errorf("parseAddress(%q, %v) = %s, want %s", c.raw, c.allowLocal, got.transport, c.want)
		}
	}
}

func TestPasswordsStayOutOfMessages(t *testing.T) {
	const password = "placeholder-token"
	for _, raw := range []string{
		"http://user:" + password + "@git.example.com/resources.git",
		"https://user:" + password + "@git.example.com/a\nb",
		"https://user:" + password + "@git.example.com:bad-port/x",
	} {
		_, err := parseAddress(raw, false)
		if err == nil {
			t.Fatalf("%q accepted", raw)
		}
		if strings.Contains(err.Error(), password) {
			t.Errorf("the message shows the password: %v", err)
		}
	}
	addr, err := parseAddress("https://user:"+password+"@git.example.com/resources.git", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(addr.shown, password) || !strings.Contains(addr.shown, "git.example.com/resources.git") {
		t.Errorf("shown = %q, want the address without the password", addr.shown)
	}
}

func TestEnvironmentForbidsPrompts(t *testing.T) {
	t.Setenv("GIT_ASKPASS", "/usr/bin/example-askpass")
	t.Setenv("GIT_DIR", "/srv/example/.git")
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext:https:ssh")
	env := environment(transportHTTPS)
	values := map[string][]string{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		values[name] = append(values[name], value)
	}
	want := map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_ASKPASS":         "",
		"SSH_ASKPASS":         "",
		"SSH_ASKPASS_REQUIRE": "never",
		"GCM_INTERACTIVE":     "never",
		"GIT_ALLOW_PROTOCOL":  "https",
	}
	for name, value := range want {
		if got := values[name]; len(got) != 1 || got[0] != value {
			t.Errorf("%s = %q, want exactly [%q]", name, got, value)
		}
	}
	if got := values["GIT_DIR"]; len(got) != 0 {
		t.Errorf("GIT_DIR = %q, want it removed", got)
	}
}
