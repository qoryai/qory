package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// fakeMountsEngine writes a program in place of docker whose ps prints ids, one a line, and
// whose inspect prints the mounts' sources of those ids, one a line; with fail set, ps
// exits 1.
func fakeMountsEngine(t *testing.T, ids, sources string, fail bool) string {
	t.Helper()
	program := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \"$1\" in\nps) "
	if fail {
		script += "echo 'Cannot connect to the Docker daemon' >&2; exit 1 ;;\n"
	} else {
		script += "printf '%s' '" + ids + "' ;;\n"
	}
	script += "inspect) printf '%s' '" + sources + "' ;;\nesac\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return program
}

// TestEngineMountsListsTheRunningContainersMounts reads the mounts' sources of the
// running containers through the engine's program, none for no container, and returns an
// error when the engine does not answer.
func TestEngineMountsListsTheRunningContainersMounts(t *testing.T) {
	got, err := engineMounts(fakeMountsEngine(t, "aaa\nbbb\n", "/srv/a\n/srv/b\n\n/srv/c\n", false))(context.Background())
	if want := []string{"/srv/a", "/srv/b", "/srv/c"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("mounts = %v, %v, want %v", got, err, want)
	}
	got, err = engineMounts(fakeMountsEngine(t, "", "/srv/never\n", false))(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("with no container: %v, %v, want none", got, err)
	}
	if _, err = engineMounts(fakeMountsEngine(t, "", "", true))(context.Background()); err == nil {
		t.Error("an engine that does not answer gave no error")
	}
}

// TestEngineMountsGivesTheEngineNoAccessKeyVariables runs the engine's program with the
// access key's variables and the run key set in this process: the program writes the
// names of its environment, never the values, and has none of the four, and has the
// others.
func TestEngineMountsGivesTheEngineNoAccessKeyVariables(t *testing.T) {
	secrets := []string{accesskey.EnvSecret, accesskey.EnvID, accesskey.EnvPin, accesskey.EnvRunKey}
	for _, name := range secrets {
		t.Setenv(name, "set-by-the-test")
	}
	t.Setenv("QORY_TEST_ENGINE_SEES", "1")
	dir := t.TempDir()
	names := filepath.Join(dir, "names")
	program := filepath.Join(dir, "docker")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nenv | sed 's/=.*//' >> "+names+"\ncase \"$1\" in\nps) echo abc ;;\nesac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := engineMounts(program)(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(names)
	if err != nil {
		t.Fatal(err)
	}
	seen := strings.Fields(string(data))
	if !slices.Contains(seen, "QORY_TEST_ENGINE_SEES") {
		t.Errorf("the engine's program did not get the process's environment: %d names", len(seen))
	}
	for _, name := range secrets {
		if slices.Contains(seen, name) {
			t.Errorf("the engine's program has the variable %s", name)
		}
	}
}

// TestEngineMountsFindsNoRunningContainerWithNoEngineProgram has no docker program on
// PATH: the machine has no running container, so there is no mount and no error.
func TestEngineMountsFindsNoRunningContainerWithNoEngineProgram(t *testing.T) {
	got, err := engineMounts("qory-test-no-such-engine")(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("mounts = %v, %v, want none and no error", got, err)
	}
}

// registryStub starts a registry that returns 401 to every request but one for
// /registry/token, which it returns {} to: with a Bearer challenge whose realm is realm()
// when challenge is set, else with none. tls starts it with TLS.
func registryStub(t *testing.T, challenge, tls bool, realm func() string) *httptest.Server {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/registry/token":
			w.Write([]byte("{}"))
		case challenge:
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm()+`",service="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	srv := httptest.NewUnstartedServer(h)
	if tls {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// installStub installs claude with fetch into a new folder, and returns its error.
func installStub(t *testing.T, fetch runtimeinstall.Fetch) error {
	t.Helper()
	_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Fetch: fetch, Runtime: "claude", Folder: t.TempDir(), Keys: rt.Keys()})
	if err == nil {
		t.Fatal("the install succeeded")
	}
	return err
}

// TestTheInstallCausesStandAlone installs claude with a registry that returns 401 with
// no Bearer challenge, a token realm whose response has no token, an instance address
// that is no URL, one with a path, and a source that returns a release with no tarball.
// A dispatched run from a run code shows each cause alone, with "; the code is used" at
// its end, as [causeOnly] makes it, and each says what is wrong without the fix line.
// For the address that is no URL and the release with no tarball, of claude 2.1.320 and
// of a version of 64 characters, each line, with "qory run: " before the first, has at
// most 100 characters. A person's run shows the fix line, then the cause.
func TestTheInstallCausesStandAlone(t *testing.T) {
	registry := func(t *testing.T, challenge bool, path string) (string, string) {
		var srv *httptest.Server
		srv = registryStub(t, challenge, false, func() string { return srv.URL + "/registry/token" })
		host := strings.TrimPrefix(srv.URL, "http://")
		return host, srv.URL + path
	}
	install := installStub
	noTarballOf := func(version string) runtimeinstall.Fetch {
		return func(context.Context, string, string) (runtimeinstall.Release, error) {
			integrity := rt.Integrity(rt.PlainTarball(t, version))
			signed := "@anthropic-ai/claude-code-linux-x64@" + version
			return runtimeinstall.Release{Version: version, Integrity: integrity, Signed: signed, Signatures: rt.Signatures(signed, integrity)}, nil
		}
	}
	noTarball := noTarballOf("2.1.320")
	long := "2.1.320-" + strings.Repeat("a", 56)
	noURL := func(t *testing.T) error {
		host, _ := registry(t, true, "")
		return install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", "ftp://apiary.example.com"))
	}
	for _, c := range []struct {
		name string
		err  func(t *testing.T) (string, error)
		fits bool
	}{
		{"a 401 with no Bearer challenge", func(t *testing.T) (string, error) {
			host, instance := registry(t, false, "")
			return "the registry\n  at " + host + "\n  returned 401 with no Bearer challenge; the code is used",
				install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", instance))
		}, true},
		{"a token response with no token", func(t *testing.T) (string, error) {
			host, instance := registry(t, true, "")
			return "the response of the token realm\n  at " + host + "\n  contains no token; the code is used",
				install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", instance))
		}, true},
		{"an address of Qory Apiary that is no URL", func(t *testing.T) (string, error) {
			return "the run's address of Qory Apiary is not an http or https URL\n  with a host and no user; the code is used", noURL(t)
		}, true},
		{"an address of Qory Apiary with a path", func(t *testing.T) (string, error) {
			host, instance := registry(t, true, "/base")
			return "the address of Qory Apiary has a path:\n  \"" + instance + "\"; the code is used",
				install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", instance))
		}, false},
		{"a release with no tarball", func(t *testing.T) (string, error) {
			return "the source of the runtime's releases returned no tarball\n  of claude 2.1.320; the code is used", install(t, noTarball)
		}, true},
		{"a release with no tarball of a long version", func(t *testing.T) (string, error) {
			return "the source of the runtime's releases returned no tarball\n  of claude " + long + "; the code is used", install(t, noTarballOf(long))
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			want, err := c.err(t)
			if got := causeOnly(installFailed(err)).Error() + codeUsed; got != want {
				t.Errorf("a dispatched run shows %q, want %q", got, want)
			}
			for _, line := range strings.Split("qory run: "+want, "\n") {
				if c.fits && len(line) > 100 {
					t.Errorf("the line has %d characters: %s", len(line), line)
				}
			}
		})
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{install(t, noTarball), "Check the source of the runtime's releases\nThe source of the runtime's releases returned no tarball\n  of claude 2.1.320"},
		{noURL(t), "Set the address of Qory Apiary to an https URL\nThe run's address of Qory Apiary is not an http or https URL\n  with a host and no user"},
	} {
		if person := installFailed(c.err).Error(); person != c.want {
			t.Errorf("a person's run shows %q, want %q", person, c.want)
		}
	}
}

// TestTheDispatchedCauseOfTheLongestPinFits is the line a run from a run code prints for
// a pin that the registry does not have, with the longest runtime name of
// runtimeinstall.Runtimes() and a pin of 32 characters, the longest one a stack may set:
// with "qory run: " and [codeUsed], the line has at most 100 characters.
func TestTheDispatchedCauseOfTheLongestPinFits(t *testing.T) {
	runtime := ""
	for _, r := range runtimeinstall.Runtimes() {
		if len(r) > len(runtime) {
			runtime = r
		}
	}
	pin := "1" + strings.Repeat("0", 31)
	if !runtimeinstall.ValidVersion(pin) || runtimeinstall.ValidVersion(pin+"0") {
		t.Fatalf("the longest pin a stack may set is not %d characters", len(pin))
	}
	err := fmt.Errorf(textNotPublished, runtime, "qory-stack.yaml", runtime, pin)
	line := "qory run: " + causeOnly(installFailed(err)).Error() + codeUsed
	if want := "qory run: the registry has no " + runtime + " " + pin + "; the code is used"; line != want {
		t.Fatalf("a dispatched run shows %q, want %q", line, want)
	}
	if len(line) > 100 {
		t.Errorf("the line has %d characters: %s", len(line), line)
	}
}

// TestALongRegistryHostIsOnASeparateLine installs claude from a registry whose host has
// 253 characters, the most a DNS name has, that returns 401 with no Bearer challenge, and
// from one whose token realm's response has no token: a dispatched run from a run code
// shows the host on an indented line, which [installFailed] cuts at its limit of a line,
// and the first line, with "qory run: ", has at most 100 characters.
func TestALongRegistryHostIsOnASeparateLine(t *testing.T) {
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for _, challenge := range []bool{false, true} {
		srv := registryStub(t, challenge, true, func() string { return "https://" + host + "/registry/token" })
		// Each connection goes to the stub, whose certificate is for example.com.
		tlsConfig := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		tlsConfig.ServerName = "example.com"
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		}}}
		err := installStub(t, runtimeinstall.RegistryFetch(client, host, "amd64", "https://"+host))
		got := causeOnly(installFailed(err)).Error() + codeUsed
		first, rest, _ := strings.Cut(got, "\n")
		if len("qory run: "+first) > 100 || !strings.HasPrefix(rest, "  at "+host[:64]) {
			t.Errorf("a dispatched run shows %q, want a short first line and the host on the second", got)
		}
	}
}
