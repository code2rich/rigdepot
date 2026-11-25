// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/manifest"
	"github.com/code2rich/rigdepot/internal/sandbox"
)

// Preview carries out a plan in a sandbox, a temporary copy of what it touches, and returns
// what changed there, translated back to the real paths. Every change of the plan is carried
// out, including global changes the person has not agreed to yet: the preview is what they are
// asked about. Exec operations are never in a plan yet.
//
// The operations are exactly those of Plan.Items, in the same order Session.Apply carries them
// out, by an executor rooted in the sandbox instead of the home directory.
func (e *Engine) Preview(ctx context.Context, p *Plan) (sandbox.Result, error) {
	var ops []adapter.Op
	touches := append([]string(nil), p.touches...)
	for _, it := range p.Items {
		switch {
		case it.Action.Writes():
			ops = append(ops, it.Op)
		case it.Action == ActionRevert:
			// The directories the entry created are removed when they end up empty; the copy
			// has to hold everything else in them for that to come out the same.
			touches = append(touches, it.Entry.Path)
			touches = append(touches, it.Entry.Created...)
		}
	}
	opts := sandbox.Options{TmpDir: e.data.Join(home.TmpDir), Home: e.sys.Home, Credentials: p.credentials}
	return sandbox.Preview(ctx, opts, sandbox.Request{Ops: ops, Touches: touches}, &previewApplier{e: e, p: p})
}

// previewApplier carries out a plan in a sandbox. It is the sandbox.Applier of Preview.
type previewApplier struct {
	e *Engine
	p *Plan
}

func (a *previewApplier) Apply(ctx context.Context, box *sandbox.Sandbox, ops []adapter.Op) error {
	inBox := func(p string) (string, error) {
		if p == "~" || strings.HasPrefix(p, "~/") {
			return p, nil
		}
		q, err := box.Path(p)
		return filepath.ToSlash(q), err
	}
	var extra []string
	for _, r := range a.p.extra {
		q, err := box.Path(filepath.ToSlash(r))
		if err != nil {
			continue
		}
		if fi, err := os.Stat(q); err == nil && fi.IsDir() {
			extra = append(extra, q)
		}
	}
	data, err := home.At(box.DataDir())
	if err != nil {
		return err
	}
	ex, err := a.e.newExec(executor.Roots{Home: box.Home(), Extra: extra}, executor.State{Data: data, Now: a.e.now})
	if err != nil {
		return err
	}
	defer ex.Close()

	i := 0
	for _, it := range a.p.Items {
		switch {
		case it.Action == ActionRevert:
			en := *it.Entry
			if en.Path, err = inBox(en.Path); err != nil {
				return err
			}
			en.Created = nil
			for _, c := range it.Entry.Created {
				if q, err := inBox(c); err == nil {
					en.Created = append(en.Created, q)
				}
			}
			if _, err := ex.Revert(ctx, en); err != nil {
				return fmt.Errorf("undoing %s in the preview: %w", it.ID, err)
			}
		case it.Action.Writes():
			if i >= len(ops) {
				return fmt.Errorf("the sandbox handed over %d operations for more writes", len(ops))
			}
			op := ops[i]
			i++
			roots := map[string]string{}
			for k, r := range a.p.roots[it.Tool] {
				if q, err := inBox(r); err == nil {
					roots[k] = q
				}
			}
			tool := executor.Tool{ID: it.Tool, Roots: roots}
			pre := ex.Observe(executor.TargetOf(op))
			var en *manifest.Entry
			if it.Entry != nil {
				mapped := *it.Entry
				if mapped.Path, err = inBox(mapped.Path); err != nil {
					return err
				}
				mapped.Created = nil
				for _, c := range it.Entry.Created {
					if q, err := inBox(c); err == nil {
						mapped.Created = append(mapped.Created, q)
					}
				}
				en = &mapped
			}
			if _, err := write(ctx, ex, tool, it, op, en, pre); err != nil {
				return fmt.Errorf("writing %s in the preview: %w", it.ID, err)
			}
		}
	}
	if i != len(ops) {
		return fmt.Errorf("the sandbox handed over %d operations for %d writes", len(ops), i)
	}
	return nil
}

var _ sandbox.Applier = (*previewApplier)(nil)
