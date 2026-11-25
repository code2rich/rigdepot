// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/code2rich/rigdepot/internal/server/access"
)

// HashParams are the cost of argon2id.
type HashParams struct {
	Memory  uint32 // KiB
	Time    uint32 // passes
	Threads uint8
}

// DefaultHashParams follow OWASP's smallest recommended argon2id setting
// (19 MiB, 2 passes, 1 thread): about 19 MiB of memory per hash, so that a
// 2 GB machine stays well clear of trouble with MaxConcurrentHashes of them.
var DefaultHashParams = HashParams{Memory: 19 * 1024, Time: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
)

// The limits of a password, in characters. The contract's minLength and
// maxLength; the HTTP layer does not enforce them, so this package does.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
)

// hasher hashes passwords with at most a fixed number of hashes running at
// once.
type hasher struct {
	p     HashParams
	slots chan struct{}

	dummyOnce sync.Once
	dummy     string
}

func newHasher(p HashParams, concurrent int) *hasher {
	return &hasher{p: p, slots: make(chan struct{}, concurrent)}
}

// acquire waits for a free slot, or for ctx to end.
func (h *hasher) acquire(ctx context.Context) (release func(), err error) {
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// hash returns the encoded argon2id hash of password, in the usual
// $argon2id$v=19$m=…,t=…,p=…$salt$key form, which carries its parameters so
// that they can change later.
func (h *hasher) hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	release, err := h.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, h.p.Time, h.p.Memory, h.p.Threads, keyLen)
	return encodeHash(h.p, salt, key), nil
}

func encodeHash(p HashParams, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

var errBadHash = errors.New("authn: stored password hash is not an argon2id hash this version reads")

func decodeHash(encoded string) (p HashParams, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errBadHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil || p.Memory == 0 || p.Time == 0 || p.Threads == 0 {
		return p, nil, nil, errBadHash
	}
	// A stored hash that asks for absurd amounts of memory is not ours.
	if p.Memory > 1<<20 || p.Time > 64 {
		return p, nil, nil, errBadHash
	}
	b64 := base64.RawStdEncoding
	if salt, err = b64.DecodeString(parts[4]); err != nil || len(salt) < 8 {
		return p, nil, nil, errBadHash
	}
	if key, err = b64.DecodeString(parts[5]); err != nil || len(key) < 16 {
		return p, nil, nil, errBadHash
	}
	return p, salt, key, nil
}

// verify reports whether password matches encoded, and whether encoded was
// made with other parameters than the current ones and should be replaced.
// A hash that does not decode is an error, never a match.
func (h *hasher) verify(ctx context.Context, encoded, password string) (ok, rehash bool, err error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	release, err := h.acquire(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, ok && (p != h.p || len(want) != keyLen || len(salt) != saltLen), nil
}

// burn spends the time and memory of one verification, for attempts on
// accounts that do not exist or have no password, so that the answer does
// not come back faster for them.
func (h *hasher) burn(ctx context.Context, password string) {
	h.dummyOnce.Do(func() {
		h.dummy = encodeHash(h.p, make([]byte, saltLen), make([]byte, keyLen))
	})
	h.verify(ctx, h.dummy, password)
}

// checkPassword applies the rules for a new password: 12 to 256 characters
// of valid UTF-8, and not the account name.
func checkPassword(field, password, username string) error {
	n := utf8.RuneCountInString(password)
	var code string
	switch {
	case !utf8.ValidString(password):
		code = "invalid"
	case n == 0:
		code = "required"
	case n < MinPasswordLength:
		code = "too_short"
	case n > MaxPasswordLength:
		code = "too_long"
	case username != "" && strings.EqualFold(strings.TrimSpace(password), username):
		code = "same_as_username"
	default:
		return nil
	}
	return invalid(access.FieldError{Field: field, Code: code})
}
