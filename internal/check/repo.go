// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package check

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	extcheck "github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/repoformat"
)

// StructureRule is the ID of the rule about the shape of a resource. What the repository reader
// refuses (unsafe paths, missing metadata, a name that is not the directory's, a file that does
// not match its schema) is reported under it, so that every reason a resource cannot be
// published comes back as a finding, whether the reader or the rule found it.
const StructureRule = "structure"

// repoMessages are the message keys of the reader's issues, by issue code. Each takes the
// parameter "detail", the reader's own description in English; unsafe_path also takes "rule".
var repoMessages = map[string]string{
	repoformat.CodeYAML:            msg.CheckStructureRepoYaml,
	repoformat.CodeSchema:          msg.CheckStructureRepoSchema,
	repoformat.CodeUnknownField:    msg.CheckStructureRepoUnknownField,
	repoformat.CodeUnknownDir:      msg.CheckStructureRepoUnknownDir,
	repoformat.CodeIgnored:         msg.CheckStructureRepoIgnored,
	repoformat.CodeUnknownProduct:  msg.CheckStructureRepoUnknownProduct,
	repoformat.CodeInvalidCode:     msg.CheckStructureRepoInvalidCode,
	repoformat.CodeDuplicateCode:   msg.CheckStructureRepoDuplicateCode,
	repoformat.CodeDuplicate:       msg.CheckStructureRepoDuplicate,
	repoformat.CodeMissingMeta:     msg.CheckStructureRepoMissingMeta,
	repoformat.CodeUndefinedStage:  msg.CheckStructureRepoUndefinedStage,
	repoformat.CodeUndefinedSecret: msg.CheckStructureRepoUndefinedSecret,
	repoformat.CodeUndefinedRef:    msg.CheckStructureRepoUndefinedRef,
	repoformat.CodeUnsafePath:      msg.CheckStructureRepoUnsafePath,
	repoformat.CodeParse:           msg.CheckStructureRepoParse,
	repoformat.CodeRead:            msg.CheckStructureRepoRead,
}

// Located is a resource of a repository with its place in it.
type Located struct {
	// Path is where the resource is in the repository: "skills/unit-testing", "mcp/jira.yaml".
	Path string
	Resource
}

// FromRepo returns the resources the reader accepted, as the engine takes them, in the
// repository's order. Their metadata is the effective one: the repository's defaults filled in,
// as the resource would be published.
func FromRepo(repo *repoformat.Repo) ([]Located, error) {
	out := make([]Located, 0, len(repo.Resources))
	for _, r := range repo.Resources {
		files := map[string][]byte{}
		err := fs.WalkDir(r.Files, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			data, err := fs.ReadFile(r.Files, p)
			files[p] = data
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("check: %s: %w", r.Path, err)
		}
		out = append(out, Located{Path: r.Path, Resource: Resource{Ref: r.Ref, Meta: r.Effective, Files: files}})
	}
	return out, nil
}

// Refused is the result of checking a resource the reader refused: no rule ran, and every
// reason the reader gave is a must-fix finding of the structure rule. A refused resource never
// passes, whatever rules were asked for.
func Refused(f repoformat.Failure, rulesetVersion int) Result {
	out := Result{Ref: f.Ref, RulesetVersion: rulesetVersion, Findings: []extcheck.Finding{}}
	for _, is := range f.Issues {
		out.Findings = append(out.Findings, IssueFinding(is, f.Path, extcheck.MustFix))
	}
	sortFindings(out.Findings)
	return out
}

// IssueFinding turns one issue of the reader into a finding of the structure rule at the given
// severity. resourcePath is the path of the resource the issue belongs to ("" for none); the
// finding's file is made relative to it, as a finding's file always is. The reader's English
// description goes into the parameter "detail".
func IssueFinding(is repoformat.Issue, resourcePath string, sev extcheck.Severity) extcheck.Finding {
	key, ok := repoMessages[is.Code]
	detail := is.Message
	if is.Field != "" && is.Code != repoformat.CodeUnsafePath {
		detail = is.Field + ": " + detail
	}
	params := map[string]any{"detail": detail}
	switch {
	case !ok:
		key = msg.CheckStructureRepoOther
		params["code"] = is.Code
	case is.Code == repoformat.CodeUnsafePath:
		params["rule"] = is.Field
	}
	f := extcheck.Finding{Rule: StructureRule, Severity: sev, File: is.File, Line: is.Line, Message: key, Params: params}
	switch {
	case resourcePath == "":
	case is.File == resourcePath && is.Line > 0:
		// Only a single-file resource has lines at its own path; its file is named by the
		// last element of the path.
		f.File = path.Base(resourcePath)
	case is.File == resourcePath:
		f.File = ""
	default:
		f.File = strings.TrimPrefix(is.File, resourcePath+"/")
	}
	if f.File == "" {
		f.Line = 0
	}
	return f
}

// Within reports whether the repository path p is the resource at resourcePath or inside it.
func Within(p, resourcePath string) bool {
	return p == resourcePath || strings.HasPrefix(p, resourcePath+"/")
}
