package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/cmd"
)

// toolsSection is gateway.tools defining the tool files, whose program is program, for
// the host files.tools.internal.
func toolsSection(program string) string {
	return "gateway:\n  tools:\n    files:\n      command: [" + program + ", --prefix, \"${argument}\"]\n      argument: \"[a-z0-9-]+/[a-z0-9-]+\"\n      serves: [files.tools.internal]\n"
}

// toolPolicy writes a run's policy file, for --policy, outside the checkout, that
// selects the tool name with the argument acme/shop.
func toolPolicy(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, path, "version: 1\negress:\n  mode: enforce\n  allow: [files.tools.internal]\ntools:\n  - {name: "+name+", argument: acme/shop}\n")
	return path
}

// stubTool writes a tool that writes a line to its standard error and exits before it
// listens, as a tool that cannot start does.
func stubTool(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "files-tool")
	writeFile(t, path, "#!/bin/sh\necho \"files-tool: the stub does not listen\" >&2\nexit 1\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunHandsForagerTheMachinesTools is gateway.tools reaching the run's gateway. A run
// without a wall whose policy selects the tool is refused for want of a wall. Behind a
// wall, a policy that selects a tool forager.yaml does not define is Forager's
// tool_unknown, and one that selects the tool it defines starts the tool's program: the
// stub exits before it listens, and its line is the reason the run does not start.
func TestRunHandsForagerTheMachinesTools(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	tool := stubTool(t)
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n"+toolsSection(tool))
	out, err := run(t, "run", "claude", "--policy", toolPolicy(t, "files"))
	if err == nil || !strings.Contains(err.Error(), "run it with a wall: the policy selects tools or has path rules") || strings.Contains(err.Error(), "does not define") {
		t.Errorf("a run without a wall that selects the tool: %v\n%s", err, out)
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	docker, _ := fakeDocker(t)
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n"+toolsSection(tool)+"wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	out, err = run(t, "run", "claude", "--policy", toolPolicy(t, "search"))
	if err == nil || !strings.Contains(err.Error(), `the policy selects the tool "search", which this machine does not define`) {
		t.Errorf("a walled run that selects a tool forager.yaml does not define: %v\n%s", err, out)
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "run", "claude", "--policy", toolPolicy(t, "files"))
	if err == nil || !strings.Contains(err.Error(), "tool files: ") || !strings.Contains(err.Error(), "files-tool: the stub does not listen") {
		t.Errorf("a walled run that selects the tool: %v\n%s", err, out)
	}
}

// TestRunRefusesAToolForagerRefuses is a tool definition Forager's check refuses: qory
// run and qory gateway say so in Forager's words after "forager.yaml: ", an input
// error, before anything starts.
func TestRunRefusesAToolForagerRefuses(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  tools:\n    files:\n      command: [/opt/tools/files]\n      serves: []\n")
	want := "forager.yaml: tool files serves no host; a tool is reached by the hosts it serves"
	if _, err := run(t, "run", "claude"); cmd.ExitCode(err) != cmd.ExitInput || err.Error() != want {
		t.Errorf("qory run: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left records %v", ids)
	}
	emptyDir(t)
	serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS+"  tools:\n    files:\n      command: [\"/opt/tools/${argument}\"]\n      serves: [files.tools.internal]\n")
	writeCertificate(t)
	want = `forager.yaml: tool files: the argument goes in the command's arguments, not in the program's name`
	// A gateway that started would wait for a signal: the test ends it at its start.
	t.Cleanup(cmd.SetStartGateway(func(gateway.Config) error { return errors.New("the gateway started") }))
	out, err := run(t, "gateway")
	if cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != want {
		t.Errorf("qory gateway: %v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	lacks(t, out, "listening on")
}

// TestGatewayHandsForagerTheMachinesTools is qory gateway handing the gateway it starts
// the tools forager.yaml defines, each as the file writes it, its program by the path
// qory checked, with its links resolved. The program is never run.
func TestGatewayHandsForagerTheMachinesTools(t *testing.T) {
	emptyDir(t)
	program := filepath.Join(tempDir(t), "files")
	writeFile(t, program, "")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS+"  tools:\n    files:\n      command: ["+program+", --prefix, \"${argument}\"]\n      argument: \"[a-z0-9-]+/[a-z0-9-]+\"\n      serves: [files.tools.internal]\n      placeholders: [FILES_TOKEN]\n")
	writeCertificate(t)
	var got []gateway.Tool
	stopped := errors.New("the test stopped the gateway before it started")
	t.Cleanup(cmd.SetStartGateway(func(cfg gateway.Config) error {
		got = cfg.Tools
		return stopped
	}))
	if _, err := run(t, "gateway"); err == nil || !strings.Contains(err.Error(), stopped.Error()) {
		t.Fatalf("qory gateway did not reach the gateway's start: %v", err)
	}
	if len(got) != 1 || got[0].Name != "files" || strings.Join(got[0].Command, " ") != program+" --prefix ${argument}" || got[0].Argument != "[a-z0-9-]+/[a-z0-9-]+" ||
		strings.Join(got[0].Serves, " ") != "files.tools.internal" || strings.Join(got[0].Placeholders, " ") != "FILES_TOKEN" {
		t.Errorf("the gateway's tools: %+v", got)
	}
}

// TestConfigListsTheTools lists each tool forager.yaml defines by its program and the
// hosts it serves.
func TestConfigListsTheTools(t *testing.T) {
	emptyDir(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n  tools:\n    files:\n      command: [/opt/tools/files, --prefix, \"${argument}\"]\n      argument: \"[a-z0-9-]+/[a-z0-9-]+\"\n      serves: [files.tools.internal, media.tools.internal]\n")
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsConfigRow(t, configRows(out), "gateway.tools.files", "/opt/tools/files, serves files.tools.internal, media.tools.internal", "~/.config/qory/forager.yaml")
}
