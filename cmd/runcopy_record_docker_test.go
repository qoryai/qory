//go:build unix

package cmd_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// filteredSetup is the devcontainer.json of the repository in
// [TestARealSetupRepositoryWithAFilterKeepsNoCopy]: postStartCommand makes a repository at tool
// with a clean and a smudge filter for *.dat, as Git LFS sets one up, and a file the filter
// changes, and leaves a file with a name in Unicode's decomposed form there.
const filteredSetup = `{"image": "example.com/agent:1", "postStartCommand": "set -e; git init -q tool; cd tool; ` +
	`git config user.name setup; git config user.email setup@example.invalid; ` +
	`git config filter.up.clean 'tr a-z A-Z'; git config filter.up.smudge 'tr A-Z a-z'; ` +
	`echo '*.dat filter=up' > .gitattributes; echo abc > a.dat; git add .; git commit -q -m setup; ` +
	`git status --porcelain > status-of-setup.txt; printf x > \"$(printf 'cafe\\314\\201.txt')\""}`

// TestARealSetupRepositoryWithAFilterKeepsNoCopy is a write run with a run key behind a real wall
// on this machine's engine, in a copy, whose setup makes a repository with a clean and a smudge
// filter and a file under it. qory records that repository in the container qory-record-<run id>,
// with the copy read-only, which exits 0 before the agent starts. With an agent that changes
// nothing, the git session finds the repository as the setup left it, and no copy stays. With an
// agent that changes the file under the filter, the copy stays. It runs only with
// QORY_TEST_DOCKER=1 and docker on the PATH.
func TestARealSetupRepositoryWithAFilterKeepsNoCopy(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint("change ", change), func(t *testing.T) { realFilteredSetup(t, change) })
	}
}

// realFilteredSetup is a run of [TestARealSetupRepositoryWithAFilterKeepsNoCopy], whose agent
// changes tool/a.dat when change is set.
func realFilteredSetup(t *testing.T, change bool) {
	agent := "#!/bin/sh\n"
	if change {
		agent += "echo abcd > tool/a.dat\n"
	}
	helper, image := realWallImage(t, "filter-agent", agent)
	h := newHostFetch(t, "", "main")
	h.server.Repository(t, "acme/filtered", "main", map[string]string{
		"README.md":                       "acme/filtered\n",
		"qory.yaml":                       ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: \"" + h.server.URL("acme/harness") + "\", ref: main, path: modules/core}\n"),
		".devcontainer/devcontainer.json": filteredSetup,
		".gitignore":                      "",
	})
	fakeLaunch(t, "/usr/local/bin/filter-agent")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	var argv []string
	t.Cleanup(cmd.GitSessionsInDocker(func(a []string) { argv = append(argv, strings.Join(a, " ")) }))
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: "acme/filtered",
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
		AtGateway: true, Real: true,
	})
	defer restore()
	start := time.Now()
	out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--run-key-fd", descriptor(t, key))
	end := time.Now().Add(time.Second)
	t.Logf("qory run:\n%s", out)
	t.Logf("the containers:\n%s", strings.Join(argv, "\n"))
	if change {
		wants(t, out, "did not come back")
		if cmd.ExitCode(err) != 1 {
			t.Errorf("exit %d, want 1", cmd.ExitCode(err))
		}
	} else {
		if err != nil {
			t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
		}
		lacks(t, out, "did not come back")
	}
	events := dockerOut(t, "", "events", "--since", start.Format(time.RFC3339), "--until", end.Format(time.RFC3339),
		"--filter", "label=dev.qory.run="+keyRunID, "--filter", "type=container",
		"--format", "{{.Action}} {{.Actor.Attributes.name}} {{.Actor.Attributes.exitCode}}")
	t.Logf("the engine's events of the run's containers:\n%s", events)
	wants(t, events, "die qory-record-"+keyRunID+" 0", "destroy qory-record-"+keyRunID, "die qory-copy-"+keyRunID+" 0")
	if recorded, agent := strings.Index(events, "die qory-record-"+keyRunID), strings.Index(events, "create qory-"+keyRunID+"-agent"); recorded < 0 || agent < recorded {
		t.Errorf("the record did not end before the agent's container was made:\n%s", events)
	}
	wants(t, strings.Join(argv, "\n"), "--name qory-record-"+keyRunID, ",readonly")
}
