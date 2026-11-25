// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/secrets"
)

// secretsAPI implements the personal secrets: listMySecrets, setMySecret, deleteMySecret and
// getSealedSecrets. No operation returns a value: the list says whether each is set, and the
// device gets the values sealed to its own public key.
type secretsAPI struct {
	secrets *secrets.Service
	cat     *catalog.Service
	// publicKey returns the public key a live device of the user signed in with
	// (authn.Devices.DevicePublicKey).
	publicKey func(ctx context.Context, userID, deviceID string) (string, error)
}

// secretsProblem maps the secrets package's errors to problem documents.
func secretsProblem(err error) error {
	switch {
	case errors.Is(err, secrets.ErrDenied):
		return errDenied
	case errors.Is(err, secrets.ErrNotFound):
		return errSecretNotFound
	case errors.Is(err, secrets.ErrInvalidName):
		return invalidField("secret_name", "pattern")
	case errors.Is(err, secrets.ErrInvalidValue):
		return invalidField("value", "invalid")
	}
	return err
}

var errSecretNotFound = httpapi.NewProblem(http.StatusNotFound, "object.not_found",
	"No resource you can see uses this secret, or you have not set it.")

func invalidField(field, code string) error {
	p := httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has invalid fields.")
	p.Problem.Errors = &[]httpapi.FieldError{{Field: field, Code: code}}
	return p
}

// needed returns, by secret name, the resources visible to a that refer to it.
func (s *secretsAPI) needed(ctx context.Context, a access.Actor) (map[string][]httpapi.ResourceRef, error) {
	rs, err := s.cat.Catalog(ctx, a, catalog.CatalogFilter{})
	if err != nil {
		return nil, err
	}
	out := map[string][]httpapi.ResourceRef{}
	for _, r := range rs {
		for _, n := range r.Meta.Secrets {
			out[n] = append(out[n], httpapi.ResourceRef{Code: r.Code, Kind: r.Kind, Name: r.Name})
		}
	}
	for _, refs := range out {
		slices.SortFunc(refs, func(x, y httpapi.ResourceRef) int { return cmp.Compare(x.Code, y.Code) })
	}
	return out, nil
}

// mySecret is one item of the list.
func mySecret(name string, used []httpapi.ResourceRef, d secrets.Def, v secrets.Value, set bool) httpapi.MySecret {
	out := httpapi.MySecret{Name: name, Label: cmp.Or(d.Label, name), UsedBy: used, Configured: set}
	if out.UsedBy == nil {
		out.UsedBy = []httpapi.ResourceRef{}
	}
	if d.Howto != "" {
		out.Howto = &d.Howto
	}
	if set {
		out.UpdatedAt = &v.UpdatedAt
		if !v.ExpiresAt.IsZero() {
			out.ExpiresAt = &v.ExpiresAt
		}
	}
	return out
}

func (s *secretsAPI) ListMySecrets(ctx context.Context, _ httpapi.ListMySecretsRequestObject) (httpapi.ListMySecretsResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	need, err := s.needed(ctx, a)
	if err != nil {
		return nil, err
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range need {
			if !yield(n) {
				return
			}
		}
	})
	defs, err := s.secrets.Defs(ctx, names)
	if err != nil {
		return nil, err
	}
	values, err := s.secrets.Values(ctx, a.UserID)
	if err != nil {
		return nil, err
	}
	out := httpapi.MySecretList{Items: []httpapi.MySecret{}}
	for _, n := range names {
		v, set := values[n]
		out.Items = append(out.Items, mySecret(n, need[n], defs[n], v, set))
	}
	return httpapi.ListMySecrets200JSONResponse(out), nil
}

func (s *secretsAPI) SetMySecret(ctx context.Context, req httpapi.SetMySecretRequestObject) (httpapi.SetMySecretResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if a.TokenID != "" {
		return nil, errDenied
	}
	if req.Body == nil {
		return nil, invalidField("value", "required")
	}
	name := req.SecretName
	if !resource.ValidSecretName(name) {
		return nil, secretsProblem(secrets.ErrInvalidName)
	}
	need, err := s.needed(ctx, a)
	if err != nil {
		return nil, err
	}
	// Only names a resource the person can see asks for: the server is not a store for
	// anything else.
	if _, ok := need[name]; !ok {
		return nil, errSecretNotFound
	}
	var expires time.Time
	if req.Body.ExpiresAt != nil {
		expires = *req.Body.ExpiresAt
	}
	v, err := s.secrets.Set(ctx, a, name, []byte(req.Body.Value), expires)
	if err != nil {
		return nil, secretsProblem(err)
	}
	defs, err := s.secrets.Defs(ctx, []string{name})
	if err != nil {
		return nil, err
	}
	return httpapi.SetMySecret200JSONResponse(mySecret(name, need[name], defs[name], v, true)), nil
}

func (s *secretsAPI) DeleteMySecret(ctx context.Context, req httpapi.DeleteMySecretRequestObject) (httpapi.DeleteMySecretResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.secrets.Delete(ctx, a, req.SecretName); err != nil {
		return nil, secretsProblem(err)
	}
	return httpapi.DeleteMySecret204Response{}, nil
}

func (s *secretsAPI) GetSealedSecrets(ctx context.Context, req httpapi.GetSealedSecretsRequestObject) (httpapi.GetSealedSecretsResponseObject, error) {
	a, err := catalogCaller(ctx)
	if err != nil {
		return nil, err
	}
	// Device tokens only (the contract's security list): the values are sealed to the device's
	// key, so there has to be one.
	if a.DeviceID == "" || a.TokenID != "" {
		return nil, errDenied
	}
	key, err := s.publicKey(ctx, a.UserID, a.DeviceID)
	if errors.Is(err, authn.ErrNotFound) {
		return nil, errSignInRequired
	}
	if err != nil {
		return nil, err
	}
	var names []string
	if req.Body != nil && req.Body.Names != nil {
		names = *req.Body.Names
	}
	items, missing, err := s.secrets.SealFor(ctx, a, secrets.Device{ID: a.DeviceID, PublicKey: key}, names)
	if err != nil {
		return nil, secretsProblem(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	out := httpapi.SealedSecrets{DeviceId: a.DeviceID, Suite: httpapi.SealedSecretsSuite(secrets.Suite), Items: []httpapi.SealedSecret{}, Missing: []string{}}
	for _, it := range items {
		v := httpapi.SealedSecret{Name: it.Name, Version: it.Version, Enc: enc(it.Enc), Ciphertext: enc(it.Ciphertext), Info: it.Info}
		if !it.ExpiresAt.IsZero() {
			v.ExpiresAt = &it.ExpiresAt
		}
		out.Items = append(out.Items, v)
	}
	out.Missing = append(out.Missing, missing...)
	return httpapi.GetSealedSecrets200JSONResponse(out), nil
}

// useSecrets keeps resource_secrets in step with the catalog: when a resource is published or
// changed on the platform, the secrets its content refers to are recorded as used by it (and
// defined, if nobody defined them yet). It runs after the catalog's change committed.
func useSecrets(cat *catalog.Service, sec *secrets.Service) events.Handler {
	return func(ctx context.Context, ev events.Event) error {
		var ref events.ResourceRef
		switch p := ev.Payload.(type) {
		case events.ResourcePublished:
			ref = p.ResourceRef
		case events.ResourceUpdated:
			ref = p.ResourceRef
		default:
			return nil
		}
		r, err := cat.Get(ctx, access.System, ref.Code)
		if err != nil {
			return err
		}
		return sec.UseSecrets(ctx, r.ID, r.Meta.Secrets)
	}
}
