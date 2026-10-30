package pool_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// probeArgs are the arguments of the check's docker run up to its script, for the
// harness's image and user.
var probeArgs = []string{
	"run", "--rm", "--pull", "never", "--network", "bridge",
	"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", "1000:1000",
	"--entrypoint", "node", probeImage, "-e",
}

// probeReportOf is what the fake docker run of the check saw; ok is false when it did
// not run.
func probeReportOf(t *testing.T, h *harness) (report, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.runConfig, probeReport))
	if err != nil {
		return report{}, false
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r, true
}

// TestTheProbeRunsBeforeTheFirstClaim is a pool whose check connects to neither
// address: it runs one docker run --rm of the agent's image on the default bridge
// network, as the wall's user, with every capability dropped, no new privileges and no
// pull, whose script tries port 80 of both addresses for 2 seconds each, with the
// allow-listed environment alone and an empty HOME, mode 0700, removed after it. Then
// the pool claims and runs the dispatch.
func TestTheProbeRunsBeforeTheFirstClaim(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.settled(1)
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	rep, ok := probeReportOf(t, h)
	if !ok {
		t.Fatal("the check did not run")
	}
	if len(rep.Args) != len(probeArgs)+1 || !slices.Equal(rep.Args[:len(probeArgs)], probeArgs) {
		t.Fatalf("the check's arguments are %q, want %q and the script", rep.Args, probeArgs)
	}
	script := rep.Args[len(probeArgs)]
	for _, want := range []string{`"169.254.169.254"`, `"fd00:ec2::254"`, "port: 80", "2000", "process.exit(reached.length > 0 ? 10 : 0)"} {
		if !strings.Contains(script, want) {
			t.Errorf("the check's script has no %s:\n%s", want, script)
		}
	}
	for _, kv := range rep.Env {
		if name, _, _ := strings.Cut(kv, "="); !slices.Contains(allowed, name) {
			t.Errorf("the check got %s", name)
		}
	}
	home := filepath.Join(h.state, "probe-home")
	if !slices.Contains(rep.Env, "HOME="+home) || rep.HomeMode != 0o700 {
		t.Errorf("the check's HOME is not %s, mode 0700: %q, %v", home, rep.Env, rep.HomeMode)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the check's HOME is still there: %v", err)
	}
	if _, ok := h.reportOf(h.only().ID); !ok {
		t.Error("qory run did not run after the check")
	}
}

// TestAProbeThatConnectsOrFailsStopsThePool is a check that connected to an address of
// the cloud metadata service, or that could not run: the pool returns the error, which
// starts with its fix, sends Qory Apiary no request, and leaves the journal's claimed
// dispatch for the next start.
func TestAProbeThatConnectsOrFailsStopsThePool(t *testing.T) {
	const (
		rules     = "drop 169.254.169.254 and fd00:ec2::254 in the raw table before Docker starts: a container on the default bridge network connected to "
		claimsNo  = ", the cloud metadata service, so the pool claims no run"
		failed    = "pull " + probeImage + " onto this machine, and check that the container engine runs: the container that checks the cloud metadata service "
		noImage   = "docker: Error response from daemon: No such image: " + probeImage
		entry     = "dp_0000000000000000 claimed\n"
		noProgram = "/nonexistent/docker"
	)
	for _, c := range []struct {
		name   string
		script probeScript
		docker string
		want   string
	}{
		{name: "IPv4", script: probeScript{Exit: 10, Say: []string{"169.254.169.254"}}, want: rules + "169.254.169.254:80" + claimsNo},
		{name: "IPv6", script: probeScript{Exit: 10, Say: []string{"fd00:ec2::254"}}, want: rules + "fd00:ec2::254:80" + claimsNo},
		{name: "both", script: probeScript{Exit: 10, Say: []string{"169.254.169.254", "fd00:ec2::254"}}, want: rules + "169.254.169.254:80 and fd00:ec2::254:80" + claimsNo},
		{name: "reached, no address printed", script: probeScript{Exit: 10}, want: rules + "an address of it" + claimsNo},
		{name: "no image", script: probeScript{Exit: 125, Say: []string{noImage}}, want: failed + "exited 125: " + noImage},
		{name: "no node in the image", script: probeScript{Exit: 127}, want: failed + "exited 127"},
		{name: "no engine program", docker: noProgram, want: "install " + noProgram + ", or set wall.command in the runs' forager.yaml: the start of " + noProgram + " for the check of the cloud metadata service failed: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.probe(c.script)
			if c.docker != "" {
				h.pool.Settings.Probe.Docker = c.docker
			}
			if err := os.MkdirAll(h.state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(h.state, pool.JournalFile), []byte(entry), 0o600); err != nil {
				t.Fatal(err)
			}
			h.srv.Offer(provisionertest.Run{})
			err := h.pool.Run(context.Background())
			if err == nil || !strings.HasPrefix(err.Error(), c.want) || c.docker == "" && err.Error() != c.want {
				t.Errorf("%v, want %q", err, c.want)
			}
			if n := len(h.srv.Calls()); n != 0 {
				t.Errorf("the fake was sent %d requests", n)
			}
			if lines := journalOf(t, h.state); !slices.Equal(lines, []string{strings.TrimSuffix(entry, "\n")}) {
				t.Errorf("the journal is %q", lines)
			}
		})
	}
}

// TestAPoolStoppedAtTheProbeClaimsNothing is a pool whose context ended before its
// check ran: Run returns nil, and sends Qory Apiary no request.
func TestAPoolStoppedAtTheProbeClaimsNothing(t *testing.T) {
	h := newHarness(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.srv.Offer(provisionertest.Run{})
	if err := h.pool.Run(ctx); err != nil {
		t.Errorf("Run returned %v", err)
	}
	if n := len(h.srv.Calls()); n != 0 {
		t.Errorf("the fake was sent %d requests", n)
	}
}
