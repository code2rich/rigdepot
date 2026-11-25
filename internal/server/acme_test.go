// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeACME is a certificate authority on this machine that speaks just enough of ACME (RFC 8555)
// for autocert: one account, orders for DNS names, the TLS-ALPN-01 challenge (RFC 8737), which
// it really checks by connecting to the server under test, and certificates signed by its own
// root. Signatures on requests are not checked: the point is the server's side of the exchange.
type fakeACME struct {
	t       *testing.T
	srv     *httptest.Server
	root    *x509.Certificate
	rootDER []byte
	key     *ecdsa.PrivateKey

	mu         sync.Mutex
	target     string // host:port the challenge is checked against
	thumbprint string // of the account key
	accounts   int
	nonce      int
	orders     []*fakeOrder
	issued     int
}

type fakeOrder struct {
	domain   string
	token    string
	authz    string // pending | valid | invalid | deactivated
	finished bool
	leaf     []byte
	problem  string
}

func newFakeACME(t *testing.T) *fakeACME {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake ACME root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ca := &fakeACME{t: t, root: root, rootDER: der, key: key}
	ca.srv = httptest.NewServer(http.HandlerFunc(ca.handle))
	t.Cleanup(ca.srv.Close)
	return ca
}

func (ca *fakeACME) directory() string { return ca.srv.URL + "/directory" }

func (ca *fakeACME) setTarget(addr string) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	ca.target = addr
}

func (ca *fakeACME) roots() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.root)
	return p
}

// counts returns how many accounts were registered and certificates issued.
func (ca *fakeACME) counts() (accounts, issued int) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.accounts, ca.issued
}

type jws struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
}

// request decodes a JWS request: the protected header and the payload.
func request(r *http.Request) (header map[string]json.RawMessage, payload []byte, err error) {
	var body jws
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, nil, err
	}
	h, err := base64.RawURLEncoding.DecodeString(body.Protected)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(h, &header); err != nil {
		return nil, nil, err
	}
	payload, err = base64.RawURLEncoding.DecodeString(body.Payload)
	return header, payload, err
}

func (ca *fakeACME) url(format string, args ...any) string {
	return ca.srv.URL + fmt.Sprintf(format, args...)
}

func (ca *fakeACME) handle(w http.ResponseWriter, r *http.Request) {
	ca.mu.Lock()
	ca.nonce++
	w.Header().Set("Replay-Nonce", "nonce-"+strconv.Itoa(ca.nonce))
	ca.mu.Unlock()
	if r.URL.Path == "/directory" {
		writeJSON(w, http.StatusOK, map[string]string{
			"newNonce": ca.url("/nonce"), "newAccount": ca.url("/account"), "newOrder": ca.url("/order"),
			"revokeCert": ca.url("/revoke"), "keyChange": ca.url("/key-change"),
		})
		return
	}
	if r.URL.Path == "/nonce" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "want POST", http.StatusMethodNotAllowed)
		return
	}
	header, payload, err := request(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	ca.mu.Lock()
	defer ca.mu.Unlock()
	var o *fakeOrder
	if len(parts) == 2 {
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 || n >= len(ca.orders) {
			http.Error(w, "no such object", http.StatusNotFound)
			return
		}
		o = ca.orders[n]
	}
	switch {
	case parts[0] == "account" && len(parts) == 1:
		var jwk struct{ Crv, Kty, X, Y string }
		if err := json.Unmarshal(header["jwk"], &jwk); err != nil || jwk.Kty != "EC" {
			http.Error(w, "want an EC account key", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256(fmt.Appendf(nil, `{"crv":%q,"kty":"EC","x":%q,"y":%q}`, jwk.Crv, jwk.X, jwk.Y))
		tp := base64.RawURLEncoding.EncodeToString(sum[:])
		w.Header().Set("Location", ca.url("/accounts/1"))
		if tp == ca.thumbprint {
			writeJSON(w, http.StatusOK, map[string]string{"status": "valid"})
			return
		}
		ca.thumbprint = tp
		ca.accounts++
		writeJSON(w, http.StatusCreated, map[string]string{"status": "valid"})
	case parts[0] == "order" && len(parts) == 1:
		var req struct {
			Identifiers []struct{ Type, Value string }
		}
		if err := json.Unmarshal(payload, &req); err != nil || len(req.Identifiers) != 1 || req.Identifiers[0].Type != "dns" {
			http.Error(w, "want one DNS identifier", http.StatusBadRequest)
			return
		}
		token := make([]byte, 16)
		rand.Read(token)
		ca.orders = append(ca.orders, &fakeOrder{domain: req.Identifiers[0].Value, token: base64.RawURLEncoding.EncodeToString(token), authz: "pending"})
		n := len(ca.orders) - 1
		w.Header().Set("Location", ca.url("/order/%d", n))
		writeJSON(w, http.StatusCreated, ca.orderJSON(n))
	case parts[0] == "order":
		writeJSON(w, http.StatusOK, ca.orderJSON(ca.index(o)))
	case parts[0] == "authz":
		var req struct{ Status string }
		if json.Unmarshal(payload, &req) == nil && req.Status == "deactivated" && o.authz == "pending" {
			o.authz = "deactivated"
		}
		writeJSON(w, http.StatusOK, ca.authzJSON(ca.index(o)))
	case parts[0] == "chal":
		if o.authz == "pending" {
			if err := ca.validate(o); err != nil {
				o.authz, o.problem = "invalid", err.Error()
				ca.t.Logf("fake ACME: challenge for %s failed: %v", o.domain, err)
			} else {
				o.authz = "valid"
			}
		}
		writeJSON(w, http.StatusOK, ca.authzJSON(ca.index(o))["challenges"].([]map[string]any)[0])
	case parts[0] == "finalize":
		var req struct{ CSR string }
		if err := json.Unmarshal(payload, &req); err != nil || o.authz != "valid" {
			http.Error(w, "order not ready", http.StatusForbidden)
			return
		}
		der, _ := base64.RawURLEncoding.DecodeString(req.CSR)
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil || len(csr.DNSNames) != 1 || csr.DNSNames[0] != o.domain {
			http.Error(w, "bad CSR", http.StatusBadRequest)
			return
		}
		ca.issued++
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(int64(100 + ca.issued)), Subject: pkix.Name{CommonName: o.domain}, DNSNames: csr.DNSNames,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		if o.leaf, err = x509.CreateCertificate(rand.Reader, leaf, ca.root, csr.PublicKey, ca.key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		o.finished = true
		writeJSON(w, http.StatusOK, ca.orderJSON(ca.index(o)))
	case parts[0] == "cert" && o.finished:
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		var b bytes.Buffer
		pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: o.leaf})
		pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: ca.rootDER})
		w.Write(b.Bytes())
	default:
		http.Error(w, "unexpected request "+r.URL.Path, http.StatusBadRequest)
	}
}

func (ca *fakeACME) index(o *fakeOrder) int {
	for i, x := range ca.orders {
		if x == o {
			return i
		}
	}
	return -1
}

func (ca *fakeACME) orderJSON(n int) map[string]any {
	o := ca.orders[n]
	status := "pending"
	switch {
	case o.finished:
		status = "valid"
	case o.authz == "valid":
		status = "ready"
	case o.authz == "invalid" || o.authz == "deactivated":
		status = "invalid"
	}
	out := map[string]any{
		"status": status, "identifiers": []map[string]string{{"type": "dns", "value": o.domain}},
		"authorizations": []string{ca.url("/authz/%d", n)}, "finalize": ca.url("/finalize/%d", n),
	}
	if o.finished {
		out["certificate"] = ca.url("/cert/%d", n)
	}
	return out
}

func (ca *fakeACME) authzJSON(n int) map[string]any {
	o := ca.orders[n]
	chal := map[string]any{"type": "tls-alpn-01", "url": ca.url("/chal/%d", n), "token": o.token, "status": map[string]string{
		"pending": "pending", "valid": "valid", "invalid": "invalid", "deactivated": "invalid"}[o.authz]}
	if o.problem != "" {
		chal["error"] = map[string]any{"type": "urn:ietf:params:acme:error:incorrectResponse", "detail": o.problem}
	}
	return map[string]any{"status": o.authz, "identifier": map[string]string{"type": "dns", "value": o.domain},
		"challenges": []map[string]any{chal}}
}

// idPeACMEIdentifier is the certificate extension of a TLS-ALPN-01 answer (RFC 8737).
var idPeACMEIdentifier = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}

// validate checks a TLS-ALPN-01 challenge the way a real authority does: connect to the
// domain's address asking for the protocol acme-tls/1 and the domain's name, and look for the
// digest of the key authorization in the certificate it gets.
func (ca *fakeACME) validate(o *fakeOrder) error {
	if ca.target == "" {
		return fmt.Errorf("no address for %s", o.domain)
	}
	conn, err := tls.Dial("tcp", ca.target, &tls.Config{ServerName: o.domain, NextProtos: []string{"acme-tls/1"}, InsecureSkipVerify: true})
	if err != nil {
		return err
	}
	defer conn.Close()
	st := conn.ConnectionState()
	if st.NegotiatedProtocol != "acme-tls/1" {
		return fmt.Errorf("negotiated %q, want acme-tls/1", st.NegotiatedProtocol)
	}
	leaf := st.PeerCertificates[0]
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != o.domain {
		return fmt.Errorf("the challenge certificate names %v", leaf.DNSNames)
	}
	want := sha256.Sum256([]byte(o.token + "." + ca.thumbprint))
	for _, ext := range leaf.Extensions {
		if !ext.Id.Equal(idPeACMEIdentifier) {
			continue
		}
		var got []byte
		if _, err := asn1.Unmarshal(ext.Value, &got); err != nil || !ext.Critical || !bytes.Equal(got, want[:]) {
			return fmt.Errorf("the acmeIdentifier extension does not answer the challenge")
		}
		return nil
	}
	return fmt.Errorf("no acmeIdentifier extension")
}
