// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package i18n

import (
	"context"

	"golang.org/x/text/language"
)

type printerKey struct{}

// NewContext returns a context that carries the printer of this call, so that
// layers below the command line or the HTTP handler render in the language
// that was negotiated there.
func NewContext(ctx context.Context, p *Printer) context.Context {
	return context.WithValue(ctx, printerKey{}, p)
}

// From returns the printer of this call. Without one it returns a printer
// that has no messages and renders every key as itself: a missing printer is
// a defect that should be visible, not hidden behind English text.
func From(ctx context.Context) *Printer {
	if p, ok := ctx.Value(printerKey{}).(*Printer); ok && p != nil {
		return p
	}
	en := language.MustParse(Fallback)
	return &Printer{tag: en, fallback: en}
}
