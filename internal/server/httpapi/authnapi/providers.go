// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authnapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// LoginStateCookie carries the encrypted state of a sign-in through an
// identity source from its start to its callback. It is only sent to the
// sign-in paths and lasts as long as the sign-in may take.
const LoginStateCookie = brand.CLI + "_login"

func (h *Ops) loginCookie(value string, maxAge int) string {
	c := http.Cookie{
		Name: LoginStateCookie, Value: value, Path: strings.TrimSuffix(h.path, "/") + "/api/v1/auth/providers/",
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	}
	return c.String()
}

// page is the absolute address of a console page.
func (h *Ops) page(path string) string { return h.base + path }

// loginError sends the browser to the login page with the code of a refused
// sign-in; a fault is logged and shown as auth.provider_failed.
func (h *Ops) loginError(ctx context.Context, err error) string {
	code := authn.LoginFailed
	var le *authn.LoginError
	if errors.As(err, &le) {
		code = le.Code
		if le.Err != nil {
			h.log.WarnContext(ctx, "sign-in through an identity provider refused", "code", le.Code, "err", le.Err)
		}
	} else {
		h.log.ErrorContext(ctx, "sign-in through an identity provider failed", "err", err)
	}
	return h.page("/login?error=" + url.QueryEscape(code))
}

// StartProviderLogin implements httpapi.StrictServerInterface.
func (h *Ops) StartProviderLogin(ctx context.Context, req httpapi.StartProviderLoginRequestObject) (httpapi.StartProviderLoginResponseObject, error) {
	returnTo := ""
	if req.Params.ReturnTo != nil {
		returnTo = *req.Params.ReturnTo
	}
	start, err := h.svc.BeginProviderLogin(ctx, req.ProviderId, returnTo)
	if err != nil {
		loc := h.loginError(ctx, err)
		return httpapi.StartProviderLogin302Response{Headers: httpapi.StartProviderLogin302ResponseHeaders{Location: &loc}}, nil
	}
	cookie := h.loginCookie(start.Cookie, int(authn.LoginStateTTL/time.Second))
	setNoStore(ctx)
	return httpapi.StartProviderLogin302Response{Headers: httpapi.StartProviderLogin302ResponseHeaders{Location: &start.RedirectURL, SetCookie: &cookie}}, nil
}

// FinishProviderLogin implements httpapi.StrictServerInterface. The session
// cookie and the removal of the sign-in state cookie both go out as headers
// of their own (the generated response has room for one Set-Cookie).
func (h *Ops) FinishProviderLogin(ctx context.Context, req httpapi.FinishProviderLoginRequestObject) (httpapi.FinishProviderLoginResponseObject, error) {
	setNoStore(ctx)
	setCookie(ctx, h.loginCookie("", -1))
	state := ""
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		if c, err := r.Cookie(LoginStateCookie); err == nil {
			state = c.Value
		}
	}
	query := url.Values{}
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		query = r.URL.Query()
	}
	in, err := h.svc.CompleteProviderLogin(ctx, req.ProviderId, state, query)
	if err != nil {
		loc := h.loginError(ctx, err)
		return httpapi.FinishProviderLogin302Response{Headers: httpapi.FinishProviderLogin302ResponseHeaders{Location: &loc}}, nil
	}
	logging.SetUser(ctx, in.User.ID)
	setCookie(ctx, h.sessionCookie(in.SignedIn))
	loc := h.page("/")
	switch {
	case in.User.Status == access.StatusPending:
		loc = h.page("/apply")
	case in.ReturnTo != "":
		loc = h.page(in.ReturnTo)
	}
	return httpapi.FinishProviderLogin302Response{Headers: httpapi.FinishProviderLogin302ResponseHeaders{Location: &loc}}, nil
}

func setNoStore(ctx context.Context) {
	if h, ok := ctx.Value(headerKey{}).(http.Header); ok {
		h.Set("Cache-Control", "no-store")
	}
}

func (h *Ops) identityProvider(p authn.Provider) httpapi.IdentityProvider {
	out := httpapi.IdentityProvider{
		Id: p.ID, Kind: p.Kind, Name: p.Name, Enabled: p.Enabled, Config: p.Config, HasClientSecret: p.HasClientSecret, Etag: p.ETag,
	}
	if p.ID != authn.LocalProviderID {
		cb := h.svc.CallbackURL(p.ID)
		out.CallbackUrl = &cb
	}
	return out
}

// ListIdentityProviders implements httpapi.StrictServerInterface.
func (h *Ops) ListIdentityProviders(ctx context.Context, _ httpapi.ListIdentityProvidersRequestObject) (httpapi.ListIdentityProvidersResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := h.svc.ListProviders(ctx, c)
	if err != nil {
		return nil, fail(ctx, err)
	}
	out := httpapi.ListIdentityProviders200JSONResponse{Items: make([]httpapi.IdentityProvider, len(ps))}
	for i, p := range ps {
		out.Items[i] = h.identityProvider(p)
	}
	return out, nil
}

// ListIdentityProviderKinds implements httpapi.StrictServerInterface: the
// kinds compiled in, with the schemas their forms are drawn from. Platform
// administrators only, like the methods themselves.
func (h *Ops) ListIdentityProviderKinds(ctx context.Context, _ httpapi.ListIdentityProviderKindsRequestObject) (httpapi.ListIdentityProviderKindsResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	// The same permission as listing the methods.
	if _, err := h.svc.ListProviders(ctx, c); err != nil {
		return nil, fail(ctx, err)
	}
	out := httpapi.ListIdentityProviderKinds200JSONResponse{Items: []httpapi.IdentityProviderKind{}}
	for _, k := range h.svc.Kinds() {
		var schema map[string]any
		if err := json.Unmarshal(k.ConfigSchema(), &schema); err != nil {
			return nil, err
		}
		out.Items = append(out.Items, httpapi.IdentityProviderKind{Kind: k.ID(), Name: k.Name(), ConfigSchema: schema})
	}
	return out, nil
}

// CreateIdentityProvider implements httpapi.StrictServerInterface.
func (h *Ops) CreateIdentityProvider(ctx context.Context, req httpapi.CreateIdentityProviderRequestObject) (httpapi.CreateIdentityProviderResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	p, err := h.svc.CreateProvider(ctx, c, authn.NewProvider{
		Kind: req.Body.Kind, Name: req.Body.Name, Enabled: req.Body.Enabled, Config: req.Body.Config, ClientSecret: req.Body.ClientSecret,
	})
	if err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.CreateIdentityProvider201JSONResponse{Body: h.identityProvider(p), Headers: httpapi.CreateIdentityProvider201ResponseHeaders{ETag: &p.ETag}}, nil
}

// GetIdentityProvider implements httpapi.StrictServerInterface.
func (h *Ops) GetIdentityProvider(ctx context.Context, req httpapi.GetIdentityProviderRequestObject) (httpapi.GetIdentityProviderResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.svc.GetProvider(ctx, c, req.ProviderId)
	if err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.GetIdentityProvider200JSONResponse{Body: h.identityProvider(p), Headers: httpapi.GetIdentityProvider200ResponseHeaders{ETag: &p.ETag}}, nil
}

// UpdateIdentityProvider implements httpapi.StrictServerInterface.
func (h *Ops) UpdateIdentityProvider(ctx context.Context, req httpapi.UpdateIdentityProviderRequestObject) (httpapi.UpdateIdentityProviderResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	ch := authn.ProviderChange{Name: req.Body.Name, Enabled: req.Body.Enabled, ClientSecret: req.Body.ClientSecret}
	if req.Body.Config != nil {
		ch.Config = *req.Body.Config
	}
	p, err := h.svc.UpdateProvider(ctx, c, req.ProviderId, req.Params.IfMatch, ch)
	if err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.UpdateIdentityProvider200JSONResponse{Body: h.identityProvider(p), Headers: httpapi.UpdateIdentityProvider200ResponseHeaders{ETag: &p.ETag}}, nil
}

// DeleteIdentityProvider implements httpapi.StrictServerInterface.
func (h *Ops) DeleteIdentityProvider(ctx context.Context, req httpapi.DeleteIdentityProviderRequestObject) (httpapi.DeleteIdentityProviderResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteProvider(ctx, c, req.ProviderId); err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.DeleteIdentityProvider204Response{}, nil
}

// TestIdentityProvider implements httpapi.StrictServerInterface.
func (h *Ops) TestIdentityProvider(ctx context.Context, req httpapi.TestIdentityProviderRequestObject) (httpapi.TestIdentityProviderResponseObject, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.TestProvider(ctx, c, req.ProviderId)
	if err != nil {
		return nil, fail(ctx, err)
	}
	out := httpapi.TestIdentityProvider200JSONResponse{Ok: res.OK}
	if res.Step != "" {
		out.Step = &res.Step
	}
	if res.Detail != "" {
		out.Detail = &res.Detail
	}
	return out, nil
}

// LoginMethods returns the enabled sign-in methods for getMeta.
func (h *Ops) LoginMethods(ctx context.Context) ([]httpapi.LoginMethod, error) {
	ms, err := h.svc.LoginMethods(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.LoginMethod, len(ms))
	for i, m := range ms {
		out[i] = httpapi.LoginMethod{Id: m.ID, Kind: m.Kind, Name: m.Name}
	}
	return out, nil
}
