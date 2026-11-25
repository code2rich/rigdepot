// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package localsecrets_test

import (
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/localsecrets"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const marker = "Qp3-local-secret-marker-7Ys1-not-a-token"

var owner = localsecrets.Owner{Server: "https://hub.example.com", UserID: "u-1", DeviceID: "d-1"}

func newVault(t *testing.T) (*localsecrets.Vault, home.Dir) {
	t.Helper()
	d, err := home.At(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	return localsecrets.New(d), d
}

func key(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// seal does what the server does: seal value to the device's public key with the info that binds
// it.
func seal(t *testing.T, k *ecdh.PrivateKey, o localsecrets.Owner, name string, version int, value string) localsecrets.Item {
	t.Helper()
	pk, err := hpke.NewDHKEMPublicKey(k.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	info := localsecrets.Info(o.UserID, o.DeviceID, name, version)
	enc, s, err := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(info))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := s.Seal(nil, []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	b := base64.RawURLEncoding.EncodeToString
	return localsecrets.Item{Name: name, Version: version, Enc: b(enc), Ciphertext: b(ct), Info: info}
}

func TestReplaceAndOpen(t *testing.T) {
	v, _ := newVault(t)
	k := key(t)
	if err := v.Replace(owner, []localsecrets.Item{seal(t, k, owner, "JIRA_API_TOKEN", 3, marker), seal(t, k, owner, "GITLAB_TOKEN", 1, "glpat")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := v.Open(k, owner, []string{"JIRA_API_TOKEN", "GITLAB_TOKEN"})
	if err != nil || string(got["JIRA_API_TOKEN"]) != marker || string(got["GITLAB_TOKEN"]) != "glpat" {
		t.Fatalf("open: %v %v", got, err)
	}
	// The file holds only what the server sealed, private to the person.
	data, err := os.ReadFile(v.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), marker) {
		t.Error("the vault holds a value in the clear")
	}
	if fi, _ := os.Stat(v.Path()); runtime.GOOS != "windows" && fi.Mode().Perm() != home.FilePerm {
		t.Errorf("permissions %v", fi.Mode().Perm())
	}

	// Several missing ones are named together, before anything is opened.
	var miss *localsecrets.MissingError
	if _, err := v.Open(k, owner, []string{"FIGMA_TOKEN", "JIRA_API_TOKEN", "AWS_TOKEN"}); !errors.As(err, &miss) || strings.Join(miss.Names, ",") != "AWS_TOKEN,FIGMA_TOKEN" {
		t.Errorf("missing: %v", err)
	}
	// Another device's key, or another sign-in, does not open them.
	if _, err := v.Open(key(t), owner, []string{"JIRA_API_TOKEN"}); !errors.Is(err, localsecrets.ErrOpen) {
		t.Errorf("another key: %v", err)
	}
	other := owner
	other.DeviceID = "d-2"
	if _, err := v.Open(k, other, []string{"JIRA_API_TOKEN"}); !errors.Is(err, localsecrets.ErrNotYours) {
		t.Errorf("another device: %v", err)
	}
}

// A copy sealed with an info that is not this person's, this device's, this name's and this
// version's is refused, even though it opens with the key.
func TestOpenChecksTheInfo(t *testing.T) {
	v, _ := newVault(t)
	k := key(t)
	it := seal(t, k, owner, "GITLAB_TOKEN", 1, marker)
	it.Name = "JIRA_API_TOKEN" // a copy filed under another name
	v.Replace(owner, []localsecrets.Item{it}, time.Now())
	if _, err := v.Open(k, owner, []string{"JIRA_API_TOKEN"}); !errors.Is(err, localsecrets.ErrOpen) {
		t.Errorf("renamed copy: %v", err)
	}
}

// A vault that cannot be read is unknown, never empty: nothing is opened, updated or removed.
func TestUnreadableVault(t *testing.T) {
	v, _ := newVault(t)
	os.MkdirAll(filepath.Dir(v.Path()), 0o700)
	os.WriteFile(v.Path(), []byte("{not json"), 0o600)
	k := key(t)
	if _, err := v.Open(k, owner, []string{"JIRA_API_TOKEN"}); !errors.Is(err, localsecrets.ErrUnknown) {
		t.Errorf("open: %v", err)
	}
	if err := v.Update(owner, []string{"JIRA_API_TOKEN"}, nil, time.Now()); !errors.Is(err, localsecrets.ErrUnknown) {
		t.Errorf("update: %v", err)
	}
	if err := v.Remove("JIRA_API_TOKEN"); !errors.Is(err, localsecrets.ErrUnknown) {
		t.Errorf("remove: %v", err)
	}
	if data, _ := os.ReadFile(v.Path()); string(data) != "{not json" {
		t.Errorf("rewritten: %s", data)
	}
	os.WriteFile(v.Path(), []byte(`{"schema":9,"items":[]}`), 0o600)
	if _, err := v.Read(); !errors.Is(err, localsecrets.ErrUnknown) {
		t.Errorf("newer format: %v", err)
	}
}

func TestUpdateRemoveDelete(t *testing.T) {
	v, _ := newVault(t)
	k := key(t)
	v.Replace(owner, []localsecrets.Item{seal(t, k, owner, "JIRA_API_TOKEN", 1, "a"), seal(t, k, owner, "GITLAB_TOKEN", 1, "b")}, time.Now())
	// A new version of one, and one asked for that is no longer set.
	if err := v.Update(owner, []string{"JIRA_API_TOKEN", "GITLAB_TOKEN"}, []localsecrets.Item{seal(t, k, owner, "JIRA_API_TOKEN", 2, "a2")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f, _ := v.Read()
	if len(f.Items) != 1 || f.Items[0].Version != 2 {
		t.Errorf("after update: %+v", f.Items)
	}
	// A vault of another sign-in is replaced as a whole.
	other := localsecrets.Owner{Server: owner.Server, UserID: "u-2", DeviceID: "d-9"}
	v.Update(other, []string{"FIGMA_TOKEN"}, []localsecrets.Item{seal(t, k, other, "FIGMA_TOKEN", 1, "c")}, time.Now())
	if f, _ := v.Read(); f.UserID != "u-2" || len(f.Items) != 1 || f.Items[0].Name != "FIGMA_TOKEN" {
		t.Errorf("other sign-in: %+v", f)
	}
	if err := v.Remove("FIGMA_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if f, _ := v.Read(); len(f.Items) != 0 {
		t.Errorf("after remove: %+v", f.Items)
	}
	if err := v.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.Path()); !os.IsNotExist(err) {
		t.Errorf("still there: %v", err)
	}
	if err := v.Delete(); err != nil {
		t.Errorf("delete twice: %v", err)
	}
}

// fakeAPI answers POST /api/v1/sync/secrets.
type fakeAPI struct {
	answer apiclient.SealedSecrets
	status int
	asked  *[]string
}

func (f *fakeAPI) GetSealedSecretsWithResponse(_ context.Context, body apiclient.GetSealedSecretsJSONRequestBody, _ ...apiclient.RequestEditorFn) (*apiclient.GetSealedSecretsResponse, error) {
	f.asked = body.Names
	r := &apiclient.GetSealedSecretsResponse{HTTPResponse: &http.Response{StatusCode: f.status, Header: http.Header{"Content-Type": {"application/json"}}}}
	if f.status == http.StatusOK {
		r.JSON200 = &f.answer
	}
	r.Body, _ = json.Marshal(f.answer)
	return r, nil
}

func TestRefresh(t *testing.T) {
	v, _ := newVault(t)
	k := key(t)
	it := seal(t, k, owner, "JIRA_API_TOKEN", 1, marker)
	api := &fakeAPI{status: http.StatusOK, answer: apiclient.SealedSecrets{DeviceId: owner.DeviceID, Suite: localsecrets.Suite,
		Items:   []apiclient.SealedSecret{{Name: it.Name, Version: it.Version, Enc: it.Enc, Ciphertext: it.Ciphertext, Info: it.Info}},
		Missing: []string{"FIGMA_TOKEN"}}}
	got, err := v.Refresh(context.Background(), api, owner, []string{"JIRA_API_TOKEN", "FIGMA_TOKEN"}, time.Now())
	if err != nil || len(got.Stored) != 1 || len(got.Missing) != 1 || api.asked == nil {
		t.Fatalf("refresh: %+v %v", got, err)
	}
	if vals, err := v.Open(k, owner, []string{"JIRA_API_TOKEN"}); err != nil || string(vals["JIRA_API_TOKEN"]) != marker {
		t.Errorf("open after refresh: %v", err)
	}
	// Sealed for another device: refused, the vault stays.
	api.answer.DeviceId = "d-2"
	if _, err := v.Refresh(context.Background(), api, owner, nil, time.Now()); err == nil {
		t.Error("took values sealed for another device")
	}
	// The server refusing leaves the vault as it is.
	api.status = http.StatusUnauthorized
	if _, err := v.Refresh(context.Background(), api, owner, nil, time.Now()); err == nil {
		t.Error("no error for 401")
	}
	if f, _ := v.Read(); len(f.Items) != 1 {
		t.Errorf("vault changed: %+v", f)
	}
}
