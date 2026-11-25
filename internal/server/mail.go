// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"encoding/json"

	extnotify "github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/server/notify"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// The mail server is a platform setting (package settings) and the outbox is package notify;
// neither may import the other, so they meet here.

// mailSettings is notify.ConfigSource on the platform settings: the e-mail channel's
// configuration is the mail server an administrator saved. While mail is turned off or has no
// host, the channel is not configured and mail waits in the outbox.
type mailSettings struct{ settings *settings.Service }

var _ notify.ConfigSource = mailSettings{}

func (m mailSettings) ChannelConfig(ctx context.Context, channel string) ([]byte, bool, error) {
	if channel != notify.EmailChannel {
		return nil, false, nil
	}
	server, err := m.settings.MailServer(ctx)
	if err != nil {
		return nil, false, err // unknown: nothing is sent, nothing is marked failed
	}
	if !server.Enabled || server.Host == "" {
		return nil, false, nil
	}
	raw, err := emailChannelConfig(server)
	return raw, err == nil, err
}

// emailChannelConfig is the mail server in the shape of the e-mail channel's ConfigSchema
// (builtin/notifiers/email): host, port, security, username, password, from.
func emailChannelConfig(s settings.MailConfig) ([]byte, error) {
	cfg := map[string]any{"host": s.Host, "port": s.Port, "security": s.Security, "from": s.From}
	if s.Username != "" {
		cfg["username"] = s.Username
	}
	if s.Password != "" {
		cfg["password"] = s.Password
	}
	return json.Marshal(cfg)
}

// staticMail is a ConfigSource that always gives the same mail server, for the test mail.
type staticMail []byte

func (c staticMail) ChannelConfig(_ context.Context, channel string) ([]byte, bool, error) {
	return c, channel == notify.EmailChannel, nil
}

// testMailSender is hooks.sendTestMail on notify.SendTest. It sends with the mail server it is
// given rather than through the outbox's configuration, so that an administrator can try the
// settings before turning mail on. The message is in the server's default language: the
// caller's language is not known here.
func testMailSender(app *App, cat *i18n.Catalog, channels func(string) (extnotify.Channel, bool)) func(context.Context, settings.MailConfig, string) error {
	return func(ctx context.Context, server settings.MailConfig, to string) error {
		raw, err := emailChannelConfig(server)
		if err != nil {
			return err
		}
		svc, err := notify.New(notify.Options{DB: app.db, Config: staticMail(raw), Channels: channels, Catalog: cat,
			DefaultLocale: app.cfg.DefaultLocale, Log: app.log})
		if err != nil {
			return err
		}
		return svc.SendTest(ctx, to, "")
	}
}
