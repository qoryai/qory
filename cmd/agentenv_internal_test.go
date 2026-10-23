package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes/catalog"
	"github.com/qoryai/forager/wall"
)

// TestTheAgentEnvKeepsTheNamesForagerSets is a dispatched walled run of Claude Code
// started through the agent-env verb: the launch goes after the verb's words, which hold
// no -p, --print or --settings, the kept names' folder is mounted read-only, and its file
// holds the names Forager sets, the wall's, the session's, the ones Claude Code reads a
// model key and its model endpoint from and CLAUDE_CONFIG_DIR, with the authority's
// apart; once the variables resolve, the server's names that apply join them, and a name
// no source's value applies to does not. The verb then drops the image's NODE_OPTIONS,
// ANTHROPIC_CUSTOM_HEADERS and ALL_PROXY, keeps the rest, and starts the launch's
// arguments unchanged. The folder goes when the run is over.
func TestTheAgentEnvKeepsTheNamesForagerSets(t *testing.T) {
	rt, err := catalog.Lookup("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	spec := runSpec{OpenSpec: session.OpenSpec{Wall: &wall.Docker{}}, SessionSpec: session.SessionSpec{Runtime: rt, Command: "sh", Args: []string{"--settings", "/h/claude/settings.json", "-p", "two words"}, HarnessHome: "/h", LaunchFixed: withAgentHome(nil), Mounts: []wall.Mount{{Path: "/work"}}}}
	done, err := wrapAgentEnv(&spec, func(line string) { said = append(said, line) })
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != wall.HelperPath || len(spec.Args) < 4 || !slices.Equal(spec.Args[:2], agentEnvArgs) || spec.Args[3] != "--" {
		t.Fatalf("the launch is %s %q", spec.Command, spec.Args)
	}
	file := spec.Args[2]
	if !slices.Equal(spec.Args[4:], []string{"sh", "--settings", "/h/claude/settings.json", "-p", "two words"}) {
		t.Errorf("the launch after the verb is %q", spec.Args[4:])
	}
	for _, w := range spec.Args[:4] {
		if w == "-p" || w == "--print" || strings.HasPrefix(w, "--settings") {
			t.Errorf("the verb's words hold %s", w)
		}
	}
	if m := spec.Mounts[len(spec.Mounts)-1]; m.Path != filepath.Dir(file) || !m.ReadOnly {
		t.Errorf("the kept names' folder is mounted as %+v", m)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"HTTP_PROXY", "https_proxy", "NO_PROXY", "QORY_RUN_SOCKET", "QORY_RUN_ID", "QORY_HARNESS_HOME", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CONFIG_DIR", "ca SSL_CERT_FILE", "ca NODE_EXTRA_CA_CERTS"} {
		if !strings.Contains("\n"+string(data), "\n"+n+"\n") {
			t.Errorf("the file lacks %s:\n%s", n, data)
		}
	}
	if strings.Contains(string(data), "QORY_RUN_SYSTEM") || strings.Contains(string(data), "ALL_PROXY") {
		t.Errorf("the file keeps a name nothing set yet:\n%s", data)
	}

	spec.OnVariables(session.Applied{
		{Name: "QORY_RUN_SYSTEM", From: session.FromApiary},
		{Name: "ANTHROPIC_CUSTOM_HEADERS", Lost: []session.Loss{{From: session.FromApiary, Why: session.WhyDenied}}},
	})
	if data, _ = os.ReadFile(file); !strings.Contains(string(data), "\nQORY_RUN_SYSTEM\n") || strings.Contains(string(data), "ANTHROPIC_CUSTOM_HEADERS") {
		t.Errorf("after the variables resolved:\n%s", data)
	}
	if len(said) != 0 {
		t.Errorf("said %q", said)
	}

	image := []string{
		"PATH=" + os.Getenv("PATH"), "NODE_OPTIONS=--require /x.js", "ANTHROPIC_CUSTOM_HEADERS=X-Other: 1",
		"ALL_PROXY=socks5://elsewhere.example", "HTTP_PROXY=http://qory-proxy:3128",
		"CLAUDE_CONFIG_DIR=/tmp/qory-agent-home", "QORY_RUN_SYSTEM=github.com", "ANTHROPIC_API_KEY=placeholder",
	}
	path, argv, env, err := agentEnvLaunch(spec.Args[2:], image)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "sh" || !slices.Equal(argv, []string{"sh", "--settings", "/h/claude/settings.json", "-p", "two words"}) {
		t.Errorf("starts %s %q", path, argv)
	}
	if want := []string{image[0], image[4], image[5], image[6], image[7]}; !slices.Equal(env, want) {
		t.Errorf("env %q\nwant %q", env, want)
	}

	done()
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Errorf("the kept names' folder stays: %v", err)
	}
}

// TestTheAgentEnvRefusesWhatItCannotRead is the verb without its file and --, or with a
// file that is not one of kept names: one error, naming no value, and nothing to start.
func TestTheAgentEnvRefusesWhatItCannotRead(t *testing.T) {
	bad := filepath.Join(t.TempDir(), keptFile)
	if err := os.WriteFile(bad, []byte("HTTP_PROXY=http://secret.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"claude"}, {bad, "claude", "-p"}, {bad, "--", "sh"}} {
		if _, _, _, err := agentEnvLaunch(args, nil); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// TestTheAgentEnvKeepsTheStartKeysEndpoint is a dispatched walled run of Claude Code
// whose start key sets ANTHROPIC_BASE_URL, the variable its descriptor's
// model.endpoint.variable names, and whose sources set no value of it, so the resolved
// variables list no entry for it: the verb keeps the endpoint Forager set and the key's
// placeholder, and still drops another ANTHROPIC_ name.
func TestTheAgentEnvKeepsTheStartKeysEndpoint(t *testing.T) {
	rt, err := catalog.Lookup("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	spec := runSpec{OpenSpec: session.OpenSpec{Wall: &wall.Docker{}}, SessionSpec: session.SessionSpec{Runtime: rt, Command: "sh", Args: []string{"-p", "two words"}, HarnessHome: "/h", LaunchFixed: withAgentHome(nil)}}
	done, err := wrapAgentEnv(&spec, func(line string) { t.Errorf("said %q", line) })
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	spec.OnVariables(session.Applied{})
	env := []string{
		"LANG=C.UTF-8", "ANTHROPIC_BASE_URL=https://api.model.example/anthropic",
		"ANTHROPIC_API_KEY=qory-model-sets-the-credential-outside-the-enclosure", "ANTHROPIC_CUSTOM_HEADERS=X-Other: 1",
	}
	_, _, got, err := agentEnvLaunch(spec.Args[2:], env)
	if err != nil {
		t.Fatal(err)
	}
	if want := env[:3]; !slices.Equal(got, want) {
		t.Errorf("env %q\nwant %q", got, want)
	}
}
