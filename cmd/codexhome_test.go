package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/render/codex"
)

// codexHomeParts are the parts of a walled Codex run's home, by their targets, that the
// run binds read-only, in the order qory mounts them.
var codexHomeParts = []string{"config.toml", "hooks.json", ".env", "environments.toml", "AGENTS.override.md", "AGENTS.md", "rules", "skills", "agents"}

// seenHome is what a join saw of the Codex home of a walled Codex run while the session
// ran: the session's spec, the config.toml bound at the home, and the names in rules/.
type seenHome struct {
	spec   session.SessionSpec
	config string
	rules  []string
}

// homeMounts are the mounts of spec whose target is the Codex home or lies in it.
func homeMounts(spec session.SessionSpec) []wall.Mount {
	var out []wall.Mount
	for _, m := range spec.Mounts {
		if m.Target == codex.HomeInWall || strings.HasPrefix(m.Target, codex.HomeInWall+"/") {
			out = append(out, m)
		}
	}
	return out
}

// sawHome reads, into seen, the Codex home that spec mounts, while the join runs: the
// files go when the run ends.
func sawHome(t *testing.T, spec session.SessionSpec) seenHome {
	t.Helper()
	seen := seenHome{spec: spec}
	for _, m := range homeMounts(spec) {
		switch m.Target {
		case codex.HomeInWall + "/config.toml":
			data, err := os.ReadFile(m.Path)
			if err != nil {
				t.Errorf("the bound config.toml: %v", err)
			}
			seen.config = string(data)
		case codex.HomeInWall + "/rules":
			entries, err := os.ReadDir(m.Path)
			if err != nil {
				t.Errorf("the bound rules: %v", err)
			}
			for _, e := range entries {
				seen.rules = append(seen.rules, e.Name())
			}
		}
	}
	return seen
}

// joinsSeeingHome makes each join of a run note what it saw of the Codex home in seen,
// and end as a session that succeeded, in a record in a new temp folder.
func joinsSeeingHome(t *testing.T, seen *[]seenHome) {
	t.Helper()
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		*seen = append(*seen, sawHome(t, spec))
		return &session.Result{Dir: t.TempDir(), State: "succeeded"}, nil
	}))
}

// checkHomeMounts fails the test unless spec mounts a writable home at the Codex home,
// in a folder codex-home of a folder of the user's cache under qory/runs, and each part
// of [codexHomeParts] read-only at its target, from the folder codex-fixed beside it, and
// unless CODEX_HOME is a fixed variable at the home and no default. It returns the
// writable home's path.
func checkHomeMounts(t *testing.T, spec session.SessionSpec) string {
	t.Helper()
	mounts := homeMounts(spec)
	if len(mounts) != 1+len(codexHomeParts) {
		t.Fatalf("the mounts at the Codex home: %+v", mounts)
	}
	home := mounts[0]
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if home.Target != codex.HomeInWall || home.ReadOnly || filepath.Base(home.Path) != "codex-home" || filepath.Dir(filepath.Dir(home.Path)) != filepath.Join(cache, "qory", "runs") {
		t.Errorf("the writable home: %+v", home)
	}
	for i, name := range codexHomeParts {
		m := mounts[1+i]
		if m.Target != codex.HomeInWall+"/"+name || !m.ReadOnly || m.Path != filepath.Join(filepath.Dir(home.Path), "codex-fixed", name) {
			t.Errorf("the mount of %s: %+v", name, m)
		}
	}
	if !slices.Contains(spec.LaunchFixed, "CODEX_HOME="+codex.HomeInWall) {
		t.Errorf("the fixed variables: %q", spec.LaunchFixed)
	}
	if slices.ContainsFunc(spec.LaunchDefaults, func(kv string) bool { return strings.HasPrefix(kv, "CODEX_HOME=") }) {
		t.Errorf("the defaults: %q", spec.LaunchDefaults)
	}
	return home.Path
}

// TestAWalledCodexRunMountsAFreshCodexHome is two walled Codex runs of a person, in a
// copy: each session mounts a writable home at /var/lib/qory-codex-home and each part of
// the home read-only at its target, with CODEX_HOME fixed at the home. config.toml marks
// the copy untrusted, a write run's rules/ has no read-access.rules, the two runs get
// different homes, and each run's folders are gone once it ends.
func TestAWalledCodexRunMountsAFreshCodexHome(t *testing.T) {
	exportingCheckout(t, fakeRuntime(t))
	walledForager(t)
	t.Setenv("QORY_RUN_KEY", "")
	var seen []seenHome
	joinsSeeingHome(t, &seen)
	for range 2 {
		if out, err := run(t, "run", "codex", "--", "hi"); err != nil {
			t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("%d joins, want 2", len(seen))
	}
	var homes []string
	for _, s := range seen {
		home := checkHomeMounts(t, s.spec)
		homes = append(homes, home)
		work := filepath.Join(filepath.Dir(home), "work")
		wants(t, s.config, "[projects.\""+work+"\"]\n    trust_level = \"untrusted\"\n", "[projects.\"/workspace\"]\n    trust_level = \"untrusted\"\n")
		if slices.Contains(s.rules, codex.ReadRulesName) {
			t.Errorf("a write run's rules: %q", s.rules)
		}
		for _, p := range []string{home, filepath.Join(filepath.Dir(home), "codex-fixed")} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				t.Errorf("%s is left after the run: %v", p, err)
			}
		}
	}
	if homes[0] == homes[1] {
		t.Errorf("both runs got the home %s", homes[0])
	}
}

// TestOnlyAWalledCodexRunMountsACodexHome is an unwalled Codex run and a walled Claude
// Code run: neither mounts anything at the Codex home, and the unwalled run keeps
// CODEX_HOME at the runtime's directory of the composed home.
func TestOnlyAWalledCodexRunMountsACodexHome(t *testing.T) {
	root, home := exportingCheckout(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	var seen []seenHome
	joinsSeeingHome(t, &seen)
	if out, err := run(t, "run", "codex", "--", "hi"); err != nil || len(seen) != 1 {
		t.Fatalf("an unwalled run: %v\n%s", err, out)
	}
	if m := homeMounts(seen[0].spec); len(m) != 0 {
		t.Errorf("an unwalled Codex run mounts %+v", m)
	}
	if !slices.Contains(seen[0].spec.LaunchFixed, "CODEX_HOME="+filepath.Join(home, "codex")) {
		t.Errorf("an unwalled Codex run's fixed variables: %q", seen[0].spec.LaunchFixed)
	}
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: claude\n  modules:\n    - name: core\n      source: {path: modules/core}\n")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      command: "+fakeRuntime(t)+"\n")
	if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	walledForager(t)
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil || len(seen) != 2 {
		t.Fatalf("a walled Claude Code run: %v\n%s", err, out)
	}
	if m := homeMounts(seen[1].spec); len(m) != 0 {
		t.Errorf("a walled Claude Code run mounts %+v", m)
	}
	for _, kv := range seen[1].spec.LaunchFixed {
		if strings.HasPrefix(kv, "CODEX_HOME=") {
			t.Errorf("a walled Claude Code run's fixed variables: %q", seen[1].spec.LaunchFixed)
		}
	}
}

// TestAWalledCodexReadRunGetsTheReadRules is a walled Codex run from a run code that only
// reads, with no copy: it mounts the Codex home from a new folder of the run under the
// user's cache, its rules/ has read-access.rules with the module's rules, its prompt
// mentions the workflow's skill, which the composed home has, by its path in the home,
// and the folder is gone once the run ends.
func TestAWalledCodexReadRunGetsTheReadRules(t *testing.T) {
	var seen []seenHome
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) { seen = append(seen, sawHome(t, s)) }))
	r := runFromCodeLinks(t, func(string) []string {
		docker, _ := fakeDocker(t)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: "+readImage+"\n")
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, declares("read"), func(d map[string]any) {
		d["run_settings"] = map[string]any{"runtime": "codex"}
	}, map[string]string{
		"examples/hello/modules/hello/skills/workflow-hello/SKILL.md":   "---\nname: workflow-hello\ndescription: Say hello.\n---\n\nSay hello.\n",
		"examples/hello/modules/hello/settings/codex/read-access.rules": "prefix_rule(pattern=[\"rm\"], decision=\"forbidden\")\n",
	}, nil)
	if !r.joined || len(seen) != 1 {
		t.Fatalf("%v (exit %d), want one join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	home := checkHomeMounts(t, seen[0].spec)
	if !slices.Contains(seen[0].rules, codex.ReadRulesName) {
		t.Errorf("a read run's rules: %q", seen[0].rules)
	}
	if args := seen[0].spec.Args; len(args) == 0 || !strings.HasPrefix(args[len(args)-1], "[$workflow-hello](/var/lib/qory-codex-home/skills/workflow-hello/SKILL.md) ") {
		t.Errorf("the arguments %q, want the prompt to mention workflow-hello by its path", args)
	}
	if _, err := os.Lstat(filepath.Dir(home)); !os.IsNotExist(err) {
		t.Errorf("the run's folder %s is left after the run: %v", filepath.Dir(home), err)
	}
}

// TestForagerBindsQorysFolderOverEtcCodex is a person's walled Codex run behind the docker
// wall, with a program in place of docker and no stand-in for the join: Forager's session
// checks the mounts, the mount at /etc/codex among them, and starts the agent's
// container, which binds the folder etc-codex of the run's codex-fixed read-only at
// /etc/codex, where the image may have a folder of its own. While the session runs, the
// folder has mode 0755 and contains hooks.json alone, mode 0644.
func TestForagerBindsQorysFolderOverEtcCodex(t *testing.T) {
	exportingCheckout(t, fakeRuntime(t))
	log := walledForager(t)
	t.Setenv("QORY_RUN_KEY", "")
	var modes map[string]os.FileMode
	var names []string
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) {
		for _, m := range s.Mounts {
			if m.Target != codex.SystemInWall {
				continue
			}
			modes = map[string]os.FileMode{}
			for _, p := range []string{m.Path, filepath.Join(m.Path, "hooks.json")} {
				if info, err := os.Lstat(p); err == nil {
					modes[filepath.Base(p)] = info.Mode()
				}
			}
			entries, _ := os.ReadDir(m.Path)
			for _, e := range entries {
				names = append(names, e.Name())
			}
		}
	}))
	out, err := run(t, "run", "codex", "--", "hi")
	data, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatalf("%v; the run: %v (exit %d)\n%s", readErr, err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "Set the target of the mount")
	bind := regexp.MustCompile(`--mount type=bind,src=(\S+),dst=/etc/codex,readonly( |$)`).FindStringSubmatch(string(data))
	if bind == nil || filepath.Base(bind[1]) != codex.SystemDir || filepath.Base(filepath.Dir(bind[1])) != "codex-fixed" {
		t.Fatalf("no read-only bind of codex-fixed/etc-codex at /etc/codex: %v (exit %d)\n%s\n%s", err, cmd.ExitCode(err), out, data)
	}
	if modes["etc-codex"] != 0o755|os.ModeDir || modes["hooks.json"] != 0o644 || !slices.Equal(names, []string{"hooks.json"}) {
		t.Errorf("the bound folder: modes %v, entries %q", modes, names)
	}
}

// codexWriteFromCode is a walled Codex write run from a run code, which gets no copy and
// binds the checkout writable, with before run first in the checkout, and what each join
// saw of the Codex home in seen.
func codexWriteFromCode(t *testing.T, seen *[]seenHome, before func(root string)) codeRun {
	t.Helper()
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) { *seen = append(*seen, sawHome(t, s)) }))
	return runFromCodeLinks(t, func(string) []string {
		root, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if before != nil {
			before(root)
		}
		docker, _ := fakeDocker(t)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: "+readImage+"\n")
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, declares("write"), func(d map[string]any) {
		d["run_settings"] = map[string]any{"runtime": "codex"}
	}, map[string]string{
		"examples/hello/modules/hello/skills/workflow-hello/SKILL.md":   "---\nname: workflow-hello\ndescription: Say hello.\n---\n\nSay hello.\n",
		"examples/hello/modules/hello/settings/codex/read-access.rules": "prefix_rule(pattern=[\"rm\"], decision=\"forbidden\")\n",
	}, nil)
}

// TestAWalledCodexWriteRunFromACodeWithTheCacheInItsCheckoutStops is a walled Codex write
// run from a run code in a checkout that contains the user's cache folder, where the run's
// Codex home would go: the run stops before it makes the home, exit 2, with qory's line
// for a checkout that contains the folder of the runs' copies, and nothing is left in
// that folder.
func TestAWalledCodexWriteRunFromACodeWithTheCacheInItsCheckoutStops(t *testing.T) {
	var seen []seenHome
	r := codexWriteFromCode(t, &seen, func(root string) { cacheIn(t, filepath.Join(root, "cache")) })
	if r.joined || cmd.ExitCode(r.err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d), want exit %d before the join\n%s", r.err, cmd.ExitCode(r.err), cmd.ExitInput, r.out)
	}
	wants(t, r.err.Error(), "start the run from a folder that does not contain your home folder, qory's state directory or the folder of the runs' copies: the folder ", "contains the folder of the runs' copies ")
	runsLeft(t)
}

// TestAWalledCodexWriteRunFromACodeWritesInTheCheckout is a walled Codex write run from a
// run code: it gets no copy, binds the checkout writable, and mounts the Codex home from a
// new folder of the run under the user's cache. config.toml marks the checkout untrusted,
// rules/ has no read-access.rules although the harness has read rules, and the run's
// folder is gone once the run ends.
func TestAWalledCodexWriteRunFromACodeWritesInTheCheckout(t *testing.T) {
	var seen []seenHome
	r := codexWriteFromCode(t, &seen, nil)
	if !r.joined || len(seen) != 1 {
		t.Fatalf("%v (exit %d), want one join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if got := checkoutMounts(r.spec); len(got) != 1 || got[0] != (wall.Mount{Path: r.spec.Dir}) {
		t.Errorf("the mounts of the checkout are %+v, want %s writable alone", got, r.spec.Dir)
	}
	home := checkHomeMounts(t, seen[0].spec)
	wants(t, seen[0].config, "[projects.\""+r.spec.Dir+"\"]\n    trust_level = \"untrusted\"\n")
	if slices.Contains(seen[0].rules, codex.ReadRulesName) {
		t.Errorf("a write run's rules: %q", seen[0].rules)
	}
	if _, err := os.Lstat(filepath.Dir(home)); !os.IsNotExist(err) {
		t.Errorf("the run's folder %s is left after the run: %v", filepath.Dir(home), err)
	}
}

// runsLeft fails the test when the folder of the runs' copies, under the user's cache
// folder, has anything in it.
func runsLeft(t *testing.T) {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if left, err := os.ReadDir(filepath.Join(cache, "qory", "runs")); err != nil && !os.IsNotExist(err) || len(left) != 0 {
		t.Errorf("left in the runs' folder: %v, %v", left, err)
	}
}

// TestAWalledCodexRunThatFailsPartwayLeavesNoRunFolder is a walled Codex run whose Codex
// home fails after part of it is written, in a copy and from a run code with no copy:
// the run stops before the join, and nothing is left in the folder of the runs' copies.
func TestAWalledCodexRunThatFailsPartwayLeavesNoRunFolder(t *testing.T) {
	failing := func(t *testing.T) {
		t.Cleanup(cmd.SetWriteWalledHome(func(dir, files string, run codex.WalledRun) ([]codex.Bound, error) {
			if _, err := codex.WriteWalledHome(dir, files, run); err != nil {
				t.Errorf("the home's parts: %v", err)
			}
			return nil, errors.New("the write failed")
		}))
	}
	t.Run("in a copy", func(t *testing.T) {
		exportingCheckout(t, fakeRuntime(t))
		walledForager(t)
		t.Setenv("QORY_RUN_KEY", "")
		var seen []seenHome
		joinsSeeingHome(t, &seen)
		failing(t)
		out, err := run(t, "run", "codex", "--", "hi")
		if err == nil || len(seen) != 0 {
			t.Fatalf("%v, %d joins, want an error before the join\n%s", err, len(seen), out)
		}
		wants(t, out+err.Error(), "the write failed")
		runsLeft(t)
	})
	t.Run("with no copy", func(t *testing.T) {
		var seen []seenHome
		r := codexWriteFromCode(t, &seen, func(string) { failing(t) })
		if r.joined || r.err == nil {
			t.Fatalf("%v (exit %d), want an error before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
		}
		wants(t, r.out+r.err.Error(), "the write failed")
		runsLeft(t)
	})
}

// TestTheCodexHomeGoesWhileTheCopyStays is a walled Codex run in a copy whose agent
// deletes the run's branch, so the copy stays: the copy is there after the run, and the
// folders codex-home and codex-fixed beside it are gone.
func TestTheCodexHomeGoesWhileTheCopyStays(t *testing.T) {
	exportingCheckout(t, fakeRuntime(t))
	walledForager(t)
	t.Setenv("QORY_RUN_KEY", "")
	var work string
	agentInCopy(t, func(spec session.SessionSpec) {
		work = spec.Dir
		checkHomeMounts(t, spec)
		branch := gitOutput(t, spec.Dir, "symbolic-ref", "--short", "HEAD")
		gitOutput(t, spec.Dir, "update-ref", "-d", "refs/heads/"+branch)
	})
	out, err := run(t, "run", "codex", "--", "hi")
	if cmd.ExitCode(err) != 1 || work == "" {
		t.Fatalf("exit %d (%v), want 1 after the join\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "read the run's files in "+work+", and run no git there: ")
	if _, err := os.Lstat(work); err != nil {
		t.Errorf("the copy %s is gone: %v", work, err)
	}
	for _, name := range []string{"codex-home", "codex-fixed"} {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(work), name)); !os.IsNotExist(err) {
			t.Errorf("%s is left beside the copy: %v", name, err)
		}
	}
}
