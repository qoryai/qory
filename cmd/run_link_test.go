package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunMakesNoLinkSocket lists the system's temporary directory while the runtime
// runs: the gateway's link is served in memory alone, so no qory-link-* entry is there,
// beside the run's own record socket, qory-run-*, which shows the listing is the one the
// run uses.
func TestRunMakesNoLinkSocket(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	tmp := shortTempDir(t)
	t.Setenv("TMPDIR", tmp)
	seen := filepath.Join(t.TempDir(), "seen")
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, "#!/bin/sh\nls -a "+tmp+" > "+seen+"\nexit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "qory-run-")
	lacks(t, string(data), "qory-link-")
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "qory-link-") {
			t.Errorf("the run left %s in %s", e.Name(), tmp)
		}
	}
}
