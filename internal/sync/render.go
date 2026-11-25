// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package sync

import (
	"errors"
	"fmt"

	"github.com/code2rich/rigdepot/ext/adapter"
	"github.com/code2rich/rigdepot/ext/kind"
	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/desired"
)

// Degraded is a resource delivered to a tool in a fallback form.
type Degraded struct {
	Resource resource.Ref
	// Note is the message key of the explanation, such as "workflow.as_skill".
	Note string
}

// rendered is every resource of the desired state, rendered for one tool.
type rendered struct {
	artifacts []adapter.Artifact
	degraded  []Degraded
	// skips are the resources no form of their kind fits the tool.
	skips []adapter.Skip
	// failed are the resources whose rendering failed: unknown for this tool.
	failed map[string]error
}

// render picks, for each resource, the first form of its kind that the tool's capabilities
// satisfy and whose Render does not say ErrNotApplicable. That order is the fallback rule.
func render(resources []desired.Resource, caps adapter.Capabilities) rendered {
	out := rendered{failed: map[string]error{}}
	for _, r := range resources {
		if r.Kind == nil {
			out.failed[r.Ref.Code] = errors.New("the resource has no kind")
			continue
		}
		delivered := false
		for _, f := range r.Kind.Forms() {
			if !f.Needs.SatisfiedBy(caps) || f.Render == nil {
				continue
			}
			arts, err := f.Render(r.Spec, caps, kind.RenderOptions{Prefix: brand.Prefix})
			if errors.Is(err, kind.ErrNotApplicable) {
				continue
			}
			if err == nil {
				err = checkArtifacts(r.Ref, arts)
			}
			if err != nil {
				out.failed[r.Ref.Code] = err
				delivered = true
				break
			}
			out.artifacts = append(out.artifacts, arts...)
			if f.Degraded != "" {
				out.degraded = append(out.degraded, Degraded{Resource: r.Ref, Note: f.Degraded})
			}
			delivered = true
			break
		}
		if !delivered {
			out.skips = append(out.skips, adapter.Skip{Resource: r.Ref, Reason: adapter.Unsupported,
				Detail: fmt.Sprintf("no form of the %s kind fits what the tool takes", r.Ref.Kind)})
		}
	}
	return out
}

// checkArtifacts refuses artifacts that do not belong to the resource they were rendered from:
// the manifest records operations by resource, and an artifact of another resource would be
// recorded, and undone, under the wrong one.
func checkArtifacts(ref resource.Ref, arts []adapter.Artifact) error {
	for _, a := range arts {
		if a == nil {
			return errors.New("the kind rendered a nil artifact")
		}
		if a.Ref().Code != ref.Code {
			return fmt.Errorf("the kind rendered an artifact of %s for %s", a.Ref(), ref)
		}
	}
	return nil
}
