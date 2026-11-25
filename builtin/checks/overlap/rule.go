// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package overlap

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"unicode"

	"github.com/code2rich/rigdepot/builtin/checks/internal/skillmd"
	"github.com/code2rich/rigdepot/ext/check"
	"github.com/code2rich/rigdepot/ext/resource"
)

const id = "overlap"

// threshold is the similarity, from 0 to 1, from which two descriptions are reported.
const threshold = 0.6

type rule struct{}

// New returns the rule.
func New() check.Rule { return rule{} }

func (rule) ID() string               { return id }
func (rule) Severity() check.Severity { return check.Advisory }
func (rule) Applies(kind string) bool { return kind == resource.KindSkill }

func (rule) Check(_ context.Context, in check.Input) ([]check.Finding, error) {
	fm, err := skillmd.Read(in.Files)
	var bad *skillmd.Error
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.As(err, &bad):
		return nil, nil // reported by the structure rule
	case err != nil:
		return nil, err
	}
	own := terms(fm.Description.Value)
	if len(own) == 0 {
		return nil, nil
	}
	var found []check.Finding
	for _, p := range in.Peers {
		if p.Ref.Kind != resource.KindSkill || p.Ref.Code == in.Ref.Code {
			continue
		}
		s := similarity(own, terms(p.Description))
		if s < threshold {
			continue
		}
		found = append(found, check.Finding{
			Rule:     id,
			Severity: check.Advisory,
			File:     skillmd.File,
			Line:     fm.Description.Line,
			Message:  "check." + id + ".similar",
			Params:   map[string]any{"peer": p.Ref.Code, "similarity": int(s*100 + 0.5)},
		})
	}
	return found, nil
}

// stopwords carry no meaning of their own in an English description.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "of": true, "and": true, "or": true, "for": true,
	"in": true, "on": true, "with": true, "when": true, "use": true, "this": true, "it": true,
	"is": true, "are": true, "be": true, "that": true, "you": true, "your": true, "by": true,
}

// terms returns the set of what a description says: lower-case English words, and each pair of
// adjacent Han characters.
func terms(text string) map[string]bool {
	out := map[string]bool{}
	var word []rune
	var prevHan rune
	flush := func() {
		if w := string(word); len(word) > 1 && !stopwords[w] {
			out[w] = true
		}
		word = word[:0]
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			if prevHan != 0 {
				out[string([]rune{prevHan, r})] = true
			}
			prevHan = r
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word = append(word, r)
		default:
			flush()
		}
		prevHan = 0
	}
	flush()
	return out
}

// similarity is the Jaccard index of two sets: what they share over what either has.
func similarity(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for t := range a {
		if b[t] {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}
