//go:build unix

package cmd_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/render/codex"
)

// hookMarker is the file in the run's writable Codex home that the Stop hook of
// [TestARealWalledCodexRunsAHookOfEtcCodexTurnedOff] writes.
const hookMarker = codex.HomeInWall + "/qory-test-stop-hook"

// homeHookOn and homeHookOff are the files in the run's writable Codex home that the two
// Stop hooks of the home's hooks.json in [TestARealWalledCodexRunsAHookOfEtcCodexTurnedOff]
// write: the first hook is on, the second turned off.
const (
	homeHookOn  = codex.HomeInWall + "/qory-test-home-hook-on"
	homeHookOff = codex.HomeInWall + "/qory-test-home-hook-off"
)

// stopHookOff is the hooks.state table that turns off the first hook of the first Stop
// group of /etc/codex/hooks.json. Codex 0.170.0 keys a hook's state as
// <file>:<event>:<group>:<hook>, the file being the hooks.json it read the hook from and the
// event in snake case (codex-rs/hooks/src/lib.rs, hook_key and hook_event_key_label;
// codex-rs/hooks/src/engine/discovery.rs, key_source), and reads hooks.state from the
// home's config.toml (codex-rs/hooks/src/config_rules.rs, hook_states_from_stack).
const stopHookOff = `[hooks.state."/etc/codex/hooks.json:stop:0:0"]` + "\nenabled = false\n"

// stopHook is a Stop hook of a hooks.json that runs command with a timeout of 30 seconds.
func stopHook(command string) map[string]any {
	return map[string]any{"type": "command", "command": command, "timeout": 30}
}

// codexHookHash is the hash by which Codex 0.170.0 trusts a hook that is not managed, in
// hooks.state's trusted_hash: the SHA-256 of the JSON of the hook's identity, with sorted
// keys and no spaces, the event in snake case and the hook in the group alone, with its
// defaults filled in (codex-rs/hooks/src/engine/discovery.rs, hook_hash;
// codex-rs/config/src/fingerprint.rs, version_for_toml). A hook with no such hash does not
// run, whatever its enabled setting.
func codexHookHash(t *testing.T, event, command string) string {
	t.Helper()
	identity := map[string]any{"event_name": event, "hooks": []any{
		map[string]any{"type": "command", "command": command, "timeout": 30, "async": false},
	}}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestARealWalledCodexRunsAHookOfEtcCodexTurnedOff is a walled Codex run from a run code
// that only reads, behind a real wall on this machine's engine, with Codex 0.170.0 in the
// image, at a gateway of Forager's in this process whose control plane has a fake API key
// for a stub of the Responses API in a container. The harness's module has one Stop hook,
// which writes a file in the writable home; qory writes it into hooks.json of the folder
// it binds at /etc/codex. The home's config.toml, written through the run's seam for the
// home, turns that hook off with hooks.state enabled = false. Codex still runs the hook:
// the home has the hook's file after the run, and the bound config.toml has the state
// table. The home's hooks.json, written through the same seam, has two Stop hooks that
// are not managed, each trusted by its hash in hooks.state, the second also with
// enabled = false: Codex runs the first and not the second, so the same state does turn
// off a hook outside /etc/codex. Codex logs no failed install of its system skills. It
// runs only with QORY_TEST_DOCKER=1 and docker on the PATH, in a separate test binary,
// and needs an engine whose containers this machine reaches at the containers'
// addresses, as [TestARealWalledCodexRunReachesTheModelThroughTheGateway] does.
func TestARealWalledCodexRunsAHookOfEtcCodexTurnedOff(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if os.Getenv(envRealCodexChild) == "" {
		runRealCodexChild(t, realCodexVersion, realCodexSums)
		return
	}
	var setup realCodexSetup
	if err := json.Unmarshal([]byte(os.Getenv(envRealCodexChild)), &setup); err != nil {
		t.Fatal(err)
	}
	ca, caKey := setup.authority(t)
	trustOnly(t, ca, caKey)
	u := upstream{name: setup.Upstream, host: setup.Host}
	startModelControl(t, u.host, false, "sk-"+setup.Mark+"-api")
	atGateway(t)
	codeGatewayAllow = []string{u.host}
	t.Cleanup(func() { codeGatewayAllow = nil })
	codeReal = true
	t.Cleanup(func() { codeReal = false })
	homeKey := codex.HomeInWall + "/hooks.json:stop:0:"
	onCommand, offCommand := "echo on >> "+homeHookOn, "echo off >> "+homeHookOff
	state := stopHookOff +
		"\n[hooks.state.\"" + homeKey + "0\"]\ntrusted_hash = \"" + codexHookHash(t, "stop", onCommand) + "\"\n" +
		"\n[hooks.state.\"" + homeKey + "1\"]\nenabled = false\ntrusted_hash = \"" + codexHookHash(t, "stop", offCommand) + "\"\n"
	homeHooks, err := json.Marshal(map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{
		stopHook(onCommand), stopHook(offCommand),
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var stateWritten bool
	t.Cleanup(cmd.SetWriteWalledHome(func(dir, files string, run codex.WalledRun) ([]codex.Bound, error) {
		binds, err := codex.WriteWalledHome(dir, files, run)
		if err != nil {
			return nil, err
		}
		// The run binds config.toml and hooks.json by their inodes, so both change in place:
		// the state goes in at the end of config.toml, and the home's hooks replace hooks.json.
		if err := writeInPlace(filepath.Join(files, "config.toml"), os.O_APPEND, "\n"+state); err != nil {
			return nil, err
		}
		if err := writeInPlace(filepath.Join(files, "hooks.json"), os.O_TRUNC, string(homeHooks)); err != nil {
			return nil, err
		}
		stateWritten = true
		return binds, nil
	}))
	hook := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{
		stopHook("echo ran >> " + hookMarker),
	}}}}}
	hooksJSON, err := json.Marshal(hook)
	if err != nil {
		t.Fatal(err)
	}
	extra := maps.Clone(helloSkill)
	extra["examples/hello/modules/hello/settings/codex/hooks.json"] = string(hooksJSON)
	r := runFromCodeLinks(t, func(string) []string {
		machineConfig(t, "harness:\n  launch:\n    codex:\n      command: /usr/local/bin/codex-scan\n")
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  helper: "+setup.Helper+"\n  user: \"1000:1000\"\n  image: "+setup.Image+"\n")
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, func(s string) string {
		return s + "workflows:\n  hello:\n    time_limit: 5m\n    access: read\n"
	}, func(d map[string]any) {
		d["run_settings"] = map[string]any{"runtime": "codex"}
	}, extra, nil)
	records := filepath.Join(r.open.RunsDir, r.open.RunID)
	if !r.joined || r.err != nil {
		record, _ := os.ReadFile(filepath.Join(records, "session.jsonl"))
		t.Fatalf("%v (exit %d), want the join and exit 0\n%s\n%s", r.err, cmd.ExitCode(r.err), r.out, record)
	}
	t.Logf("qory run:\n%s", r.out)
	if !stateWritten {
		t.Fatal("the seam wrote no hooks.state and hooks into the home")
	}
	wants(t, r.out, `"text":"Hi"`, "scan: CODEX_HOME="+codex.HomeInWall+"\n")
	for _, line := range strings.Split(strings.TrimSuffix(state, "\n"), "\n") {
		if line != "" {
			wants(t, r.out, "scan: config "+line+"\n")
		}
	}
	wants(t, r.out, "scan: file "+hookMarker+"\n", "scan: file "+homeHookOn+"\n")
	lacks(t, r.out, "scan: file "+homeHookOff+"\n")
	lacks(t, r.out, `"type":"error"`, systemSkillsLine)
}

// writeInPlace writes text into the file at path, opened write-only with flag, os.O_APPEND
// or os.O_TRUNC, so the file keeps its inode.
func writeInPlace(path string, flag int, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|flag, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
