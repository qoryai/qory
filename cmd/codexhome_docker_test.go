//go:build unix

package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// stubCodex is the program codex of the image in
// [TestARealWalledCodexReadRunHasItsHomeBound]: it prints its arguments and CODEX_HOME,
// tries to write each part of the home that the run binds read-only, to move it away and
// to remove it, and to write a session file in the writable home, and prints whether the
// home has the skill workflow-hello and rules/read-access.rules, and whether config.toml
// marks a project untrusted.
const stubCodex = `#!/bin/sh
echo "stub: args $*"
echo "stub: CODEX_HOME=$CODEX_HOME"
for f in config.toml hooks.json .env environments.toml AGENTS.override.md AGENTS.md; do
  if echo x >> "$CODEX_HOME/$f" 2>/dev/null; then echo "stub: wrote $f"; else echo "stub: cannot write $f"; fi
  if mv "$CODEX_HOME/$f" "$CODEX_HOME/$f-moved" 2>/dev/null; then echo "stub: moved $f"; else echo "stub: cannot move $f"; fi
  if rm -f "$CODEX_HOME/$f" 2>/dev/null; then echo "stub: removed $f"; else echo "stub: cannot remove $f"; fi
done
for d in rules skills agents; do
  if touch "$CODEX_HOME/$d/planted" 2>/dev/null; then echo "stub: wrote $d/"; else echo "stub: cannot write $d/"; fi
  if mv "$CODEX_HOME/$d" "$CODEX_HOME/$d-moved" 2>/dev/null; then echo "stub: moved $d"; else echo "stub: cannot move $d"; fi
  if rm -rf "$CODEX_HOME/$d" 2>/dev/null; then echo "stub: removed $d"; else echo "stub: cannot remove $d"; fi
done
if mkdir -p "$CODEX_HOME/sessions/2026" && echo line > "$CODEX_HOME/sessions/2026/rollout.jsonl"; then echo "stub: wrote sessions"; else echo "stub: cannot write sessions"; fi
if test -f "$CODEX_HOME/skills/workflow-hello/SKILL.md"; then echo "stub: skill"; fi
if test -f "$CODEX_HOME/rules/read-access.rules"; then echo "stub: read rules"; else echo "stub: no read rules"; fi
if grep -q 'trust_level = "untrusted"' "$CODEX_HOME/config.toml"; then echo "stub: untrusted"; fi
`

// TestARealWalledCodexReadRunHasItsHomeBound is a walled Codex run from a run code that
// only reads, behind a real wall on this machine's engine, with a stub of Codex in the
// image. Codex sees CODEX_HOME at /var/lib/qory-codex-home and the prompt mentions the
// skill by its path there; a write into each file and folder that the run binds
// read-only fails, and so do a move and a removal of each; a write of a session file in the
// home passes; the home has the harness's skill, the read run's read-access.rules, and a
// config.toml that marks a project untrusted. The run's folder is gone once the run ends.
// It runs only with QORY_TEST_DOCKER=1 and docker on the PATH.
func TestARealWalledCodexReadRunHasItsHomeBound(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	helper, image := realWallImage(t, "codex", stubCodex)
	atGateway(t)
	codeReal = true
	t.Cleanup(func() { codeReal = false })
	r := runFromCodeLinks(t, func(string) []string {
		machineConfig(t, "harness:\n  launch:\n    codex:\n      command: /usr/local/bin/codex\n")
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, declares("read"), func(d map[string]any) {
		d["run_settings"] = map[string]any{"runtime": "codex"}
	}, map[string]string{
		"examples/hello/modules/hello/skills/workflow-hello/SKILL.md":   "---\nname: workflow-hello\ndescription: Say hello.\n---\n\nSay hello.\n",
		"examples/hello/modules/hello/settings/codex/read-access.rules": "prefix_rule(pattern=[\"rm\"], decision=\"forbidden\")\n",
	}, nil)
	if !r.joined || r.err != nil {
		record, _ := os.ReadFile(filepath.Join(r.open.RunsDir, r.open.RunID, "session.jsonl"))
		t.Fatalf("%v (exit %d), want the join and exit 0\n%s\n%s", r.err, cmd.ExitCode(r.err), r.out, record)
	}
	t.Logf("qory run:\n%s", r.out)
	wants(t, r.out, "stub: CODEX_HOME=/var/lib/qory-codex-home\n", "[$workflow-hello](/var/lib/qory-codex-home/skills/workflow-hello/SKILL.md)")
	for _, name := range []string{"config.toml", "hooks.json", ".env", "environments.toml", "AGENTS.override.md", "AGENTS.md", "rules/", "skills/", "agents/"} {
		wants(t, r.out, "stub: cannot write "+name+"\n")
		lacks(t, r.out, "stub: wrote "+name+"\n")
	}
	for _, name := range codexHomeParts {
		wants(t, r.out, "stub: cannot move "+name+"\n", "stub: cannot remove "+name+"\n")
	}
	wants(t, r.out, "stub: wrote sessions\n", "stub: skill\n", "stub: read rules\n", "stub: untrusted\n")
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if left, err := os.ReadDir(filepath.Join(cache, "qory", "runs")); err != nil && !os.IsNotExist(err) || len(left) != 0 {
		t.Errorf("left after the run: %v, %v", left, err)
	}
}
