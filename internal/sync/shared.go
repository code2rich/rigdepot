// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"path"
	"slices"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/internal/executor"
	"github.com/code2rich/rigdepot/internal/manifest"
)

// dedupe makes sure a resource does not show up twice in one tool, after every tool has planned:
//
//   - A tool B that also reads skill directory D (Profile.AlsoReads) loses its own WriteTree of a
//     resource when another tool A writes that resource into D. B reads A's copy.
//   - Two tools that would write the very same target: the second one loses its operation.
//
// Tools are visited in identifier order and only operations still in a plan count, so two tools
// that read each other's directories do not both lose their copy. It returns, for each target
// that is read by more tools than the one that wrote it, the manifest entry id and those tools;
// that becomes the entry's sharedWith.
func dedupe(tps []*toolPlan) map[string][]string {
	shared := map[string][]string{}
	planned := func(tp *toolPlan) bool { return tp.info.Status == ToolPlanned }
	for _, b := range tps {
		b.shared = shared
		if !planned(b) {
			continue
		}
		var dirs []string
		for _, sd := range b.t.profile.AlsoReads {
			if sd.Kind != "skill" {
				continue
			}
			d, err := adapter.ExpandPath(sd.Dir, b.t.det.Roots)
			if err != nil {
				d = sd.Dir
			}
			dirs = append(dirs, path.Clean(d))
		}
		b.ops = slices.DeleteFunc(b.ops, func(op adapter.Op) bool {
			mine := executor.TargetOf(op)
			for _, a := range tps {
				if a == b || !planned(a) {
					continue
				}
				for _, other := range a.ops {
					theirs := executor.TargetOf(other)
					if other.Ref().Code != op.Ref().Code || theirs.Op != mine.Op {
						continue
					}
					same := theirs.Path == mine.Path && slices.Equal(theirs.Key, mine.Key)
					read := mine.Op == manifest.OpTree && slices.Contains(dirs, path.Dir(theirs.Path))
					if !same && !read {
						continue
					}
					id := manifest.EntryID(a.t.id(), theirs.Op, theirs.Path, theirs.Key)
					if !slices.Contains(shared[id], b.t.id()) {
						shared[id] = append(shared[id], b.t.id())
						slices.Sort(shared[id])
					}
					b.info.Shared = append(b.info.Shared, Shared{Resource: op.Ref(), Via: a.t.id(), Path: theirs.Path})
					return true
				}
			}
			return false
		})
	}
	return shared
}

// sharedWith returns the tools that read the target with the given entry id as well.
func (tp *toolPlan) sharedWith(id string) []string {
	return slices.Clone(tp.shared[id])
}
