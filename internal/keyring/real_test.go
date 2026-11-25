// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package keyring

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	gokeyring "github.com/zalando/go-keyring"

	"github.com/code2rich/rigdepot/internal/brand"
)

// TestRealKeychain stores, reads and removes entries in the real system keychain of this
// computer. It runs only when EnvRealKeyring is 1, under a service name of its own that it
// removes again; nothing the program itself stores is touched.
func TestRealKeychain(t *testing.T) {
	if os.Getenv(EnvRealKeyring) != "1" {
		t.Skip("set " + EnvRealKeyring + "=1 to run this test against the real system keychain")
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	service := brand.CLILong + "-test-" + hex.EncodeToString(b)
	t.Cleanup(func() {
		for _, n := range names {
			if err := gokeyring.Delete(service, n); err != nil && !errors.Is(err, gokeyring.ErrNotFound) {
				t.Errorf("cleaning up %s/%s: %v", service, n, err)
			}
		}
	})

	// On macOS, /usr/bin/security looks for the login keychain under HOME, which the tests
	// point at a temporary directory; it may then wait for a dialog nobody answers. A short
	// timeout turns that into a failure.
	k := Open(dataDir(t), Options{Service: service, Timeout: 20 * time.Second})
	if st := k.Probe(); st.Kind != System || st.Err != nil {
		t.Fatalf("the system keychain is not usable here: %+v", st)
	}
	notFound(t, k, DeviceKey)
	if err := k.Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	if err := k.Set(DeviceToken, []byte("<placeholder: device token>")); err != nil {
		t.Fatal(err)
	}
	mustGet(t, k, DeviceKey, deviceKey)
	if err := k.Set(DeviceKey, deviceKey[:16]); err != nil {
		t.Fatal(err)
	}
	mustGet(t, k, DeviceKey, deviceKey[:16])
	for _, n := range names {
		if err := k.Delete(n); err != nil {
			t.Fatal(err)
		}
		notFound(t, k, n)
	}
}
