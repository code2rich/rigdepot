// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package repoformat

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/schema"
)

// MetaFile is the file a directory-shaped resource keeps its metadata in.
const MetaFile = "meta.yaml"

// Kinds is where Read finds the resource kinds: *kind.Registry, or a test's own.
type Kinds interface {
	All() []kind.Kind
}

// defaultKinds is the program's registry of kinds.
type defaultKinds struct{}

func (defaultKinds) All() []kind.Kind { return kind.All() }

// Options changes how Read reads a repository.
type Options struct {
	// Kinds lists the resource kinds; nil means the program's registry. A top-level directory
	// that no kind owns is skipped with a notice.
	Kinds Kinds
	// Version is the version of the running program, checked against requires.rgd in
	// rigdepot.yaml. Empty, or a development build, skips the check.
	Version string
}

// Repo is a resource repository as read: its configuration, the resources that can be used, the
// ones that cannot and why, and advisory notices.
type Repo struct {
	Config Config
	// Resources are the resources that were read and passed every check, sorted by code.
	Resources []Resource
	// Failed are the resources that could not be read or broke a rule, sorted by path. A
	// failed resource is unknown, not absent: whoever acts on the repository must not take it
	// as deleted.
	Failed []Failure
	// Notices are advisory: unknown fields and directories, entries that are not resources,
	// scopes that are not among the products. They block nothing.
	Notices []Issue
}

// Resource is one resource that was read and passed every check.
type Resource struct {
	// Bundle holds the reference (Version is 0: a repository has no published versions), the
	// metadata as written, the content digest and the files. Files is an in-memory snapshot
	// laid out as resource.Bundle describes; the digest is of exactly these bytes.
	resource.Bundle
	// Path is where the resource is in the repository: "skills/unit-testing" or
	// "mcp/jira.yaml".
	Path string
	// Effective is Meta with the repository's defaults filled in.
	Effective resource.Meta
	// Kind is the kind that parsed the resource, and Spec what its Parse returned. The Spec is
	// handed back unchanged to the kind's forms to render the resource.
	Kind kind.Kind
	Spec kind.Spec
}

// Failure is a resource that cannot be used, with every reason found.
type Failure struct {
	// Path is where the resource is in the repository.
	Path string
	// Ref holds the kind and the code as found; the code may not be a valid one.
	Ref    resource.Ref
	Issues []Issue
}

// Lookup returns the resource with the given code.
func (r *Repo) Lookup(code string) (Resource, bool) {
	i, ok := slices.BinarySearchFunc(r.Resources, code, func(x Resource, c string) int { return cmp.Compare(x.Ref.Code, c) })
	if !ok {
		return Resource{}, false
	}
	return r.Resources[i], true
}

// Read reads the resource repository at the root of fsys: os.DirFS of a work tree, or the tree
// of a git archive from bundle.ReadTree, which is what the programs use, since it does not
// depend on how a person's Git converts line endings.
//
// It returns an error, and nothing else, when the repository as a whole cannot be read:
// ErrNoConfig, a *ConfigError, ErrNewerFormat, ErrNeedsNewer, or an error reading fsys. A
// problem with one resource fails only that resource; it is listed in Repo.Failed and the
// others are read.
func Read(fsys fs.FS, opt Options) (*Repo, error) {
	cfg, notices, err := readConfig(fsys, opt.Version)
	if err != nil {
		return nil, err
	}
	kinds := opt.Kinds
	if kinds == nil {
		kinds = defaultKinds{}
	}
	byDir := map[string]kind.Kind{}
	for _, k := range kinds.All() {
		l := k.Layout()
		if !fs.ValidPath(l.Dir) || l.Dir == "." || strings.Contains(l.Dir, "/") || (l.File && !strings.HasPrefix(l.Ext, ".")) {
			return nil, fmt.Errorf("repoformat: kind %q has an invalid layout %+v", k.ID(), l)
		}
		if other, dup := byDir[l.Dir]; dup {
			return nil, fmt.Errorf("repoformat: kinds %q and %q both claim the directory %s", other.ID(), k.ID(), l.Dir)
		}
		byDir[l.Dir] = k
	}

	r := &reader{fsys: fsys, repo: &Repo{Config: cfg, Notices: notices}}
	top, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("repoformat: %w", err)
	}
	for _, e := range top {
		name := e.Name()
		k, known := byDir[name]
		switch {
		case name == brand.RepoConfig || strings.HasPrefix(name, "."):
		case known && e.IsDir():
			if err := r.readKind(k); err != nil {
				return nil, err
			}
		case known:
			// The directory of a kind is a link or a file: none of its resources can be read.
			r.fail(name, resource.Ref{Kind: k.ID()}, Issue{File: name, Code: CodeUnsafePath, Field: string(bundle.RuleLink),
				Message: "the directory of a resource kind has to be a plain directory"})
		case e.IsDir():
			r.notice(Issue{File: name, Code: CodeUnknownDir, Message: fmt.Sprintf("no resource kind uses the directory %s; it is skipped", name)})
		}
	}
	r.dedupe()
	repo := r.repo
	slices.SortFunc(repo.Resources, func(a, b Resource) int { return cmp.Compare(a.Ref.Code, b.Ref.Code) })
	slices.SortFunc(repo.Failed, func(a, b Failure) int { return cmp.Compare(a.Path, b.Path) })
	slices.SortStableFunc(repo.Notices, func(a, b Issue) int { return cmp.Compare(a.File, b.File) })
	return repo, nil
}

type reader struct {
	fsys fs.FS
	repo *Repo
}

func (r *reader) notice(is ...Issue) { r.repo.Notices = append(r.repo.Notices, is...) }

func (r *reader) fail(p string, ref resource.Ref, is ...Issue) {
	r.repo.Failed = append(r.repo.Failed, Failure{Path: p, Ref: ref, Issues: is})
}

// readKind reads every resource in the directory of one kind.
func (r *reader) readKind(k kind.Kind) error {
	l := k.Layout()
	entries, err := fs.ReadDir(r.fsys, l.Dir)
	if err != nil {
		return fmt.Errorf("repoformat: %w", err)
	}
	var kindSchema *jsonschema.Schema
	var schemaErr error
	if l.File {
		kindSchema, schemaErr = compileKind(k)
	}
	for _, e := range entries {
		name := e.Name()
		p := l.Dir + "/" + name
		if strings.HasPrefix(name, ".") {
			continue
		}
		code := name
		if l.File {
			stem, ok := strings.CutSuffix(name, l.Ext)
			if !ok || e.IsDir() {
				r.notice(Issue{File: p, Code: CodeIgnored, Message: fmt.Sprintf("a %s resource is a %s file; this entry is skipped", k.ID(), l.Ext)})
				continue
			}
			code = stem
		} else if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			r.notice(Issue{File: p, Code: CodeIgnored, Message: fmt.Sprintf("a %s resource is a directory; this entry is skipped", k.ID())})
			continue
		}
		ref := resource.Ref{Kind: k.ID(), Code: code}
		if !l.File && e.Type()&fs.ModeSymlink != 0 {
			// Reading through it would read whatever it points at.
			r.fail(p, ref, Issue{File: p, Field: string(bundle.RuleLink), Code: CodeUnsafePath, Message: "is a symbolic link"})
			continue
		}
		if !resource.ValidCode(code) {
			r.fail(p, ref, Issue{File: p, Code: CodeInvalidCode,
				Message: fmt.Sprintf("%q is not a valid resource code: %d to %d lower-case letters, digits and single hyphens", code, resource.MinCodeLength, resource.MaxCodeLength)})
			continue
		}
		if schemaErr != nil {
			r.fail(p, ref, Issue{File: p, Code: CodeSchema, Message: schemaErr.Error()})
			continue
		}
		res, issues, notices := r.readOne(k, ref, p, kindSchema)
		r.notice(notices...)
		if len(issues) > 0 {
			r.fail(p, ref, issues...)
			continue
		}
		r.repo.Resources = append(r.repo.Resources, res)
	}
	return nil
}

// readOne reads one resource. It returns the reasons it cannot be used, and advisory notices.
func (r *reader) readOne(k kind.Kind, ref resource.Ref, p string, kindSchema *jsonschema.Schema) (Resource, []Issue, []Issue) {
	l := k.Layout()
	var files *bundle.Files
	var err error
	if l.File {
		files, err = bundle.SnapshotFile(r.fsys, p)
	} else {
		var sub fs.FS
		if sub, err = fs.Sub(r.fsys, p); err == nil {
			files, err = bundle.Snapshot(sub)
		}
	}
	if err != nil {
		return Resource{}, readIssues(p, l.File, err), nil
	}
	digest, err := bundle.Digest(files)
	if err != nil {
		return Resource{}, []Issue{{File: p, Code: CodeRead, Message: err.Error()}}, nil
	}

	// The metadata: meta.yaml of a directory, or the "meta" key of the one file.
	metaFile, at := path.Join(p, MetaFile), "/meta"
	var data []byte
	if l.File {
		metaFile = p
		if !isYAML(l.Ext) {
			return Resource{}, []Issue{{File: p, Code: CodeMissingMeta,
				Message: "a single-file resource keeps its metadata under the \"meta\" key of a YAML file, and this kind's files are not YAML"}}, nil
		}
		data, err = files.ReadFile(path.Base(p))
	} else {
		at = ""
		data, err = files.ReadFile(MetaFile)
		if errors.Is(err, fs.ErrNotExist) {
			return Resource{}, []Issue{{File: metaFile, Code: CodeMissingMeta, Message: "a " + k.ID() + " needs a " + MetaFile + " with at least a name"}}, nil
		}
	}
	if err != nil {
		return Resource{}, []Issue{{File: metaFile, Code: CodeRead, Message: err.Error()}}, nil
	}
	doc, err := parseYAML(data)
	if err != nil {
		return Resource{}, []Issue{yamlIssue(metaFile, err)}, nil
	}
	if at != "" {
		m, _ := doc.value.(map[string]any)
		if _, ok := m["meta"]; !ok {
			return Resource{}, []Issue{{File: metaFile, Line: doc.line(""), Code: CodeMissingMeta, Message: "the file needs a \"meta\" key with at least a name"}}, nil
		}
	}
	schemas, err := embedded()
	if err != nil {
		return Resource{}, []Issue{{File: metaFile, Code: CodeSchema, Message: err.Error()}}, nil
	}
	issues := validate(schemas[schema.Meta], doc, metaFile, at)
	notices := unknownFields(schemas[schema.Meta], doc, metaFile, at)
	if kindSchema != nil {
		// The kind's schema covers the whole file, usually the metadata too; what the
		// metadata schema already said is not said twice.
		issues = merge(issues, validate(kindSchema, doc, metaFile, ""))
		notices = merge(notices, unknownFields(kindSchema, doc, metaFile, ""))
	}
	if len(issues) > 0 {
		return Resource{}, issues, notices
	}
	var meta resource.Meta
	if err := decode(lookup(doc.value, at), &meta); err != nil {
		return Resource{}, []Issue{{File: metaFile, Code: CodeSchema, Message: err.Error()}}, notices
	}

	// What needs rigdepot.yaml to decide.
	cfg := r.repo.Config
	for i, s := range meta.Stages {
		if _, ok := cfg.Stage(s); !ok {
			ptr := fmt.Sprintf("%s/stages/%d", at, i)
			issues = append(issues, Issue{File: metaFile, Line: doc.line(ptr), Field: field(ptr), Code: CodeUndefinedStage,
				Message: fmt.Sprintf("the stage %q is not one of the repository's stages", s)})
		}
	}
	for i, s := range meta.Secrets {
		if _, ok := cfg.Secrets[s]; !ok {
			ptr := fmt.Sprintf("%s/secrets/%d", at, i)
			issues = append(issues, Issue{File: metaFile, Line: doc.line(ptr), Field: field(ptr), Code: CodeUndefinedSecret,
				Message: fmt.Sprintf("the secret %q is not defined under secrets in %s", s, brand.RepoConfig)})
		}
	}
	if len(issues) > 0 {
		return Resource{}, issues, notices
	}
	if code, ok := meta.Scope.Product(); ok {
		if _, known := cfg.Product(code); !known {
			ptr := at + "/scope"
			notices = append(notices, Issue{File: metaFile, Line: doc.line(ptr), Field: field(ptr), Code: CodeUnknownProduct,
				Message: fmt.Sprintf("the scope %q is not among the products in %s", code, brand.RepoConfig)})
		}
	}

	spec, err := k.Parse(ref, files)
	if err != nil {
		return Resource{}, []Issue{{File: p, Code: CodeParse, Message: err.Error()}}, notices
	}
	return Resource{
		Bundle:    resource.Bundle{Ref: ref, Meta: meta, Digest: digest, Files: files},
		Path:      p,
		Effective: cfg.Effective(meta),
		Kind:      k,
		Spec:      spec,
	}, nil, notices
}

func isYAML(ext string) bool {
	return ext == ".yaml" || ext == ".yml"
}

// merge appends the issues of b that are not about a field a already has an issue with.
func merge(a, b []Issue) []Issue {
	for _, is := range b {
		if !slices.ContainsFunc(a, func(x Issue) bool { return x.File == is.File && x.Field == is.Field }) {
			a = append(a, is)
		}
	}
	return a
}

// readIssues turns an error from taking a resource's files into issues.
func readIssues(p string, single bool, err error) []Issue {
	var unsafe *bundle.UnsafeError
	if !errors.As(err, &unsafe) {
		return []Issue{{File: p, Code: CodeRead, Message: err.Error()}}
	}
	out := make([]Issue, 0, len(unsafe.Problems))
	for _, pr := range unsafe.Problems {
		file := p
		if !single && pr.Path != "" {
			file = p + "/" + pr.Path
		}
		out = append(out, Issue{File: file, Field: string(pr.Rule), Code: CodeUnsafePath, Message: pr.Detail})
	}
	return out
}

// dedupe fails every resource whose code another resource also has: the code is unique across
// all kinds, and which one was meant cannot be told.
func (r *reader) dedupe() {
	paths := map[string][]string{}
	for _, res := range r.repo.Resources {
		paths[res.Ref.Code] = append(paths[res.Ref.Code], res.Path)
	}
	for _, f := range r.repo.Failed {
		if resource.ValidCode(f.Ref.Code) {
			paths[f.Ref.Code] = append(paths[f.Ref.Code], f.Path)
		}
	}
	kept := r.repo.Resources[:0]
	for _, res := range r.repo.Resources {
		ps := paths[res.Ref.Code]
		if len(ps) < 2 {
			kept = append(kept, res)
			continue
		}
		others := slices.DeleteFunc(slices.Clone(ps), func(x string) bool { return x == res.Path })
		r.fail(res.Path, res.Ref, Issue{File: res.Path, Code: CodeDuplicateCode,
			Message: fmt.Sprintf("the code %q is also used by %s; codes are unique across all kinds", res.Ref.Code, strings.Join(others, ", "))})
	}
	r.repo.Resources = kept
}
