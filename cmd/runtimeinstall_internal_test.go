package cmd

import (
	"context"
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

// TestTheInstallCausesStandAlone installs claude with a registry that returns 401 with
// no Bearer challenge, a token realm whose response has no token, an instance address
// that is no URL, one with a path, and a source that returns a release with no tarball.
// A dispatched run from a run code shows each cause alone, with "; the code is used" at
// its end, as [causeOnly] makes it, and each says what is wrong without the fix line.
// The first line of the address that is no URL, with "qory run: ", has at most 100
// characters. A person's run shows the fix line, then the cause.
func TestTheInstallCausesStandAlone(t *testing.T) {
	registry := func(t *testing.T, challenge bool, path string) (string, string) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/registry/token":
				w.Write([]byte("{}"))
			case challenge:
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/registry/token",service="registry"`)
				w.WriteHeader(http.StatusUnauthorized)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		t.Cleanup(srv.Close)
		host := strings.TrimPrefix(srv.URL, "http://")
		return host, srv.URL + path
	}
	install := func(t *testing.T, fetch runtimeinstall.Fetch) error {
		_, err := runtimeinstall.Install(context.Background(), runtimeinstall.Options{Fetch: fetch, Runtime: "claude", Folder: t.TempDir(), Keys: rt.Keys()})
		if err == nil {
			t.Fatal("the install succeeded")
		}
		return err
	}
	noTarball := func(context.Context, string) (runtimeinstall.Release, error) {
		integrity := rt.Integrity(rt.PlainTarball(t, "2.1.320"))
		signed := "@anthropic-ai/claude-code-linux-x64@2.1.320"
		return runtimeinstall.Release{Version: "2.1.320", Integrity: integrity, Signed: signed, Signatures: rt.Signatures(signed, integrity)}, nil
	}
	for _, c := range []struct {
		name string
		err  func(t *testing.T) (string, error)
	}{
		{"a 401 with no Bearer challenge", func(t *testing.T) (string, error) {
			host, instance := registry(t, false, "")
			return "the registry at " + host + " returned 401 with no Bearer challenge; the code is used",
				install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", instance))
		}},
		{"a token response with no token", func(t *testing.T) (string, error) {
			host, instance := registry(t, true, "")
			return "the response of the token realm at " + host + " contains no token; the code is used",
				install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", instance))
		}},
		{"an address of Qory Apiary that is no URL", func(t *testing.T) (string, error) {
			host, _ := registry(t, true, "")
			return "the run's address of Qory Apiary is not an http or https URL\n  with a host and no user; the code is used",
				install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", "ftp://apiary.example.com"))
		}},
		{"an address of Qory Apiary with a path", func(t *testing.T) (string, error) {
			host, instance := registry(t, true, "/base")
			return "the address of Qory Apiary has a path:\n  \"" + instance + "\"; the code is used",
				install(t, runtimeinstall.RegistryFetch(http.DefaultClient, host, "amd64", instance))
		}},
		{"a release with no tarball", func(t *testing.T) (string, error) {
			return "the source of the runtime's releases returned no tarball of claude 2.1.320; the code is used", install(t, noTarball)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			want, err := c.err(t)
			if got := causeOnly(installFailed(err)).Error() + codeUsed; got != want {
				t.Errorf("a dispatched run shows %q, want %q", got, want)
			}
			if first, _, _ := strings.Cut("qory run: "+want, "\n"); c.name == "an address of Qory Apiary that is no URL" && len(first) > 100 {
				t.Errorf("the first line has %d characters: %s", len(first), first)
			}
		})
	}
	person := installFailed(install(t, noTarball)).Error()
	if want := "Check the source of the runtime's releases\nThe source of the runtime's releases returned no tarball of claude 2.1.320"; person != want {
		t.Errorf("a person's run shows %q, want %q", person, want)
	}
}
