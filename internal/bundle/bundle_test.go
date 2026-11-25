// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundle

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// skillFiles is a small skill as an author writes it: a SKILL.md, a meta.yaml, references and
// an executable script. "a-b.md" and "a/b.md" sort differently as whole paths than in a
// directory walk, which is the trap the digest has to avoid.
var skillFiles = map[string]string{
	"SKILL.md":              "---\nname: unit-testing\ndescription: Write unit tests for a change\n---\n\n# Unit testing\n",
	"meta.yaml":             "name: 单元测试\nstages: [test]\n",
	".env.example":          "# Where the report goes\nTEST_REPORT_DIR=./build/test-report\n",
	"references/a-b.md":     "dash\n",
	"references/a/b.md":     "slash\n",
	"references/规范.md":      "中文文件名\n",
	"scripts/run-tests.sh":  "#!/bin/sh\nexec ./gradlew test -q\n",
	"scripts/README":        "",
	"z/deep/er/than/x.json": "{}\n",
}

// writeTree writes files into a new directory under the test's temporary directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mustDigest(t *testing.T, fsys fs.FS, exclude ...string) string {
	t.Helper()
	d, err := Digest(fsys, exclude...)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Valid() {
		t.Fatalf("Digest returned %q, which is not a well-formed digest", d)
	}
	return string(d)
}

// The digest is defined as what one shell line prints, so that anyone can recompute it. This
// runs that line from the format documentation and compares.
func TestDigestMatchesTheShellCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the documented command needs a POSIX shell, find, sort, sed, xargs and sha256sum")
	}
	for _, tool := range []string{"sh", "find", "sort", "sed", "xargs", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("the documented command needs %s, which is not on PATH: %v", tool, err)
		}
	}
	dir := writeTree(t, skillFiles)
	const line = `find . -type f | LC_ALL=C sort | sed 's|^\./||' | xargs sha256sum | sha256sum`
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	want, _, _ := strings.Cut(string(out), " ")
	if got := mustDigest(t, os.DirFS(dir)); got != "sha256:"+want {
		t.Errorf("Digest = %s, the shell command prints %s", got, want)
	}
}

func TestDigestCountsOnlyPathsAndContents(t *testing.T) {
	a := writeTree(t, skillFiles)
	b := writeTree(t, skillFiles)
	// Modes and times differ; the digest must not.
	if err := os.Chmod(filepath.Join(b, "scripts", "run-tests.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(b, "SKILL.md"), old, old); err != nil {
		t.Fatal(err)
	}
	da, db := mustDigest(t, os.DirFS(a)), mustDigest(t, os.DirFS(b))
	if da != db {
		t.Errorf("a different mode or time changed the digest: %s != %s", da, db)
	}

	// Our own marker does not count; an excluded directory does not count.
	if err := os.WriteFile(filepath.Join(b, MarkerFile), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDigest(t, os.DirFS(b)); got != da {
		t.Errorf("the marker file %s changed the digest", MarkerFile)
	}
	if err := os.MkdirAll(filepath.Join(b, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "memory", "notes.md"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDigest(t, os.DirFS(b), "memory"); got != da {
		t.Error("an excluded directory changed the digest")
	}
	if got := mustDigest(t, os.DirFS(b)); got == da {
		t.Error("a file that is not excluded did not change the digest")
	}

	// Content and path both count.
	c := writeTree(t, skillFiles)
	if err := os.WriteFile(filepath.Join(c, "SKILL.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if mustDigest(t, os.DirFS(c)) == da {
		t.Error("changing a file's content did not change the digest")
	}
	moved := map[string]string{}
	for p, body := range skillFiles {
		moved[strings.Replace(p, "references/", "refs/", 1)] = body
	}
	if mustDigest(t, os.DirFS(writeTree(t, moved))) == da {
		t.Error("moving files did not change the digest")
	}
}

func TestDigestRefusesWhatItCannotDigest(t *testing.T) {
	if _, err := Digest(os.DirFS(t.TempDir())); !errors.Is(err, ErrEmpty) {
		t.Errorf("Digest of an empty directory: %v, want ErrEmpty", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	dir := writeTree(t, map[string]string{"SKILL.md": "x"})
	if err := os.Symlink("SKILL.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Digest(os.DirFS(dir)); err == nil {
		t.Error("Digest skipped a symbolic link instead of refusing it")
	}
}

func TestPackIsDeterministic(t *testing.T) {
	a := writeTree(t, skillFiles)
	b := writeTree(t, skillFiles)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(b, "SKILL.md"), old, old); err != nil {
		t.Fatal(err)
	}
	pa, err := Pack(os.DirFS(a))
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Pack(os.DirFS(b))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pa.Data, pb.Data) || pa.Address != pb.Address {
		t.Fatal("the same files packed into different bytes")
	}
	if !strings.HasPrefix(pa.Address, "sha256:") || len(pa.Address) != len("sha256:")+64 {
		t.Errorf("Address = %q", pa.Address)
	}

	// Entries in byte order, parents first, nothing of the machine's state in the headers.
	tr := tar.NewReader(bytes.NewReader(pa.Data))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if !h.ModTime.Equal(time.Unix(0, 0)) || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: header carries time %v, owner %d:%d %q:%q", h.Name, h.ModTime, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			t.Errorf("%s: type %q", h.Name, h.Typeflag)
		}
		want := int64(0o644)
		if h.Typeflag == tar.TypeDir || h.Name == "scripts/run-tests.sh" {
			want = 0o755
		}
		if runtime.GOOS != "windows" && h.Mode != want {
			t.Errorf("%s: mode %o, want %o", h.Name, h.Mode, want)
		}
	}
	for i, n := range names {
		if dir := path.Dir(strings.TrimSuffix(n, "/")); dir != "." && !slices.Contains(names[:i], dir+"/") {
			t.Errorf("%s comes before its directory %s/", n, dir)
		}
	}
	files := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return strings.HasSuffix(n, "/") })
	if !slices.IsSorted(files) {
		t.Errorf("files are not in byte order: %v", files)
	}

	// Unpacking gives back the same files and the same digest.
	got, err := Unpack(bytes.NewReader(pa.Data))
	if err != nil {
		t.Fatal(err)
	}
	if mustDigest(t, got) != mustDigest(t, os.DirFS(a)) {
		t.Error("the unpacked files have another digest")
	}
	for p, body := range skillFiles {
		data, err := fs.ReadFile(got, p)
		if err != nil || string(data) != body {
			t.Errorf("%s after unpacking: %q, %v", p, data, err)
		}
	}
	if fi, err := fs.Stat(got, "scripts/run-tests.sh"); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755) {
		t.Errorf("the executable bit did not survive: %v %v", fi.Mode(), err)
	}
	again, err := Pack(got)
	if err != nil || !bytes.Equal(again.Data, pa.Data) {
		t.Errorf("packing the unpacked files gave other bytes (%v)", err)
	}
}

// tarEntry is one entry of a hand-made archive.
type tarEntry struct {
	name string
	typ  byte
	body string
	size int64 // for large bodies; written as zero bytes
}

func makeTar(t testing.TB, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Format: tar.FormatPAX}
		switch typ {
		case tar.TypeXGlobalHeader:
			h = &tar.Header{Typeflag: typ, PAXRecords: map[string]string{"comment": "0123456789abcdef"}}
		case tar.TypeReg:
			h.Size = int64(len(e.body)) + e.size
		case tar.TypeSymlink, tar.TypeLink:
			h.Linkname = "SKILL.md"
		case tar.TypeDir:
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
			if _, err := io.CopyN(tw, zeros{}, e.size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type zeros struct{}

func (zeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }

var skillMD = tarEntry{name: "SKILL.md", body: "---\nname: x\n---\n"}

func many(n int) []tarEntry {
	out := []tarEntry{skillMD}
	for i := range n {
		out = append(out, tarEntry{name: "refs/" + strings.Repeat("r", i%7+1) + "-" + string(rune('a'+i%26)) + "-" + itoa(i) + ".md", body: "x"})
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }

func fsTest(fsys fs.FS, want ...string) error { return fstest.TestFS(fsys, want...) }

func fstestMap(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for p, body := range files {
		m[p] = &fstest.MapFile{Data: []byte(body), Mode: 0o644}
	}
	return m
}

// unsafeFixtures has, for every row of the path safety table in the format documentation, at
// least one archive that has to be refused, and the rule it breaks.
var unsafeFixtures = []struct {
	name    string
	rule    Rule
	entries []tarEntry
}{
	// Row 1: only relative paths; no "..", no leading "/", no "\" and no drive letter.
	{"parent", RuleNotRelative, []tarEntry{skillMD, {name: "../outside.md", body: "x"}}},
	{"parent-inside", RuleNotRelative, []tarEntry{skillMD, {name: "refs/../../outside.md", body: "x"}}},
	{"absolute", RuleNotRelative, []tarEntry{skillMD, {name: "/etc/passwd", body: "x"}}},
	{"backslash", RuleNotRelative, []tarEntry{skillMD, {name: `refs\..\..\outside.md`, body: "x"}}},
	{"drive", RuleNotRelative, []tarEntry{skillMD, {name: "C:/Windows/x.md", body: "x"}}},
	// Row 2: no symbolic links, hard links or device files.
	{"symlink", RuleLink, []tarEntry{skillMD, {name: "link.md", typ: tar.TypeSymlink}}},
	{"hardlink", RuleLink, []tarEntry{skillMD, {name: "hard.md", typ: tar.TypeLink}}},
	{"device", RuleSpecial, []tarEntry{skillMD, {name: "tty", typ: tar.TypeChar}}},
	{"fifo", RuleSpecial, []tarEntry{skillMD, {name: "pipe", typ: tar.TypeFifo}}},
	// Row 3: Windows reserved names, trailing dot or space, < > : " | ? * and control characters.
	{"reserved", RuleWindowsName, []tarEntry{skillMD, {name: "refs/con.md", body: "x"}}},
	{"reserved-dir", RuleWindowsName, []tarEntry{skillMD, {name: "COM1/x.md", body: "x"}}},
	{"trailing-dot", RuleWindowsName, []tarEntry{skillMD, {name: "notes.", body: "x"}}},
	{"trailing-space", RuleWindowsName, []tarEntry{skillMD, {name: "notes ", body: "x"}}},
	{"colon", RuleWindowsName, []tarEntry{skillMD, {name: "refs/a:b.md", body: "x"}}},
	{"question", RuleWindowsName, []tarEntry{skillMD, {name: "why?.md", body: "x"}}},
	{"control", RuleWindowsName, []tarEntry{skillMD, {name: "a\tb.md", body: "x"}}},
	// Row 4: two names in one directory that differ only in case.
	{"case", RuleCaseCollision, []tarEntry{skillMD, {name: "skill.md", body: "x"}}},
	{"case-dir", RuleCaseCollision, []tarEntry{skillMD, {name: "Refs/a.md", body: "x"}, {name: "refs/b.md", body: "x"}}},
	// Row 5: at most 180 bytes and 8 levels.
	{"too-long", RuleTooLong, []tarEntry{skillMD, {name: strings.Repeat("a", MaxPathBytes-2) + ".md", body: "x"}}},
	{"too-deep", RuleTooDeep, []tarEntry{skillMD, {name: "1/2/3/4/5/6/7/8/9.md", body: "x"}}},
	// Row 6: at most 200 files and 5 MB in all.
	{"too-many-files", RuleTooManyFiles, many(MaxFiles)},
	{"too-large", RuleTooLarge, []tarEntry{skillMD, {name: "big.bin", size: MaxBytes}}},
}

func TestUnpackRefusesEveryUnsafeFixture(t *testing.T) {
	for _, f := range unsafeFixtures {
		t.Run(f.name, func(t *testing.T) {
			got, err := Unpack(bytes.NewReader(makeTar(t, f.entries...)))
			var unsafe *UnsafeError
			if !errors.As(err, &unsafe) {
				t.Fatalf("Unpack = %v, %v; want an *UnsafeError", got, err)
			}
			if !unsafe.Has(f.rule) {
				t.Errorf("refused for %v, want %s", unsafe, f.rule)
			}
		})
	}
}

// The limits are on the far side of the boundary: exactly at the limit is allowed.
func TestUnpackAcceptsWhatIsJustInsideTheLimits(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"files":  many(MaxFiles - 1),
		"bytes":  {{name: "SKILL.md", size: MaxBytes}},
		"length": {skillMD, {name: strings.Repeat("a", MaxPathBytes-3) + ".md", body: "x"}},
		"depth":  {skillMD, {name: "1/2/3/4/5/6/7/8.md", body: "x"}},
		"names":  {skillMD, {name: "refs/console.md", body: "x"}, {name: "refs/com10.md", body: "x"}, {name: "refs/规范.md", body: "x"}, {name: "refs/.hidden", body: "x"}},
		"dirs":   {{name: "refs/", typ: tar.TypeDir}, {name: "refs/a.md", body: "x"}, skillMD},
	} {
		if _, err := Unpack(bytes.NewReader(makeTar(t, entries...))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUnpackRefusesTheSamePathTwice(t *testing.T) {
	_, err := Unpack(bytes.NewReader(makeTar(t, skillMD, skillMD)))
	if err == nil {
		t.Fatal("an archive with SKILL.md twice was unpacked")
	}
}

// Every rule applies to files on disk too, as far as a file system can hold them.
func TestSnapshotRefusesUnsafeFilesOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links and these names need a Unix file system")
	}
	cases := map[string]struct {
		rule  Rule
		setup func(dir string) error
	}{
		"symlink": {RuleLink, func(dir string) error { return os.Symlink("/etc", filepath.Join(dir, "etc")) }},
		"hardlink": {RuleLink, func(dir string) error {
			return os.Link(filepath.Join(dir, "SKILL.md"), filepath.Join(dir, "again.md"))
		}},
		"backslash": {RuleNotRelative, func(dir string) error { return os.WriteFile(filepath.Join(dir, `a\b.md`), nil, 0o644) }},
		"reserved":  {RuleWindowsName, func(dir string) error { return os.WriteFile(filepath.Join(dir, "aux.txt"), nil, 0o644) }},
		"too-large": {RuleTooLarge, func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "big.bin"), make([]byte, MaxBytes), 0o644)
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{"SKILL.md": "x"})
			if err := c.setup(dir); err != nil {
				t.Fatal(err)
			}
			_, err := Snapshot(os.DirFS(dir))
			var unsafe *UnsafeError
			if !errors.As(err, &unsafe) || !unsafe.Has(c.rule) {
				t.Errorf("Snapshot = %v, want %s", err, c.rule)
			}
		})
	}
	if runtime.GOOS == "darwin" {
		return // the default file system there would not hold both names
	}
	dir := writeTree(t, map[string]string{"SKILL.md": "x", "skill.md": "y"})
	var unsafe *UnsafeError
	if _, err := Snapshot(os.DirFS(dir)); !errors.As(err, &unsafe) || !unsafe.Has(RuleCaseCollision) {
		t.Errorf("Snapshot = %v, want %s", err, RuleCaseCollision)
	}
}

func TestSnapshotFile(t *testing.T) {
	dir := writeTree(t, map[string]string{"mcp/jira.yaml": "meta: {name: Jira}\n", "mcp/other.yaml": "x"})
	files, err := SnapshotFile(os.DirFS(dir), "mcp/jira.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := files.Paths(); !slices.Equal(got, []string{"jira.yaml"}) {
		t.Errorf("Paths = %v, want just jira.yaml", got)
	}
	if _, err := SnapshotFile(os.DirFS(dir), "mcp/none.yaml"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Symlink("jira.yaml", filepath.Join(dir, "mcp", "link.yaml")); err != nil {
		t.Fatal(err)
	}
	var unsafe *UnsafeError
	if _, err := SnapshotFile(os.DirFS(dir), "mcp/link.yaml"); !errors.As(err, &unsafe) || !unsafe.Has(RuleLink) {
		t.Errorf("a symbolic link was read through: %v", err)
	}
}

// A repository archive keeps what Unpack refuses, so that one bad resource leaves the others
// readable, and refusing happens when a resource is taken out of the tree.
func TestReadTreeKeepsLinksForTheResourceToBeRefused(t *testing.T) {
	data := makeTar(t,
		tarEntry{name: "pax_global_header", typ: tar.TypeXGlobalHeader},
		tarEntry{name: "skills/", typ: tar.TypeDir},
		tarEntry{name: "skills/good/", typ: tar.TypeDir},
		tarEntry{name: "skills/good/SKILL.md", body: "good"},
		tarEntry{name: "skills/bad/SKILL.md", body: "bad"},
		tarEntry{name: "skills/bad/link.md", typ: tar.TypeSymlink},
		tarEntry{name: "skills/bad/hard.md", typ: tar.TypeLink},
	)
	tree, err := ReadTree(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	good, _ := fs.Sub(tree, "skills/good")
	if _, err := Snapshot(good); err != nil {
		t.Errorf("the good resource: %v", err)
	}
	bad, _ := fs.Sub(tree, "skills/bad")
	_, err = Snapshot(bad)
	var unsafe *UnsafeError
	if !errors.As(err, &unsafe) || len(unsafe.Problems) != 2 {
		t.Errorf("the resource with a symbolic and a hard link: %v", err)
	}
	if _, err := tree.Open("skills/bad/link.md"); err == nil {
		t.Error("a symbolic link in the tree could be opened")
	}

	for name, entries := range map[string][]tarEntry{
		"parent":   {{name: "../x", body: "x"}},
		"absolute": {{name: "/x", body: "x"}},
		"twice":    {{name: "a", body: "x"}, {name: "a", body: "y"}},
		"file-dir": {{name: "a", body: "x"}, {name: "a/b", body: "y"}},
	} {
		if _, err := ReadTree(bytes.NewReader(makeTar(t, entries...))); err == nil {
			t.Errorf("%s: ReadTree accepted an archive that is not a tree", name)
		}
	}
}

func TestFilesIsAFileSystem(t *testing.T) {
	files, err := Snapshot(os.DirFS(writeTree(t, skillFiles)))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for p := range skillFiles {
		want = append(want, p)
	}
	slices.Sort(want)
	if got := files.Paths(); !slices.Equal(got, want) {
		t.Errorf("Paths = %v, want %v", got, want)
	}
	// testing/fstest checks the contracts of fs.FS, fs.ReadDirFS, fs.SubFS and the rest.
	if err := fsTest(files, want...); err != nil {
		t.Error(err)
	}
	sub, err := fs.Sub(files, "references")
	if err != nil {
		t.Fatal(err)
	}
	if err := fsTest(sub, "a-b.md", "a/b.md", "规范.md"); err != nil {
		t.Error(err)
	}
}

func FuzzUnpack(f *testing.F) {
	for _, fx := range unsafeFixtures[:len(unsafeFixtures)-2] {
		f.Add(makeTar(f, fx.entries...))
	}
	p, err := Pack(fstestMap(skillFiles))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(p.Data)
	f.Fuzz(func(t *testing.T, data []byte) {
		files, err := Unpack(bytes.NewReader(data))
		if err != nil {
			return
		}
		// Whatever was accepted packs, and packs again to the same bytes.
		a, err := Pack(files)
		if err != nil {
			t.Fatalf("accepted files do not pack: %v", err)
		}
		again, err := Unpack(bytes.NewReader(a.Data))
		if err != nil {
			t.Fatalf("a packed bundle does not unpack: %v", err)
		}
		b, err := Pack(again)
		if err != nil || !bytes.Equal(a.Data, b.Data) {
			t.Fatalf("pack, unpack, pack changed the bytes (%v)", err)
		}
	})
}
