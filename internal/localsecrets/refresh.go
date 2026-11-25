// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package localsecrets

import (
	"context"
	"fmt"
	"time"

	"github.com/code2rich/rigdepot/internal/apiclient"
)

// API is what Refresh needs of the server; the generated client has it.
type API interface {
	GetSealedSecretsWithResponse(ctx context.Context, body apiclient.GetSealedSecretsJSONRequestBody, reqEditors ...apiclient.RequestEditorFn) (*apiclient.GetSealedSecretsResponse, error)
}

// Refreshed says what Refresh did.
type Refreshed struct {
	// Stored are the secrets whose copies were written, and Missing the ones asked for that the
	// person has not set (their copies were dropped).
	Stored, Missing []string
}

// Refresh fetches the person's secrets sealed to this device and stores them: every secret when
// names is empty, which replaces the whole vault (what was deleted on the server goes), or only
// the named ones. The client must carry this device's token. When the server cannot be asked,
// the vault stays as it is.
func (v *Vault) Refresh(ctx context.Context, api API, owner Owner, names []string, now time.Time) (Refreshed, error) {
	body := apiclient.GetSealedSecretsJSONRequestBody{}
	if len(names) > 0 {
		body.Names = &names
	}
	resp, err := api.GetSealedSecretsWithResponse(ctx, body)
	if err != nil {
		return Refreshed{}, err
	}
	if err := apiclient.Check(resp.HTTPResponse, resp.Body); err != nil {
		return Refreshed{}, err
	}
	s := resp.JSON200
	if s == nil {
		return Refreshed{}, fmt.Errorf("localsecrets: the server answered without the sealed secrets")
	}
	if s.DeviceId != owner.DeviceID {
		return Refreshed{}, fmt.Errorf("localsecrets: the server sealed the secrets for device %s, not for this one (%s)", s.DeviceId, owner.DeviceID)
	}
	if string(s.Suite) != Suite {
		return Refreshed{}, fmt.Errorf("localsecrets: unknown cipher suite %q", s.Suite)
	}
	items := make([]Item, 0, len(s.Items))
	out := Refreshed{Missing: append([]string{}, s.Missing...)}
	for _, it := range s.Items {
		items = append(items, Item{Name: it.Name, Version: it.Version, Enc: it.Enc, Ciphertext: it.Ciphertext, Info: it.Info, ExpiresAt: it.ExpiresAt})
		out.Stored = append(out.Stored, it.Name)
	}
	if len(names) == 0 {
		err = v.Replace(owner, items, now)
	} else {
		err = v.Update(owner, names, items, now)
	}
	return out, err
}
