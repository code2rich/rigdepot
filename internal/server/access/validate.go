// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package access

import (
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// The patterns of api/openapi.yaml. The generated HTTP layer does not
// enforce them, so the domain does.
var (
	codePattern     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)
	stagePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	idPattern       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// reservedProductCodes are the words a product code cannot be: the two
// scopes that are not products, and the command-line program's commands,
// because a product code is the first word of product commands
// ("rgd shop sync"). Keep it in step with the commands in internal/cli
// (docs/architecture/04-cli.md#命令); the server cannot import the
// command-line side to read them.
var reservedProductCodes = []string{
	"org", "me",
	"check", "completion", "config", "daemon", "disable", "doctor", "enable",
	"feedback", "flow", "help", "login", "logout", "mcp", "privacy", "repo",
	"secret", "skill", "status", "sync", "uninstall", "upgrade", "version",
}

// ReservedProductCodes returns the words that cannot be product codes.
func ReservedProductCodes() []string { return slices.Clone(reservedProductCodes) }

func checkProductCode(field, code string) *FieldError {
	switch {
	case code == "":
		return &FieldError{field, "required"}
	case len(code) < 2:
		return &FieldError{field, "too_short"}
	case len(code) > 32:
		return &FieldError{field, "too_long"}
	case !codePattern.MatchString(code):
		return &FieldError{field, "pattern"}
	}
	return nil
}

// checkText checks a free-text field's length in characters.
func checkText(field, v string, min, max int) *FieldError {
	n := utf8.RuneCountInString(v)
	switch {
	case !utf8.ValidString(v):
		return &FieldError{field, "invalid"}
	case n < min && n == 0:
		return &FieldError{field, "required"}
	case n < min:
		return &FieldError{field, "too_short"}
	case n > max:
		return &FieldError{field, "too_long"}
	}
	return nil
}

func checkID(field, id string) *FieldError {
	if !idPattern.MatchString(id) {
		return &FieldError{field, "pattern"}
	}
	return nil
}

// normalEmail returns a bare e-mail address in lower case, or false when v
// is not one.
func normalEmail(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 254 {
		return "", false
	}
	a, err := mail.ParseAddress(v)
	if err != nil || a.Name != "" || a.Address != v {
		return "", false
	}
	return strings.ToLower(v), true
}

// collect turns the field errors that are not nil into an error, or nil.
func collect(errs ...*FieldError) error {
	var out []FieldError
	for _, e := range errs {
		if e != nil {
			out = append(out, *e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return invalid(out...)
}
