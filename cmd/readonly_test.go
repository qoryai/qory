package cmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
)

// declares is a stack file change that declares the workflow hello with access, "" for
// a workflow with no access.
func declares(access string) func(string) string {
	return func(s string) string {
		s += "workflows:\n  hello:\n"
		if access == "" {
			return s + "    time_limit: 1h\n"
		}
		return s + "    access: " + access + "\n"
	}
}

// writesHello is the stack file f with the workflow hello declared write, so a run from
// a run code with no wall joins; a file that declares workflows stays as it is.
func writesHello(f string) string {
	if strings.Contains(f, "\nworkflows:") {
		return f
	}
	return f + "workflows:\n  hello:\n    access: write\n"
}

// readRun is a walled run from a run code whose workflow hello the stack file, changed
// by stackFile, declares, with the fake docker and the wall section of forager.yaml
// whose wall.image is image, with more after it; it returns the run and the fake
// docker's log.
func readRun(t *testing.T, stackFile func(string) string, image, more string) (codeRun, string) {
	t.Helper()
	var log string
	r := runFromCodeWith(t, func(string) []string {
		var docker string
		docker, log = fakeDocker(t)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: "+image+"\n"+more)
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, stackFile, nil)
	return r, log
}

// checkoutMounts is the mounts of spec that are, or contain, its working folder.
func checkoutMounts(spec session.SessionSpec) []wall.Mount {
	var out []wall.Mount
	for _, m := range spec.Mounts {
		if rel, err := filepath.Rel(m.Path, spec.Dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out = append(out, m)
		}
	}
	return out
}

// TestAReadWorkflowSeesTheCheckoutReadOnly is a walled run from a run code whose
// workflow the stack declares with access read, with no access, or not at all: the
// session binds the workspace read-only, the checkout's mount is read-only, and the
// checkout stays among the places the run may write.
func TestAReadWorkflowSeesTheCheckoutReadOnly(t *testing.T) {
	for name, stackFile := range map[string]func(string) string{
		"access read": declares("read"),
		"no access":   declares(""),
		"undeclared":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := readRun(t, stackFile, readImage, "")
			if !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if !r.spec.WorkspaceReadOnly {
				t.Error("the session does not bind the workspace read-only")
			}
			if got := checkoutMounts(r.spec); len(got) != 1 || got[0] != (wall.Mount{Path: r.spec.Dir, ReadOnly: true}) {
				t.Errorf("the mounts of the checkout are %+v, want %s read-only alone", got, r.spec.Dir)
			}
			if !slices.Contains(r.spec.Written, r.spec.Dir) {
				t.Errorf("Written %q has no checkout %s", r.spec.Written, r.spec.Dir)
			}
		})
	}
}

// TestAWriteWorkflowSeesTheCheckoutWritable is a walled run from a run code whose
// workflow the stack declares with access write: the workspace and the checkout's mount
// stay writable, and Claude Code's settings get no rules from qory.
func TestAWriteWorkflowSeesTheCheckoutWritable(t *testing.T) {
	r, _ := readRun(t, declares("write"), readImage, "")
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if r.spec.WorkspaceReadOnly {
		t.Error("the session binds the workspace read-only")
	}
	if got := checkoutMounts(r.spec); len(got) != 1 || got[0] != (wall.Mount{Path: r.spec.Dir}) {
		t.Errorf("the mounts of the checkout are %+v, want %s writable alone", got, r.spec.Dir)
	}
}

// TestARunKeyRunSeesTheCheckoutWritable is a walled run given a run key and no run
// code, which starts no workflow: the workspace and the checkout's mount stay writable.
func TestARunKeyRunSeesTheCheckoutWritable(t *testing.T) {
	h := newHostFetch(t, "", "main")
	r := runSetUp(t, h, "", "", nil)
	if len(r.joins) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	j := r.joins[0]
	if j.WorkspaceReadOnly {
		t.Error("the session binds the workspace read-only")
	}
	for _, m := range checkoutMounts(j) {
		if m.ReadOnly {
			t.Errorf("the checkout's mount %+v is read-only", m)
		}
	}
}

// readImage is the wall.image of a read run's forager.yaml: a reference.
const readImage = "example.com/agent:1"

// readRefusals are the runs from a run code of the workflow hello, which the stack does
// not declare, so read, that cannot see the checkout read-only: each with its wall.image
// and the rest of its wall section, "" for no wall, its reason and the line qory run
// prints.
var readRefusals = []struct {
	name, image, wall, reason, line string
}{
	{"no wall", "", "", "read_needs_wall",
		"run this workflow with a wall: workflow hello only reads the checkout, and a run with no wall mounts nothing read-only (read_needs_wall); the code is used"},
	{"setup inside", readImage, "  setup: inside\n", "read_needs_setup_outside",
		"set wall.setup to outside in forager.yaml, or remove it: workflow hello only reads the checkout, and setup inside runs in the agent's container, which sees the checkout read-only (read_needs_setup_outside); the code is used"},
	{"nested Docker", "nested", "  images:\n    nested:\n      ref: example.com/nested:1\n      runtime: sysbox-runc\n      docker: true\n", "read_needs_no_nested_docker",
		"use an image without nested Docker for read workflow hello, or declare it write: image nested has nested Docker, so the checkout cannot be made read-only (read_needs_no_nested_docker); the code is used"},
}

// refusedRead runs the run of a refusal of [readRefusals] with image and wall.
func refusedRead(t *testing.T, image, wall string) (codeRun, string) {
	t.Helper()
	if image == "" {
		return runFromCode(t, nil, nil), ""
	}
	return readRun(t, nil, image, wall)
}

// TestAReadWorkflowThatCannotBeReadOnlyStops is a run from a run code of a read
// workflow with no wall, with wall.setup inside, and on an image with nested Docker:
// each stops after the compose, before the image's fetch, the setup and the join,
// exit 1, with its line, closes failed with its reason and -1, and starts no container.
func TestAReadWorkflowThatCannotBeReadOnlyStops(t *testing.T) {
	for _, c := range readRefusals {
		t.Run(c.name, func(t *testing.T) {
			r, log := refusedRead(t, c.image, c.wall)
			if r.joined || cmd.ExitCode(r.err) != 1 {
				t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			wants(t, r.out+r.err.Error(), c.line)
			if want := []session.Outcome{{State: "failed", Reason: c.reason, ExitCode: -1}}; !slices.Equal(r.closes, want) {
				t.Errorf("the run closed with %v, want %v", r.closes, want)
			}
			for _, call := range []string{"fetch image", "agent env", "setup", "join"} {
				if slices.Contains(r.calls, call) {
					t.Errorf("the calls %v contain %s", r.calls, call)
				}
			}
			if log != "" {
				b, _ := os.ReadFile(log)
				for _, line := range strings.Split(string(b), "\n") {
					if strings.HasPrefix(line, "run ") || strings.HasPrefix(line, "create ") {
						t.Errorf("docker started a container: %s", line)
					}
				}
			}
		})
	}
}

// TestAGatewayTakesTheCloseOfAReadWorkflowThatCannotBeReadOnly is each run of
// [TestAReadWorkflowThatCannotBeReadOnlyStops] opened and closed at a gateway of
// Forager's in this process: the gateway accepts the close, so the last run.exited of
// the gateway's record, events.jsonl, and of the session's, session.jsonl, is failed
// with the reason and -1, and neither record contains batch_refused.
func TestAGatewayTakesTheCloseOfAReadWorkflowThatCannotBeReadOnly(t *testing.T) {
	codeAtGateway = true
	t.Cleanup(func() { codeAtGateway = false })
	for _, c := range readRefusals {
		t.Run(c.name, func(t *testing.T) {
			r, _ := refusedRead(t, c.image, c.wall)
			if r.joined || cmd.ExitCode(r.err) != 1 {
				t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			wants(t, r.out+r.err.Error(), "("+c.reason+"); the code is used")
			dir := filepath.Join(r.open.RunsDir, r.open.RunID)
			for _, file := range []string{"events.jsonl", "session.jsonl"} {
				b, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil {
					t.Fatal(err)
				}
				if got, want := lastExited(t, b), "failed/"+c.reason+"/-1"; got != want {
					t.Errorf("%s ends the run %s, want %s\n%s", file, got, want, b)
				}
				lacks(t, string(b), "batch_refused")
			}
			lacks(t, r.out, "batch_refused")
		})
	}
}

// TestAReadWorkflowOnAnImageWithoutNestedDockerRuns is a walled run from a run code of a
// read workflow whose image is an entry of wall.images with no nested Docker: the run
// joins with the checkout read-only.
func TestAReadWorkflowOnAnImageWithoutNestedDockerRuns(t *testing.T) {
	r, _ := readRun(t, nil, "plain", "  images:\n    plain:\n      ref: example.com/plain:1\n")
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if !r.spec.WorkspaceReadOnly {
		t.Error("the session does not bind the workspace read-only")
	}
}
