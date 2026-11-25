// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package description

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/builtin/checks/internal/skillmd"
	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
)

const id = "description"

// minLength is the shortest description, in characters, that is not reported.
const minLength = 20

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.Advisory }
func (rule) Applies(kind string) bool { return kind == resource.KindSkill }

// when are the words that say when to use something, in Chinese and English.
var when = regexp.MustCompile(`(?i)(时|当|如果|需要|用于|适用|场景|之前|之后|遇到|\bwhen(ever)?\b|\bif\b|\bbefore\b|\bafter\b|\buse(d|s)?\s+(it\s+|this\s+)?(for|to)\b)`)

func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	fm, err := skillmd.Read(in.Files)
	var bad *skillmd.Error
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.As(err, &bad):
		return nil, nil // reported by the structure rule
	case err != nil:
		return nil, err
	}
	text := strings.Join(strings.Fields(fm.Description.Value), " ")
	if !fm.Description.Text || text == "" {
		return nil, nil // reported by the structure rule
	}
	var found []check.Finding
	add := func(problem string, params map[string]any) {
		found = append(found, check.Finding{
			Rule:     id,
			Severity: check.Advisory,
			File:     skillmd.File,
			Line:     fm.Description.Line,
			Message:  "check." + id + "." + problem,
			Params:   params,
		})
	}
	if n := utf8.RuneCountInString(text); n < minLength {
		add("too_short", map[string]any{"length": n, "min": minLength})
	}
	if !when.MatchString(text) {
		add("no_when", nil)
	}
	return found, nil
}
