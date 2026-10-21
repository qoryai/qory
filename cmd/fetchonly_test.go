package cmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/qoryai/qory/cmd"
)

// fetchOnlyCheckout is the working folder of a fetch-only run: an empty git working
// tree with no harness, where the run's code would be fetched to.
func fetchOnlyCheckout(t *testing.T) string {
	t.Helper()
	dir := emptyDir(t)
	runGit(t, dir, "init", "--quiet")
	t.Chdir(dir)
	t.Cleanup(func() { os.Remove(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")) })
	return dir
}

// fetchOnlyWall writes forager.yaml with a docker wall the fake docker runs, and a
// named image and an allowed prefix beside the default image, and returns the file.
func fetchOnlyWall(t *testing.T) string {
	t.Helper()
	docker, _ := fakeDocker(t)
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	writeFile(t, file, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  images:\n    built: {ref: example.com/built:1}\n  images_allow: [example.com/]\n")
	return file
}

// fetchOnlyRefused are the flags of qory run a fetch-only run is refused with, each with
// a value it takes; every other flag of qory run is one it takes.
var fetchOnlyRefused = map[string][]string{
	"local": {"--local"}, "relabel": {"--relabel"}, "headless": {"--headless"},
	"policy": {"--policy", "policy.yaml"}, "run-id": {"--run-id", keyRunID},
	"label": {"--label", "team=a"}, "kind": {"--kind", "review"}, "title": {"--title", "t"},
	"subject": {"--subject", "type=ticket,ref=7"}, "details": {"--details", "d.json"},
	"stop-signal": {"--stop-signal", "SIGINT"}, "image": {"--image", "example.com/built:1"},
	"env": {"--env", "GH_TOKEN"}, "mount": {"--mount", "/tmp:ro"}, "cpus": {"--cpus", "1"},
	"memory": {"--memory", "1g"}, "pids-limit": {"--pids-limit", "10"}, "shm-size": {"--shm-size", "1g"},
	"access-key-secret-fd": {"--access-key-secret-fd", "secret-fd"}, "home": {"--home", "/tmp/homes"},
}

// fetchOnlyTaken are the flags a fetch-only run takes.
var fetchOnlyTaken = []string{"fetch-only", "run-key-fd", "wall", "timeout", "stop-grace", "verbose", "help"}

// TestFetchOnlyFlagsAreAllKnown checks that every flag of qory run is one a fetch-only
// run takes or one it is refused with, so a new flag is one or the other by choice.
func TestFetchOnlyFlagsAreAllKnown(t *testing.T) {
	c, _, err := cmd.Root().Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	if f := c.Flags().Lookup("fetch-only"); f == nil || !f.Hidden {
		t.Error("--fetch-only is not a hidden flag of qory run")
	}
	if p, _, err := cmd.Root().Find([]string{"run", "prepare"}); err != nil || p.Flags().Lookup("fetch-only") == nil || !p.Flags().Lookup("fetch-only").Hidden {
		t.Error("--fetch-only is not a hidden flag of qory run prepare")
	}
	var names []string
	c.InheritedFlags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	c.LocalFlags().VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	for _, n := range names {
		_, refused := fetchOnlyRefused[n]
		if refused == slices.Contains(fetchOnlyTaken, n) {
			t.Errorf("--%s is neither taken nor refused by a fetch-only run, or both", n)
		}
	}
}

// TestFetchOnlyNeedsARunKey is qory run --fetch-only with no run key: refused as input,
// before anything is read or recorded.
func TestFetchOnlyNeedsARunKey(t *testing.T) {
	dir := fetchOnlyCheckout(t)
	fetchOnlyWall(t)
	for _, args := range [][]string{{"run", "--fetch-only"}, {"run", "--fetch-only", "--wall", "docker"}, {"run", "--fetch-only", "--local"}} {
		out, err := run(t, args...)
		want := "--fetch-only fetches the code of a run given a run key, and no run key is given: give it with --run-key-fd"
		if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v: %v (exit %d)\nwant %q", args, err, cmd.ExitCode(err), want)
		}
		lacks(t, out, "through the gateway", "hello from")
	}
	if ids := recorded(t, dir); len(ids) != 0 {
		t.Errorf("refused runs left records %v", ids)
	}
}

// TestFetchOnlyRefusesWhatRunsMore is a fetch-only run given a run key, by
// QORY_RUN_KEY and by --run-key-fd, with a runtime, arguments for an agent, or any
// flag it does not take, and one with no wall: each is refused as input, before the
// gateway's address is reached and before anything is recorded.
func TestFetchOnlyRefusesWhatRunsMore(t *testing.T) {
	dir := fetchOnlyCheckout(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	writeFile(t, filepath.Join(dir, "policy.yaml"), "egress:\n  mode: observe\n")
	writeFile(t, filepath.Join(dir, "d.json"), "{}\n")
	type refusal struct {
		name string
		args []string
		want string
	}
	const runtime = "--fetch-only fetches the run's code and starts no agent, so it takes no runtime and no arguments after --"
	cases := []refusal{
		{"a runtime", []string{"claude"}, runtime},
		{"a runtime and arguments", []string{"claude", "--", "-p", "hi"}, runtime},
		{"arguments", []string{"--", "-p", "hi"}, runtime},
		{"a dash alone", []string{"--"}, runtime},
	}
	for name, args := range fetchOnlyRefused {
		cases = append(cases, refusal{"--" + name, args, "--fetch-only fetches the run's code and starts no agent, so it takes no --" + name})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fetchOnlyWall(t)
			for _, by := range []string{"QORY_RUN_KEY", "--run-key-fd"} {
				args := []string{"run", "--fetch-only"}
				t.Setenv("QORY_RUN_KEY", key)
				if by == "--run-key-fd" {
					t.Setenv("QORY_RUN_KEY", "")
					args = append(args, "--run-key-fd", descriptor(t, key))
				}
				for _, a := range c.args {
					if a == "secret-fd" {
						a = descriptor(t, newKey(t).Secret())
					}
					args = append(args, a)
				}
				_, before := gw.received()
				out, err := run(t, args...)
				if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
					t.Errorf("by %s: %v (exit %d)\nwant %q", by, err, cmd.ExitCode(err), c.want)
				}
				lacks(t, out, key[4:40], "hello from", "through the gateway")
				if _, n := gw.received(); n != before {
					t.Error("a refused run reached the gateway")
				}
			}
		})
	}
	t.Run("no wall", func(t *testing.T) {
		os.Remove(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"))
		t.Setenv("QORY_RUN_KEY", "")
		_, before := gw.received()
		out, err := run(t, "run", "--fetch-only", "--run-key-fd", descriptor(t, key))
		want := "--fetch-only fetches the run's code inside the wall, and the run has none: --wall docker, or wall in forager.yaml"
		if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
		}
		lacks(t, out, key[4:40], "through the gateway")
		if _, n := gw.received(); n != before {
			t.Error("a refused run reached the gateway")
		}
	})
	if ids := recorded(t, dir); len(ids) != 0 {
		t.Errorf("refused runs left records %v", ids)
	}
}

// TestFetchOnlyNeedsNoHarness is a fetch-only run given a run key, behind the wall, in
// an empty git working tree with no harness composed, and the flags the pool program
// gives it: it goes to the gateway its run key names, as any run given a run key, and
// the key reaches nothing before the gateway's TLS.
func TestFetchOnlyNeedsNoHarness(t *testing.T) {
	dir := fetchOnlyCheckout(t)
	fetchOnlyWall(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	out, err := run(t, "run", "--fetch-only", "--run-key-fd", descriptor(t, key), "--wall", "docker", "--timeout", "15m", "--stop-grace", "5s")
	if err == nil {
		t.Fatalf("the run went on without its gateway:\n%s", out)
	}
	if first, _, _ := strings.Cut(out, "\n"); first != "qory run: through the gateway "+gw.host()+", run "+keyRunID {
		t.Errorf("the first line is %q\n%s; %v", first, out, err)
	}
	lacks(t, out+err.Error(), key[4:40], "harness report", "compose")
	if _, n := gw.received(); n == 0 {
		t.Error("the gateway's address was not reached")
	}
	noValueUnder(t, dir, key)
}
