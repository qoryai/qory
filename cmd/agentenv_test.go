package cmd_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/preparation"
)

// TestAgentEnvStartsTheLaunchAsGiven is qory run agent-env in a separate process: it
// replaces itself with the launch, whose arguments are as given, -p and words with
// spaces among them, and whose environment holds none of the image's values of the
// names qory removes but the ones the file keeps. The verb prints nothing.
func TestAgentEnvStartsTheLaunchAsGiven(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	file := filepath.Join(t.TempDir(), "kept")
	if err := os.WriteFile(file, preparation.KeptNames([]string{"HTTP_PROXY", "CLAUDE_CONFIG_DIR", "QORY_RUN_SYSTEM", "ANTHROPIC_API_KEY"}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	c := exec.Command(os.Args[0], "-test.run=^$", "--", file, "--", sh, "-c", `printf '%s|' "$0" "$@"; echo; env`, "arg0", "-p", "two words", "--settings", "/s.json")
	c.Stdout, c.Stderr = &stdout, &stderr
	c.Env = []string{
		"QORY_TEST_AGENT_ENV=1", "PATH=" + os.Getenv("PATH"),
		"NODE_OPTIONS=--require /x.js", "ANTHROPIC_BASE_URL=https://elsewhere.example", "ALL_PROXY=socks5://elsewhere.example",
		"HTTP_PROXY=http://qory-proxy:3128", "CLAUDE_CONFIG_DIR=/home/agent/.claude", "QORY_RUN_SYSTEM=github.com",
		"ANTHROPIC_API_KEY=placeholder", "LANG=C",
	}
	if err := c.Run(); err != nil {
		t.Fatalf("%v\n%s%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if first, _, _ := strings.Cut(out, "\n"); first != "arg0|-p|two words|--settings|/s.json|" {
		t.Errorf("the launch's arguments are %q", first)
	}
	wants(t, out, "\nHTTP_PROXY=http://qory-proxy:3128\n", "\nCLAUDE_CONFIG_DIR=/home/agent/.claude\n", "\nQORY_RUN_SYSTEM=github.com\n", "\nANTHROPIC_API_KEY=placeholder\n", "\nLANG=C\n")
	lacks(t, out, "NODE_OPTIONS", "ANTHROPIC_BASE_URL", "ALL_PROXY")
	if stderr.Len() != 0 {
		t.Errorf("printed %q", stderr.String())
	}
}

// TestAWalledRunThroughAgentEnv is a walled run started through the agent-env verb, as a
// dispatched run of Claude Code is: the container starts qory inside with the verb, its
// file of kept names and the launch, and Forager still puts its settings, with the
// hooks, in place of the launch's --settings; the file's folder is mounted read-only
// and holds the names Forager sets and the run's --env, and goes once the run is over.
func TestAWalledRunThroughAgentEnv(t *testing.T) {
	defer cmd.StartThroughAgentEnv()()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	docker, log := fakeDocker(t)
	// The docker that stands in also logs the kept names' file the agent-env verb is
	// given, as the container would read it.
	logging := filepath.Join(t.TempDir(), "docker")
	writeFile(t, logging, `#!/bin/sh
prev=
for a in "$@"; do
	if [ "$prev" = agent-env ]; then sed 's/^/kept: /' "$a" >> `+log+`; fi
	prev=$a
done
exec `+docker+` "$@"
`)
	if err := os.Chmod(logging, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+logging+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	t.Setenv("QORY_RUN_KEY", "")
	t.Setenv("FLAG_NAMED", "goes in")
	out, err := run(t, "run", "claude", "--env", "FLAG_NAMED", "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != 4 {
		t.Fatalf("run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := string(data)
	runs := runsDir(t, root)
	wants(t, lines,
		" -- "+wall.HelperPath+" run agent-env ",
		"/kept -- ",
		"/fake-runtime --settings "+runs,
		"/settings.json -p hi",
		"kept: HTTP_PROXY\n", "kept: QORY_RUN_ID\n", "kept: QORY_RUN_SOCKET\n", "kept: ANTHROPIC_API_KEY\n",
		"kept: FLAG_NAMED\n", "kept: ca SSL_CERT_FILE\n",
	)
	var dir string
	for _, f := range strings.Fields(lines) {
		if strings.HasSuffix(f, "/kept") {
			dir = filepath.Dir(f)
		}
	}
	if dir == "" {
		t.Fatalf("no kept names' file on the docker line:\n%s", lines)
	}
	wants(t, lines, "--mount type=bind,src="+dir+",dst="+dir+",readonly")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the kept names' folder stays after the run: %v", err)
	}
}
