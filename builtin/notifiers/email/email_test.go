// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/ext/notify"
	"github.com/code2rich/rigdepot/ext/notify/notifytest"
)

func TestMain(m *testing.M) { notifytest.Main(m) }

func TestContract(t *testing.T) { notifytest.Run(t, New()) }

// ---- a fake SMTP server on the loopback interface ----
// It answers the subset of RFC 5321 a client needs: EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT,
// DATA, QUIT. Its 535 reply quotes the credentials it got back, the way a careless server does,
// so that the test can see they do not come through.

type received struct {
	from, rcpt string
	data       string
}

type fakeSMTP struct {
	t        *testing.T
	ln       net.Listener
	tlsCfg   *tls.Config
	pool     *x509.CertPool
	implicit bool // TLS from the first byte; otherwise plain, offering STARTTLS when starttls
	starttls bool
	user     string
	pass     string

	mu       sync.Mutex
	silent   bool // accepts connections and never says a word
	authSeen bool
	authFail bool
	msgs     []received
}

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func newFakeSMTP(t *testing.T, implicit, starttls bool) *fakeSMTP {
	t.Helper()
	cert, pool := selfSigned(t)
	fs := &fakeSMTP{t: t, tlsCfg: &tls.Config{Certificates: []tls.Certificate{cert}}, pool: pool,
		implicit: implicit, starttls: starttls, user: "notify@example.com", pass: "p@ss word-123"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicit {
		ln = tls.NewListener(ln, fs.tlsCfg)
	}
	fs.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fs.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return fs
}

// config is the channel configuration that reaches this server.
func (fs *fakeSMTP) config() notify.Config {
	_, port, _ := net.SplitHostPort(fs.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	security := "starttls"
	if fs.implicit {
		security = "tls"
	}
	return notify.Config{keyHost: "127.0.0.1", keyPort: p, keySecurity: security,
		keyUsername: fs.user, keyPassword: fs.pass, keyFrom: "notify@example.com"}
}

func (fs *fakeSMTP) channel() *channel {
	return &channel{rootCAs: fs.pool, now: time.Now}
}

func (fs *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	fs.mu.Lock()
	silent := fs.silent
	fs.mu.Unlock()
	if silent {
		_, _ = io.Copy(io.Discard, c)
		return
	}
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
	say("220 fake.example.com ESMTP")
	tlsOn := fs.implicit
	var cur received
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); verb {
		case "EHLO", "HELO":
			ext := []string{"250-fake.example.com"}
			if fs.starttls && !tlsOn {
				ext = append(ext, "250-STARTTLS")
			}
			say(strings.Join(append(ext, "250-AUTH PLAIN", "250 8BITMIME"), "\r\n"))
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, fs.tlsCfg)
			if tc.Handshake() != nil {
				return
			}
			c, tlsOn = tc, true
			r, w = bufio.NewReader(c), bufio.NewWriter(c)
		case "AUTH":
			parts := strings.Fields(line)
			got, _ := base64.StdEncoding.DecodeString(parts[len(parts)-1])
			fs.mu.Lock()
			fs.authSeen = true
			fail := fs.authFail || string(got) != "\x00"+fs.user+"\x00"+fs.pass
			fs.mu.Unlock()
			if fail {
				say("535 Login fail for " + parts[len(parts)-1] + " (password " + fs.pass + ")")
			} else {
				say("235 Authentication successful")
			}
		case "MAIL":
			cur.from = angle(line)
			say("250 OK")
		case "RCPT":
			cur.rcpt = angle(line)
			say("250 OK")
		case "DATA":
			say("354 End data with <CR><LF>.<CR><LF>")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.data = data.String()
			fs.mu.Lock()
			fs.msgs = append(fs.msgs, cur)
			fs.mu.Unlock()
			cur = received{}
			say("250 OK: queued")
		case "QUIT":
			say("221 Bye")
			return
		default:
			say("502 Command not implemented")
		}
	}
}

func angle(line string) string {
	i, j := strings.Index(line, "<"), strings.Index(line, ">")
	if i < 0 || j < i {
		return ""
	}
	return line[i+1 : j]
}

func (fs *fakeSMTP) got() []received {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]received(nil), fs.msgs...)
}

func (fs *fakeSMTP) sawAuth() bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.authSeen
}

// parsed is a received message taken apart.
type parsed struct {
	h          mail.Header
	subject    string
	text, html string
}

func parse(t *testing.T, raw string) parsed {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, raw)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed{h: m.Header, subject: subject}
	typ, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if typ == "text/plain" {
		b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
		p.text = string(b)
		return p
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		switch {
		case strings.HasPrefix(part.Header.Get("Content-Type"), "text/plain"):
			p.text = string(b)
		case strings.HasPrefix(part.Header.Get("Content-Type"), "text/html"):
			p.html = string(b)
		}
	}
	return p
}

var sample = notify.Message{
	To:      notify.Recipient{Address: "member@example.com", Name: "张三"},
	From:    "Example Corp",
	Subject: "资源 code-review 发布了 v5",
	Text:    "code-review 发布了 v5。\n\n查看：https://depot.example.com/resources/code-review",
	HTML:    "<p>code-review 发布了 v5。</p>",
	Link:    "https://depot.example.com/resources/code-review",
	Kind:    "resource.published",
	Locale:  "zh-CN",
}

func send(t *testing.T, fs *fakeSMTP, cfg notify.Config, msg notify.Message) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return fs.channel().Send(ctx, cfg, msg)
}

func TestSendOverImplicitTLS(t *testing.T) {
	fs := newFakeSMTP(t, true, false)
	if err := send(t, fs, fs.config(), sample); err != nil {
		t.Fatal(err)
	}
	got := fs.got()
	if len(got) != 1 || got[0].from != "notify@example.com" || got[0].rcpt != "member@example.com" {
		t.Fatalf("envelope: %+v", got)
	}
	m := parse(t, got[0].data)
	from, _ := mail.ParseAddress(m.h.Get("From"))
	to, _ := mail.ParseAddress(m.h.Get("To"))
	if from == nil || from.Name != "Example Corp" || from.Address != "notify@example.com" {
		t.Errorf("From: %q", m.h.Get("From"))
	}
	if to == nil || to.Name != "张三" || to.Address != "member@example.com" {
		t.Errorf("To: %q", m.h.Get("To"))
	}
	if m.subject != sample.Subject || m.text != sample.Text || m.html != sample.HTML {
		t.Errorf("content: subject %q text %q html %q", m.subject, m.text, m.html)
	}
	for k, want := range map[string]string{"Auto-Submitted": "auto-generated", "Content-Language": "zh-CN", "MIME-Version": "1.0"} {
		if got := m.h.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if !strings.HasSuffix(m.h.Get("Message-Id"), "@example.com>") {
		t.Errorf("Message-ID: %q", m.h.Get("Message-Id"))
	}
}

func TestPlainTextOnly(t *testing.T) {
	fs := newFakeSMTP(t, true, false)
	msg := sample
	msg.HTML = ""
	if err := send(t, fs, fs.config(), msg); err != nil {
		t.Fatal(err)
	}
	if m := parse(t, fs.got()[0].data); m.text != sample.Text || m.html != "" {
		t.Errorf("text %q html %q", m.text, m.html)
	}
}

func TestSendOverSTARTTLS(t *testing.T) {
	fs := newFakeSMTP(t, false, true)
	if err := send(t, fs, fs.config(), sample); err != nil {
		t.Fatal(err)
	}
	if len(fs.got()) != 1 {
		t.Fatalf("got %d messages", len(fs.got()))
	}
}

// TestRefusesPlaintext: a server without STARTTLS gets nothing, and above all not the password.
func TestRefusesPlaintext(t *testing.T) {
	fs := newFakeSMTP(t, false, false)
	err := send(t, fs, fs.config(), sample)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("Send to a server without STARTTLS: %v", err)
	}
	if fs.sawAuth() || len(fs.got()) != 0 {
		t.Error("credentials or a message went over a plain connection")
	}
}

// TestErrorHidesThePassword: the server's reply quotes the password and the AUTH PLAIN string;
// the error the channel returns has neither.
func TestErrorHidesThePassword(t *testing.T) {
	fs := newFakeSMTP(t, true, false)
	fs.mu.Lock()
	fs.authFail = true
	fs.mu.Unlock()
	err := send(t, fs, fs.config(), sample)
	if err == nil {
		t.Fatal("a refused login did not fail")
	}
	msg := err.Error()
	plain := base64.StdEncoding.EncodeToString([]byte("\x00" + fs.user + "\x00" + fs.pass))
	if strings.Contains(msg, fs.pass) || strings.Contains(msg, plain) || strings.Contains(msg, "word-123") {
		t.Errorf("the password is in the error: %q", msg)
	}
	if !strings.Contains(msg, "535") || !strings.Contains(msg, "***") {
		t.Errorf("the error lost what went wrong: %q", msg)
	}
}

// TestHeaderInjection: text from people cannot add headers or recipients.
func TestHeaderInjection(t *testing.T) {
	fs := newFakeSMTP(t, true, false)
	msg := sample
	msg.Subject = "hello\r\nBcc: attacker@example.com"
	msg.To.Name = "Eve\r\nCc: attacker@example.com"
	msg.From = "Corp\nX-Evil: 1"
	if err := send(t, fs, fs.config(), msg); err != nil {
		t.Fatal(err)
	}
	m := parse(t, fs.got()[0].data)
	for _, h := range []string{"Bcc", "Cc", "X-Evil"} {
		if v := m.h.Get(h); v != "" {
			t.Errorf("an injected %s header got through: %q", h, v)
		}
	}
	if m.subject != "hello Bcc: attacker@example.com" {
		t.Errorf("Subject: %q", m.subject)
	}

	for _, to := range []string{"a@example.com, b@example.com", "Eve <a@example.com>", "a@example.com\r\nRCPT TO:<b@example.com>", "not an address"} {
		msg := sample
		msg.To.Address = to
		if err := send(t, fs, fs.config(), msg); err == nil {
			t.Errorf("recipient %q was accepted", to)
		}
	}
	if n := len(fs.got()); n != 1 {
		t.Errorf("%d messages sent, want only the first", n)
	}
}

func TestBadSettings(t *testing.T) {
	fs := newFakeSMTP(t, true, false)
	for name, change := range map[string]func(notify.Config){
		"no host":        func(c notify.Config) { delete(c, keyHost) },
		"no port":        func(c notify.Config) { delete(c, keyPort) },
		"bad security":   func(c notify.Config) { c[keySecurity] = "none" },
		"no security":    func(c notify.Config) { delete(c, keySecurity) },
		"bad sender":     func(c notify.Config) { c[keyFrom] = "Corp <notify@example.com>" },
		"port too large": func(c notify.Config) { c[keyPort] = 70000 },
	} {
		cfg := fs.config()
		change(cfg)
		if err := send(t, fs, cfg, sample); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(fs.got()) != 0 {
		t.Error("a message went out with bad settings")
	}
}

// TestSilentServerTimesOut: a server that never answers does not hold Send past its deadline.
func TestSilentServerTimesOut(t *testing.T) {
	fs := newFakeSMTP(t, false, true)
	fs.mu.Lock()
	fs.silent = true
	fs.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := fs.channel().Send(ctx, fs.config(), sample); err == nil {
		t.Fatal("Send to a silent server succeeded")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Send took %v with a 200ms deadline", d)
	}
}
