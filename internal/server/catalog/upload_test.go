// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package catalog_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/catalog"
)

type member struct {
	name, data string
	link       bool // a symbolic link to data
}

func tarOf(t *testing.T, ms []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range ms {
		h := &tar.Header{Name: m.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(m.data))}
		if m.link {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, m.data, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if !m.link {
			tw.Write([]byte(m.data))
		}
	}
	tw.Close()
	return buf.Bytes()
}

func gzipOf(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, ms []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range ms {
		w, err := zw.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(m.data))
	}
	zw.Close()
	return buf.Bytes()
}

func skillMembers(prefix string) []member {
	return []member{
		{name: prefix + "SKILL.md", data: "---\nname: unit-testing\ndescription: Writes unit tests.\n---\nSteps.\npassword=hunter2hunter2\n"},
		{name: prefix + "meta.yaml", data: "name: Unit testing\nwhen: When a change needs tests\n"},
		{name: prefix + "references/checklist.md", data: "- edge cases\n"},
	}
}

func TestUploadFormats(t *testing.T) {
	for name, data := range map[string]func(t *testing.T) []byte{
		"zip of a folder": func(t *testing.T) []byte { return zipOf(t, skillMembers("unit-testing/")) },
		"tar":             func(t *testing.T) []byte { return tarOf(t, skillMembers("")) },
		"tar.gz":          func(t *testing.T) []byte { return gzipOf(tarOf(t, skillMembers("./"))) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSQLite(t)
			ctx := context.Background()
			res, err := f.svc.Upload(ctx, f.as("admin"), "unit-testing", "skill", data(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{}
			for _, x := range res.Draft.Files {
				paths = append(paths, x.Path)
			}
			if !res.Created || !slices.Equal(paths, []string{"SKILL.md", "meta.yaml", "references/checklist.md"}) || res.Draft.Checks.Counts().Blocking != 1 {
				t.Fatalf("upload: %+v %v", res, paths)
			}
			r, _ := f.svc.Get(ctx, f.as("admin"), "unit-testing")
			if r.Origin != catalog.OriginUpload || r.Status != catalog.StatusDraft {
				t.Errorf("resource: %+v", r)
			}
			_, err = f.svc.Upload(ctx, f.as("admin"), "unit-testing", "", data(t), nil)
			mustErr(t, err, catalog.ErrPreconditionRequired)
			stale := `"sha256:0000"`
			_, err = f.svc.Upload(ctx, f.as("admin"), "unit-testing", "", data(t), &stale)
			mustErr(t, err, catalog.ErrPreconditionFailed)
			etag := res.Draft.ETag()
			again, err := f.svc.Upload(ctx, f.as("admin"), "unit-testing", "", data(t), &etag)
			if err != nil || again.Created || again.Draft.Digest != res.Draft.Digest {
				t.Errorf("again: %v %+v", err, again)
			}
		})
	}
}

// An upload is checked exactly like an import of the same files: same digest, same findings.
func TestUploadAndImportAgree(t *testing.T) {
	ctx := context.Background()
	imported := newSQLite(t)
	files := map[string]string{}
	for _, m := range skillMembers("skills/unit-testing/") {
		files[m.name] = m.data
	}
	imported.importRepo(files, "c1")
	want, err := imported.svc.Draft(ctx, imported.as("admin"), "unit-testing")
	if err != nil {
		t.Fatal(err)
	}
	uploaded := newSQLite(t)
	got, err := uploaded.svc.Upload(ctx, uploaded.as("admin"), "unit-testing", "skill", zipOf(t, skillMembers("")), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(fs []catalog.Finding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return out
	}
	if got.Draft.Digest != want.Digest || !slices.Equal(ids(got.Draft.Checks.Findings), ids(want.Checks.Findings)) || len(want.Checks.Findings) == 0 {
		t.Errorf("upload %s %v, import %s %v", got.Draft.Digest, ids(got.Draft.Checks.Findings), want.Digest, ids(want.Checks.Findings))
	}
}

func TestUploadRefusals(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	big := make([]byte, 6<<20)
	for name, c := range map[string]struct {
		kind string
		data []byte
		want error
	}{
		"not an archive":      {"skill", []byte("just text"), catalog.ErrUnknownFormat},
		"parent directory":    {"skill", zipOf(t, append(skillMembers(""), member{name: "../evil.md", data: "x"})), catalog.ErrUnsafePath},
		"absolute":            {"skill", tarOf(t, append(skillMembers(""), member{name: "/etc/evil", data: "x"})), catalog.ErrUnsafePath},
		"symbolic link":       {"skill", tarOf(t, append(skillMembers(""), member{name: "passwd", data: "/etc/passwd", link: true})), catalog.ErrUnsafePath},
		"unpacks too large":   {"skill", gzipOf(tarOf(t, append(skillMembers(""), member{name: "zeros.bin", data: string(big)}))), catalog.ErrUnsafePath},
		"no metadata":         {"skill", zipOf(t, skillMembers("")[:1]), catalog.ErrInvalid},
		"a one-document kind": {"mcp", zipOf(t, skillMembers("")), catalog.ErrInvalid},
		"unknown kind":        {"widget", zipOf(t, skillMembers("")), catalog.ErrInvalid},
	} {
		_, err := f.svc.Upload(ctx, f.as("admin"), "unit-testing", c.kind, c.data, nil)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := f.svc.Get(ctx, f.as("admin"), "unit-testing"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("a refused upload left a resource: %v", err)
	}
	// A resource from a repository is changed in the repository.
	files := map[string]string{}
	for _, m := range skillMembers("skills/unit-testing/") {
		files[m.name] = m.data
	}
	f.importRepo(files, "c1")
	d, _ := f.svc.Draft(ctx, f.as("admin"), "unit-testing")
	etag := d.ETag()
	_, err := f.svc.Upload(ctx, f.as("admin"), "unit-testing", "skill", zipOf(t, skillMembers("")), &etag)
	mustErr(t, err, catalog.ErrFromRepository)
}

func TestRegisterAnMCPServerOnTheWeb(t *testing.T) {
	f := newSQLite(t)
	ctx := context.Background()
	meta := resource.Meta{Name: "Jira", When: "When issues are involved", Scope: "shop"}
	plain := map[string]any{"server": map[string]any{"type": "http", "url": "https://jira.example.com/mcp",
		"headers": map[string]any{"Authorization": "password=hunter2hunter2"}}}
	_, err := f.svc.PutDefinition(ctx, f.as("owner"), "jira", "mcp", meta, plain, nil)
	mustErr(t, err, catalog.ErrPlaintextSecret)
	if _, err := f.svc.Get(ctx, f.as("admin"), "jira"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("a plain-text secret was stored: %v", err)
	}
	def := map[string]any{"server": map[string]any{"type": "http", "url": "https://jira.example.com/mcp",
		"headers": map[string]any{"Authorization": "Bearer ${secret:JIRA_TOKEN}"}}, "delivery": "native"}
	_, err = f.svc.PutDefinition(ctx, f.as("member"), "jira", "mcp", meta, def, nil)
	mustErr(t, err, catalog.ErrDenied) // members of the product do not create its resources
	res, err := f.svc.PutDefinition(ctx, f.as("owner"), "jira", "mcp", meta, def, nil)
	if err != nil || !res.Created || !slices.Contains(res.Draft.Meta.Secrets, "JIRA_TOKEN") || res.Draft.Meta.Delivery != catalog.DeliveryNative {
		t.Fatalf("register: %v %+v", err, res)
	}
	got, _, err := f.svc.Definition(ctx, "mcp", "jira", res.Draft.Bundle)
	if err != nil || got["delivery"] != "native" || got["meta"] != nil {
		t.Errorf("definition: %v %+v", err, got)
	}
	r, _ := f.svc.Get(ctx, f.as("owner"), "jira")
	if r.Origin != catalog.OriginWeb || r.Scope != "shop" || r.ProductID == "" {
		t.Errorf("resource: %+v", r)
	}
	meta.Summary = "Reads and writes issues."
	_, err = f.svc.PutDefinition(ctx, f.as("owner"), "jira", "mcp", meta, def, nil)
	mustErr(t, err, catalog.ErrPreconditionRequired)
	etag := res.Draft.ETag()
	res2, err := f.svc.PutDefinition(ctx, f.as("owner"), "jira", "mcp", meta, def, &etag)
	if err != nil || res2.Created || res2.Draft.Digest == res.Draft.Digest {
		t.Fatalf("replace: %v %+v", err, res2)
	}
	_, err = f.svc.PutDefinition(ctx, f.as("owner"), "jira", "skill", meta, def, &etag)
	mustErr(t, err, catalog.ErrInvalid) // a skill is not one document
}
