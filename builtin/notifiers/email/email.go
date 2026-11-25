// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package email sends notifications by e-mail through the SMTP server the deployment configured.
//
// Only encrypted connections: port 465 style (security "tls") speaks TLS from the first byte;
// anything else (security "starttls") must offer STARTTLS, or nothing is sent, so that the
// password never crosses the network in the clear. TLS 1.2 or later, certificate checked.
//
// The message is plain text plus HTML (multipart/alternative), base64-encoded, with header values
// stripped of line breaks and control characters and encoded per RFC 2047, so that text from
// people cannot add headers. It carries Auto-Submitted: auto-generated so that auto-responders
// stay quiet. Errors never contain the password, in clear or base64.
package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/code2rich/rigdepot/ext/notify"
)

// The settings, as the settings page and the API name them.
const (
	keyHost     = "host"
	keyPort     = "port"
	keySecurity = "security" // "tls" or "starttls"
	keyUsername = "username"
	keyPassword = "password"
	keyFrom     = "from"
)

var schema = []byte(`{
  "type": "object",
  "required": ["host", "port", "security", "from"],
  "properties": {
    "host": {"type": "string", "minLength": 1, "maxLength": 255},
    "port": {"type": "integer", "minimum": 1, "maximum": 65535},
    "security": {"type": "string", "enum": ["tls", "starttls"], "description": "tls on port 465; starttls otherwise, and the server must offer it"},
    "username": {"type": "string", "maxLength": 255},
    "password": {"type": "string", "maxLength": 1024, "writeOnly": true},
    "from": {"type": "string", "format": "email"}
  }
}`)

// sessionTimeout bounds one whole SMTP session when the caller gave no earlier deadline.
const sessionTimeout = time.Minute

type channel struct {
	rootCAs *x509.CertPool // nil: the system's
	now     func() time.Time
}

// New returns the e-mail channel.
func New() notify.Channel { return &channel{now: time.Now} }

func (*channel) ID() string           { return "email" }
func (*channel) ConfigSchema() []byte { return schema }

// settings are the parsed and checked configuration.
type settings struct {
	host       string
	port       int
	implicit   bool
	user, pass string
	from       string
}

func (s settings) addr() string { return net.JoinHostPort(s.host, strconv.Itoa(s.port)) }

func parseSettings(cfg notify.Config) (settings, error) {
	s := settings{
		host: strings.TrimSpace(cfg.String(keyHost)),
		port: cfg.Int(keyPort),
		user: cfg.String(keyUsername),
		pass: cfg.String(keyPassword),
	}
	switch cfg.String(keySecurity) {
	case "tls":
		s.implicit = true
	case "starttls":
	default:
		return s, errors.New("email: security must be tls or starttls")
	}
	if s.host == "" || strings.ContainsAny(s.host, " /\r\n") {
		return s, errors.New("email: no SMTP host")
	}
	if s.port < 1 || s.port > 65535 {
		return s, errors.New("email: the SMTP port must be between 1 and 65535")
	}
	from, err := bareAddress(cfg.String(keyFrom))
	if err != nil {
		return s, fmt.Errorf("email: the sender address: %w", err)
	}
	s.from = from
	return s, nil
}

// bareAddress accepts exactly one address without a display name.
func bareAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\r\n<>,;\" ") {
		return "", fmt.Errorf("%q is not one e-mail address", s)
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s || !strings.Contains(s, "@") {
		return "", fmt.Errorf("%q is not one e-mail address", s)
	}
	return a.Address, nil
}

// Send implements notify.Channel.
func (c *channel) Send(ctx context.Context, cfg notify.Config, msg notify.Message) error {
	s, err := parseSettings(cfg)
	if err != nil {
		return err
	}
	to, err := bareAddress(msg.To.Address)
	if err != nil {
		return fmt.Errorf("email: the recipient: %w", err)
	}
	data := render(s.from, to, msg, c.now())
	if err := c.deliver(ctx, s, to, data); err != nil {
		return errors.New(redact(s, err.Error()))
	}
	return nil
}

func (c *channel) deliver(ctx context.Context, s settings, to string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, sessionTimeout)
		defer cancel()
	}
	tlsCfg := &tls.Config{ServerName: s.host, RootCAs: c.rootCAs, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if s.implicit {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", s.addr())
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", s.addr())
	}
	if err != nil {
		return fmt.Errorf("%s: connect: %w", s.addr(), err)
	}
	// The session ends with the context, whatever it is waiting for.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	cl, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("%s: greeting: %w", s.addr(), err)
	}
	defer cl.Close()
	if !s.implicit {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%s: the server does not offer STARTTLS; nothing is sent without encryption", s.addr())
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("%s: STARTTLS: %w", s.addr(), err)
		}
	}
	if s.user != "" {
		if err := cl.Auth(smtp.PlainAuth("", s.user, s.pass, s.host)); err != nil {
			return fmt.Errorf("%s: authentication failed: %w", s.addr(), err)
		}
	}
	if err := cl.Mail(s.from); err != nil {
		return fmt.Errorf("%s: MAIL FROM: %w", s.addr(), err)
	}
	if err := cl.Rcpt(to); err != nil {
		return fmt.Errorf("%s: RCPT TO: %w", s.addr(), err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("%s: DATA: %w", s.addr(), err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("%s: sending the message: %w", s.addr(), err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("%s: the server refused the message: %w", s.addr(), err)
	}
	_ = cl.Quit()
	return nil
}

// redact replaces the password, in clear and in the base64 forms AUTH PLAIN sends, with ***.
func redact(s settings, text string) string {
	if s.pass == "" {
		return text
	}
	for _, secret := range []string{
		base64.StdEncoding.EncodeToString([]byte("\x00" + s.user + "\x00" + s.pass)),
		base64.StdEncoding.EncodeToString([]byte(s.pass)),
		s.pass,
	} {
		text = strings.ReplaceAll(text, secret, "***")
	}
	return text
}

// headerText removes line breaks and other control characters and collapses white space, so
// that a value cannot start a header of its own.
func headerText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// encodeHeader encodes a non-ASCII value per RFC 2047 and folds between encoded words.
func encodeHeader(s string) string {
	enc := mime.BEncoding.Encode("UTF-8", headerText(s))
	return strings.ReplaceAll(enc, "?= =?", "?=\r\n =?")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// b64Lines encodes s in base64 lines of 76 characters (RFC 2045).
func b64Lines(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.String()
}

// render builds the RFC 5322 message.
func render(from, to string, msg notify.Message, now time.Time) []byte {
	domain := from[strings.LastIndex(from, "@")+1:]
	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", (&mail.Address{Name: headerText(msg.From), Address: from}).String())
	hdr("To", (&mail.Address{Name: headerText(msg.To.Name), Address: to}).String())
	hdr("Subject", encodeHeader(msg.Subject))
	hdr("Date", now.UTC().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+randHex(16)+"@"+domain+">")
	hdr("Auto-Submitted", "auto-generated")
	if lang := headerText(msg.Locale); lang != "" && !strings.ContainsAny(lang, " ;:") {
		hdr("Content-Language", lang)
	}
	hdr("MIME-Version", "1.0")
	part := func(typ, body string) {
		b.WriteString("Content-Type: " + typ + "; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(b64Lines(body))
	}
	if msg.HTML == "" {
		part("text/plain", msg.Text)
		return b.Bytes()
	}
	boundary := "alt-" + randHex(12)
	hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, p := range []struct{ typ, body string }{{"text/plain", msg.Text}, {"text/html", msg.HTML}} {
		b.WriteString("--" + boundary + "\r\n")
		part(p.typ, p.body)
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}
