package runnerdir_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/runner/accesskey"

	"github.com/qoryai/qory/internal/runnerdir"
)

// fixtureSecret is the runner contract's published fixture access key secret.
const fixtureSecret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// newDir is a runner file's directory of the test's own, mode 0700.
func newDir(t *testing.T) runnerdir.Dir {
	t.Helper()
	d := runnerdir.Dir(filepath.Join(t.TempDir(), "qory"))
	if _, err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	return d
}

func newKey(t *testing.T) *accesskey.Key {
	t.Helper()
	k, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// TestEnsureMakesTheDirectoryPrivate is a new directory created 0700 and an existing
// one made 0700.
func TestEnsureMakesTheDirectoryPrivate(t *testing.T) {
	d := runnerdir.Dir(filepath.Join(t.TempDir(), "a", "qory"))
	if changed, err := d.Ensure(); err != nil || changed || mode(t, string(d)) != 0o700 {
		t.Fatalf("a new directory: %v, %v, %v", changed, err, mode(t, string(d)))
	}
	os.Chmod(string(d), 0o755)
	if err := d.Check(); err == nil || !strings.Contains(err.Error(), "is mode 0755, which grants access to the group or others") {
		t.Errorf("a directory others may read: %v", err)
	}
	if changed, err := d.Ensure(); err != nil || !changed || mode(t, string(d)) != 0o700 || d.Check() != nil {
		t.Errorf("an existing directory: %v, %v, %v", changed, err, mode(t, string(d)))
	}
}

// TestTheSecretIsWrittenAndReadByTheRules is a secret written 0600 with O_EXCL and read
// back; a second write refused; and every file the rules refuse.
func TestTheSecretIsWrittenAndReadByTheRules(t *testing.T) {
	d := newDir(t)
	if _, err := d.ReadSecret(); err != runnerdir.ErrNoSecret {
		t.Fatalf("no file: %v", err)
	}
	k := newKey(t)
	if err := d.WriteSecret(k); err != nil {
		t.Fatal(err)
	}
	path := d.Path(runnerdir.SecretFile)
	if mode(t, path) != 0o600 {
		t.Errorf("mode %v", mode(t, path))
	}
	if b, _ := os.ReadFile(path); string(b) != k.Secret()+"\n" {
		t.Errorf("the file holds %d bytes", len(b))
	}
	got, err := d.ReadSecret()
	if err != nil || got.PublicKey() != k.PublicKey() || !d.SameSecret(k) || d.SameSecret(newKey(t)) {
		t.Fatalf("read back: %v", err)
	}
	if err := d.WriteSecret(newKey(t)); err == nil || !os.IsExist(errUnwrap(err)) {
		t.Errorf("a second write: %v", err)
	}
	for _, c := range []struct {
		name    string
		content string
		perm    os.FileMode
		want    string
	}{
		{"group", k.Secret(), 0o640, "is mode 0640"},
		{"others", k.Secret(), 0o602, "is mode 0602"},
		{"fixture", fixtureSecret + "\n", 0o600, "published fixture key"},
		{"crlf", k.Secret() + "\r\n", 0o600, ""},
		{"none", "", 0o600, "not an access key secret"},
		{"padding", k.Secret() + "=", 0o600, "not an access key secret"},
		{"space", " " + k.Secret(), 0o600, "not an access key secret"},
		{"large", strings.Repeat("a", 5000), 0o600, "larger than an access key secret"},
	} {
		os.Remove(path)
		if err := os.WriteFile(path, []byte(c.content), c.perm); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, c.perm)
		_, err := d.ReadSecret()
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if err != nil && (strings.Contains(err.Error(), k.Secret()[4:]) || strings.Contains(err.Error(), fixtureSecret[4:])) {
			t.Errorf("%s: the error contains the secret", c.name)
		}
	}
	os.Remove(path)
	target := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(target, []byte(k.Secret()), 0o600)
	os.Symlink(target, path)
	if _, err := d.ReadSecret(); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Errorf("a link: %v", err)
	}
	if err := d.WriteSecret(k); err == nil {
		t.Error("a write through a link")
	}
	os.Remove(path)
	os.Mkdir(path, 0o700)
	if _, err := d.ReadSecret(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a directory: %v", err)
	}
}

// errUnwrap is the innermost error.
func errUnwrap(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

// TestASecretMovedAsideGetsANameNoFileHas moves three secrets aside at the same second:
// each gets a name of its own, the moved files keep their content, and deleting them
// leaves the directory's own.
func TestASecretMovedAsideGetsANameNoFileHas(t *testing.T) {
	d := newDir(t)
	now := time.Unix(1700000000, 0)
	var keys []*accesskey.Key
	var names []string
	for range 3 {
		k := newKey(t)
		keys = append(keys, k)
		if err := d.WriteSecret(k); err != nil {
			t.Fatal(err)
		}
		name, err := d.MoveAside(now)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, filepath.Base(name))
	}
	if want := []string{"access-key-secret.old.1700000000", "access-key-secret.old.1700000001", "access-key-secret.old.1700000002"}; !slices.Equal(names, want) {
		t.Errorf("moved aside as %v, want %v", names, want)
	}
	for i, name := range names {
		if b, _ := os.ReadFile(d.Path(name)); string(b) != keys[i].Secret()+"\n" || mode(t, d.Path(name)) != 0o600 {
			t.Errorf("%s does not hold the key moved", name)
		}
	}
	if d.HasSecret() {
		t.Error("the secret is still there")
	}
	if name, err := d.MoveAside(now); name != "" || err != nil {
		t.Errorf("nothing to move: %q, %v", name, err)
	}
	old, _ := d.OldSecrets()
	if len(old) != 3 {
		t.Errorf("old secrets %v", old)
	}
	d.WriteSecret(newKey(t))
	if err := d.DeleteOldSecrets(); err != nil {
		t.Fatal(err)
	}
	if old, _ := d.OldSecrets(); len(old) != 0 || !d.HasSecret() {
		t.Errorf("after deleting: %v, %v", old, d.HasSecret())
	}
}

// TestTheMarkerAndThePendingEnrolment are the stored-secrets marker, written 0600 and
// kept when written twice, and enrolment-pending, which holds the key's fingerprint and
// not its secret, and matches its code and its key for 15 minutes; a record without a
// key's fingerprint matches nothing.
func TestTheMarkerAndThePendingEnrolment(t *testing.T) {
	d := newDir(t)
	if has, err := d.HasMarker(); has || err != nil {
		t.Fatalf("no marker: %v, %v", has, err)
	}
	if err := d.WriteMarker(); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteMarker(); err != nil {
		t.Errorf("a second marker: %v", err)
	}
	if has, _ := d.HasMarker(); !has || mode(t, d.Path(runnerdir.MarkerFile)) != 0o600 {
		t.Error("the marker")
	}
	d.RemoveMarker()
	if has, _ := d.HasMarker(); has {
		t.Error("the marker stayed")
	}

	const code = "qec_F1XT0RE0000000000000000000.uoES-kuj1vk0sq0qoGlmAg"
	now := time.Unix(1700000000, 0)
	k, other := newKey(t), newKey(t)
	key := k.PublicKey()
	if d.Pending(code, key, now) {
		t.Error("pending with no file")
	}
	if err := d.WritePending(code, key, now); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(d.Path(runnerdir.PendingFile))
	if strings.Contains(string(b), "F1XT0RE") || strings.Contains(string(b), k.Secret()) || !strings.Contains(string(b), "\n"+k.Fingerprint()+"\n") || mode(t, d.Path(runnerdir.PendingFile)) != 0o600 {
		t.Errorf("enrolment-pending holds %d bytes: no code and no secret, the key's fingerprint", len(b))
	}
	for _, c := range []struct {
		name string
		code string
		key  accesskey.PublicKey
		at   time.Time
		want bool
	}{
		{"the code and its key", code, key, now, true},
		{"within its 15 minutes", code, key, now.Add(14*time.Minute + 59*time.Second), true},
		{"after its 15 minutes", code, key, now.Add(15 * time.Minute), false},
		{"before it was written", code, key, now.Add(-time.Second), false},
		{"another code", "qec_F1XT0RE0000000000000000001.uoES-kuj1vk0sq0qoGlmAg", key, now, false},
		{"another key", code, other.PublicKey(), now, false},
	} {
		if got := d.Pending(c.code, c.key, c.at); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if err := d.WritePending(code, key, now.Add(time.Hour)); err != nil || !d.Pending(code, key, now.Add(time.Hour)) {
		t.Errorf("rewritten: %v", err)
	}
	if err := d.WritePending(code, other.PublicKey(), now); err != nil || d.Pending(code, key, now) || !d.Pending(code, other.PublicKey(), now) {
		t.Errorf("rewritten for another key: %v", err)
	}
	d.RemovePending()
	if d.Pending(code, key, now) {
		t.Error("pending after removal")
	}
	// A record of the code and the time alone, with no key's fingerprint, matches no key.
	lines := strings.SplitN(string(b), "\n", 3)
	if err := os.WriteFile(d.Path(runnerdir.PendingFile), []byte(lines[0]+"\n"+strconv.FormatInt(now.Unix(), 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d.Pending(code, key, now) {
		t.Error("a record without a key's fingerprint matched")
	}
	d.RemovePending()
}

// TestTheInstanceIDIsKeptForThisMachine is an id written with the machine's hash and
// read again, a new one where the hash or the id is not this machine's, the other
// process's file read on EEXIST, and a directory that cannot be written.
func TestTheInstanceIDIsKeptForThisMachine(t *testing.T) {
	d := newDir(t)
	machine := []byte("machine-a")
	id, kept, err := d.InstanceID(machine)
	if err != nil || !kept || accesskey.CheckInstanceID(id) != nil || !strings.HasPrefix(id, "i_") || len(id) != 24 {
		t.Fatalf("a new id: %q, %v, %v", id, kept, err)
	}
	if mode(t, d.Path(runnerdir.InstanceFile)) != 0o600 {
		t.Error("instance-id mode")
	}
	if again, kept, _ := d.InstanceID(machine); again != id || !kept {
		t.Errorf("read again: %q", again)
	}
	if other, _, _ := d.InstanceID([]byte("machine-b")); other == id {
		t.Error("another machine kept the id")
	}
	if got, ok := d.ReadInstanceID([]byte("machine-b")); !ok || got == id {
		t.Errorf("the file was not rewritten for machine b: %q", got)
	}
	os.WriteFile(d.Path(runnerdir.InstanceFile), accesskey.InstanceFile("i_written-by-another", machine), 0o600)
	if got, kept, _ := d.InstanceID(machine); got != "i_written-by-another" || !kept {
		t.Errorf("another process's file: %q", got)
	}

	t.Run("a read-only directory", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root writes a directory whatever its mode")
		}
		ro := newDir(t)
		os.Chmod(string(ro), 0o500)
		t.Cleanup(func() { os.Chmod(string(ro), 0o700) })
		a, kept, err := ro.InstanceID(machine)
		b, _, _ := ro.InstanceID(machine)
		if err != nil || kept || a == b || accesskey.CheckInstanceID(a) != nil {
			t.Errorf("%q %q %v %v", a, b, kept, err)
		}
	})
}

// TestLocksKeepKeyCommandsAndRunsApart is the key lock, shared by runs and exclusive to
// a key command, and the run lock files a key command sweeps: one whose run ended is
// removed, a live walled run's is kept, and a live unwalled run's is reported.
func TestLocksKeepKeyCommandsAndRunsApart(t *testing.T) {
	d := newDir(t)
	shared1, err := d.LockKey(false)
	if err != nil {
		t.Fatal(err)
	}
	shared2, err := d.LockKey(false)
	if err != nil {
		t.Fatal(err)
	}
	if mode(t, d.Path(runnerdir.LocksDir)) != 0o700 || mode(t, filepath.Join(d.Path(runnerdir.LocksDir), runnerdir.KeyLock)) != 0o600 {
		t.Error("the locks' modes")
	}
	got := make(chan struct{})
	go func() {
		l, err := d.LockKey(true)
		if err == nil {
			l.Release()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("the exclusive lock was taken while runs held it shared")
	case <-time.After(200 * time.Millisecond):
	}
	shared1.Release()
	shared2.Release()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the exclusive lock was never taken")
	}

	walled, err := d.LockRun("0191f2a4-3c5e-7b8d-9e0f-000000000001", true)
	if err != nil {
		t.Fatal(err)
	}
	unwalled, err := d.LockRun("0191f2a4-3c5e-7b8d-9e0f-000000000002", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.LockRun("0191f2a4-3c5e-7b8d-9e0f-000000000002", false); err == nil {
		t.Error("a second lock of a live run")
	}
	ended, err := d.LockRun("0191f2a4-3c5e-7b8d-9e0f-000000000003", false)
	if err != nil {
		t.Fatal(err)
	}
	ended.Release()
	live, err := d.Sweep()
	if err != nil || !slices.Equal(live, []string{"0191f2a4-3c5e-7b8d-9e0f-000000000002"}) {
		t.Errorf("sweep: %v, %v", live, err)
	}
	entries, _ := os.ReadDir(d.Path(runnerdir.LocksDir))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"0191f2a4-3c5e-7b8d-9e0f-000000000001.lock", "0191f2a4-3c5e-7b8d-9e0f-000000000002.lock", "key.lock"}; !slices.Equal(names, want) {
		t.Errorf("lock files %v, want %v", names, want)
	}
	unwalled.Release()
	walled.Release()
	if live, _ := d.Sweep(); len(live) != 0 {
		t.Errorf("after the runs ended: %v", live)
	}
	if entries, _ := os.ReadDir(d.Path(runnerdir.LocksDir)); len(entries) != 1 {
		t.Errorf("%d lock files after the runs ended", len(entries))
	}

	t.Run("a read-only directory", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root writes a directory whatever its mode")
		}
		ro := runnerdir.Dir(filepath.Join(t.TempDir(), "ro"))
		os.Mkdir(string(ro), 0o500)
		t.Cleanup(func() { os.Chmod(string(ro), 0o700) })
		if _, err := ro.LockKey(false); err != runnerdir.ErrReadOnly {
			t.Errorf("%v", err)
		}
	})
}
