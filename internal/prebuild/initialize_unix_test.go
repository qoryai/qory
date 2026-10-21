//go:build unix

package prebuild_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/prebuild"
)

// initializeLine is the line that says initializeCommand is not run.
const initializeLine = "qory run prebuild: initializeCommand is not run; the image is built without it\n"

// configArg is the --config of the devcontainer command line that starts with verb.
func configArg(t *testing.T, f fakes, verb string) string {
	t.Helper()
	for _, l := range f.calls("devcontainer") {
		if !strings.HasPrefix(l, verb+" ") {
			continue
		}
		_, rest, ok := strings.Cut(l, " --config ")
		if !ok {
			t.Fatalf("devcontainer %s has no --config: %s", verb, l)
		}
		config, _, _ := strings.Cut(rest, " ")
		return config
	}
	t.Fatalf("devcontainer %s did not run:\n%s", verb, f.read("devcontainer.log"))
	return ""
}

// TestBuildIgnoresInitializeCommand is a configuration with comments that sets
// initializeCommand, at each place a configuration is found: the devcontainer command
// reads a copy of it without initializeCommand, from a folder outside the repository,
// whose Dockerfile, context and local Feature name the repository's; the repository is
// still its workspace folder, a line says so, and the configuration is left as it was.
func TestBuildIgnoresInitializeCommand(t *testing.T) {
	for _, c := range []struct{ place, feature string }{
		{".devcontainer/devcontainer.json", "./feature"},
		{".devcontainer.json", "./.devcontainer/feature"},
	} {
		t.Run(c.place, func(t *testing.T) {
			f := newFakes(t)
			repo := repository(t, "")
			if err := os.Remove(filepath.Join(repo, ".devcontainer", "devcontainer.json")); err != nil {
				t.Fatal(err)
			}
			config := `{
	// The shop, built from its Dockerfile.
	"initializeCommand": "touch /tmp/qory-test-host", /* on the host */
	"build": {"dockerfile": "Dockerfile", "context": "."},
	"features": {"` + c.feature + `": {}},
	"onCreateCommand": "make setup",
}
`
			original := filepath.Join(repo, filepath.FromSlash(c.place))
			folder := filepath.Dir(original)
			for name, content := range map[string]string{original: config, filepath.Join(folder, "Dockerfile"): "FROM alpine:3.20\n"} {
				if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			o := options(t, repo, &out, io.Discard)
			if err := prebuild.Build(t.Context(), o); err != nil {
				t.Fatalf("Build: %v\n%s", err, out.String())
			}
			if strings.Count(out.String(), initializeLine) != 1 {
				t.Errorf("printed\n%s\nwant the line once:\n%s", out.String(), initializeLine)
			}
			for _, verb := range []string{"read-configuration", "build"} {
				copied := configArg(t, f, verb)
				if copied == original || filepath.Base(copied) != filepath.Base(original) || strings.HasPrefix(copied, repo+string(filepath.Separator)) {
					t.Errorf("devcontainer %s read %s, want a copy named %s outside the repository", verb, copied, filepath.Base(original))
				}
				if !f.called("devcontainer", verb+" --workspace-folder "+repo+" --config ") {
					t.Errorf("devcontainer %s has another workspace folder:\n%s", verb, f.read("devcontainer.log"))
				}
				if _, err := os.Stat(copied); err == nil {
					t.Errorf("the copy %s is left after the build", copied)
				}
				data := f.read(verb + ".config")
				if strings.Contains(data, "initializeCommand") || strings.Contains(data, "qory-test-host") {
					t.Errorf("devcontainer %s read initializeCommand:\n%s", verb, data)
				}
				var read struct {
					Build           struct{ Dockerfile, Context string }
					Features        map[string]json.RawMessage
					OnCreateCommand string
				}
				if err := json.Unmarshal([]byte(data), &read); err != nil {
					t.Fatalf("devcontainer %s read what is not JSON: %v\n%s", verb, err, data)
				}
				dir := filepath.ToSlash(filepath.Dir(copied))
				if p := path.Join(dir, read.Build.Dockerfile); p != filepath.ToSlash(filepath.Join(folder, "Dockerfile")) {
					t.Errorf("build.dockerfile names %s", p)
				}
				if p := path.Join(dir, read.Build.Context); p != filepath.ToSlash(folder) {
					t.Errorf("build.context names %s", p)
				}
				for id := range read.Features {
					if !strings.HasPrefix(id, "../") || path.Join(dir, id) != filepath.ToSlash(filepath.Join(repo, ".devcontainer", "feature")) {
						t.Errorf("the local Feature is %s", id)
					}
				}
				if len(read.Features) != 1 || read.OnCreateCommand != "make setup" {
					t.Errorf("devcontainer %s read\n%s", verb, data)
				}
			}
			if data, err := os.ReadFile(original); err != nil || string(data) != config {
				t.Errorf("the configuration was changed: %v\n%s", err, data)
			}
		})
	}
}

// TestBuildWithoutInitializeCommand is a configuration without it: the devcontainer
// command reads the repository's, and no line says it is not run.
func TestBuildWithoutInitializeCommand(t *testing.T) {
	f := newFakes(t)
	var out bytes.Buffer
	repo := repository(t, shopConfig)
	if err := prebuild.Build(t.Context(), options(t, repo, &out, io.Discard)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	original := filepath.Join(repo, ".devcontainer", "devcontainer.json")
	for _, verb := range []string{"read-configuration", "build"} {
		if got := configArg(t, f, verb); got != original {
			t.Errorf("devcontainer %s read %s, want %s", verb, got, original)
		}
	}
	if strings.Contains(out.String(), "initializeCommand") {
		t.Errorf("printed\n%s", out.String())
	}
}

// TestBuildIgnoresInitializeCommandOfTheMergedConfiguration is a merged configuration
// that has initializeCommand when the configuration has none: the line, once.
func TestBuildIgnoresInitializeCommandOfTheMergedConfiguration(t *testing.T) {
	f := newFakes(t)
	f.set(t, "merged", `{"initializeCommand":"touch /tmp/qory-test-host"}`)
	var out bytes.Buffer
	if err := prebuild.Build(t.Context(), options(t, repository(t, shopConfig), &out, io.Discard)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Count(out.String(), initializeLine) != 1 {
		t.Errorf("printed\n%s\nwant the line once:\n%s", out.String(), initializeLine)
	}
}
