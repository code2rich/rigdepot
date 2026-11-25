// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/code2rich/rigdepot/internal/server/access"
	"github.com/code2rich/rigdepot/internal/server/audit"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// The device code flow (RFC 8628) for the command line's sign-in
// (docs/architecture/06-server.md#rgd-login设备码流程):
//
//  1. The command line sends its computer's name, system, version and the
//     device's X25519 public key (StartDeviceLogin) and gets a device code,
//     which only it knows, and a user code, which the person sees.
//  2. The person opens the confirmation page in a browser where they are
//     signed in, compares the user code and the computer
//     (DeviceRequest), and approves (ApproveDevice) or refuses (DenyDevice).
//     Approving makes the device; it is audited as a sign-in from the
//     command line by device code.
//  3. Meanwhile the command line polls with the device code (PollDevice).
//     The first poll after the approval issues the device token, once.
//
// A device token is a bearer token bound to one device. Signing a device out
// (SignOutDevice, SignOutDevices) or 90 days without a request
// (DeviceIdle) ends it; so does disabling the account, which Service.Device
// checks on every request.

// The parameters of the flow.
const (
	// DeviceCodeLifetime is how long a request waits for the person.
	DeviceCodeLifetime = 10 * time.Minute
	// DevicePollInterval is how long the command line waits between polls
	// at first. Polling faster adds slowDownStep (RFC 8628, 3.5).
	DevicePollInterval = 5 * time.Second
	slowDownStep       = 5 * time.Second
	// DeviceIdle is how long a device may go without a request before it
	// is signed out.
	DeviceIdle = 90 * 24 * time.Hour
	// deviceCodeKept is how long an ended request stays, so that the
	// confirmation page can still say what became of it.
	deviceCodeKept = 24 * time.Hour
)

// The statuses of a sign-in request, as the confirmation page shows them.
const (
	DeviceRequestPending  = "pending"
	DeviceRequestApproved = "approved"
	DeviceRequestDenied   = "denied"
	DeviceRequestExpired  = "expired"
)

// What device_codes.status holds: the request statuses, and issued once the
// command line has its token.
const codeIssued = "issued"

// Where the device key lives (the contract's key_storage).
const (
	KeyStorageKeyring = "keyring"
	KeyStorageFile    = "file"
)

// MethodDeviceCode is the sign-in method of the device code flow in the
// audit log.
const MethodDeviceCode = "device_code"

// The audit log's actions of the device code flow, besides ActionLogin.
const (
	ActionDeviceDeny    = "auth.device.deny"
	ActionDeviceSignOut = "auth.device.sign_out"
)

// The failures of the device code flow. The ones of polling are RFC 8628's
// error responses (3.5), which the HTTP layer answers with 400.
var (
	// ErrAuthorizationPending: the person has not decided yet.
	ErrAuthorizationPending = &Error{Kind: KindDeviceFlow, Code: "device.authorization_pending"}
	// ErrSlowDown: polled before the interval was over. Params["interval"]
	// is the new interval in seconds.
	ErrSlowDown = &Error{Kind: KindDeviceFlow, Code: "device.slow_down"}
	// ErrDeviceExpired: the device code is unknown, expired or already used.
	ErrDeviceExpired = &Error{Kind: KindDeviceFlow, Code: "device.expired"}
	// ErrDeviceDenied: the person refused the request.
	ErrDeviceDenied = &Error{Kind: KindDeviceFlow, Code: "device.denied"}
	// ErrDeviceRequestExpired: approving or refusing a request that has
	// expired.
	ErrDeviceRequestExpired = &Error{Kind: KindConflict, Code: "device.expired"}
	// ErrDeviceRequestDecided: approving or refusing a request that was
	// already approved or refused.
	ErrDeviceRequestDecided = &Error{Kind: KindConflict, Code: "device.decided"}
)

// DeviceInfo is what the command line says about its computer.
type DeviceInfo struct {
	Name       string // the computer's name, 1 to 64 characters
	OS         string // darwin, linux or windows
	Arch       string // such as arm64
	CLIVersion string // MAJOR.MINOR.PATCH with an optional pre-release suffix
	PublicKey  string // X25519, 32 bytes, unpadded base64url
	KeyStorage string // KeyStorageKeyring or KeyStorageFile
}

// DeviceCode is the answer to the command line's request.
type DeviceCode struct {
	DeviceCode string // the command line's secret: only its digest is kept
	UserCode   string // eight characters, without the hyphen (ShowUserCode adds it)
	ExpiresIn  time.Duration
	Interval   time.Duration
}

// DeviceRequest is a sign-in request as the confirmation page shows it.
type DeviceRequest struct {
	UserCode    string // XXXX-XXXX
	Name        string
	OS          string
	Arch        string
	CLIVersion  string
	Status      string // DeviceRequestPending, …Approved, …Denied or …Expired
	RequestedAt time.Time
	ExpiresAt   time.Time
}

// IssuedDevice is the answer to the poll after the approval: the device
// token, in plain, in this one answer only.
type IssuedDevice struct {
	Token    string
	DeviceID string
	User     access.User
}

// Device is a signed-in computer as its owner sees it.
type Device struct {
	ID         string
	Name       string
	OS         string
	Arch       string
	CLIVersion string
	KeyStorage string
	PublicKey  string
	Current    bool // the device making the request
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// DevicesOptions are what Devices needs besides the database.
type DevicesOptions struct {
	// Access keeps the accounts. Required.
	Access *access.Service
	// Audit receives the sign-ins, refusals and sign-outs. Required.
	Audit *audit.Log
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Devices runs the device code flow and keeps the devices. It is the
// DeviceTokens of Service.
type Devices struct {
	db     *dbx.DB
	access *access.Service
	audit  *audit.Log
	now    func() time.Time
}

var _ DeviceTokens = (*Devices)(nil)

// NewDevices returns the devices on db.
func NewDevices(db *dbx.DB, o DevicesOptions) *Devices {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Devices{db: db, access: o.Access, audit: o.Audit, now: o.Now}
}

func (d *Devices) millis() int64 { return dbx.Millis(d.now()) }

// userCodeAlphabet is the alphabet of user codes: capital letters and
// digits without the look-alikes 0, O, 1, I and L.
const userCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

const userCodeLen = 8

func newUserCode() string {
	b := make([]byte, userCodeLen)
	n := big.NewInt(int64(len(userCodeAlphabet)))
	for i := range b {
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			panic("authn: no randomness: " + err.Error())
		}
		b[i] = userCodeAlphabet[k.Int64()]
	}
	return string(b)
}

// normalUserCode returns a user code as stored (eight characters, no
// hyphen), from what a person typed: any case, with or without the hyphen
// and spaces. False when it cannot be one.
func normalUserCode(s string) (string, bool) {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	if len(s) != userCodeLen {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(userCodeAlphabet, s[i]) < 0 {
			return "", false
		}
	}
	return s, true
}

// ShowUserCode writes a stored user code the way people see it: KQ7M-2XHP.
func ShowUserCode(code string) string {
	if len(code) != userCodeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

func newDeviceCode() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("authn: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func codeDigest(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// pendingDigest is the token digest of a device whose command line has not
// collected its token yet. It is not hex, so no token's digest equals it.
func pendingDigest(codeID string) string { return "pending:" + codeID }

var (
	semver   = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	archForm = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
)

const maxDeviceName = 64

// check validates and tidies what the command line sent.
func (i *DeviceInfo) check() error {
	var fields []access.FieldError
	i.Name = strings.TrimSpace(i.Name)
	switch l := utf8.RuneCountInString(i.Name); {
	case !utf8.ValidString(i.Name) || strings.ContainsFunc(i.Name, unicode.IsControl):
		fields = append(fields, access.FieldError{Field: "name", Code: "invalid"})
	case l == 0:
		fields = append(fields, access.FieldError{Field: "name", Code: "required"})
	case l > maxDeviceName:
		fields = append(fields, access.FieldError{Field: "name", Code: "too_long"})
	}
	switch i.OS {
	case "darwin", "linux", "windows":
	default:
		fields = append(fields, access.FieldError{Field: "os", Code: "enum"})
	}
	if !archForm.MatchString(i.Arch) {
		fields = append(fields, access.FieldError{Field: "arch", Code: "invalid"})
	}
	if len(i.CLIVersion) > 64 || !semver.MatchString(i.CLIVersion) {
		fields = append(fields, access.FieldError{Field: "cli_version", Code: "invalid"})
	}
	if !validPublicKey(i.PublicKey) {
		fields = append(fields, access.FieldError{Field: "public_key", Code: "invalid"})
	}
	switch i.KeyStorage {
	case KeyStorageKeyring, KeyStorageFile:
	default:
		fields = append(fields, access.FieldError{Field: "key_storage", Code: "enum"})
	}
	if len(fields) > 0 {
		return invalid(fields...)
	}
	return nil
}

// validPublicKey reports whether s is an X25519 public key the way the
// contract sends it: 32 bytes in unpadded base64url. The all-zero key is
// refused: nothing could be sealed to it.
func validPublicKey(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != 32 {
		return false
	}
	if _, err := ecdh.X25519().NewPublicKey(b); err != nil {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return true
		}
	}
	return false
}

// StartDeviceLogin records a sign-in request of the command line and
// returns its codes. Anyone may ask; nothing is signed in until a person
// approves in the browser.
func (d *Devices) StartDeviceLogin(ctx context.Context, info DeviceInfo) (DeviceCode, error) {
	if err := info.check(); err != nil {
		return DeviceCode{}, err
	}
	now := d.now()
	out := DeviceCode{DeviceCode: newDeviceCode(), ExpiresIn: DeviceCodeLifetime, Interval: DevicePollInterval}
	// A user code may collide with one still kept; draw again.
	for attempt := 0; ; attempt++ {
		out.UserCode = newUserCode()
		_, err := d.db.ExecContext(ctx, `INSERT INTO device_codes (id, device_code_digest, user_code, device_name, os, arch, cli_version, public_key,
			key_storage, status, interval_seconds, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			dbx.NewID(), codeDigest(out.DeviceCode), out.UserCode, info.Name, info.OS, info.Arch, info.CLIVersion, info.PublicKey,
			info.KeyStorage, DeviceRequestPending, int(DevicePollInterval/time.Second), dbx.Millis(now), dbx.Millis(now.Add(DeviceCodeLifetime)))
		if err == nil {
			return out, nil
		}
		if !dbx.IsUniqueViolation(err) || attempt >= 4 {
			return DeviceCode{}, err
		}
	}
}

// codeRow is a row of device_codes.
type codeRow struct {
	id, userCode, name, os, arch, version, publicKey, keyStorage, status string
	userID, deviceID                                                     sql.NullString
	interval                                                             int
	created, expires                                                     int64
	lastPolled                                                           sql.NullInt64
}

const codeColumns = `id, user_code, device_name, os, arch, cli_version, public_key, key_storage, status, user_id, device_id,
	interval_seconds, created_at, expires_at, last_polled_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanCode(r rowScanner) (codeRow, error) {
	var c codeRow
	err := r.Scan(&c.id, &c.userCode, &c.name, &c.os, &c.arch, &c.version, &c.publicKey, &c.keyStorage, &c.status,
		&c.userID, &c.deviceID, &c.interval, &c.created, &c.expires, &c.lastPolled)
	return c, err
}

// PollDevice is the command line asking whether the person has decided.
// It returns the device token once, at the first poll after the approval;
// otherwise one of ErrAuthorizationPending, ErrSlowDown, ErrDeviceDenied
// and ErrDeviceExpired.
func (d *Devices) PollDevice(ctx context.Context, deviceCode string) (IssuedDevice, error) {
	if deviceCode == "" || len(deviceCode) > 256 {
		return IssuedDevice{}, ErrDeviceExpired
	}
	now := d.now()
	var (
		out    IssuedDevice
		answer error // the poll's answer; the transaction commits either way
	)
	err := d.db.InTx(ctx, func(tx *dbx.Tx) error {
		c, err := scanCode(tx.QueryRowContext(ctx, `SELECT `+codeColumns+` FROM device_codes WHERE device_code_digest = ?`, codeDigest(deviceCode)))
		if errors.Is(err, sql.ErrNoRows) {
			answer = ErrDeviceExpired
			return nil
		}
		if err != nil {
			return err
		}
		interval := time.Duration(c.interval) * time.Second
		if c.lastPolled.Valid && now.Sub(dbx.Time(c.lastPolled.Int64)) < interval {
			// Too fast: the interval grows for this and every later poll.
			interval += slowDownStep
			if _, err := tx.ExecContext(ctx, `UPDATE device_codes SET interval_seconds = ?, last_polled_at = ? WHERE id = ?`,
				int(interval/time.Second), dbx.Millis(now), c.id); err != nil {
				return err
			}
			e := *ErrSlowDown
			e.Params = map[string]any{"interval": int(interval / time.Second)}
			answer = &e
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE device_codes SET last_polled_at = ? WHERE id = ?`, dbx.Millis(now), c.id); err != nil {
			return err
		}
		switch {
		case c.status == DeviceRequestDenied:
			answer = ErrDeviceDenied
			return nil
		case c.status == codeIssued, !now.Before(dbx.Time(c.expires)):
			answer = ErrDeviceExpired
			return nil
		case c.status == DeviceRequestPending:
			answer = ErrAuthorizationPending
			return nil
		case c.status != DeviceRequestApproved || !c.userID.Valid || !c.deviceID.Valid:
			answer = ErrDeviceExpired
			return nil
		}
		u, err := d.access.GetUser(ctx, access.System, c.userID.String)
		if errors.Is(err, access.ErrNotFound) || err == nil && u.Status != access.StatusActive {
			// The account went away or was disabled since it approved.
			answer = ErrDeviceDenied
			return nil
		}
		if err != nil {
			return err
		}
		token := NewToken(DeviceTokenPrefix)
		res, err := tx.ExecContext(ctx, `UPDATE devices SET token_digest = ?, issued_at = ?, last_seen_at = ? WHERE id = ? AND revoked_at IS NULL`,
			TokenDigest(token), dbx.Millis(now), dbx.Millis(now), c.deviceID.String)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			// Signed out before it collected its token.
			answer = ErrDeviceExpired
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE device_codes SET status = ? WHERE id = ?`, codeIssued, c.id); err != nil {
			return err
		}
		out = IssuedDevice{Token: token, DeviceID: c.deviceID.String, User: u}
		return nil
	})
	if err != nil {
		return IssuedDevice{}, err
	}
	if answer != nil {
		return IssuedDevice{}, answer
	}
	return out, nil
}

// requestStatus is what the confirmation page shows for a row.
func requestStatus(c codeRow, now time.Time) string {
	switch c.status {
	case DeviceRequestDenied:
		return DeviceRequestDenied
	case DeviceRequestApproved, codeIssued:
		return DeviceRequestApproved
	}
	if !now.Before(dbx.Time(c.expires)) {
		return DeviceRequestExpired
	}
	return DeviceRequestPending
}

// findRequest returns the request with the user code a person typed, as
// far as caller may see it: a request someone else decided is not theirs.
func findRequest(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, userCode, callerID string) (codeRow, error) {
	code, ok := normalUserCode(userCode)
	if !ok {
		return codeRow{}, ErrNotFound
	}
	c, err := scanCode(q.QueryRowContext(ctx, `SELECT `+codeColumns+` FROM device_codes WHERE user_code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if c.userID.Valid && c.userID.String != callerID {
		return c, ErrNotFound
	}
	return c, nil
}

// browserUser checks that c is a signed-in browser of an active account:
// only they may look at, approve or refuse sign-in requests.
func (d *Devices) browserUser(ctx context.Context, c Caller) (access.User, error) {
	if !c.browser() {
		return access.User{}, ErrDenied
	}
	u, err := d.access.GetUser(ctx, access.System, c.Actor.UserID)
	if errors.Is(err, access.ErrNotFound) {
		return u, ErrUnauthenticated
	}
	if err != nil {
		return u, err
	}
	if u.Status != access.StatusActive {
		return u, ErrDenied
	}
	return u, nil
}

// DeviceRequest returns the sign-in request with the user code, for the
// confirmation page. Only from a browser session.
func (d *Devices) DeviceRequest(ctx context.Context, c Caller, userCode string) (DeviceRequest, error) {
	u, err := d.browserUser(ctx, c)
	if err != nil {
		return DeviceRequest{}, err
	}
	r, err := findRequest(ctx, d.db, userCode, u.ID)
	if err != nil {
		return DeviceRequest{}, err
	}
	return DeviceRequest{
		UserCode: ShowUserCode(r.userCode), Name: r.name, OS: r.os, Arch: r.arch, CLIVersion: r.version,
		Status: requestStatus(r, d.now()), RequestedAt: dbx.Time(r.created), ExpiresAt: dbx.Time(r.expires),
	}, nil
}

// decidable returns the pending request with the user code inside tx, or
// why it cannot be decided.
func (d *Devices) decidable(ctx context.Context, tx *dbx.Tx, userCode, userID string) (codeRow, error) {
	r, err := findRequest(ctx, tx, userCode, userID)
	if err != nil {
		return r, err
	}
	switch requestStatus(r, d.now()) {
	case DeviceRequestPending:
		return r, nil
	case DeviceRequestExpired:
		return r, ErrDeviceRequestExpired
	}
	return r, ErrDeviceRequestDecided
}

func deviceObject(id, name string) audit.Object {
	return audit.Object{Kind: "device", ID: id, Label: name}
}

// ApproveDevice lets the computer of a request sign in as the caller: it
// makes the device, whose command line collects its token at the next poll.
// Only from a browser session, for an activated account. The sign-in is in
// the audit log as the command line's, by device code.
func (d *Devices) ApproveDevice(ctx context.Context, c Caller, userCode string) error {
	u, err := d.browserUser(ctx, c)
	if err != nil {
		return err
	}
	return d.db.InTx(ctx, func(tx *dbx.Tx) error {
		r, err := d.decidable(ctx, tx, userCode, u.ID)
		if err != nil {
			return err
		}
		now := d.millis()
		deviceID := dbx.NewID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, user_id, name, os, arch, cli_version, public_key, token_digest, key_storage, created_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			deviceID, u.ID, r.name, r.os, r.arch, r.version, r.publicKey, pendingDigest(r.id), r.keyStorage, now, now); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE device_codes SET status = ?, user_id = ?, device_id = ? WHERE id = ? AND status = ?`,
			DeviceRequestApproved, u.ID, deviceID, r.id, DeviceRequestPending)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err == nil {
				err = ErrDeviceRequestDecided
			}
			return err
		}
		_, err = d.audit.Append(ctx, tx, audit.Entry{
			Category: audit.CategoryLogin, Method: MethodDeviceCode,
			Actor:  audit.Actor{UserID: u.ID, Origin: audit.OriginCLI, DeviceID: deviceID},
			Action: ActionLogin, Object: deviceObject(deviceID, r.name),
			Change: map[string]any{"os": r.os, "arch": r.arch, "cli_version": r.version, "key_storage": r.keyStorage},
		})
		return err
	})
}

// DenyDevice refuses a sign-in request: its command line stops polling with
// ErrDeviceDenied. Only from a browser session.
func (d *Devices) DenyDevice(ctx context.Context, c Caller, userCode string) error {
	u, err := d.browserUser(ctx, c)
	if err != nil {
		return err
	}
	return d.db.InTx(ctx, func(tx *dbx.Tx) error {
		r, err := d.decidable(ctx, tx, userCode, u.ID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE device_codes SET status = ?, user_id = ? WHERE id = ? AND status = ?`,
			DeviceRequestDenied, u.ID, r.id, DeviceRequestPending)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err == nil {
				err = ErrDeviceRequestDecided
			}
			return err
		}
		_, err = d.audit.Append(ctx, tx, audit.Entry{
			Category: audit.CategoryLogin, Method: MethodDeviceCode, Actor: auditActor(c.Actor),
			Action: ActionDeviceDeny, Object: audit.Object{Kind: "device_request", ID: r.id, Label: r.name},
			Change: map[string]any{"os": r.os, "arch": r.arch, "cli_version": r.version},
		})
		return err
	})
}

// liveDevices is the condition of the devices that are signed in at now:
// collected their token, not signed out, not idle for DeviceIdle.
const liveDevices = `issued_at IS NOT NULL AND revoked_at IS NULL AND last_seen_at > ?`

// MyDevices returns the computers signed in as the caller, most recently
// seen first.
func (d *Devices) MyDevices(ctx context.Context, c Caller) ([]Device, error) {
	if c.Actor.UserID == "" || c.Actor.TokenID != "" {
		return nil, ErrDenied
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, os, arch, cli_version, key_storage, public_key, created_at, last_seen_at
		FROM devices WHERE user_id = ? AND `+liveDevices+` ORDER BY last_seen_at DESC, id DESC`,
		c.Actor.UserID, dbx.Millis(d.now().Add(-DeviceIdle)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var v Device
		var created, seen int64
		if err := rows.Scan(&v.ID, &v.Name, &v.OS, &v.Arch, &v.CLIVersion, &v.KeyStorage, &v.PublicKey, &created, &seen); err != nil {
			return nil, err
		}
		v.CreatedAt, v.LastSeenAt = dbx.Time(created), dbx.Time(seen)
		v.Current = v.ID == c.Actor.DeviceID
		out = append(out, v)
	}
	return out, rows.Err()
}

// LiveDevices returns every computer that is signed in right now, as device id → user id: it
// collected its token, was not signed out and was not idle for DeviceIdle. Package delivery
// counts installs on these.
func (d *Devices) LiveDevices(ctx context.Context) (map[string]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, user_id FROM devices WHERE `+liveDevices, dbx.Millis(d.now().Add(-DeviceIdle)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, user string
		if err := rows.Scan(&id, &user); err != nil {
			return nil, err
		}
		out[id] = user
	}
	return out, rows.Err()
}

// SignOutDevice signs one of the caller's computers out: its device token
// is refused from its next request. Signing out a device that is already
// signed out is not an error; someone else's is ErrNotFound.
func (d *Devices) SignOutDevice(ctx context.Context, c Caller, deviceID string) error {
	if c.Actor.UserID == "" || c.Actor.TokenID != "" {
		return ErrDenied
	}
	return d.db.InTx(ctx, func(tx *dbx.Tx) error {
		var name string
		var revoked sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT name, revoked_at FROM devices WHERE id = ? AND user_id = ?`, deviceID, c.Actor.UserID).Scan(&name, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || revoked.Valid {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE id = ?`, d.millis(), deviceID); err != nil {
			return err
		}
		_, err = d.audit.Append(ctx, tx, audit.Entry{
			Category: audit.CategoryLogin, Actor: auditActor(c.Actor), Action: ActionDeviceSignOut, Object: deviceObject(deviceID, name),
		})
		return err
	})
}

// SignOutDevices signs out every computer of the account inside tx; see
// DeviceTokens. Devices approved whose command line has not collected its
// token yet are signed out too, but not counted.
func (d *Devices) SignOutDevices(ctx context.Context, tx *dbx.Tx, userID string) (int, error) {
	now := d.millis()
	res, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE user_id = ? AND `+liveDevices,
		now, userID, dbx.Millis(d.now().Add(-DeviceIdle)))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE user_id = ? AND issued_at IS NULL AND revoked_at IS NULL`, now, userID); err != nil {
		return 0, err
	}
	return int(n), nil
}

// VerifyDeviceToken returns the account and the device of a live device
// token; see DeviceTokens. A device idle for DeviceIdle is refused even
// before the background job signs it out.
func (d *Devices) VerifyDeviceToken(ctx context.Context, token string) (string, string, error) {
	if !TokenWellFormed(DeviceTokenPrefix, token) {
		return "", "", ErrUnauthenticated
	}
	var (
		id, userID string
		seen       int64
		issued     sql.NullInt64
		revoked    sql.NullInt64
	)
	err := d.db.QueryRowContext(ctx, `SELECT id, user_id, last_seen_at, issued_at, revoked_at FROM devices WHERE token_digest = ?`,
		TokenDigest(token)).Scan(&id, &userID, &seen, &issued, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUnauthenticated
	}
	if err != nil {
		return "", "", err
	}
	now := d.now()
	if revoked.Valid || !issued.Valid || !now.Before(dbx.Time(seen).Add(DeviceIdle)) {
		return "", "", ErrUnauthenticated
	}
	if now.Sub(dbx.Time(seen)) >= touchEvery {
		if _, err := d.db.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL`, dbx.Millis(now), id); err != nil {
			return "", "", err
		}
	}
	return userID, id, nil
}

// DeviceName returns the name of a device for the audit log, signed out or
// not; false when there is no such device.
func (d *Devices) DeviceName(ctx context.Context, id string) (string, bool) {
	var name string
	if err := d.db.QueryRowContext(ctx, `SELECT name FROM devices WHERE id = ?`, id).Scan(&name); err != nil {
		return "", false
	}
	return name, true
}

// PurgeDevices is the background job of the device code flow: it signs
// out the devices idle for DeviceIdle (each in the audit log, by the
// system), and deletes the requests that ended more than a day ago with the
// devices that never collected their token.
func (d *Devices) PurgeDevices(ctx context.Context) error {
	now := d.now()
	err := d.db.InTx(ctx, func(tx *dbx.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, user_id, name FROM devices WHERE issued_at IS NOT NULL AND revoked_at IS NULL AND last_seen_at <= ?`,
			dbx.Millis(now.Add(-DeviceIdle)))
		if err != nil {
			return err
		}
		type idle struct{ id, userID, name string }
		var list []idle
		for rows.Next() {
			var v idle
			if err := rows.Scan(&v.id, &v.userID, &v.name); err != nil {
				rows.Close()
				return err
			}
			list = append(list, v)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, v := range list {
			if _, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE id = ?`, dbx.Millis(now), v.id); err != nil {
				return err
			}
			if _, err := d.audit.Append(ctx, tx, audit.Entry{
				Category: audit.CategoryLogin, Actor: audit.Actor{Origin: audit.OriginSystem}, Action: ActionDeviceSignOut,
				Object: deviceObject(v.id, v.name), Change: map[string]any{"reason": "inactive", "user_id": v.userID},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	cutoff := dbx.Millis(now.Add(-deviceCodeKept))
	return d.db.InTx(ctx, func(tx *dbx.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_codes WHERE expires_at <= ?`, cutoff); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE issued_at IS NULL AND created_at <= ?
			AND NOT EXISTS (SELECT 1 FROM device_codes WHERE device_codes.device_id = devices.id)`, cutoff)
		return err
	})
}
