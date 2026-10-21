package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"
	forefusal "github.com/qoryai/forager/refusal"

	"github.com/qoryai/qory/cmd"
)

// machinePaths is gateway.egress of forager.yaml, observe, with path rules for two hosts.
const machinePaths = "  egress:\n    mode: observe\n    paths:\n      api.example.com: [\"/v2/*\"]\n      files.example.com: [\"/docs/*\"]\n"

// pathsOf is the path rules an event's data holds under key, as strings by host.
func pathsOf(data map[string]any, key string) map[string]string {
	m, _ := data[key].(map[string]any)
	if m == nil {
		return nil
	}
	out := map[string]string{}
	for host, list := range m {
		var paths []string
		for _, p := range list.([]any) {
			paths = append(paths, p.(string))
		}
		out[host] = strings.Join(paths, " ")
	}
	return out
}

// terminated is the hosts run.policy_applied says the proxy terminates TLS for.
func terminated(applied map[string]any) []string {
	var out []string
	for _, h := range applied["terminated"].([]any) {
		out = append(out, h.(string))
	}
	return out
}

// TestGatewayHandsForagerTheMachinesPaths is qory gateway handing the gateway it starts
// the path rules of gateway.egress.paths, in the machine's policy.
func TestGatewayHandsForagerTheMachinesPaths(t *testing.T) {
	emptyDir(t)
	serverFile(t, newGatewayServer(t), "  listen: 127.0.0.1:0\n"+gatewayTLS+machinePaths)
	writeCertificate(t)
	var got *gateway.Policy
	stopped := errors.New("the test stopped the gateway before it started")
	t.Cleanup(cmd.SetStartGateway(func(cfg gateway.Config) error {
		got = cfg.Policy
		return stopped
	}))
	if _, err := run(t, "gateway"); err == nil || !strings.Contains(err.Error(), stopped.Error()) {
		t.Fatalf("qory gateway did not reach the gateway's start: %v", err)
	}
	if got == nil || got.Egress.Mode != "observe" || len(got.Egress.Paths) != 2 || strings.Join(got.Egress.Paths["api.example.com"], " ") != "/v2/*" || strings.Join(got.Egress.Paths["files.example.com"], " ") != "/docs/*" {
		t.Errorf("the gateway's policy: %+v", got)
	}
}

// TestARunUnderTheMachinesPathsNeedsAWall is a run without a wall on a machine whose
// forager.yaml sets gateway.egress.paths: Forager refuses it for its path rules, in
// Forager's words, with a policy file that has none as without one.
func TestARunUnderTheMachinesPathsNeedsAWall(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\ngateway:\n"+machinePaths)
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: enforce\n  allow: [api.example.com]\n")
	for name, args := range map[string][]string{
		"no policy file":            {"run", "claude"},
		"a policy file of no paths": {"run", "claude", "--policy", policy},
	} {
		clearRuns(t, root)
		out, err := run(t, args...)
		var ref *forefusal.NeedsWall
		if !errors.As(err, &ref) || !slices.Contains(ref.Names, "paths") ||
			!strings.Contains(err.Error(), "run it with a wall: the policy selects tools or has path rules, and without a wall a program that ignores the proxy is bound by neither") {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
		lacks(t, out, "hello from")
	}
}

// TestTheMachinesPathsStayBesideARunsPolicy runs behind a wall on a machine whose
// forager.yaml sets gateway.egress.paths. Alone, they are the run's path rules. Under a
// policy file, the file's path rules are the policy's, and the machine's are a side
// beside them, which the proxy reads the hosts of. With a server, they are the node's.
func TestTheMachinesPathsStayBesideARunsPolicy(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	t.Setenv("QORY_TEST_EXIT", "0")
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+machinePaths+passingWall(t))
	machine := map[string]string{"api.example.com": "/v2/*", "files.example.com": "/docs/*"}

	if out, err := run(t, "run", "claude"); err != nil || !strings.Contains(out, "hello from") {
		t.Fatalf("the machine's paths alone: %v\n%s", err, out)
	}
	_, evs := events(t, root)
	applied := evs["dev.qory.run.policy_applied"][0]
	if got := pathsOf(applied, "paths"); len(got) != 2 || got["api.example.com"] != machine["api.example.com"] || got["files.example.com"] != machine["files.example.com"] {
		t.Errorf("the machine's paths alone: policy_applied paths %v", got)
	}

	clearRuns(t, root)
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, policy, "version: 1\negress:\n  mode: enforce\n  allow: [api.example.com, files.example.com]\n  paths:\n    api.example.com: [\"/v1/*\"]\n")
	if out, err := run(t, "run", "claude", "--policy", policy); err != nil || !strings.Contains(out, "hello from") {
		t.Fatalf("under a policy file: %v\n%s", err, out)
	}
	_, evs = events(t, root)
	applied = evs["dev.qory.run.policy_applied"][0]
	if got := pathsOf(applied, "paths"); len(got) != 1 || got["api.example.com"] != "/v1/*" {
		t.Errorf("under a policy file: policy_applied paths %v, want the file's alone", got)
	}
	if hosts := terminated(applied); !slices.Contains(hosts, "api.example.com") || !slices.Contains(hosts, "files.example.com") {
		t.Errorf("under a policy file: terminated %v, want the hosts of both sides", hosts)
	}

	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"observe","paths":{"api.example.com":["/v1/*"]}}}`)
	walledServerFile(t, srv, machinePaths)
	clearRuns(t, root)
	if out, err := run(t, "run", "claude"); err != nil || !strings.Contains(out, "hello from") {
		t.Fatalf("with a server: %v\n%s", err, out)
	}
	_, evs = events(t, root)
	applied = evs["dev.qory.run.policy_applied"][0]
	node, _ := applied["node_policy"].(map[string]any)
	if got := pathsOf(applied, "paths"); len(got) != 1 || got["api.example.com"] != "/v1/*" {
		t.Errorf("with a server: policy_applied paths %v, want the server's", got)
	}
	if got := pathsOf(node, "paths"); len(got) != 2 || got["api.example.com"] != machine["api.example.com"] || got["files.example.com"] != machine["files.example.com"] {
		t.Errorf("with a server: node_policy paths %v, want the machine's", got)
	}
}
