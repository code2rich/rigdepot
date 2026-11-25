// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testMIT = "MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\nof this software and associated documentation files (the \"Software\")\n"

// fakeWeb lays out web/ the way pnpm does: each package in node_modules/.pnpm/<id>/node_modules/
// next to links to its own dependencies, and links to the direct dependencies in node_modules/.
func fakeWeb(t *testing.T, license map[string]string) string {
	t.Helper()
	root := t.TempDir()
	web := filepath.Join(root, webDir)
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
	}
	store := filepath.Join(web, "node_modules", ".pnpm")
	pkg := func(id, name, version, deps string) string {
		dir := filepath.Join(store, id, "node_modules", filepath.FromSlash(name))
		write(filepath.Join(dir, "package.json"), `{"name":"`+name+`","version":"`+version+`"`+deps+`}`)
		text, ok := license[name]
		if !ok {
			text = testMIT
		}
		if text != "" {
			write(filepath.Join(dir, "LICENSE"), text)
		}
		return dir
	}
	write(filepath.Join(web, "package.json"), `{"dependencies":{"app-lib":"1.0.0","@scope/ui":"2.0.0"},"devDependencies":{"build-tool":"3.0.0"}}`)
	lib := pkg("app-lib@1.0.0", "app-lib", "1.0.0", `,"dependencies":{"helper":"^4"},"optionalDependencies":{"not-installed":"1"}`)
	helper := pkg("helper@4.1.0", "helper", "4.1.0", "")
	ui := pkg("@scope+ui@2.0.0", "@scope/ui", "2.0.0", `,"peerDependencies":{"app-lib":"1","absent-peer":"1"}`)
	tool := pkg("build-tool@3.0.0", "build-tool", "3.0.0", "")
	link(helper, filepath.Join(store, "app-lib@1.0.0", "node_modules", "helper"))
	link(lib, filepath.Join(store, "@scope+ui@2.0.0", "node_modules", "app-lib"))
	link(lib, filepath.Join(web, "node_modules", "app-lib"))
	link(ui, filepath.Join(web, "node_modules", "@scope", "ui"))
	link(tool, filepath.Join(web, "node_modules", "build-tool"))
	write(filepath.Join(web, filepath.FromSlash(webFontsDir), "OFL-Example.txt"),
		"SIL OPEN FONT LICENSE Version 1.1\n\nPermission is hereby granted, free of charge, to any person obtaining\na copy of the Font Software\n")
	return root
}

func TestWebPackagesFollowProductionDependencies(t *testing.T) {
	root := fakeWeb(t, nil)
	pkgs, err := webPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pkgs {
		got = append(got, p.name+"@"+p.version)
	}
	// Not the development dependency; not the optional or peer dependencies that are not
	// installed; the peer dependency that is, once.
	if want := "@scope/ui@2.0.0 app-lib@1.0.0 helper@4.1.0"; strings.Join(got, " ") != want {
		t.Errorf("packages = %v, want %s", got, want)
	}

	notices, err := webNotices(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\n@scope/ui 2.0.0\nLicense: MIT\n", "\nhelper 4.1.0\nLicense: MIT\n", "\nFont Example\nLicense: OFL-1.1\n", "--- LICENSE\n\nMIT License"} {
		if !strings.Contains(string(notices), want) {
			t.Errorf("notices lack %q:\n%s", want, notices)
		}
	}
	if strings.Contains(string(notices), root) {
		t.Errorf("the notices name a path of this machine:\n%s", notices)
	}
}

func TestWebNoticesRefuseWhatIsNotAllowed(t *testing.T) {
	for name, tt := range map[string]struct {
		license map[string]string
		want    string
	}{
		"GPL":          {map[string]string{"helper": "GNU GENERAL PUBLIC LICENSE\nVersion 3"}, "helper: LICENSE is GPL, which is not allowed"},
		"unrecognised": {map[string]string{"app-lib": "All rights reserved."}, "app-lib: the license could not be recognised"},
		"no file":      {map[string]string{"@scope/ui": ""}, "@scope/ui: no license file"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := webNotices(fakeWeb(t, tt.license))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWebPackagesNeedTheInstalledDependencies(t *testing.T) {
	root := t.TempDir()
	if _, err := webPackages(root); err == nil || !strings.Contains(err.Error(), "web-deps") {
		t.Errorf("without node_modules: %v", err)
	}
}
