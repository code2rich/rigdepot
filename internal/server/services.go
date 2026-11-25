// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/config"
	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// openServices builds the domain services on the open, migrated database:
// the master key, the audit log and the settings, then gets the setup
// wizard ready. Other domains add theirs here, and set what they provide in
// a.hooks.
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
	a.settings = settings.New(a.db, keys, log, settings.Options{Authorize: a.hooks.settingsAuthorizer(), SetupToken: a.cfg.SetupToken})

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
			"base_url", a.cfg.BaseURL.String(), "setup_code", token)
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
// here: TODO(M1-28) secrets (secret_values), TODO(M1-25) catalog (sources'
// credentials), TODO(M1-21) identity providers' client secrets.
func rewrappers() []masterkey.Rewrapper {
	return []masterkey.Rewrapper{settings.Rewrapper}
}

// keyRotationObject is what the rotation's audit entry is about.
func keyRotationObject(keys *masterkey.Keyring) audit.Object {
	return audit.Object{Kind: "master_key", ID: keys.CurrentID(), Label: brand.Server + " master key"}
}
