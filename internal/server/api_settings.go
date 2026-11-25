// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// settingsAPI implements the operations tagged settings: the platform
// settings and the test mail. Listing and switching modules (listModules,
// updateModule) are modulesAPI's (api_modules.go).
type settingsAPI struct {
	settings *settings.Service
	hooks    *hooks
}

// settingsProblem maps the settings package's errors to problem documents.
// Errors it does not know stay as they are (500).
func settingsProblem(err error) error {
	var invalid *settings.ValidationError
	switch {
	case errors.Is(err, settings.ErrDenied):
		return errDenied
	case errors.Is(err, settings.ErrPreconditionFailed):
		return httpapi.NewProblem(http.StatusPreconditionFailed, "precondition.failed", "This changed after you opened it. Reload to see the latest.")
	case errors.As(err, &invalid):
		p := httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has invalid fields.")
		p.Problem.Errors = &[]httpapi.FieldError{{Field: invalid.Field, Code: invalid.Code}}
		return p
	case errors.Is(err, settings.ErrSetupClosed):
		return errSetupClosed
	case errors.Is(err, settings.ErrSetupToken):
		return httpapi.NewProblem(http.StatusForbidden, "setup.token_invalid", "The setup token is not the one in the server log.")
	case errors.Is(err, settings.ErrSetupRateLimited):
		return httpapi.NewProblem(http.StatusTooManyRequests, "rate.limited", "Too many wrong setup tokens. Try again in a few minutes.")
	case errors.Is(err, settings.ErrAdminExists):
		return httpapi.NewProblem(http.StatusConflict, "setup.admin_exists", "The first administrator has already been created.")
	}
	return err
}

func platformSettings(p settings.Platform) httpapi.PlatformSettings {
	var out httpapi.PlatformSettings
	out.Etag = p.ETag
	out.Organization.Name = p.OrganizationName
	m := p.Mail
	out.Mail = httpapi.MailSettings{Enabled: m.Enabled, HasPassword: p.MailPasswordSet}
	if m.Host != "" {
		out.Mail.Host = &m.Host
	}
	if m.Port != 0 {
		out.Mail.Port = &m.Port
	}
	if m.Security != "" {
		sec := httpapi.MailSettingsSecurity(m.Security)
		out.Mail.Security = &sec
	}
	if m.Username != "" {
		out.Mail.Username = &m.Username
	}
	if m.From != "" {
		from := openapi_types.Email(m.From)
		out.Mail.From = &from
	}
	return out
}

func (s *settingsAPI) GetSettings(ctx context.Context, _ httpapi.GetSettingsRequestObject) (httpapi.GetSettingsResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	p, err := s.settings.Platform(ctx, actor)
	if err != nil {
		return nil, settingsProblem(err)
	}
	return httpapi.GetSettings200JSONResponse{Body: platformSettings(p), Headers: httpapi.GetSettings200ResponseHeaders{ETag: &p.ETag}}, nil
}

func (s *settingsAPI) UpdateSettings(ctx context.Context, req httpapi.UpdateSettingsRequestObject) (httpapi.UpdateSettingsResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if actor.Origin != audit.OriginWeb {
		// It can carry the mail password, and secrets are only entered in
		// the browser (docs/subsystems/secrets.md).
		return nil, httpapi.NewProblem(http.StatusForbidden, "access.denied", "Platform settings are changed in the browser.")
	}
	if req.Body == nil {
		return nil, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has no body.")
	}
	var c settings.PlatformChange
	if o := req.Body.Organization; o != nil {
		c.OrganizationName = o.Name
	}
	if m := req.Body.Mail; m != nil {
		c.Mail = &settings.MailChange{Enabled: m.Enabled, Host: m.Host, Port: m.Port, Username: m.Username, Password: m.Password}
		if m.Security != nil {
			sec := string(*m.Security)
			c.Mail.Security = &sec
		}
		if m.From != nil {
			from := string(*m.From)
			c.Mail.From = &from
		}
	}
	p, err := s.settings.UpdatePlatform(ctx, actor, req.Params.IfMatch, c)
	if err != nil {
		return nil, settingsProblem(err)
	}
	return httpapi.UpdateSettings200JSONResponse{Body: platformSettings(p), Headers: httpapi.UpdateSettings200ResponseHeaders{ETag: &p.ETag}}, nil
}

func (s *settingsAPI) SendTestMail(ctx context.Context, req httpapi.SendTestMailRequestObject) (httpapi.SendTestMailResponseObject, error) {
	actor, ok := callerFrom(ctx)
	if !ok {
		return nil, errSignInRequired
	}
	if s.hooks.sendTestMail == nil {
		return nil, httpapi.ErrNotImplemented
	}
	if req.Body == nil {
		return nil, httpapi.NewProblem(http.StatusBadRequest, httpapi.CodeRequestInvalid, "The request has no body.")
	}
	// Whoever may see the settings may try them.
	if _, err := s.settings.Platform(ctx, actor); err != nil {
		return nil, settingsProblem(err)
	}
	server, err := s.settings.MailServer(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.hooks.sendTestMail(ctx, server, string(req.Body.To)); err != nil {
		detail := logging.Redact(err.Error())
		if server.Password != "" {
			detail = strings.ReplaceAll(detail, server.Password, logging.Redacted)
		}
		return httpapi.SendTestMail502ApplicationProblemPlusJSONResponse{Type: "about:blank", Title: http.StatusText(http.StatusBadGateway),
			Status: http.StatusBadGateway, Code: "mail.failed", Detail: &detail}, nil
	}
	return httpapi.SendTestMail204Response{}, nil
}
