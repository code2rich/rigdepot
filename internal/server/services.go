// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/repoformat"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/catalog"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/delivery"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/server/feedback"
	"github.com/code2rich/rigdepot/internal/server/httpapi/authnapi"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/modhost"
	"github.com/code2rich/rigdepot/internal/server/secrets"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// openServices builds the domain services on the open, migrated database:
// the master key, the audit log, access, the settings and signing in, then gets the
// setup wizard ready. Other domains add theirs here, and set what they provide in
// a.hooks. When the app has background parts (a.bg, made by newBackground),
// the services publish their events on its bus; without them (tests of the
// API alone) events are dropped.
func (a *App) openServices(ctx context.Context) error {
	keys, err := loadKeys(ctx, a.cfg, a.db, true)
	if err != nil {
		return err
	}
	switch keys.Source() {
	case masterkey.FromGenerated:
		a.log.Warn("generated a new master key: back up this file now and keep the copy apart from the database backups; "+
			"without it nothing encrypted in the database can be read, and with the data directory anyone can read it",
			"file", keys.Path(), "key_id", keys.CurrentID())
	default:
		a.log.Info("master key loaded", "from", keys.Source().String(), "key_id", keys.CurrentID())
	}
	chainKey, err := settings.AuditKey(ctx, a.db, keys, true)
	if err != nil {
		return err
	}
	log, err := audit.New(a.db, chainKey)
	if err != nil {
		return err
	}
	a.keys, a.audit = keys, log
	accessOpts := access.Options{Auditor: accessAuditor{log}}
	if a.bg != nil {
		// Access publishes after its own commit; the bus hands its events
		// to notify and the modules.
		accessOpts.Events = events.Publisher[access.Event]{Bus: a.bg.Events}
	}
	a.access = access.New(a.db, accessOpts)
	if a.hooks.authorize == nil {
		a.hooks.authorize = accessAuthorizer(a.access, func(ctx context.Context, code string) (access.Target, bool, error) {
			return a.catalog.Target(ctx, code)
		})
	}
	if a.hooks.names == nil {
		a.hooks.names = accessNames{svc: a.access}
	}
	a.settings = settings.New(a.db, keys, log, settings.Options{Authorize: a.hooks.settingsAuthorizer(), SetupToken: a.cfg.SetupToken})
	// The device code flow keeps the devices and verifies their tokens;
	// tests may put a stand-in into hooks.devices for the verification.
	a.devices = authn.NewDevices(a.db, authn.DevicesOptions{Access: a.access, Audit: log})
	if a.hooks.devices == nil {
		a.hooks.devices = a.devices
	}
	if n, ok := a.hooks.names.(accessNames); ok && n.devices == nil {
		n.devices = a.devices
		a.hooks.names = n
	}
	// Identity sources: the kinds compiled in (builtin/all/server registers
	// them), their client secrets sealed with the master key.
	a.authn = authn.New(a.db, authn.Options{Access: a.access, Audit: log, Devices: a.hooks.devices,
		Keys: keys, Sources: a.identitySources, BaseURL: a.cfg.BaseURL})
	if a.authnOps, err = authnapi.New(a.authn, authnapi.Options{
		BaseURL: a.cfg.BaseURL, TrustedProxies: a.cfg.TrustedProxies, Log: a.log, Devices: a.devices,
		Protected: []string{modhost.RoutePrefix}, SignInRate: a.signInRate,
		// What each computer said at its last sync; delivery exists by the time anyone asks.
		DeviceSyncs: func(ctx context.Context, ids []string) (map[string]authnapi.DeviceSync, error) {
			return deliveryHooks{svc: a.delivery}.DeviceSyncs(ctx, ids)
		},
	}); err != nil {
		return err
	}
	if a.hooks.firstAdmin == nil {
		a.hooks.firstAdmin = a.authnOps
	}
	co := a.catalogOptions
	co.Auditor, co.Version, co.Keys = log, buildinfo.Version, keys
	if co.DefineSecrets == nil {
		// The secrets a repository defines go to the secrets domain (made below).
		co.DefineSecrets = func(ctx context.Context, defs map[string]repoformat.Secret) error {
			if a.secrets == nil {
				return errors.New("the secrets service is not ready")
			}
			return a.secrets.Define(ctx, secretDefs(defs))
		}
	}
	if a.bg != nil {
		co.Events = a.bg.Events
		jobs := a.bg.Jobs
		co.PullNow = func() { jobs.Trigger(catalog.SourceSyncJob) }
	}
	if a.catalog, err = catalog.New(a.db, a.access, co); err != nil {
		return err
	}
	fo := feedback.Options{Auditor: log}
	if a.bg != nil {
		fo.Events = a.bg.Events
	}
	if a.feedback, err = feedback.New(a.db, a.access, a.catalog, fo); err != nil {
		return err
	}
	// Delivery counts the installs of the computers that are signed in: the device code flow's.
	a.delivery = delivery.New(a.db, a.access, a.catalog, delivery.Options{Auditor: log, LiveDevices: a.devices.LiveDevices})
	if a.hooks.delivery == nil {
		a.hooks.delivery = deliveryHooks{svc: a.delivery, names: a.hooks.names}
	}
	if a.hooks.toolUsage == nil {
		a.hooks.toolUsage = deliveryHooks{svc: a.delivery}
	}
	// Personal secrets: values sealed with the master key, handed out sealed to one device.
	a.secrets = secrets.New(a.db, keys, secrets.Options{Audit: log})
	if a.hooks.secretsConfigured == nil {
		a.hooks.secretsConfigured = a.secrets.Configured
	}
	if a.hooks.secretVersions == nil {
		a.hooks.secretVersions = a.secrets.Versions
	}
	if a.bg != nil {
		// Which resource uses which secret, once a version is live or its settings changed.
		a.bg.Events.Subscribe("secrets", []string{events.ResourcePublished{}.EventName(), events.ResourceUpdated{}.EventName()},
			useSecrets(a.catalog, a.secrets))
	}

	// The master key is loaded and is the one the database was sealed with.
	a.AddReadyCheck(ReadyCheck{Name: "master_key", Critical: true, Check: func(ctx context.Context) error {
		_, err := settings.AuditKey(ctx, a.db, keys, false)
		return err
	}})

	token, generated, err := a.settings.SetupToken(ctx)
	switch {
	case err != nil:
		return err
	case token == "":
	case generated:
		// The one secret the server prints, on purpose (decision D7): whoever
		// can read this log runs the server anyway. It stops working when the
		// wizard is complete.
		a.log.Warn("setup is not complete: open the server's address in a browser and enter this one-time setup code",
			"base_url", a.cfg.BaseURL.String(), "setup_code", logging.Disclosed(token))
	default:
		a.log.Info("setup is not complete: open the server's address in a browser and enter the setup token given in " + config.EnvName("SETUP_TOKEN"))
	}
	return nil
}

// loadKeys loads the master key from where the configuration says. A new key
// file is generated only when allowed and the database holds nothing sealed
// yet: a missing key file next to a database that needs it is reported, not
// replaced.
func loadKeys(ctx context.Context, cfg *config.Config, db *dbx.DB, generate bool) (*masterkey.Keyring, error) {
	sealed, err := settings.HasSealedData(ctx, db)
	if err != nil {
		return nil, err
	}
	keys, err := masterkey.Load(masterkey.LoadOptions{Key: cfg.MasterKey, File: cfg.MasterKeyFile, Generate: generate && !sealed})
	if errors.Is(err, masterkey.ErrNoKeyFile) && sealed {
		return nil, fmt.Errorf("%w; the database holds data encrypted with a master key, so a new one is not generated: "+
			"restore the key file from your backup, or point %s or %s at the key", err, config.EnvName("MASTER_KEY_FILE"), config.EnvName("MASTER_KEY"))
	}
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// rewrappers are the tables with sealed values, for "keys rotate". Every
// domain package that stores sealed values adds its masterkey.Rewrapper
// here.
func rewrappers() []masterkey.Rewrapper {
	return []masterkey.Rewrapper{settings.Rewrapper, authn.Rewrapper, catalog.SourcesRewrapper, secrets.Rewrapper}
}

// keyRotationObject is what the rotation's audit entry is about.
func keyRotationObject(keys *masterkey.Keyring) audit.Object {
	return audit.Object{Kind: "master_key", ID: keys.CurrentID(), Label: brand.Server + " master key"}
}

// secretDefs turns the secrets of a rigdepot.yaml into definitions, by name.
func secretDefs(in map[string]repoformat.Secret) []secrets.Def {
	out := make([]secrets.Def, 0, len(in))
	for _, name := range slices.Sorted(maps.Keys(in)) {
		out = append(out, secrets.Def{Name: name, Label: in[name].Label, Howto: in[name].Howto})
	}
	return out
}
