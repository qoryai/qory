package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/config"
)

// testEnginePassword is a password of the engine proxy file's shape: 43 characters of
// base64url with no padding, 32 bytes.
const testEnginePassword = "FakeEngineProxyPassword-0123456789abcdefghg"

// engineMarker is in every file the tests of the engine proxy file write, so a refusal
// that quotes the file shows.
const engineMarker = "zz-file-content-zz"

// engineFile writes body into a file of a new folder with mode perm, and returns its
// path.
func engineFile(t *testing.T, body string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine-proxy.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	return path
}

// engineBody is the file's JSON with the members given, in order.
func engineBody(members ...string) string {
	return "{" + strings.Join(members, ", ") + "}\n"
}

// The members of a file the start unit writes.
var (
	engineVersion  = `"version": 1`
	engineAddress  = `"address": "127.0.0.1:3129"`
	engineSecret   = `"password": "` + testEnginePassword + `"`
	engineComplete = engineBody(engineVersion, engineAddress, engineSecret)
)

// refusedEngine fails t unless err is an input error, exit 2, whose text starts with
// want, contains the path, and contains neither the password nor engineMarker.
func refusedEngine(t *testing.T, what string, err error, path, want string) {
	t.Helper()
	switch {
	case err == nil:
		t.Errorf("%s: read, want a refusal", what)
	case ExitCode(err) != ExitInput:
		t.Errorf("%s: %v, exit %d, want %d", what, err, ExitCode(err), ExitInput)
	case !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), path):
		t.Errorf("%s: %v, want one that starts with %q and contains %s", what, err, want, path)
	}
	if err != nil && (strings.Contains(err.Error(), testEnginePassword) || strings.Contains(err.Error(), engineMarker) || strings.Contains(fmt.Sprintf("%+v %#v", err, err), testEnginePassword)) {
		t.Errorf("%s: the refusal contains the file: %v", what, err)
	}
}

// TestTheEngineProxyFileIsRead reads the file the start unit writes, mode 0400 and 0600,
// with an IPv4 loopback address and ::1: the address and the password, and an
// EngineProxy that prints the address alone.
func TestTheEngineProxyFileIsRead(t *testing.T) {
	for _, c := range []struct {
		perm    os.FileMode
		address string
	}{{0o400, "127.0.0.1:3129"}, {0o600, "127.0.0.1:3129"}, {0o400, "[::1]:3129"}, {0o400, "127.1.2.3:65535"}} {
		path := engineFile(t, engineBody(engineVersion, `"address": "`+c.address+`"`, engineSecret), c.perm)
		p, err := readEngineProxy(path)
		if err != nil || p.Addr != c.address || p.Password != testEnginePassword {
			t.Errorf("%04o %s: %v, %v", c.perm, c.address, p, err)
			continue
		}
		if s := fmt.Sprintf("%v %+v %#v", p, p, *p); strings.Contains(s, testEnginePassword) {
			t.Errorf("the EngineProxy prints its password: %s", s)
		}
	}
}

// TestTheEngineProxyFileIsAFileOfQorysUser refuses a link at the path, to a file that
// reads, a folder, a named pipe, a file that is not there, a file of another user, and
// the modes 0644, 0640, 0604, 0700 and 0440, each with the fix first and the path.
func TestTheEngineProxyFileIsAFileOfQorysUser(t *testing.T) {
	good := engineFile(t, engineComplete, 0o400)
	link := filepath.Join(t.TempDir(), "engine-proxy.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	_, err := readEngineProxy(link)
	refusedEngine(t, "a link", err, link, "set wall.engine_proxy_file to the file itself: ")

	dir := t.TempDir()
	_, err = readEngineProxy(dir)
	refusedEngine(t, "a folder", err, dir, "set wall.engine_proxy_file to a regular file: ")

	pipe := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readEngineProxy(pipe)
	refusedEngine(t, "a named pipe", err, pipe, "set wall.engine_proxy_file to a regular file: ")

	missing := filepath.Join(t.TempDir(), "missing.json")
	_, err = readEngineProxy(missing)
	refusedEngine(t, "a file that is not there", err, missing, "check wall.engine_proxy_file: qory cannot open ")

	for _, c := range []struct {
		perm os.FileMode
		bits string
	}{{0o644, "0644"}, {0o640, "0640"}, {0o604, "0604"}, {0o700, "0700"}, {0o440, "0440"}, {os.ModeSetuid | 0o400, "4400"}, {os.ModeSetgid | 0o600, "2600"}} {
		path := engineFile(t, engineComplete, c.perm)
		_, err := readEngineProxy(path)
		want := "run chmod 0400 " + path + ": its mode is " + c.bits + ", and qory reads it only with no group or other bits"
		refusedEngine(t, "mode "+c.bits, err, path, want)
		if err != nil && err.Error() != want {
			t.Errorf("mode %s: %v, want %s", c.bits, err, want)
		}
	}

	before := engineProxyUID
	engineProxyUID = func() int { return os.Getuid() + 1 }
	_, err = readEngineProxy(good)
	engineProxyUID = before
	other := os.Getuid() + 1
	want := fmt.Sprintf("run chown %d %s, where %d is the user qory runs as: its owner is uid %d", other, good, other, os.Getuid())
	refusedEngine(t, "another user's", err, good, want)
	if err != nil && err.Error() != want {
		t.Errorf("another user's: %v, want %s", err, want)
	}
}

// TestTheEngineProxyFileOfAnotherOwnerStops makes the file another user's, which only
// root can, and reads it as root: it stops, as the file of a user other than qory's.
func TestTheEngineProxyFileOfAnotherOwnerStops(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root gives a file to another user")
	}
	path := engineFile(t, engineComplete, 0o400)
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	_, err := readEngineProxy(path)
	refusedEngine(t, "another owner", err, path, "run chown 0 "+path+", where 0 is the user qory runs as: its owner is uid 65534")
}

// TestTheEngineProxyFileHasTheThreeMembers refuses each file the start unit does not
// write: one over 4 KiB, one that is not JSON, not an object, or has more after the
// object, a member that is none of the three, a member written twice, a member missing,
// another version, an address that is not loopback or has no valid port, and a password
// of another shape. No refusal quotes the file.
func TestTheEngineProxyFileHasTheThreeMembers(t *testing.T) {
	for _, c := range []struct {
		what, body, want string
	}{
		{"over 4 KiB", engineBody(engineVersion, engineAddress, engineSecret, `"x": "`+strings.Repeat(engineMarker, 300)+`"`), "write only the engine proxy setting into "},
		{"not JSON", engineMarker + " " + testEnginePassword, "write one JSON object with version, address and password into "},
		{"a list", `["` + engineMarker + `", "` + testEnginePassword + `"]`, "write one JSON object with version, address and password into "},
		{"cut off", `{"version": 1, "password": "` + testEnginePassword + `", "address": "` + engineMarker, "write one JSON object with version, address and password into "},
		{"more after it", engineComplete + `{"` + engineMarker + `": 1}`, "write one JSON object with version, address and password into "},
		{"an extra member", engineBody(engineVersion, engineAddress, engineSecret, `"`+engineMarker+`": "`+testEnginePassword+`"`), "leave only version, address and password in "},
		{"a member in capitals", engineBody(engineVersion, engineAddress, `"Password": "`+testEnginePassword+`"`), "leave only version, address and password in "},
		{"the password twice", engineBody(engineVersion, engineAddress, engineSecret, engineSecret), "write password once in "},
		{"the version twice", engineBody(engineVersion, engineVersion, engineAddress, engineSecret), "write version once in "},
		{"no version", engineBody(engineAddress, engineSecret), "write version into "},
		{"no address", engineBody(engineVersion, engineSecret), "write address into "},
		{"no password", engineBody(engineVersion, engineAddress), "write password into "},
		{"version 2", engineBody(`"version": 2`, engineAddress, engineSecret), "set version to 1 in "},
		{"version as a string", engineBody(`"version": "1"`, engineAddress, engineSecret), "set version to 1 in "},
		{"version 1.0", engineBody(`"version": 1.0`, engineAddress, engineSecret), "set version to 1 in "},
		{"a private address", engineBody(engineVersion, `"address": "10.0.0.5:3129"`, engineSecret), "set address in "},
		{"an address of every interface", engineBody(engineVersion, `"address": "0.0.0.0:3129"`, engineSecret), "set address in "},
		{"a host name", engineBody(engineVersion, `"address": "localhost:3129"`, engineSecret), "set address in "},
		{"no port", engineBody(engineVersion, `"address": "127.0.0.1"`, engineSecret), "set address in "},
		{"port 0", engineBody(engineVersion, `"address": "127.0.0.1:0"`, engineSecret), "set address in "},
		{"port 65536", engineBody(engineVersion, `"address": "127.0.0.1:65536"`, engineSecret), "set address in "},
		{"a signed port", engineBody(engineVersion, `"address": "127.0.0.1:+3129"`, engineSecret), "set address in "},
		{"an address that is a number", engineBody(engineVersion, `"address": 3129`, engineSecret), "set address in "},
		{"a short password", engineBody(engineVersion, engineAddress, `"password": "`+testEnginePassword[:42]+`"`), "set password in "},
		{"a long password", engineBody(engineVersion, engineAddress, `"password": "`+testEnginePassword+`A"`), "set password in "},
		{"a padded password", engineBody(engineVersion, engineAddress, `"password": "`+testEnginePassword[:42]+`="`), "set password in "},
		{"a password of base64", engineBody(engineVersion, engineAddress, `"password": "`+testEnginePassword[:42]+`+"`), "set password in "},
		{"a password with bits after its 32 bytes", engineBody(engineVersion, engineAddress, `"password": "`+testEnginePassword[:42]+`h"`), "set password in "},
		{"an empty password", engineBody(engineVersion, engineAddress, `"password": ""`), "set password in "},
		{"a password that is a number", engineBody(engineVersion, engineAddress, `"password": 1`), "set password in "},
	} {
		path := engineFile(t, c.body, 0o400)
		_, err := readEngineProxy(path)
		refusedEngine(t, c.what, err, path, c.want)
	}
}

// TestEncloseGivesTheOpenTheMachineAndTheEngineProxy holds enclose to setting
// OpenSpec.Machine from machine, with a wall and without, and OpenSpec.EngineProxy from
// the file of wall.engine_proxy_file for a walled run alone: none without the key, and
// none, with the file not read, for a run that --wall none takes out of the wall. A file
// that does not read stops enclose as input.
func TestEncloseGivesTheOpenTheMachineAndTheEngineProxy(t *testing.T) {
	file := engineFile(t, engineComplete, 0o400)
	walled := func(proxyFile string) *config.Forager {
		return &config.Forager{Machine: "aws-ec2", Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux", EngineProxyFile: proxyFile}}
	}
	for _, c := range []struct {
		what  string
		r     *config.Forager
		o     wallOptions
		proxy bool
	}{
		{"walled, with the file", walled(file), wallOptions{}, true},
		{"walled, without it", walled(""), wallOptions{}, false},
		{"taken out of the wall", walled(filepath.Join(t.TempDir(), "missing.json")), wallOptions{name: wallOff}, false},
		{"no wall", &config.Forager{Machine: "aws-ec2"}, wallOptions{}, false},
	} {
		var spec runSpec
		if err := enclose(&spec, c.r, c.o, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
			t.Errorf("%s: %v", c.what, err)
			continue
		}
		if spec.Machine != "aws-ec2" {
			t.Errorf("%s: OpenSpec.Machine is %q", c.what, spec.Machine)
		}
		if got := spec.EngineProxy != nil; got != c.proxy {
			t.Errorf("%s: OpenSpec.EngineProxy %v, want one %v", c.what, spec.EngineProxy, c.proxy)
		}
		if c.proxy && (spec.EngineProxy.Addr != "127.0.0.1:3129" || spec.EngineProxy.Password != testEnginePassword) {
			t.Errorf("%s: OpenSpec.EngineProxy is %v", c.what, spec.EngineProxy)
		}
	}
	var spec runSpec
	if err := enclose(&spec, &config.Forager{}, wallOptions{}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil || spec.Machine != "" {
		t.Errorf("no machine: %q, %v", spec.Machine, err)
	}
	bad := engineFile(t, engineComplete, 0o644)
	err := enclose(&spec, walled(bad), wallOptions{}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir())
	refusedEngine(t, "enclose with a file of mode 0644", err, bad, "run chmod 0400 ")
}

// fetchingRun is a run whose FetchImage returns err.
type fetchingRun struct {
	nowhere
	err error
}

func (r *fetchingRun) FetchImage(context.Context) error { return r.err }

// TestFetchImageSaysHowTheFetchEnded holds fetchImage to its lines: nil for a fetch that
// succeeded; a refusal of the image, image_missing or image_user_root, as a line with the
// fix first and Forager's detail, and the code to close the run with; any other error as
// a line with the fix first and Forager's text, the engine proxy's password hidden, that
// unwraps to no error of Forager's. For a run from a run code, each line ends with
// "; the code is used", and the other error's fix is to take the work item in again.
func TestFetchImageSaysHowTheFetchEnded(t *testing.T) {
	proxy := &session.EngineProxy{Addr: "127.0.0.1:3129", Password: testEnginePassword}
	for _, fromCode := range []bool{false, true} {
		if err := fetchImage(t.Context(), &fetchingRun{}, proxy, fromCode); err != nil {
			t.Errorf("a fetch that succeeded: %v", err)
		}
	}
	const image = "registry.example/agents/go@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		err            error
		code           string
		want, fromCode string
	}{
		{refusal.New(refusal.ImageMissing, []string{image}, "the image %s is not in its registry", image), refusal.ImageMissing,
			"push the image to its registry, or select another image in the run's policy: the image " + image + " is not in its registry (image_missing)",
			"push the image to its registry, or select another image in the run's policy: the image " + image + " is not in its registry (image_missing); the code is used"},
		{refusal.New(refusal.ImageUserRoot, []string{image}, "the image %s runs as root; a run's image runs as the wall's user", image), refusal.ImageUserRoot,
			"set USER in the image to a user other than root that its /etc/passwd lists, or select another image in the run's policy: the image " + image + " runs as root; a run's image runs as the wall's user (image_user_root)",
			"set USER in the image to a user other than root that its /etc/passwd lists, or select another image in the run's policy: the image " + image + " runs as root; a run's image runs as the wall's user (image_user_root); the code is used"},
		{errors.New("wall docker: pull: proxyconnect tcp: http://qory:" + testEnginePassword + "@127.0.0.1:3129: connection refused"), "",
			"run again: the fetch of the run's image failed: wall docker: pull: proxyconnect tcp: http://qory:<hidden>@127.0.0.1:3129: connection refused",
			"take the work item in again in Qory Apiary: the fetch of the run's image failed: wall docker: pull: proxyconnect tcp: http://qory:<hidden>@127.0.0.1:3129: connection refused; the code is used"},
		{errors.New("open a new run for the host step: the run is closed"), "",
			"run again: the fetch of the run's image failed: open a new run for the host step: the run is closed",
			"take the work item in again in Qory Apiary: the fetch of the run's image failed: open a new run for the host step: the run is closed; the code is used"},
	} {
		for fromCode, want := range map[bool]string{false: c.want, true: c.fromCode} {
			err := fetchImage(t.Context(), &fetchingRun{err: c.err}, proxy, fromCode)
			var line *runLineError
			if !errors.As(err, &line) || line.Error() != want {
				t.Errorf("%v, from a run code %v: %v, want the line %q", c.err, fromCode, err, want)
				continue
			}
			var refused *imageRefusedError
			if got := errors.As(err, &refused); got != (c.code != "") || got && refused.code != c.code {
				t.Errorf("%v: the image refusal %+v, want %q", c.err, refused, c.code)
			}
			if errors.Is(err, c.err) || strings.Contains(fmt.Sprintf("%+v", err), testEnginePassword) {
				t.Errorf("%v: the line unwraps to Forager's error or contains the password", c.err)
			}
		}
	}
}
