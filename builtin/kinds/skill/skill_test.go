// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package skill

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"runtime"
	"slices"
	"testing"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/kind/kindtest"
	"github.com/code2rich/rigdepot/ext/resource"
)

func TestMain(m *testing.M) { kindtest.Main(m) }

func TestKind(t *testing.T) { kindtest.Run(t, New(), "testdata") }

// What a tool gets is the skill as it is in the repository: every file, byte for byte, with
// its executable bit, and nothing but meta.yaml left out. That holds for a SKILL.md with CRLF
// line endings and a byte order mark as well.
func TestRenderedSkillIsTheRepositoryByteForByte(t *testing.T) {
	for _, code := range []string{"unit-testing", "crlf-checkout"} {
		t.Run(code, func(t *testing.T) {
			dir := os.DirFS(path.Join("testdata", "valid", code))
			ref := resource.Ref{Kind: resource.KindSkill, Code: code, Version: 3}
			s, err := New().Parse(ref, dir)
			if err != nil {
				t.Fatal(err)
			}
			arts, err := New().Forms()[0].Render(s, adapter.Capabilities{Skills: true}, kind.RenderOptions{Prefix: "acme-"})
			if err != nil {
				t.Fatal(err)
			}
			sd := arts[0].(adapter.SkillDir)
			if sd.DirName != "acme-"+code || sd.Resource != ref || sd.Degraded != "" {
				t.Errorf("rendered %q for %v, degraded %q", sd.DirName, sd.Resource, sd.Degraded)
			}
			got := map[string]adapter.File{}
			for _, f := range sd.Files {
				got[f.Path] = f
			}
			var want []string
			err = fs.WalkDir(dir, ".", func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || p == "meta.yaml" {
					return err
				}
				want = append(want, p)
				data, err := fs.ReadFile(dir, p)
				if err != nil {
					return err
				}
				f, ok := got[p]
				if !ok {
					t.Errorf("%s is not delivered", p)
					return nil
				}
				if !bytes.Equal(f.Data, data) {
					t.Errorf("%s changed on the way:\n got %q\nwant %q", p, f.Data, data)
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				if exec := info.Mode().Perm()&0o111 != 0; runtime.GOOS != "windows" && f.Exec != exec {
					t.Errorf("%s: Exec is %v, the file's executable bit %v", p, f.Exec, exec)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := got["meta.yaml"]; ok {
				t.Error("meta.yaml is delivered; it is the product's, not the skill's")
			}
			paths := make([]string, 0, len(got))
			for p := range got {
				paths = append(paths, p)
			}
			slices.Sort(paths)
			slices.Sort(want)
			if !slices.Equal(paths, want) {
				t.Errorf("delivered %v, want %v", paths, want)
			}
		})
	}
	// The CRLF fixture really has CRLF and a byte order mark, or the test above proves nothing.
	data, err := os.ReadFile("testdata/valid/crlf-checkout/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\xef\xbb\xbf---\r\n")) {
		t.Errorf("testdata/valid/crlf-checkout/SKILL.md lost its byte order mark or CRLF: %q", data[:min(len(data), 12)])
	}
}
