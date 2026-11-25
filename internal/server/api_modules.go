// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/modhost"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// modulesAPI implements listModules and updateModule: the optional modules compiled into this
// server and their switches. The host (modhost) starts and stops them; the switch itself is a
// setting, checked and audited by the settings package.
type modulesAPI struct {
	host     *modhost.Host // nil while nothing runs in the background: 501
	settings *settings.Service
	// text renders a module's message keys in the server's default language.
	text *i18n.Printer
}

var errModuleNotFound = httpapi.NewProblem(http.StatusNotFound, "module.not_found", "This server was built without that module.")

func (m *modulesAPI) module(s modhost.Status) httpapi.Module {
	t := func(key string) string {
		if key == "" || m.text == nil {
			return key
		}
		return m.text.T(key)
	}
	out := httpapi.Module{Id: s.ID, Name: t(s.Info.Name), Description: t(s.Info.Description), Enabled: s.Enabled, Locked: s.Locked}
	if n := t(s.Info.Notice); n != "" {
		out.Notice = &n
	}
	return out
}

// label is a module's name for the audit log.
func (m *modulesAPI) label(id string) string {
	for _, s := range m.host.Modules() {
		if s.ID == id {
			return m.module(s).Name
		}
	}
	return id
}

func (m *modulesAPI) ListModules(ctx context.Context, _ httpapi.ListModulesRequestObject) (httpapi.ListModulesResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if m.host == nil {
		return nil, httpapi.ErrNotImplemented
	}
	// Whoever may see the platform settings may see the modules.
	if _, err := m.settings.Platform(ctx, actor); err != nil {
		return nil, settingsProblem(err)
	}
	out := httpapi.ListModules200JSONResponse{Items: []httpapi.Module{}}
	for _, s := range m.host.Modules() {
		out.Items = append(out.Items, m.module(s))
	}
	return out, nil
}

func (m *modulesAPI) UpdateModule(ctx context.Context, req httpapi.UpdateModuleRequestObject) (httpapi.UpdateModuleResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if m.host == nil {
		return nil, httpapi.ErrNotImplemented
	}
	if req.Body == nil {
		return nil, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has no body.")
	}
	id, on := req.ModuleId, req.Body.Enabled
	var status *modhost.Status
	for _, s := range m.host.Modules() {
		if s.ID == id {
			status = &s
		}
	}
	if status == nil {
		return nil, errModuleNotFound
	}
	// Ask before the host starts anything: it starts a module first and records the switch
	// after, and the recording checks again.
	if err := m.settings.AuthorizeModule(ctx, actor, id, m.label(id), on); err != nil {
		return nil, settingsProblem(err)
	}
	var err error
	if on {
		err = m.host.Enable(ctx, id)
	} else {
		err = m.host.Disable(ctx, id)
	}
	switch {
	case errors.Is(err, modhost.ErrUnknown):
		return nil, errModuleNotFound
	case errors.Is(err, modhost.ErrLocked):
		return nil, httpapi.NewProblem(http.StatusConflict, "module.locked", "The deployment disabled this module; it cannot be turned on here.")
	case errors.Is(err, settings.ErrDenied):
		return nil, errDenied
	case errors.Is(err, modhost.ErrStart):
		return nil, httpapi.NewProblem(http.StatusInternalServerError, "module.start_failed", "The module did not start. The server log says why.")
	case err != nil:
		return nil, err
	}
	for _, s := range m.host.Modules() {
		if s.ID == id {
			return httpapi.UpdateModule200JSONResponse(m.module(s)), nil
		}
	}
	return nil, errModuleNotFound
}
