package cmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/pool/prebuild"
)

// prebuildAgent is a wall.image pinned by its digest.
const prebuildAgent = "registry.example.com/agents/qory@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// prebuildDocuments is the contract's run/v1 documents fixture name, of the step
// prebuild.
func prebuildDocuments(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := fs.ReadFile(contracts.RunFS, "fixtures/documents/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// launched is what the prebuild seam was called with: the settings, the run, the run
// key's bytes as the run had them, and what the run's lock file contained then.
type launched struct {
	settings prebuild.Settings
	run      prebuild.Run
	key      string
	lock     string
	calls    int
}

// prebuildFromCode runs qory run from a run code whose documents are documents, on a
// machine whose forager.yaml is wall, "" for none, with the prebuild seam returning
// err. Each of setup runs with the code relay once the machine's directories are set,
// before qory run.
func prebuildFromCode(t *testing.T, documents json.RawMessage, wall string, err error, setup ...func(*codeRelay)) (*launched, string, error, string) {
	t.Helper()
	runKeyCheckout(t)
	if wall != "" {
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), wall)
	}
	g := newCodeRelay(t)
	g.documents = documents
	for _, f := range setup {
		f(g)
	}
	got := &launched{}
	t.Cleanup(cmd.SetLaunchPrebuild(func(_ context.Context, s prebuild.Settings, r prebuild.Run) (prebuild.Result, error) {
		got.settings, got.run, got.key = s, r, string(r.Key.Bytes())
		lock, _ := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "locks", r.RunID+".lock"))
		got.lock = string(lock)
		got.calls++
		return prebuild.Result{}, err
	}))
	out, runErr := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL)
	return got, out, runErr, g.runKey()
}

// TestARunCodeForAPrebuildRunsTheBuild is a run code whose documents have step
// prebuild, with and without environment_image, on a machine whose forager.yaml sets
// the wall's image by digest, user and helper: qory run says which gateway the run goes
// through, and runs the prebuild once with the documents' run, inputs and places, the
// run key, and the wall settings, with qory's state and configuration directories,
// while it holds the run's lock file, marked unwalled. Its end is qory run's: exit 0,
// or exit 1 with its error.
func TestARunCodeForAPrebuildRunsTheBuild(t *testing.T) {
	docker, _ := fakeDocker(t)
	helper := staticELF(t)
	wall := "wall:\n  adapter: docker\n  image: " + prebuildAgent + "\n  images_allow: [registry.example.com/agents/]\n  command: " + docker + "\n  helper: " + helper + "\n  user: \"1000:1000\"\n"
	for _, name := range []string{"prebuild.json", "prebuild-environment-image.json"} {
		t.Run(name, func(t *testing.T) {
			documents := prebuildDocuments(t, name)
			var doc contracts.Documents
			if err := json.Unmarshal(documents, &doc); err != nil {
				t.Fatal(err)
			}
			got, out, err, key := prebuildFromCode(t, documents, wall, nil)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			wants(t, out, "qory run: through the gateway 127.0.0.1:")
			if got.calls != 1 {
				t.Fatalf("the prebuild ran %d times", got.calls)
			}
			r := got.run
			if r.RunID != keyRunID || got.key != key || string(r.Inputs) != string(doc.Inputs) ||
				r.ImageDestination != doc.ImageDestination || r.EnvironmentDestination != doc.EnvironmentDestination || r.EnvironmentImage != doc.EnvironmentImage {
				t.Errorf("the prebuild's run is %+v, by the run key %v; want the documents' %+v", r, got.key == key, doc)
			}
			s := got.settings
			config, state := os.Getenv("XDG_CONFIG_HOME"), filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory")
			if s.AgentImage != prebuildAgent || s.User != "1000:1000" || s.Helper != helper || s.RunConfig != config || s.State != state || s.Version == "" || s.Log == nil {
				t.Errorf("the prebuild's settings are %+v", s)
			}
			if got.lock != "unwalled\n" {
				t.Errorf("the run's lock file contained %q during the prebuild, want unwalled", got.lock)
			}
			lacks(t, out, key, key[4:40])
		})
	}
	t.Run("failed", func(t *testing.T) {
		const line = "docker push exited 1"
		_, out, err, _ := prebuildFromCode(t, prebuildDocuments(t, "prebuild.json"), wall, errors.New(line))
		if err == nil || err.Error() != line || cmd.ExitCode(err) != 1 {
			t.Errorf("%v (exit %d), want %q, exit 1\n%s", err, cmd.ExitCode(err), line, out)
		}
	})
}

// TestARunCodeForAPrebuildNeedsTheWallSettings is a prebuild from a run code on a
// machine whose forager.yaml lacks a setting the build takes: wall.image pinned by its
// digest, wall.user or wall.helper. qory run stops after the redemption with the line
// that names the three, exit 1, and runs no prebuild. A wall with no helper stops a run
// before the redemption on any system but Linux, so that case runs on Linux alone.
func TestARunCodeForAPrebuildNeedsTheWallSettings(t *testing.T) {
	const want = "set wall.image, pinned by its digest, wall.user and wall.helper in forager.yaml: a prebuild from a run code builds with them; the code is used"
	docker, _ := fakeDocker(t)
	helper := staticELF(t)
	for _, c := range []struct{ name, wall string }{
		{"image by tag", "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + helper + "\n  user: \"1000:1000\"\n"},
		{"no user", "wall:\n  adapter: docker\n  image: " + prebuildAgent + "\n  images_allow: [registry.example.com/agents/]\n  command: " + docker + "\n  helper: " + helper + "\n"},
		{"no helper", "wall:\n  adapter: docker\n  image: " + prebuildAgent + "\n  images_allow: [registry.example.com/agents/]\n  command: " + docker + "\n  user: \"1000:1000\"\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "no helper" && runtime.GOOS != "linux" {
				t.Skip("a wall with no helper stops the run before the redemption here")
			}
			got, out, err, _ := prebuildFromCode(t, prebuildDocuments(t, "prebuild.json"), c.wall, nil)
			if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
				t.Errorf("%v (exit %d), want %q, exit 1\n%s", err, cmd.ExitCode(err), want, out)
			}
			if got.calls != 0 {
				t.Error("the prebuild ran")
			}
		})
	}
}

// TestARunCodeForAPrebuildNeedsAMachineWithoutAnAccessKey is a prebuild from a run code
// on a machine where the key file of forager.yaml's directory is made while the code is
// redeemed, after qory run checked that the machine has no access key: qory run stops
// after the redemption with the line that starts with the fix and contains the file's
// path, exit 1, runs no prebuild, and prints no secret.
func TestARunCodeForAPrebuildNeedsAMachineWithoutAnAccessKey(t *testing.T) {
	const want = "run the prebuild on a machine without an access key: this machine has an access key in %s, and a prebuild runs the target's build commands without a wall; the code is used"
	const secret = "qak_0123456789abcdef0123456789abcdef"
	docker, _ := fakeDocker(t)
	helper := staticELF(t)
	wall := "wall:\n  adapter: docker\n  image: " + prebuildAgent + "\n  images_allow: [registry.example.com/agents/]\n  command: " + docker + "\n  helper: " + helper + "\n  user: \"1000:1000\"\n"
	var file string
	got, out, err, _ := prebuildFromCode(t, prebuildDocuments(t, "prebuild.json"), wall, nil, func(g *codeRelay) {
		file = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", foragerdir.SecretFile)
		g.redeeming = func() {
			if err := os.WriteFile(file, []byte(secret+"\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	if w := fmt.Sprintf(want, file); err == nil || err.Error() != w || cmd.ExitCode(err) != 1 {
		t.Errorf("%v (exit %d), want %q, exit 1\n%s", err, cmd.ExitCode(err), w, out)
	}
	if got.calls != 0 {
		t.Error("the prebuild ran")
	}
	lacks(t, out, secret)
}
