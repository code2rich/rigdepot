// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package keyring

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/home"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fakeKeychain stands in for the system keychain. err, when set, is what every call returns.
type fakeKeychain struct {
	mu      sync.Mutex
	entries map[string]string // service + "/" + user -> stored text
	err     error
	block   chan struct{} // when set, calls wait on it
	calls   int
}

func newFake() *fakeKeychain { return &fakeKeychain{entries: map[string]string{}} }

func (f *fakeKeychain) enter() error {
	f.mu.Lock()
	f.calls++
	block, err := f.block, f.err
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return err
}

func (f *fakeKeychain) Get(service, user string) (string, error) {
	if err := f.enter(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.entries[service+"/"+user]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fakeKeychain) Set(service, user, password string) error {
	if err := f.enter(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[service+"/"+user] = password
	return nil
}

func (f *fakeKeychain) Delete(service, user string) error {
	if err := f.enter(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.entries[service+"/"+user]; !ok {
		return ErrNotFound
	}
	delete(f.entries, service+"/"+user)
	return nil
}

func (f *fakeKeychain) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// errLocked is what a keychain that is there and locked says.
var errLocked = errors.New("user interaction is not allowed")

// dataDir returns a fresh local data directory (not created) in the temporary home.
func dataDir(t *testing.T) home.Dir {
	t.Helper()
	d, err := home.At(filepath.Join(t.TempDir(), brand.Home))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func open(dir home.Dir, f *fakeKeychain, opts ...func(*Options)) *Keyring {
	o := Options{Service: "test-service", backend: f}
	for _, fn := range opts {
		fn(&o)
	}
	return Open(dir, o)
}

var deviceKey = []byte{0x00, 0xff, 0x10, '\n', 0x80, 'k', 'e', 'y', 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7}

func marker(t *testing.T, dir home.Dir) string {
	t.Helper()
	data, err := os.ReadFile(dir.Join(home.SecretsDir, MarkerFile))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func mustGet(t *testing.T, s Store, name string, want []byte) {
	t.Helper()
	got, err := s.Get(name)
	if err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get(%s) = %x, want %x", name, got, want)
	}
}

func notFound(t *testing.T, s Store, name string) {
	t.Helper()
	if _, err := s.Get(name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(%s) = %v, want ErrNotFound", name, err)
	}
}

func TestSystemKeychainKeepsBinaryValues(t *testing.T) {
	dir, f := dataDir(t), newFake()
	k := open(dir, f)
	if k.Kind() != System {
		t.Fatalf("Kind = %s with a keychain that answers", k.Kind())
	}
	notFound(t, k, DeviceKey)
	if err := k.Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	if err := k.Set(DeviceToken, []byte("<placeholder: device token>")); err != nil {
		t.Fatal(err)
	}
	mustGet(t, k, DeviceKey, deviceKey)
	// A new process finds them, too.
	mustGet(t, open(dir, f), DeviceKey, deviceKey)

	if got := f.entries["test-service/"+DeviceKey]; got != base64.StdEncoding.EncodeToString(deviceKey) {
		t.Errorf("the keychain holds %q", got)
	}
	if marker(t, dir) != markerSystem {
		t.Errorf("the choice was not recorded: %q", marker(t, dir))
	}
	if _, err := os.Stat(dir.Join(home.SecretsDir, DeviceKey)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a value went to a file although the keychain answers: %v", err)
	}
}

// The acceptance test of the task: absent and unreadable are two different answers.
func TestAbsentAndUnreadableAreDifferent(t *testing.T) {
	dir, f := dataDir(t), newFake()
	k := open(dir, f)
	if err := k.Set(DeviceToken, []byte("token")); err != nil {
		t.Fatal(err)
	}
	notFound(t, k, DeviceKey)

	f.fail(errLocked)
	for _, name := range []string{DeviceKey, DeviceToken} {
		_, err := k.Get(name)
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%s) on a locked keychain = %v; it must be an error other than ErrNotFound", name, err)
		}
	}
	if k.Kind() != System {
		t.Errorf("Kind = %s", k.Kind())
	}
}

// A keychain that held the values and is gone later (an SSH login without a desktop session)
// is unknown, never a reason to look in the files.
func TestRecordedKeychainThatIsGoneIsUnavailable(t *testing.T) {
	dir, f := dataDir(t), newFake()
	if err := open(dir, f).Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	f.fail(errNoSessionBus)
	k := open(dir, f)
	_, err := k.Get(DeviceKey)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrUnavailable", err)
	}
	if err := k.Set(DeviceToken, []byte("t")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Set = %v, want ErrUnavailable", err)
	}
	if err := k.Delete(DeviceKey); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Delete = %v, want ErrUnavailable", err)
	}
	if _, err := os.Stat(dir.Join(home.SecretsDir, DeviceToken)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a value went to a file while the keychain that holds the others is away")
	}
	if marker(t, dir) != markerSystem {
		t.Error("the record changed")
	}
}

func TestFallsBackToAPrivateFileWithoutAKeychain(t *testing.T) {
	dir, f := dataDir(t), newFake()
	f.fail(errNoSessionBus)
	var told []Fallback
	k := open(dir, f, func(o *Options) { o.OnFallback = func(fb Fallback) { told = append(told, fb) } })
	if k.Kind() != fileKind {
		t.Fatalf("Kind = %s without a keychain, want %s", k.Kind(), fileKind)
	}
	notFound(t, k, DeviceKey)
	if len(told) != 0 {
		t.Fatal("told about the fallback before anything was stored")
	}
	if err := k.Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	if err := k.Set(DeviceToken, []byte("token")); err != nil {
		t.Fatal(err)
	}
	mustGet(t, k, DeviceKey, deviceKey)
	mustGet(t, open(dir, f), DeviceToken, []byte("token"))

	if len(told) != 1 {
		t.Fatalf("told %d times, want once", len(told))
	}
	if told[0].Kind != fileKind || told[0].Dir != dir.Join(home.SecretsDir) || !errors.Is(told[0].Reason, errNoSessionBus) {
		t.Errorf("fallback notice: %+v", told[0])
	}
	if marker(t, dir) != markerFile {
		t.Errorf("record %q", marker(t, dir))
	}
	if runtime.GOOS != "windows" {
		assertPerm(t, dir.Path(), home.DirPerm)
		assertPerm(t, dir.Join(home.SecretsDir), home.DirPerm)
		assertPerm(t, dir.Join(home.SecretsDir, DeviceKey), home.FilePerm)
		assertPerm(t, dir.Join(home.SecretsDir, MarkerFile), home.FilePerm)
		data, _ := os.ReadFile(dir.Join(home.SecretsDir, DeviceKey))
		if !bytes.Equal(data, deviceKey) {
			t.Errorf("the file holds %x", data)
		}
	}
	if len(f.entries) != 0 {
		t.Errorf("the keychain got entries: %v", f.entries)
	}
}

func assertPerm(t *testing.T, p string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Errorf("%s has mode %v, want %v", p, fi.Mode().Perm(), want)
	}
}

// Values kept in files stay there when a keychain turns up later: nothing moves by itself.
func TestFilesStayInUseWhenAKeychainAppears(t *testing.T) {
	dir, f := dataDir(t), newFake()
	f.fail(errNoSessionBus)
	if err := open(dir, f).Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	f.fail(nil)
	k := open(dir, f)
	mustGet(t, k, DeviceKey, deviceKey)
	if err := k.Set(DeviceToken, []byte("t")); err != nil {
		t.Fatal(err)
	}
	if len(f.entries) != 0 || k.Kind() != fileKind {
		t.Errorf("moved to the keychain: %v, kind %s", f.entries, k.Kind())
	}
}

// A keychain that is there but fails (locked, refused) is not a reason to put the values in a
// file: they would end up outside the keychain the person has.
func TestFailingKeychainDoesNotFallBack(t *testing.T) {
	dir, f := dataDir(t), newFake()
	f.fail(errLocked)
	k := open(dir, f)
	if _, err := k.Get(DeviceKey); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v", err)
	}
	if err := k.Set(DeviceKey, deviceKey); err == nil {
		t.Fatal("Set succeeded")
	}
	if k.Kind() != Unknown {
		t.Errorf("Kind = %s", k.Kind())
	}
	if _, err := os.Stat(dir.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Error("something was written")
	}
	// Once it answers, it is used.
	f.fail(nil)
	if err := k.Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	if k.Kind() != System {
		t.Errorf("Kind = %s", k.Kind())
	}
}

func TestKeychainThatDoesNotAnswerTimesOut(t *testing.T) {
	dir, f := dataDir(t), newFake()
	f.block = make(chan struct{})
	defer close(f.block)
	k := open(dir, f, func(o *Options) { o.Timeout = 20 * time.Millisecond })
	_, err := k.Get(DeviceKey)
	if !errors.Is(err, ErrTimeout) || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrTimeout", err)
	}
	if unavailable(err) {
		t.Error("a timeout is not a missing keychain")
	}
}

func TestLooseOrOddFileIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits on Windows; the file is encrypted for the user instead")
	}
	dir, f := dataDir(t), newFake()
	f.fail(errNoSessionBus)
	k := open(dir, f)
	if err := k.Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	p := dir.Join(home.SecretsDir, DeviceKey)
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get(DeviceKey); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Get of a 0644 file = %v, want ErrInsecure", err)
	}
	st := k.Probe()
	if len(st.Insecure) != 1 || st.Insecure[0] != p {
		t.Errorf("Probe: %+v", st)
	}
	// Writing replaces it with a private file.
	if err := k.Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	mustGet(t, k, DeviceKey, deviceKey)

	elsewhere := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(elsewhere, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir.Join(home.SecretsDir, DeviceToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get(DeviceToken); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Get of a link = %v, want ErrInsecure", err)
	}
}

func TestUnreadableFileIsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs permission bits that bind the test's user")
	}
	dir, f := dataDir(t), newFake()
	f.fail(errNoSessionBus)
	k := open(dir, f)
	if err := k.Set(DeviceKey, deviceKey); err != nil {
		t.Fatal(err)
	}
	secrets := dir.Join(home.SecretsDir)
	if err := os.Chmod(secrets, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(secrets, 0o700) })
	for _, kr := range []*Keyring{k, open(dir, f)} {
		if _, err := kr.Get(DeviceKey); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Get of an unreadable file = %v; want an error other than ErrNotFound", err)
		}
	}
}

// Entry files without a record (someone removed it) are ours: they are used, not ignored.
func TestFilesWithoutARecordAreUsed(t *testing.T) {
	dir, f := dataDir(t), newFake()
	secrets, err := dir.EnsureSub(home.SecretsDir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := protect(deviceKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, DeviceKey), data, 0o600); err != nil {
		t.Fatal(err)
	}
	mustGet(t, open(dir, f), DeviceKey, deviceKey)
}

func TestUnrecognisedRecordIsUnknown(t *testing.T) {
	dir, f := dataDir(t), newFake()
	secrets, _ := dir.EnsureSub(home.SecretsDir)
	if err := os.WriteFile(filepath.Join(secrets, MarkerFile), []byte("somewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k := open(dir, f)
	if _, err := k.Get(DeviceKey); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v", err)
	}
	if err := k.Set(DeviceKey, deviceKey); err == nil {
		t.Fatal("Set succeeded")
	}
}

// Removing the last entry (logging out) removes the record, so that logging in again uses a
// keychain that has turned up meanwhile.
func TestRemovingTheLastEntryForgetsWhere(t *testing.T) {
	for _, keychain := range []bool{false, true} {
		t.Run(fmt.Sprintf("keychain=%v", keychain), func(t *testing.T) {
			dir, f := dataDir(t), newFake()
			if !keychain {
				f.fail(errNoSessionBus)
			}
			k := open(dir, f)
			for _, n := range names {
				if err := k.Set(n, []byte(n)); err != nil {
					t.Fatal(err)
				}
			}
			if err := k.Delete(DeviceKey); err != nil {
				t.Fatal(err)
			}
			if marker(t, dir) == "" {
				t.Fatal("the record went while an entry is left")
			}
			notFound(t, k, DeviceKey)
			if err := k.Delete(DeviceToken); err != nil {
				t.Fatal(err)
			}
			if err := k.Delete(DeviceToken); err != nil {
				t.Fatalf("deleting again: %v", err)
			}
			if m := marker(t, dir); m != "" {
				t.Fatalf("the record %q stayed after the last entry went", m)
			}
			f.fail(nil)
			k = open(dir, f) // the next login is another run of the program
			if err := k.Set(DeviceKey, deviceKey); err != nil {
				t.Fatal(err)
			}
			if k.Kind() != System || marker(t, dir) != markerSystem {
				t.Errorf("after logging in again: kind %s, record %q", k.Kind(), marker(t, dir))
			}
		})
	}
}

func TestNamesAndSizes(t *testing.T) {
	for _, s := range []Store{open(dataDir(t), newFake()), NewMemory()} {
		for _, name := range []string{"", "vault.json", MarkerFile, probeName, "../device.key", "Device.key"} {
			if _, err := s.Get(name); !errors.Is(err, ErrInvalidName) {
				t.Errorf("%T Get(%q) = %v", s, name, err)
			}
			if err := s.Set(name, nil); !errors.Is(err, ErrInvalidName) {
				t.Errorf("%T Set(%q) = %v", s, name, err)
			}
		}
		if err := s.Set(DeviceToken, make([]byte, MaxValueSize+1)); !errors.Is(err, ErrTooBig) {
			t.Errorf("%T Set of a large value = %v", s, err)
		}
		if err := s.Set(DeviceToken, make([]byte, MaxValueSize)); err != nil {
			t.Errorf("%T Set of the largest value = %v", s, err)
		}
	}
}

func TestServiceNameFollowsTheDataDirectory(t *testing.T) {
	h, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	def, _ := home.At(filepath.Join(h, brand.Home))
	if got := serviceFor(def); got != brand.CLILong {
		t.Errorf("default directory: %q", got)
	}
	a, b := dataDir(t), dataDir(t)
	sa, sb := serviceFor(a), serviceFor(b)
	if sa == sb || !strings.HasPrefix(sa, brand.CLILong+"-") || sa != serviceFor(a) {
		t.Errorf("service names %q and %q", sa, sb)
	}
}

func TestProbe(t *testing.T) {
	t.Run("keychain", func(t *testing.T) {
		dir, f := dataDir(t), newFake()
		st := open(dir, f).Probe()
		if st.Kind != System || st.Recorded || st.Err != nil {
			t.Errorf("%+v", st)
		}
		if _, err := os.Stat(dir.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Error("Probe created the local data directory")
		}
	})
	t.Run("no keychain", func(t *testing.T) {
		dir, f := dataDir(t), newFake()
		f.fail(errNoSessionBus)
		st := open(dir, f).Probe()
		if st.Kind != fileKind || st.Recorded || !errors.Is(st.Reason, errNoSessionBus) || st.Err != nil || st.Dir != dir.Join(home.SecretsDir) {
			t.Errorf("%+v", st)
		}
	})
	t.Run("recorded keychain gone", func(t *testing.T) {
		dir, f := dataDir(t), newFake()
		if err := open(dir, f).Set(DeviceKey, deviceKey); err != nil {
			t.Fatal(err)
		}
		f.fail(errNoSessionBus)
		st := open(dir, f).Probe()
		if st.Kind != System || !st.Recorded || !errors.Is(st.Err, ErrUnavailable) {
			t.Errorf("%+v", st)
		}
	})
	t.Run("locked", func(t *testing.T) {
		dir, f := dataDir(t), newFake()
		f.fail(errLocked)
		st := open(dir, f).Probe()
		if st.Kind != Unknown || !errors.Is(st.Err, errLocked) {
			t.Errorf("%+v", st)
		}
	})
}

// Inside a test binary the real keychain is out of reach unless the run asks for it: a test
// that opens the real store by mistake ends up with files in the temporary home.
func TestRealKeychainIsOffInTests(t *testing.T) {
	if os.Getenv(EnvRealKeyring) == "1" {
		t.Skip(EnvRealKeyring + " is set")
	}
	dir := dataDir(t)
	k := Open(dir, Options{})
	if k.Kind() != fileKind {
		t.Fatalf("Kind = %s; the real keychain answered inside a test", k.Kind())
	}
	if err := k.Set(DeviceToken, []byte("t")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir.Join(home.SecretsDir, DeviceToken)); err != nil {
		t.Fatal(err)
	}
}

// Error has the shape of the D-Bus library's error.
type Error struct {
	Name string
	Body []any
}

func (e Error) Error() string { return e.Name }

func TestUnavailableIsNarrow(t *testing.T) {
	yes := []error{
		errTestBinary,
		errNoSessionBus,
		fmt.Errorf("fork/exec /usr/bin/security: %w", os.ErrNotExist),
		fmt.Errorf("dial unix /run/user/1000/bus: %w", errConnRefused),
		Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"The name org.freedesktop.secrets was not provided by any .service files"}},
		&Error{Name: "org.freedesktop.DBus.Error.Spawn.ChildExited"},
		fmt.Errorf("wrapped: %w", Error{Name: "org.freedesktop.DBus.Error.NameHasNoOwner"}),
	}
	no := []error{
		nil,
		errLocked,
		ErrNotFound,
		fmt.Errorf("%w (2m0s)", ErrTimeout),
		Error{Name: "org.freedesktop.DBus.Error.NoReply"},
		Error{Name: "org.freedesktop.DBus.Error.AccessDenied"},
		os.ErrPermission,
	}
	for _, err := range yes {
		if !unavailable(err) {
			t.Errorf("unavailable(%v) = false", err)
		}
	}
	for _, err := range no {
		if unavailable(err) {
			t.Errorf("unavailable(%v) = true", err)
		}
	}
	if !windowsNoCredentialManager(fmt.Errorf("CredRead: %w", errorNoSuchLogonSession)) || windowsNoCredentialManager(errLocked) {
		t.Error("windowsNoCredentialManager")
	}
}

func TestSessionBusConfigured(t *testing.T) {
	none := func(string) bool { return false }
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "DBUS_SESSION_BUS_ADDRESS" {
				return v
			}
			return ""
		}
	}
	if !sessionBusConfigured(env("unix:path=/run/user/1000/bus"), none, "1000") {
		t.Error("address in the environment")
	}
	for _, v := range []string{"", "autolaunch:"} {
		if sessionBusConfigured(env(v), none, "1000") {
			t.Errorf("%q with no socket counts as a bus; the D-Bus library would start one", v)
		}
	}
	socket := func(p string) bool { return p == "/run/user/1000/bus" }
	if !sessionBusConfigured(env(""), socket, "1000") || sessionBusConfigured(env(""), socket, "1001") {
		t.Error("socket in the runtime directory")
	}
}

func TestMemory(t *testing.T) {
	m := NewMemory()
	notFound(t, m, DeviceKey)
	v := append([]byte(nil), deviceKey...)
	if err := m.Set(DeviceKey, v); err != nil {
		t.Fatal(err)
	}
	v[0] = 0x42
	mustGet(t, m, DeviceKey, deviceKey)
	got, _ := m.Get(DeviceKey)
	got[1] = 0x42
	mustGet(t, m, DeviceKey, deviceKey)

	m.Fail(errLocked)
	if _, err := m.Get(DeviceKey); !errors.Is(err, errLocked) || errors.Is(err, ErrNotFound) {
		t.Errorf("Get while failing = %v", err)
	}
	if m.Kind() != Unknown || m.Set(DeviceKey, nil) == nil || m.Delete(DeviceKey) == nil {
		t.Error("a failing store answered")
	}
	m.Fail(nil)
	mustGet(t, m, DeviceKey, deviceKey)
	m.SetKind(PlainFile)
	if m.Kind() != PlainFile || !m.Kind().IsFile() {
		t.Errorf("Kind = %s", m.Kind())
	}
	if err := m.Delete(DeviceKey); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(DeviceKey); err != nil {
		t.Fatal(err)
	}
	notFound(t, m, DeviceKey)
}

func TestKindNames(t *testing.T) {
	for k, want := range map[Kind]string{Unknown: "unknown", System: "system", Protected: "protected-file", PlainFile: "plain-file"} {
		if k.String() != want {
			t.Errorf("%d: %q", k, k.String())
		}
	}
}
