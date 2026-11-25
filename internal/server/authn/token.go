// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"math/big"
	"strings"

	"github.com/code2rich/rigdepot/internal/brand"
)

// The prefixes of the bearer tokens this server issues
// (docs/architecture/README.md#工程命名提议). They keep tokens recognisable:
// in a log (logging.Redact masks them), in a leaked file (the "suspected
// secret" check rule), and in the Authorization header, where the prefix
// says which kind of token it is.
const (
	AccessTokenPrefix = brand.CLI + "_"
	DeviceTokenPrefix = brand.CLI + "d_"
)

// A token is its prefix, then randomLen base62 characters (256 random bits)
// and checkLen base62 characters of CRC-32 over the prefix and the random
// part. The check digits let a malformed or mistyped token be refused
// without asking the database, and let a scanner tell a real token from a
// lookalike.
const (
	randomLen = 43 // ceil(256 / log2(62))
	checkLen  = 6  // ceil(32 / log2(62))
)

// tokenDisplay returns the characters of a token that are kept to recognise
// it by: the prefix and four more (an access token's "prefix", rgd_Qm4x).
func tokenDisplay(token string) string {
	end := strings.IndexByte(token, '_') + 1 + 4
	if end > len(token) {
		end = len(token)
	}
	return token[:end]
}

func base62(b []byte, width int) string {
	s := new(big.Int).SetBytes(b).Text(62)
	if len(s) < width {
		s = strings.Repeat("0", width-len(s)) + s
	}
	return s
}

func checksum(prefix, random string) string {
	sum := crc32.ChecksumIEEE([]byte(prefix + random))
	return base62([]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)}, checkLen)
}

// NewToken returns a new random token with the given prefix
// (AccessTokenPrefix or DeviceTokenPrefix).
func NewToken(prefix string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("authn: no randomness: " + err.Error())
	}
	random := base62(b[:], randomLen)
	return prefix + random + checksum(prefix, random)
}

// TokenWellFormed reports whether token looks like one NewToken(prefix)
// made: the prefix, the length, the alphabet and the check digits.
func TokenWellFormed(prefix, token string) bool {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok || len(rest) != randomLen+checkLen {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			return false
		}
	}
	return checksum(prefix, rest[:randomLen]) == rest[randomLen:]
}

// TokenDigest is what the database keeps of a token or a session: its
// SHA-256, in hex.
func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newSessionToken returns the value of a session cookie: 256 random bits.
func newSessionToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("authn: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
