package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/ui"
)

// TestComposeReadsALayerFromGit is a product repository that pins its core layer to a tag
// of the harness repository: the compose fetches it, pins it by commit, and reads the
// cache from then on.
func TestComposeReadsALayerFromGit(t *testing.T) {
	root := newCheckout(t)
	// The layer repository, with its own identity, tagged v1.
	remote := tempDir(t)
	runGit(t, remote, "init", "--quiet", "--initial-branch=main")
	runGit(t, remote, "config", "user.name", "Tester")
	runGit(t, remote, "config", "user.email", "tester@example.com")
	writeFile(t, filepath.Join(remote, "layers", "core", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review.\n---\n\nReview it.\n")
	writeFile(t, filepath.Join(remote, "layers", "core", "AGENTS.md"), "# Core\n")
	runGit(t, remote, "add", "-A")
	runGit(t, remote, "commit", "-q", "-m", "first")
	runGit(t, remote, "tag", "v1")
	url := "file://" + remote

	writeFile(t, filepath.Join(root, "harness-compose.yaml"), "apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source: {git: "+url+", ref: v1, path: layers/core}\n")
	out, err := run(t, "hc")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · claude", "composed 1 entry from 1 layer")
	rep := readReport(t, root)
	if len(rep.Layers) != 1 || rep.Layers[0].Source != url+"#v1:layers/core" {
		t.Fatalf("report layers = %+v", rep.Layers)
	}
	pin := rep.Layers[0].Pin
	if len(pin) != 12 || rep.Layers[0].Dirty {
		t.Errorf("pin %q dirty %v, want a twelve-character commit, clean", pin, rep.Layers[0].Dirty)
	}
	// The home reaches the layer through its cache clone.
	if _, err := os.Stat(filepath.Join(root, ".qory", "harness", "layers", "core", "AGENTS.md")); err != nil {
		t.Errorf("layers/core does not resolve: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(root, ".qory", "harness", "layers", "core")); err != nil || strings.HasPrefix(target, remote) {
		t.Errorf("layers/core links to %q (%v), want the cache clone, not the remote", target, err)
	}

	// inspect prints the pin, and a second compose reads the cache: the remote may go.
	out, err = run(t, "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, url+"#v1:layers/core", pin)
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("second compose without the remote: %v\n%s", err, out)
	}
	if out, err := run(t, "hc", "--update"); err == nil || !strings.Contains(err.Error(), "fetching "+url+"#v1") {
		t.Errorf("--update without the remote: %v\n%s", err, out)
	}
}
