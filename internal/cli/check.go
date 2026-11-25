// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/bundle"
	"github.com/code2rich/rigdepot/internal/check"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/gitrepo"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

func init() { register("", newCheckCmd) }

type checkFlags struct {
	strict bool
	rules  []string
	repo   string
	branch string
	dir    string
}

// newCheckCmd: rgd check [path]. It runs the intake check the server runs on import and upload,
// on a local resource repository (or a directory or resource in one) or, with --repo, on a
// repository read by its address. It exits with 4 when something must be fixed (with --strict,
// when anything was found), and 0 otherwise.
func newCheckCmd(a *app) *cobra.Command {
	var f checkFlags
	cmd := &cobra.Command{
		Use:   "check [path]",
		Short: a.p.T(msg.CheckCLIShort),
		Long:  a.p.T(msg.CheckCLILong),
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return a.usageError("argument_count",
					a.p.T(msg.ErrorArgCount, i18n.Count(1), i18n.V("command", cmd.CommandPath()), i18n.V("got", len(args))), cmd.CommandPath())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCheck(cmd, f, args)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&f.strict, "strict", false, a.p.T(msg.CheckCLIFlagStrict))
	fl.StringSliceVar(&f.rules, "rules", nil, a.p.T(msg.CheckCLIFlagRules))
	fl.StringVar(&f.repo, "repo", "", a.p.T(msg.CheckCLIFlagRepo))
	fl.StringVar(&f.branch, "branch", "", a.p.T(msg.CheckCLIFlagBranch))
	fl.StringVar(&f.dir, "dir", "", a.p.T(msg.CheckCLIFlagDir))
	return cmd
}

// checkDoc is the JSON output of rgd check (schema rgd.check/v1). The checks of each resource
// have the shape of CheckReport in api/openapi.yaml, so that the server can store them as they
// are.
type checkDoc struct {
	// Source is the path as given, or the repository's address with --repo.
	Source string `json:"source"`
	// Commit is the commit that was checked, with --repo.
	Commit  string   `json:"commit,omitempty"`
	Ruleset int      `json:"ruleset"`
	Rules   []string `json:"rules"`
	// Resources are sorted by path.
	Resources []checkResourceDoc `json:"resources"`
	// Notices are advisory notes about the repository that belong to no resource.
	Notices []checkNoticeDoc `json:"notices"`
	Summary checkSummaryDoc  `json:"summary"`
}

type checkResourceDoc struct {
	Kind string `json:"kind"`
	Code string `json:"code"`
	Path string `json:"path"`
	// Refused: the resource could not be read, so no rule ran; the findings say why.
	Refused bool           `json:"refused,omitempty"`
	Checks  checkReportDoc `json:"checks"`
}

// checkReportDoc is components/schemas/CheckReport.
type checkReportDoc struct {
	Ruleset  int               `json:"ruleset"`
	Blocking int               `json:"blocking"`
	Advisory int               `json:"advisory"`
	Findings []checkFindingDoc `json:"findings"`
}

// checkFindingDoc is components/schemas/Finding, plus the message key and its parameters, so
// that whoever stores it can show it again in another language.
type checkFindingDoc struct {
	ID       string            `json:"id"`
	Rule     string            `json:"rule"`
	Severity extcheck.Severity `json:"severity"`
	Message  string            `json:"message"`
	File     string            `json:"file,omitempty"`
	Line     int               `json:"line,omitempty"`
	Key      string            `json:"key"`
	Params   map[string]any    `json:"params,omitempty"`
}

type checkNoticeDoc struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type checkSummaryDoc struct {
	Resources int  `json:"resources"`
	Refused   int  `json:"refused"`
	Blocking  int  `json:"blocking"`
	Advisory  int  `json:"advisory"`
	Notices   int  `json:"notices"`
	Passed    bool `json:"passed"`
}

// checkSource is a repository to check: its files, and which part of it was asked for.
type checkSource struct {
	fsys   fs.FS
	show   string // how the person named it
	rel    string // the part asked for, relative to the repository's root; "." for all of it
	commit string
}

func (a *app) runCheck(cmd *cobra.Command, f checkFlags, args []string) error {
	path := cmd.CommandPath()
	if f.repo != "" && len(args) > 0 {
		return a.usageError("repo_and_path", a.p.T(msg.CheckCLIRepoAndPath), path)
	}
	for _, only := range []struct{ flag, value string }{{"--branch", f.branch}, {"--dir", f.dir}} {
		if f.repo == "" && only.value != "" {
			return a.usageError("repo_only", a.p.T(msg.CheckCLIRepoOnly, i18n.V("flag", only.flag)), path)
		}
	}
	engine, err := a.checkEngine(f.rules, path)
	if err != nil {
		return err
	}

	var src checkSource
	if f.repo != "" {
		src, err = a.fetchCheckSource(cmd.Context(), f)
	} else {
		target := "."
		if len(args) == 1 {
			target = args[0]
		}
		src, err = a.localCheckSource(target)
	}
	if err != nil {
		return err
	}

	repo, err := repoformat.Read(src.fsys, repoformat.Options{Version: buildinfo.Get().Version})
	if err != nil {
		return a.repoError(src, err)
	}
	doc, err := runChecks(cmd.Context(), engine, repo, src)
	if errors.Is(err, errNoResource) {
		return cliout.Fail(cliout.ExitFailure, "no_resource", a.p.T(msg.CheckCLINoResource, i18n.V("path", src.show)))
	}
	if err != nil {
		return err
	}
	a.translate(&doc)
	if err := a.out.Result(cliout.Schema("check", 1), doc, func(w io.Writer) error {
		return a.writeCheck(w, doc)
	}); err != nil {
		return err
	}
	if doc.Summary.Blocking > 0 || (f.strict && doc.Summary.Advisory+doc.Summary.Notices > 0) {
		return cliout.Status(cliout.ExitCheckFailed)
	}
	return nil
}

// checkEngine returns the engine for the program's rule set, or for the rules named by --rules.
func (a *app) checkEngine(only []string, path string) (*check.Engine, error) {
	rs := extcheck.DefaultRuleset()
	if rs.Version < 1 || len(rs.Rules) == 0 {
		return nil, cliout.Fail(cliout.ExitFailure, "no_rules", a.p.T(msg.CheckCLINoRules))
	}
	if len(only) > 0 {
		var ids []string
		for _, r := range rs.Rules {
			ids = append(ids, r.ID())
		}
		var rules []extcheck.Rule
		for _, id := range only {
			id = strings.TrimSpace(id)
			i := slices.Index(ids, id)
			if i < 0 {
				return nil, a.usageError("unknown_rule", a.p.T(msg.CheckCLIUnknownRule, i18n.V("rule", id), i18n.V("rules", strings.Join(ids, ", "))), path)
			}
			if !slices.Contains(rules, rs.Rules[i]) {
				rules = append(rules, rs.Rules[i])
			}
		}
		slices.SortFunc(rules, func(x, y extcheck.Rule) int { return cmp.Compare(x.ID(), y.ID()) })
		rs.Rules = rules
	}
	e, err := check.New(rs)
	if err != nil {
		return nil, err // a defect: the registry hands out a valid rule set
	}
	return e, nil
}

// localCheckSource finds the repository a local path is in: the path itself or the nearest
// directory above it with the repository's configuration file.
func (a *app) localCheckSource(target string) (checkSource, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return checkSource{}, a.unreadable(target, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return checkSource{}, a.unreadable(target, err)
	}
	root := abs
	if !fi.IsDir() {
		root = filepath.Dir(abs) // a file in a resource
	}
	for {
		fi, err := os.Stat(filepath.Join(root, brand.RepoConfig))
		if err == nil && fi.Mode().IsRegular() {
			break
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return checkSource{}, a.unreadable(filepath.Join(root, brand.RepoConfig), err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			return checkSource{}, cliout.Fail(cliout.ExitFailure, "not_a_repository",
				a.p.T(msg.CheckCLINotARepository, i18n.V("path", a.showPath(abs)), i18n.V("file", brand.RepoConfig)))
		}
		root = parent
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return checkSource{}, a.unreadable(target, err)
	}
	return checkSource{fsys: os.DirFS(root), show: target, rel: filepath.ToSlash(rel)}, nil
}

func (a *app) unreadable(path string, err error) error {
	detail := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) {
		detail = pe.Err.Error()
	}
	e := cliout.Fail(cliout.ExitFailure, "unreadable", a.p.T(msg.CheckCLIUnreadable, i18n.V("path", a.showPath(path)), i18n.V("detail", detail)))
	e.Err = err
	return e
}

// fetchCheckSource reads a repository by its address, as sync --repo does: the bytes as
// committed, through the person's own git.
func (a *app) fetchCheckSource(ctx context.Context, f checkFlags) (checkSource, error) {
	cfg, err := a.config()
	if err != nil {
		return checkSource{}, err
	}
	dir, err := home.At(a.home)
	if err != nil {
		return checkSource{}, a.noHomeError()
	}
	fetcher, err := gitrepo.New(gitrepo.Options{
		Git:        cfg.String(localconfig.KeyGit),
		CacheDir:   dir.Join(home.CacheDir, "repos"),
		AllowLocal: true, // the person typed the address
	})
	if err != nil {
		return checkSource{}, a.repoFetchError(f.repo, err)
	}
	var archive bytes.Buffer
	commit, err := fetcher.Archive(ctx, gitrepo.Source{URL: f.repo, Ref: f.branch, Dir: f.dir}, &archive)
	if err != nil {
		return checkSource{}, a.repoFetchError(f.repo, err)
	}
	files, err := bundle.ReadTree(&archive)
	if err != nil {
		return checkSource{}, a.repoFetchError(f.repo, err)
	}
	return checkSource{fsys: files, show: f.repo, rel: ".", commit: commit}, nil
}

func (a *app) repoFetchError(addr string, err error) error {
	exit, code := cliout.ExitFailure, "repo_unreadable"
	switch {
	case errors.Is(err, gitrepo.ErrAddress):
		exit, code = cliout.ExitUsage, "repo_address"
	case errors.Is(err, gitrepo.ErrFetch), errors.Is(err, gitrepo.ErrTimeout):
		exit, code = cliout.ExitUnreachable, "repo_unreachable"
	case errors.Is(err, gitrepo.ErrNoGit):
		code = "git_missing"
	case errors.Is(err, gitrepo.ErrNoRef):
		code = "repo_no_ref"
	case errors.Is(err, gitrepo.ErrNoDir):
		code = "repo_no_dir"
	case errors.Is(err, gitrepo.ErrTooLarge), errors.Is(err, bundle.ErrTreeTooLarge):
		code = "repo_too_large"
	}
	e := cliout.Fail(exit, code, a.p.T(msg.CheckCLIRepoFailed, i18n.V("repo", addr), i18n.V("detail", err.Error())))
	e.Err = err
	return e
}

// repoError explains why a repository as a whole could not be read.
func (a *app) repoError(src checkSource, err error) error {
	var ce *repoformat.ConfigError
	var e *cliout.Error
	switch {
	case errors.Is(err, repoformat.ErrNoConfig):
		e = cliout.Fail(cliout.ExitFailure, "not_a_repository",
			a.p.T(msg.CheckCLINotARepository, i18n.V("path", src.show), i18n.V("file", brand.RepoConfig)))
	case errors.As(err, &ce):
		// The configuration is something to fix like any finding: it blocks the whole repository.
		lines := make([]string, 0, len(ce.Issues))
		for _, is := range ce.Issues {
			f := check.IssueFinding(is, "", extcheck.MustFix)
			loc := f.File
			if f.Line > 0 {
				loc += ":" + strconv.Itoa(f.Line)
			}
			lines = append(lines, "  "+loc+"  "+a.findingText(f))
		}
		e = cliout.Fail(cliout.ExitCheckFailed, "repo_config_invalid",
			a.p.T(msg.CheckCLIConfigInvalid, i18n.V("file", brand.RepoConfig), i18n.V("detail", strings.Join(lines, "\n"))))
	case errors.Is(err, repoformat.ErrNewerFormat), errors.Is(err, repoformat.ErrNeedsNewer):
		e = cliout.Fail(cliout.ExitIncompatible, "incompatible", a.p.T(msg.CheckCLIIncompatible, i18n.V("detail", err.Error())))
	default:
		e = cliout.Fail(cliout.ExitFailure, "unreadable", a.p.T(msg.CheckCLIUnreadable, i18n.V("path", src.show), i18n.V("detail", err.Error())))
	}
	e.Err = err
	return e
}

// errNoResource: the path asked for is in the repository but is not, and has no, resource.
var errNoResource = errors.New("no resource at the path")

// runChecks checks the part of the repository that was asked for. Every resource of the
// repository is a peer of the ones checked, as they would be published together.
func runChecks(ctx context.Context, e *check.Engine, repo *repoformat.Repo, src checkSource) (checkDoc, error) {
	doc := checkDoc{Source: src.show, Commit: src.commit, Ruleset: e.Version(), Rules: []string{},
		Resources: []checkResourceDoc{}, Notices: []checkNoticeDoc{}}
	wanted := func(p string) bool {
		return src.rel == "." || check.Within(p, src.rel) || check.Within(src.rel, p)
	}

	located, err := check.FromRepo(repo)
	if err != nil {
		return doc, err
	}
	all := make([]check.Resource, len(located))
	for i, l := range located {
		all[i] = l.Resource
	}
	type item struct {
		doc    checkResourceDoc
		res    check.Resource // no files for a refused resource
		result check.Result
	}
	var items []*item
	ran := map[string]bool{}
	for _, l := range located {
		if !wanted(l.Path) {
			continue
		}
		r, err := e.Check(ctx, l.Resource, check.PeersOf(all, l.Ref))
		if err != nil {
			return doc, err
		}
		for _, id := range r.Rules {
			ran[id] = true
		}
		items = append(items, &item{doc: checkResourceDoc{Path: l.Path}, res: l.Resource, result: r})
	}
	for _, f := range repo.Failed {
		if !wanted(f.Path) {
			continue
		}
		items = append(items, &item{doc: checkResourceDoc{Path: f.Path, Refused: true}, result: check.Refused(f, e.Version())})
	}
	if len(items) == 0 && src.rel != "." {
		return doc, errNoResource
	}

	// Notices about a resource's files are advisory findings of that resource; the others are
	// about the repository.
	for _, n := range repo.Notices {
		owner := slices.IndexFunc(items, func(it *item) bool { return check.Within(n.File, it.doc.Path) })
		if owner >= 0 {
			it := items[owner]
			it.result.Findings = append(it.result.Findings, check.IssueFinding(n, it.doc.Path, extcheck.Advisory))
			continue
		}
		if src.rel == "." || check.Within(n.File, src.rel) {
			doc.Notices = append(doc.Notices, checkNoticeDoc{Code: n.Code, File: n.File, Line: n.Line, Field: n.Field, Message: n.Message})
		}
	}

	slices.SortFunc(items, func(x, y *item) int { return cmp.Compare(x.doc.Path, y.doc.Path) })
	for _, it := range items {
		ref := it.result.Ref
		it.doc.Kind, it.doc.Code = ref.Kind, ref.Code
		findings := it.result.Findings
		ids := check.FindingIDs(it.res, findings)
		rep := checkReportDoc{Ruleset: e.Version(), Findings: []checkFindingDoc{}}
		for i, f := range findings {
			if f.Severity == extcheck.MustFix {
				rep.Blocking++
			} else {
				rep.Advisory++
			}
			rep.Findings = append(rep.Findings, checkFindingDoc{
				ID: ids[i], Rule: f.Rule, Severity: f.Severity, File: f.File, Line: f.Line, Key: f.Message, Params: f.Params,
			})
		}
		it.doc.Checks = rep
		doc.Resources = append(doc.Resources, it.doc)
		doc.Summary.Resources++
		if it.doc.Refused {
			doc.Summary.Refused++
		}
		doc.Summary.Blocking += rep.Blocking
		doc.Summary.Advisory += rep.Advisory
	}
	doc.Summary.Notices = len(doc.Notices)
	doc.Summary.Passed = doc.Summary.Blocking == 0
	for id := range ran {
		doc.Rules = append(doc.Rules, id)
	}
	slices.Sort(doc.Rules)
	return doc, nil
}

// translate fills in the text of every finding and notice in the printer's language.
func (a *app) translate(doc *checkDoc) {
	for i := range doc.Resources {
		for j := range doc.Resources[i].Checks.Findings {
			f := &doc.Resources[i].Checks.Findings[j]
			f.Message = a.findingText(extcheck.Finding{Message: f.Key, Params: f.Params})
		}
	}
	for i := range doc.Notices {
		n := &doc.Notices[i]
		n.Message = a.findingText(check.IssueFinding(repoformat.Issue{File: n.File, Field: n.Field, Code: n.Code, Message: n.Message}, "", extcheck.Advisory))
	}
}

// findingText renders a finding's message with its parameters.
func (a *app) findingText(f extcheck.Finding) string {
	args := make([]i18n.Arg, 0, len(f.Params))
	for k, v := range f.Params {
		args = append(args, i18n.V(k, v))
	}
	return a.p.T(f.Message, args...)
}

// checkMarks are the marks in front of each finding in the human output.
var checkMarks = map[extcheck.Severity]struct {
	mark  string
	style cliout.Style
}{
	extcheck.MustFix:  {"x", cliout.Red},
	extcheck.Advisory: {"!", cliout.Yellow},
}

// writeCheck writes the human output: a line saying what was checked, then each resource with
// findings and its findings, the notes on the repository, and the totals.
func (a *app) writeCheck(w io.Writer, doc checkDoc) error {
	var b strings.Builder
	b.WriteString("  " + a.p.T(msg.CheckCLIHeader, i18n.Count(doc.Summary.Resources),
		i18n.V("source", doc.Source), i18n.V("ruleset", doc.Ruleset)) + "\n")
	for _, r := range doc.Resources {
		if len(r.Checks.Findings) == 0 {
			continue
		}
		b.WriteString("\n  " + r.Path + "\n")
		for _, f := range r.Checks.Findings {
			loc := f.File
			if f.Line > 0 {
				loc += ":" + strconv.Itoa(f.Line)
			}
			if loc != "" {
				loc += "  "
			}
			m := checkMarks[f.Severity]
			fmt.Fprintf(&b, "    %s %s%s  %s\n", a.out.Paint(m.style, m.mark), loc, a.out.Paint(cliout.Dim, f.Rule), f.Message)
		}
	}
	if len(doc.Notices) > 0 {
		b.WriteString("\n  " + a.p.T(msg.CheckCLINotices) + "\n")
		for _, n := range doc.Notices {
			loc := n.File
			if n.Line > 0 {
				loc += ":" + strconv.Itoa(n.Line)
			}
			fmt.Fprintf(&b, "    %s %s  %s\n", a.out.Paint(cliout.Yellow, "!"), loc, n.Message)
		}
	}
	s := doc.Summary
	b.WriteString("\n  ")
	if s.Blocking+s.Advisory+s.Notices == 0 {
		b.WriteString(a.p.T(msg.CheckCLIPassed) + "\n")
	} else {
		b.WriteString(a.p.T(msg.CheckCLISummary, i18n.V("blocking", s.Blocking), i18n.V("advisory", s.Advisory+s.Notices)) + "\n")
	}
	if s.Blocking > 0 {
		b.WriteString("  " + a.out.Paint(cliout.Dim, a.p.T(msg.CheckCLIBlockedHint)) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
