// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package bundledenv

import (
	"context"
	"io/fs"
	"path"
	"strings"

	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
)

const id = "bundledenv"

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.MustFix }

// Applies to the kinds that are a directory of files; single-file kinds cannot carry one.
func (rule) Applies(kind string) bool {
	return kind == resource.KindSkill || kind == resource.KindAgent
}

// templates are the suffixes of files that declare settings without values.
var templates = []string{"example", "sample", "template", "dist"}

func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	var found []check.Finding
	err := fs.WalkDir(in.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // unreadable: the engine counts this as not passed, never as clean
		}
		if !d.IsDir() && isEnvFile(path.Base(p)) {
			found = append(found, check.Finding{
				Rule:     id,
				Severity: check.MustFix,
				File:     p,
				Message:  "check." + id + ".found",
				Params:   map[string]any{"file": p},
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func isEnvFile(name string) bool {
	if name == ".env" {
		return true
	}
	suffix, ok := strings.CutPrefix(name, ".env.")
	if !ok || suffix == "" {
		return false
	}
	for _, t := range templates {
		if strings.EqualFold(suffix, t) {
			return false
		}
	}
	return true
}
