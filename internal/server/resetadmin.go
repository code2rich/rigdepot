// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/server/settings"
)

// runResetAdmin is "reset-admin" (docs/architecture/06-server.md#本地账号): set a new password
// for a platform administrator, on the server, straight in the database. It is the way back in
// when the administrator forgot the password or the identity provider is down; being able to
// run commands on the server is the permission. The new password is generated and printed
// once, or read from standard input with --password-stdin. The account's lock is lifted, its
// browser sessions end, and the audit log records it.
func runResetAdmin(ctx context.Context, e *env, args []string) int {
	var user string
	var fromStdin bool
	cfg, code := loadConfig("reset-admin", e, args, func(fs *flag.FlagSet) {
		fs.StringVar(&user, "user", "", "account name of the administrator (needed when there is more than one)")
		fs.BoolVar(&fromStdin, "password-stdin", false, "read the new password from the first line of standard input instead of generating one")
	})
	if cfg == nil {
		return code
	}
	fail := func(format string, args ...any) int {
		fmt.Fprintf(e.stderr, "%s reset-admin: %s\n", brand.Server, fmt.Sprintf(format, args...))
		return exitFailure
	}
	db, code := openForCommand(ctx, e, "reset-admin", cfg)
	if db == nil {
		return code
	}
	defer db.Close()
	notStarted := "the server has not started on this database yet: start it and create the first administrator in the setup wizard"
	keys, err := loadKeys(ctx, cfg, db, false)
	if errors.Is(err, masterkey.ErrNoKeyFile) {
		if sealed, serr := settings.HasSealedData(ctx, db); serr == nil && !sealed {
			return fail("%s", notStarted)
		}
	}
	if err != nil {
		return fail("%v", err)
	}
	chainKey, err := settings.AuditKey(ctx, db, keys, false)
	if err != nil {
		return fail("%v", err)
	}
	if chainKey == nil {
		return fail("%s", notStarted)
	}
	log, err := audit.New(db, chainKey)
	if err != nil {
		return fail("%v", err)
	}
	svc := authn.New(db, authn.Options{Access: access.New(db, access.Options{Auditor: accessAuditor{log}}), Audit: log})

	if user == "" {
		admins, err := svc.Admins(ctx)
		if err != nil {
			return fail("%v", err)
		}
		switch len(admins) {
		case 0:
			return fail("there is no active platform administrator: if setup is not complete, finish it in the browser with the setup code")
		case 1:
			user = admins[0].Username
		default:
			names := make([]string, len(admins))
			for i, a := range admins {
				names[i] = a.Username
			}
			return fail("there are %d administrators (%s): name one with --user", len(admins), strings.Join(names, ", "))
		}
	}

	password := ""
	if fromStdin {
		if e.stdin == nil {
			return fail("no standard input to read the password from")
		}
		line, err := bufio.NewReader(e.stdin).ReadString('\n')
		if err != nil && line == "" {
			return fail("read the password from standard input: %v", err)
		}
		password = strings.TrimRight(line, "\r\n")
	} else {
		password = rand.Text()
	}

	u, err := svc.ResetAdminPassword(ctx, user, password)
	var ae *authn.Error
	switch {
	case errors.Is(err, authn.ErrNotFound):
		return fail("there is no account named %q", user)
	case errors.Is(err, authn.ErrNotAdmin):
		return fail("%q is not an active platform administrator", user)
	case errors.As(err, &ae) && ae.Kind == authn.KindInvalid:
		return fail("the password is not accepted (%v): it needs %d to %d characters and must not be the account name",
			ae.Fields, authn.MinPasswordLength, authn.MaxPasswordLength)
	case err != nil:
		return fail("%v", err)
	}
	if fromStdin {
		fmt.Fprintf(e.stdout, "The password of %s is set. Their browser sessions have ended.\n", u.Username)
	} else {
		fmt.Fprintf(e.stdout, "New password for %s: %s\n", u.Username, password)
		fmt.Fprintf(e.stdout, "It is shown only this once. Sign in and change it on your profile page. Their browser sessions have ended.\n")
	}
	return exitOK
}
