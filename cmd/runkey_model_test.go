package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// modelKeyValue is the value the tests' model credentials hold; no output carries it.
const modelKeyValue = "model-key-value-0123456789abcdef"

// modelCredentialLine is qory run's refusal of the variable name, which from names.
func modelCredentialLine(name, from string) string {
	return "qory run: " + name + " is the model's credential, which a walled run given a run key takes only through the gateway; remove it from " + from
}

// TestARunKeyTakesNoModelCredentialPassedIn is a walled run given a run key whose --env
// or wall.env names a variable Claude Code reads a model key from, as its descriptor's
// model.keys lists, ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_AUTH_TOKEN:
// --env by the name and as NAME=value, and wall.env, the run key by QORY_RUN_KEY and by
// --run-key-fd. Each stops the run with an input error that names the variable and never
// its value, before the gateway's address is reached, a container starts or anything is
// recorded.
func TestARunKeyTakesNoModelCredentialPassedIn(t *testing.T) {
	root := runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	docker, log := fakeDocker(t)
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	wallSection := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n"
	// Behind a wall, a run given a run key needs its harness outside the checkout, so a
	// walled run the check did not refuse would go on to start its container.
	outside := t.TempDir()
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--home", outside); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("ANTHROPIC_API_KEY", modelKeyValue)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", modelKeyValue)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", modelKeyValue)
	for _, c := range []struct {
		name    string
		forager string
		byFD    bool
		args    []string
		want    string
	}{
		{"--env NAME, --run-key-fd", wallSection, true, []string{"run", "--env", "ANTHROPIC_API_KEY"}, modelCredentialLine("ANTHROPIC_API_KEY", "--env")},
		{"--env NAME=value, QORY_RUN_KEY", wallSection, false, []string{"run", "--env", "ANTHROPIC_API_KEY=" + modelKeyValue}, modelCredentialLine("ANTHROPIC_API_KEY", "--env")},
		{"--env the OAuth NAME, QORY_RUN_KEY", wallSection, false, []string{"run", "--env", "CLAUDE_CODE_OAUTH_TOKEN"}, modelCredentialLine("CLAUDE_CODE_OAUTH_TOKEN", "--env")},
		{"--env the OAuth NAME=value, --run-key-fd", wallSection, true, []string{"run", "--env", "LOG_LEVEL", "--env", "CLAUDE_CODE_OAUTH_TOKEN=" + modelKeyValue}, modelCredentialLine("CLAUDE_CODE_OAUTH_TOKEN", "--env")},
		{"wall.env, QORY_RUN_KEY", wallSection + "  env: [LOG_LEVEL, ANTHROPIC_API_KEY]\n", false, []string{"run"}, modelCredentialLine("ANTHROPIC_API_KEY", "wall.env in forager.yaml")},
		{"wall.env, --run-key-fd", wallSection + "  env: [CLAUDE_CODE_OAUTH_TOKEN]\n", true, []string{"run"}, modelCredentialLine("CLAUDE_CODE_OAUTH_TOKEN", "wall.env in forager.yaml")},
		{"--env the bearer key NAME, --run-key-fd", wallSection, true, []string{"run", "--env", "ANTHROPIC_AUTH_TOKEN"}, modelCredentialLine("ANTHROPIC_AUTH_TOKEN", "--env")},
		{"wall.env the bearer key NAME, QORY_RUN_KEY", wallSection + "  env: [ANTHROPIC_AUTH_TOKEN]\n", false, []string{"run"}, modelCredentialLine("ANTHROPIC_AUTH_TOKEN", "wall.env in forager.yaml")},
	} {
		t.Run(c.name, func(t *testing.T) {
			os.Remove(file)
			if c.forager != "" {
				writeFile(t, file, c.forager)
			}
			args := c.args
			if c.forager != "" {
				args = append(args, "--home", outside)
			}
			if c.byFD {
				t.Setenv("QORY_RUN_KEY", "")
				args = append(args, "--run-key-fd", descriptor(t, key))
			} else {
				t.Setenv("QORY_RUN_KEY", key)
			}
			out, err := run(t, args...)
			if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("%v (exit %d)\nwant %q\n%s", err, cmd.ExitCode(err), c.want, out)
			}
			all := out
			if err != nil {
				all += err.Error()
			}
			lacks(t, all, modelKeyValue, key[4:40], "qory run: through the gateway", "hello from")
		})
	}
	if _, n := gw.received(); n != 0 {
		t.Error("a refused run reached the gateway's address")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("a refused run ran docker: %v", err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left records %v", ids)
	}
	for _, dir := range []string{root, filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory"), filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory")} {
		noValueUnder(t, dir, modelKeyValue)
	}
}

// TestAnUnwalledRunKeyPassesTheModelCredentialAsBefore is an unwalled run given a run
// key by --run-key-fd whose --env names a variable Claude Code reads its model credential
// from, ANTHROPIC_API_KEY by the name and as NAME=value, CLAUDE_CODE_OAUTH_TOKEN or
// ANTHROPIC_AUTH_TOKEN, or whose wall.env names one and --wall none runs it without the
// wall: the check does not refuse it, and the run goes on to the gateway its run key
// names, as before; no output carries the value.
func TestAnUnwalledRunKeyPassesTheModelCredentialAsBefore(t *testing.T) {
	runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", modelKeyValue)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", modelKeyValue)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", modelKeyValue)
	first := "qory run: through the gateway " + gw.host() + ", run " + keyRunID
	check := func(args ...string) {
		t.Helper()
		out, err := run(t, append(args, "--run-key-fd", descriptor(t, key))...)
		all := out
		if err != nil {
			all += err.Error()
		}
		if strings.Contains(all, "the model's credential") {
			t.Errorf("%v: refused: %v", args, err)
		}
		wants(t, out, first)
		lacks(t, all, modelKeyValue, key[4:40])
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY=" + modelKeyValue, "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		check("run", "--env", name)
	}
	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  env: [ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN]\n")
	check("run", "--wall", "none")
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("a run with --wall none ran docker: %v", err)
	}
	if _, n := gw.received(); n < 5 {
		t.Errorf("the gateway's address was reached %d times, want once a run", n)
	}
}

// TestModelCredentialPassedInWithoutARunKey is a person's qory run, given no run key,
// whose --env or wall.env names ANTHROPIC_API_KEY: the run goes on as before.
func TestModelCredentialPassedInWithoutARunKey(t *testing.T) {
	runKeyCheckout(t)
	t.Setenv("QORY_RUN_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", modelKeyValue)
	out, err := run(t, "run", "--env", "ANTHROPIC_API_KEY")
	if err != nil {
		t.Errorf("--env, unwalled: %v\n%s", err, out)
	}
	wants(t, out, "hello from")

	docker, log := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  env: [ANTHROPIC_API_KEY]\n")
	for _, args := range [][]string{{"run"}, {"run", "--env", "ANTHROPIC_API_KEY"}} {
		out, err := run(t, args...)
		if err != nil && strings.Contains(err.Error(), "the model's credential") {
			t.Errorf("%v, walled: %v\n%s", args, err, out)
		}
		lacks(t, out, "the model's credential")
	}
	if _, err := os.Stat(log); err != nil {
		t.Errorf("the walled runs ran no docker: %v", err)
	}
}

// TestARunKeyPassesAVariableOfNoModelCredential is a walled run given a run key whose
// --env or wall.env names a variable Claude Code declares no credential in, LOG_LEVEL, or
// anthropic_api_key, which is another name than ANTHROPIC_API_KEY: the run passes the
// check and goes on to the gateway its run key names.
func TestARunKeyPassesAVariableOfNoModelCredential(t *testing.T) {
	runKeyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("anthropic_api_key", modelKeyValue)
	first := "qory run: through the gateway " + gw.host() + ", run " + keyRunID
	docker, _ := fakeDocker(t)
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	wallSection := "wall:\n  adapter: docker\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n"
	// Behind a wall, a run given a run key needs its harness outside the checkout.
	outside := t.TempDir()
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--home", outside); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	walled := []string{"run", "--home", outside, "--image", "example.com/agent:1"}

	writeFile(t, file, wallSection)
	t.Setenv("QORY_RUN_KEY", "")
	for _, name := range []string{"LOG_LEVEL", "LOG_LEVEL=debug", "anthropic_api_key"} {
		out, err := run(t, append(walled, "--env", name, "--run-key-fd", descriptor(t, key))...)
		if err != nil && strings.Contains(err.Error(), "the model's credential") {
			t.Errorf("--env %s: %v", name, err)
		}
		wants(t, out, first)
		lacks(t, out, modelKeyValue)
	}

	writeFile(t, file, wallSection+"  env: [LOG_LEVEL, anthropic_api_key]\n")
	t.Setenv("QORY_RUN_KEY", key)
	out, err := run(t, walled...)
	if err != nil && strings.Contains(err.Error(), "the model's credential") {
		t.Errorf("wall.env: %v", err)
	}
	wants(t, out, first)
	lacks(t, out, modelKeyValue)
}
