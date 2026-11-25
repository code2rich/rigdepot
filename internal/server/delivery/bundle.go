// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package delivery

import (
	"context"
	"errors"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/catalog"
)

// Bundle returns the bundle stored at address (catalog.Service.Bundle) when the actor sees a
// published resource with a version in it: a live version, or an earlier one, which a computer
// may still be asking for while the next one is published. Otherwise it is ErrNotFound, the
// same as a bundle that does not exist.
func (s *Service) Bundle(ctx context.Context, a access.Actor, address string) ([]byte, error) {
	if a.UserID == "" {
		return nil, ErrDenied
	}
	if !resource.Digest(address).Valid() {
		return nil, ErrNotFound
	}
	rs, err := s.catalog.BundleResources(ctx, address)
	if err != nil {
		return nil, err
	}
	v, err := s.access.VisibilityOf(ctx, a)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if r.Visible(v) {
			data, err := s.catalog.Bundle(ctx, address)
			if errors.Is(err, catalog.ErrNotFound) {
				return nil, ErrNotFound
			}
			return data, err
		}
	}
	return nil, ErrNotFound
}
