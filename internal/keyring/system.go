// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package keyring

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	gokeyring "github.com/zalando/go-keyring"

	"github.com/code2rich/rigdepot/internal/brand"
)

// EnvKeyring is the environment variable that chooses the store. Unset, empty or "auto": the
// system keychain when there is one, files otherwise. "file": the system keychain counts as not
// there and is never called, so entries go to files unless they are recorded as kept in the
// keychain, which then is ErrUnavailable. Package testenv sets it to "file" for every test
// binary, which is how tests never reach the real keychain.
const EnvKeyring = brand.EnvPrefix + "KEYRING"

// KeyringFile is the value of EnvKeyring that keeps the system keychain out.
const KeyringFile = "file"

// EnvRealKeyring is the environment variable that runs the test of the real system keychain
// when it is "1". That test clears EnvKeyring for itself.
const EnvRealKeyring = brand.EnvPrefix + "TEST_REAL_KEYRING"

// DefaultTimeout is how long one call to the system keychain may take. Unlocking a keychain can
// ask the person for a password, so it is generous.
const DefaultTimeout = 2 * time.Minute

// backend is the system keychain as go-keyring presents it. Tests put a fake in its place.
type backend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

var (
	// errFileForced: EnvKeyring keeps the system keychain out.
	errFileForced = errors.New("keyring: " + EnvKeyring + "=" + KeyringFile + " keeps the system keychain out")
	// errNoSessionBus: on Linux, there is no D-Bus session bus to find a Secret Service on.
	errNoSessionBus = errors.New("keyring: there is no D-Bus session bus (no desktop session)")
)

// goKeyring is the real system keychain.
type goKeyring struct{}

func (goKeyring) Get(service, user string) (string, error) {
	if err := reachable(); err != nil {
		return "", err
	}
	v, err := gokeyring.Get(service, user)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (goKeyring) Set(service, user, password string) error {
	if err := reachable(); err != nil {
		return err
	}
	err := gokeyring.Set(service, user, password)
	if errors.Is(err, gokeyring.ErrSetDataTooBig) {
		return fmt.Errorf("%w: %v", ErrTooBig, err)
	}
	return err
}

func (goKeyring) Delete(service, user string) error {
	if err := reachable(); err != nil {
		return err
	}
	err := gokeyring.Delete(service, user)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// reachable rules out, before go-keyring is called, the cases where calling it would do harm or
// is not wanted: EnvKeyring says files (every test binary, through package testenv), and Linux
// without a session bus, where the D-Bus library would start a bus of its own with dbus-launch.
// An EnvKeyring value it does not know is an error rather than a guess.
func reachable() error {
	switch v := os.Getenv(EnvKeyring); v {
	case "", "auto":
	case KeyringFile:
		return errFileForced
	default:
		return fmt.Errorf("keyring: %s is %q; use \"auto\" or %q", EnvKeyring, v, KeyringFile)
	}
	if runtime.GOOS == "linux" && !sessionBusConfigured(os.Getenv, fileExists, strconv.Itoa(os.Getuid())) {
		return errNoSessionBus
	}
	return nil
}

// sessionBusConfigured reports whether the D-Bus library would find a session bus without
// starting one: the places it looks are DBUS_SESSION_BUS_ADDRESS and, in the user's runtime
// directory, the bus socket or the file that names it.
func sessionBusConfigured(getenv func(string) string, exists func(string) bool, uid string) bool {
	if a := getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" && a != "autolaunch:" {
		return true
	}
	run := "/run/user/" + uid + "/"
	return exists(run+"bus") || exists(run+"dbus-session")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// systemStore keeps entries in the system keychain under one service name. Values are
// base64-encoded there: the device key is binary, and not every keychain keeps arbitrary bytes.
type systemStore struct {
	b       backend
	service string
	timeout time.Duration
}

func (s systemStore) get(name string) ([]byte, error) {
	var v string
	err := s.call(func() (err error) { v, err = s.b.Get(s.service, name); return err })
	if err != nil {
		return nil, err
	}
	value, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("keyring: the entry %s of %s in the system keychain was not written by this program: %w", name, s.service, err)
	}
	return value, nil
}

func (s systemStore) set(name string, value []byte) error {
	enc := base64.StdEncoding.EncodeToString(value)
	return s.call(func() error { return s.b.Set(s.service, name, enc) })
}

func (s systemStore) delete(name string) error {
	err := s.call(func() error { return s.b.Delete(s.service, name) })
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// call runs f and gives up after the timeout. A call that hangs (D-Bus waiting for an answer
// that never comes) is left running; the program ends soon anyway.
func (s systemStore) call(f func() error) error {
	timeout := s.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	done := make(chan error, 1)
	go func() { done <- f() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		return fmt.Errorf("%w (%s)", ErrTimeout, timeout)
	}
}

// unavailable reports whether err says that there is no system keychain to use, as opposed to a
// keychain that is there and failed (locked, timed out, refused). Only the first is a reason to
// fall back to a file.
func unavailable(err error) bool {
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrTimeout) {
		return false
	}
	switch {
	case errors.Is(err, errFileForced), errors.Is(err, errNoSessionBus), errors.Is(err, gokeyring.ErrUnsupportedPlatform):
		return true
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, exec.ErrNotFound):
		// macOS: /usr/bin/security is missing. Linux: the bus socket named in the
		// environment is gone (a session that ended, an SSH login with a stale variable).
		return true
	case errors.Is(err, syscall.ECONNREFUSED):
		return true
	}
	if dbusNoSecretService(dbusErrorName(err)) {
		return true
	}
	if runtime.GOOS == "windows" {
		return windowsNoCredentialManager(err)
	}
	return false
}

// dbusNoSecretService reports whether a D-Bus error name means that no Secret Service answers:
// nothing owns its name and nothing could be started for it, or it has no collection go-keyring
// can use.
func dbusNoSecretService(name string) bool {
	switch {
	case name == "org.freedesktop.DBus.Error.ServiceUnknown",
		name == "org.freedesktop.DBus.Error.NameHasNoOwner",
		name == "org.freedesktop.Secret.Error.NoSuchObject",
		strings.HasPrefix(name, "org.freedesktop.DBus.Error.Spawn."):
		return true
	}
	return false
}

// dbusErrorName returns the name of the D-Bus error in err's chain, or "". The D-Bus library's
// error is a struct named Error with a string field Name; it is recognised by that shape, so
// that this module does not import the library itself (it comes with go-keyring).
func dbusErrorName(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		v := reflect.ValueOf(e)
		for v.Kind() == reflect.Pointer && !v.IsNil() {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || v.Type().Name() != "Error" {
			continue
		}
		if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.String && strings.HasPrefix(f.String(), "org.freedesktop.") {
			return f.String()
		}
	}
	return ""
}

// Windows error codes that mean the Credential Manager cannot be used in this session.
const (
	errorNotSupported       = syscall.Errno(50)   // ERROR_NOT_SUPPORTED
	errorNoSuchLogonSession = syscall.Errno(1312) // ERROR_NO_SUCH_LOGON_SESSION: no user profile loaded
)

func windowsNoCredentialManager(err error) bool {
	return errors.Is(err, errorNoSuchLogonSession) || errors.Is(err, errorNotSupported)
}
