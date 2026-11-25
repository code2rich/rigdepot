// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package masterkey_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/masterkey"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func secret(b byte) []byte { return bytes.Repeat([]byte{b}, masterkey.KeySize) }

func ring(t *testing.T, secrets ...[]byte) *masterkey.Keyring {
	t.Helper()
	k, err := masterkey.New(secrets...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpens(t *testing.T) {
	k := ring(t, secret(1))
	s, err := k.Seal([]byte("<placeholder: token>"), []byte("secret_values/alice/JIRA/1"))
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyID != k.CurrentID() || bytes.Contains(s.Ciphertext, []byte("placeholder")) {
		t.Fatalf("sealed = %+v", s)
	}
	got, err := k.Open(s, []byte("secret_values/alice/JIRA/1"))
	if err != nil || string(got) != "<placeholder: token>" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	// Two seals of the same value share nothing: each has its own data key.
	s2, _ := k.Seal([]byte("<placeholder: token>"), []byte("secret_values/alice/JIRA/1"))
	if bytes.Equal(s.Ciphertext, s2.Ciphertext) || bytes.Equal(s.WrappedKey, s2.WrappedKey) {
		t.Error("two seals of one value are alike")
	}
}

// TestMovedToSomeoneElseDoesNotOpen is the acceptance check "ciphertext
// moved to another owner does not decrypt".
func TestMovedToSomeoneElseDoesNotOpen(t *testing.T) {
	k := ring(t, secret(1))
	s, err := k.Seal([]byte("<placeholder: alice's token>"), []byte("secret_values/alice/JIRA/1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ad := range []string{"secret_values/bob/JIRA/1", "secret_values/alice/GITLAB/1", "secret_values/alice/JIRA/2", ""} {
		if _, err := k.Open(s, []byte(ad)); !errors.Is(err, masterkey.ErrOpen) {
			t.Errorf("opened with %q: %v", ad, err)
		}
	}
	// Changing any part of the record fails as well.
	for name, change := range map[string]func(*masterkey.Sealed){
		"ciphertext": func(s *masterkey.Sealed) { s.Ciphertext[0] ^= 1 },
		"nonce":      func(s *masterkey.Sealed) { s.Nonce[0] ^= 1 },
		"wrapped":    func(s *masterkey.Sealed) { s.WrappedKey[20] ^= 1 },
	} {
		c := masterkey.Sealed{KeyID: s.KeyID, WrappedKey: bytes.Clone(s.WrappedKey), Nonce: bytes.Clone(s.Nonce), Ciphertext: bytes.Clone(s.Ciphertext)}
		change(&c)
		if _, err := k.Open(c, []byte("secret_values/alice/JIRA/1")); !errors.Is(err, masterkey.ErrOpen) {
			t.Errorf("changed %s: %v", name, err)
		}
	}
}

func TestAnotherMasterKeyDoesNotOpen(t *testing.T) {
	s, _ := ring(t, secret(1)).Seal([]byte("x"), nil)
	if _, err := ring(t, secret(2)).Open(s, nil); !errors.Is(err, masterkey.ErrUnknownKey) {
		t.Errorf("err = %v", err)
	}
	// Claiming the other key's id does not help either.
	other := ring(t, secret(2))
	s.KeyID = other.CurrentID()
	if _, err := other.Open(s, nil); !errors.Is(err, masterkey.ErrOpen) {
		t.Errorf("err = %v", err)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	k := ring(t, secret(1))
	s, _ := k.Seal([]byte("value"), []byte("ad"))
	text := s.Encode()
	if !strings.HasPrefix(text, "mk1."+k.CurrentID()+".") {
		t.Errorf("encoded = %q", text)
	}
	back, err := masterkey.Decode(text)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k.Open(back, []byte("ad")); err != nil || string(got) != "value" {
		t.Errorf("Open(Decode(Encode)) = %q, %v", got, err)
	}
	for _, bad := range []string{"", "mk1", "mk2.a.b.c.d", "mk1..AA.AA.AA", "mk1.a.!.AA.AA", `"x"`} {
		if _, err := masterkey.Decode(bad); err == nil {
			t.Errorf("Decode(%q) succeeded", bad)
		}
	}
}

func TestDerive(t *testing.T) {
	k := ring(t, secret(1))
	a, b := k.Derive("session cookie"), k.Derive("oidc state")
	if len(a) != 32 || bytes.Equal(a, b) || !bytes.Equal(a, k.Derive("session cookie")) {
		t.Error("derived keys are not distinct and stable")
	}
	if bytes.Equal(a, ring(t, secret(2)).Derive("session cookie")) {
		t.Error("another master key derives the same key")
	}
	if k.CurrentID() == ring(t, secret(2)).CurrentID() || strings.Contains(k.CurrentID(), base64.StdEncoding.EncodeToString(secret(1))[:8]) {
		t.Error("key ids are not distinct or reveal the key")
	}
}

// TestThreeSources is the acceptance check "the master key comes from a
// file, the environment, or is generated".
func TestThreeSources(t *testing.T) {
	dir := t.TempDir()

	env, err := masterkey.Load(masterkey.LoadOptions{Key: secret(7), File: filepath.Join(dir, "ignored")})
	if err != nil || env.Source() != masterkey.FromEnv || env.CurrentID() != ring(t, secret(7)).CurrentID() {
		t.Fatalf("environment: %v %v", env, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ignored")); !os.IsNotExist(err) {
		t.Error("a key from the environment created a file")
	}

	b64 := filepath.Join(dir, "b64.key")
	os.WriteFile(b64, []byte("# a comment\n\n"+base64.StdEncoding.EncodeToString(secret(7))+"\n"), 0o600)
	raw := filepath.Join(dir, "raw.key")
	os.WriteFile(raw, secret(7), 0o600)
	for _, f := range []string{b64, raw} {
		k, err := masterkey.Load(masterkey.LoadOptions{File: f})
		if err != nil || k.Source() != masterkey.FromFile || k.CurrentID() != env.CurrentID() {
			t.Errorf("%s: %v %v", filepath.Base(f), k, err)
		}
	}

	gen := filepath.Join(dir, "data", "master.key")
	if _, err := masterkey.Load(masterkey.LoadOptions{File: gen}); !errors.Is(err, masterkey.ErrNoKeyFile) {
		t.Errorf("missing file without Generate: %v", err)
	}
	k, err := masterkey.Load(masterkey.LoadOptions{File: gen, Generate: true})
	if err != nil || k.Source() != masterkey.FromGenerated {
		t.Fatalf("generate: %v %v", k, err)
	}
	info, err := os.Stat(gen)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("generated key file mode %v, want 0600", info.Mode().Perm())
	}
	again, err := masterkey.Load(masterkey.LoadOptions{File: gen, Generate: true})
	if err != nil || again.Source() != masterkey.FromFile || again.CurrentID() != k.CurrentID() {
		t.Errorf("second start: %v %v; want the same key read back", again, err)
	}
}

func TestBadKeyFileIsAnErrorThatDoesNotQuoteIt(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"short":   base64.StdEncoding.EncodeToString(secret(1)[:16]) + "\n",
		"not b64": "Zq8-not-a-key-marker-9xY\n",
		"empty":   "# only a comment\n",
		"two bad": base64.StdEncoding.EncodeToString(secret(1)) + "\nZq8-not-a-key-marker-9xY\n",
	} {
		f := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
		os.WriteFile(f, []byte(content), 0o600)
		_, err := masterkey.Load(masterkey.LoadOptions{File: f, Generate: true})
		if err == nil {
			t.Errorf("%s: loaded", name)
			continue
		}
		if strings.Contains(err.Error(), "marker") {
			t.Errorf("%s: the error quotes the file: %v", name, err)
		}
	}
}

// TestRotate is the acceptance check "after a rotation the old ciphertext
// still decrypts".
func TestRotate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "master.key")
	old, err := masterkey.Load(masterkey.LoadOptions{File: file, Generate: true})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := old.Seal([]byte("<placeholder: mail password>"), []byte("settings/mail.password"))

	rotated, err := old.Rotate("")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.CurrentID() == old.CurrentID() || len(rotated.IDs()) != 2 || rotated.IDs()[1] != old.CurrentID() {
		t.Fatalf("rotated ids %v, old %s", rotated.IDs(), old.CurrentID())
	}
	// The file on disk is the new keyring.
	reread, err := masterkey.Load(masterkey.LoadOptions{File: file})
	if err != nil || strings.Join(reread.IDs(), ",") != strings.Join(rotated.IDs(), ",") {
		t.Fatalf("reread %v, %v", reread.IDs(), err)
	}
	if info, _ := os.Stat(file); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("rotated key file mode %v", info.Mode().Perm())
	}

	// Not rewrapped yet: opens with the retired key.
	if got, err := reread.Open(s, []byte("settings/mail.password")); err != nil || string(got) != "<placeholder: mail password>" {
		t.Errorf("old record after rotation: %q %v", got, err)
	}
	// Rewrapped: under the new key, same ciphertext, still opens.
	rw, changed, err := reread.Rewrap(s)
	if err != nil || !changed || rw.KeyID != reread.CurrentID() || !bytes.Equal(rw.Ciphertext, s.Ciphertext) {
		t.Fatalf("Rewrap = %+v %v %v", rw, changed, err)
	}
	if got, err := reread.Open(rw, []byte("settings/mail.password")); err != nil || string(got) != "<placeholder: mail password>" {
		t.Errorf("rewrapped record: %q %v", got, err)
	}
	if _, changed, _ := reread.Rewrap(rw); changed {
		t.Error("rewrapping a current record changed it")
	}
}

func TestRotateAKeyFromTheEnvironmentNeedsAFile(t *testing.T) {
	env, _ := masterkey.Load(masterkey.LoadOptions{Key: secret(3)})
	if _, err := env.Rotate(""); err == nil {
		t.Fatal("rotated a key from the environment without a file to write")
	}
	out := filepath.Join(t.TempDir(), "rotated.key")
	rotated, err := env.Rotate(out)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Path() != out || len(rotated.IDs()) != 2 {
		t.Errorf("rotated %v at %s", rotated.IDs(), rotated.Path())
	}
	if _, err := env.Rotate(out); err == nil {
		t.Error("overwrote an existing file")
	}
}

// TestRunningServerFollowsARotation: a server that loaded the key file keeps
// opening records after "keys rotate" ran next to it.
func TestRunningServerFollowsARotation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "master.key")
	running, err := masterkey.Load(masterkey.LoadOptions{File: file, Generate: true})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := masterkey.Load(masterkey.LoadOptions{File: file})
	rotated, err := other.Rotate("")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := rotated.Seal([]byte("v"), nil)
	if got, err := running.Open(s, nil); err != nil || string(got) != "v" {
		t.Errorf("the running keyring did not pick up the rotated file: %q %v", got, err)
	}
	if running.CurrentID() != rotated.CurrentID() {
		t.Error("after reloading, the running keyring does not seal with the new key")
	}
}

// TestRewrapAllIsOneTransaction: when one table fails, nothing is rewrapped.
func TestRewrapAllIsOneTransaction(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		if _, err := db.ExecContext(ctx, `CREATE TABLE sealed (id TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		old := ring(t, secret(1))
		s, _ := old.Seal([]byte("v"), []byte("sealed/a"))
		db.ExecContext(ctx, `INSERT INTO sealed (id, value) VALUES ('a', ?)`, s.Encode())
		k := ring(t, secret(2), secret(1))

		table := masterkey.Rewrapper{Name: "sealed", Rewrap: func(ctx context.Context, tx *dbx.Tx, k *masterkey.Keyring) (int, error) {
			var text string
			if err := tx.QueryRowContext(ctx, `SELECT value FROM sealed WHERE id = 'a'`).Scan(&text); err != nil {
				return 0, err
			}
			s, err := masterkey.Decode(text)
			if err != nil {
				return 0, err
			}
			s, changed, err := k.Rewrap(s)
			if err != nil || !changed {
				return 0, err
			}
			_, err = tx.ExecContext(ctx, `UPDATE sealed SET value = ? WHERE id = 'a'`, s.Encode())
			return 1, err
		}}
		failing := masterkey.Rewrapper{Name: "failing", Rewrap: func(context.Context, *dbx.Tx, *masterkey.Keyring) (int, error) {
			return 0, errors.New("boom")
		}}
		if _, err := masterkey.RewrapAll(ctx, db, k, []masterkey.Rewrapper{table, failing}); err == nil {
			t.Fatal("no error")
		}
		var text string
		db.QueryRowContext(ctx, `SELECT value FROM sealed`).Scan(&text)
		if text != s.Encode() {
			t.Error("a failed rotation left a record rewrapped")
		}
		counts, err := masterkey.RewrapAll(ctx, db, k, []masterkey.Rewrapper{table})
		if err != nil || counts["sealed"] != 1 {
			t.Fatalf("counts %v, %v", counts, err)
		}
		db.QueryRowContext(ctx, `SELECT value FROM sealed`).Scan(&text)
		back, _ := masterkey.Decode(text)
		if back.KeyID != k.CurrentID() {
			t.Error("not rewrapped")
		}
		if got, err := ring(t, secret(2)).Open(back, []byte("sealed/a")); err != nil || string(got) != "v" {
			t.Errorf("the new key alone does not open the rewrapped record: %v", err)
		}
	})
}
