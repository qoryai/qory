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

// lowered sets access in a run's documents to read.
func lowered(d map[string]any) { d["access"] = "read" }

// readRun is a walled run from a run code whose workflow hello the stack file, changed
// by stackFile, declares, with the fake docker and the wall section of forager.yaml
// whose wall.image is image, with more after it; it returns the run and the fake
// docker's log.
func readRun(t *testing.T, stackFile func(string) string, image, more string) (codeRun, string) {
	t.Helper()
	return readRunArgs(t, nil, stackFile, image, more)
}

// readRunArgs is [readRun] with the flags args.
func readRunArgs(t *testing.T, args []string, stackFile func(string) string, image, more string) (codeRun, string) {
	t.Helper()
	return readRunDocuments(t, args, stackFile, nil, image, more)
}

// readRunDocuments is [readRunArgs] with the documents changed by change.
func readRunDocuments(t *testing.T, args []string, stackFile func(string) string, change func(map[string]any), image, more string) (codeRun, string) {
	t.Helper()
	var log string
	r := runFromCodeWith(t, func(string) []string {
		var docker string
		docker, log = fakeDocker(t)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: "+image+"\n"+more)
		return nil
	}, append([]string{"--home", filepath.Join(tempDir(t), "home")}, args...), stackFile, change)
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

// TestAReadRunSeesTheCheckoutReadOnly is a walled run from a run code that only reads:
// its documents have access write and the stack declares its workflow with access read,
// with no access, or not at all, or its documents have access read and the stack
// declares its workflow write. The session binds the workspace read-only, the
// checkout's mount is read-only, the checkout stays among the places the run may write,
// and Claude Code's settings deny the edit tools, allow /qory/qory in Bash and set
// defaultMode to dontAsk.
func TestAReadRunSeesTheCheckoutReadOnly(t *testing.T) {
	for _, c := range []struct {
		name      string
		stackFile func(string) string
		change    func(map[string]any)
	}{
		{"access read", declares("read"), nil},
		{"no access", declares(""), nil},
		{"undeclared", nil, nil},
		{"lowered by the documents", declares("write"), lowered},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := readRunDocuments(t, nil, c.stackFile, c.change, readImage, "")
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
			settings := settingsOf(t, r.spec.Args)
			wants(t, settings, `"Edit"`, `"Write"`, `"NotebookEdit"`, `"Bash(/qory/qory *)"`, `"defaultMode": "dontAsk"`)
		})
	}
}

// TestAWriteWorkflowSeesTheCheckoutWritable is a walled run from a run code whose
// workflow the stack declares with access write and whose documents have access write:
// the workspace and the checkout's mount stay writable, and Claude Code's settings get
// no rules from qory.
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
	lacks(t, settingsOf(t, r.spec.Args), "/qory/qory", "dontAsk", "NotebookEdit")
}

// settingsOf is the value of --settings in args, Claude Code's settings as JSON.
func settingsOf(t *testing.T, args []string) string {
	t.Helper()
	i := slices.Index(args, "--settings")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("no --settings in %q", args)
	}
	return args[i+1]
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

// readImages is a wall.images section with an entry with nested Docker and one without.
const readImages = "  images:\n    nested:\n      ref: example.com/nested:1\n      runtime: sysbox-runc\n      docker: true\n" +
	"    plain:\n      ref: example.com/plain:1\n"

// Each of these is the image of a run on an entry of [readImages], as the gateway's run
// answer sets it.
var (
	nestedEntry = session.Image{Name: "nested", Ref: "example.com/nested:1", Runtime: "sysbox-runc", Docker: true}
	plainEntry  = session.Image{Name: "plain", Ref: "example.com/plain:1"}
)

// selectsImage has the policy of the runs of test t select image, [cmd.Nowhere]
// PolicyImage, for the rest of the test.
func selectsImage(t *testing.T, image string) {
	t.Helper()
	codePolicyImage = image
	t.Cleanup(func() { codePolicyImage = "" })
}

// atGateway has the runs of test t open at a gateway of Forager's in this process, for
// the rest of the test.
func atGateway(t *testing.T) {
	t.Helper()
	codeAtGateway = true
	t.Cleanup(func() { codeAtGateway = false })
}

// readRefusals are the runs from a run code of the workflow hello, which the stack does
// not declare, so read, that cannot see the checkout read-only: each with its wall.image
// and the rest of its wall section, "" for no wall, the image its policy selects, its
// reason and the line qory run prints.
var readRefusals = []struct {
	name, image, wall, policy, reason, line string
}{
	{"no wall", "", "", "", "read_needs_wall",
		"run this workflow with a wall: this run of workflow hello only reads the checkout, and a run with no wall mounts nothing read-only (read_needs_wall); the code is used"},
	{"setup inside", readImage, "  setup: inside\n", "", "read_needs_setup_outside",
		"set wall.setup to outside in forager.yaml, or remove it: this run of workflow hello only reads the checkout, and setup inside runs in the agent's container, which sees the checkout read-only (read_needs_setup_outside); the code is used"},
	{"nested Docker", pinnedImage, readImages, "nested", "read_needs_no_nested_docker",
		"use an image without nested Docker: this run of workflow hello only reads the checkout, and image nested has nested Docker, so the checkout cannot be made read-only (read_needs_no_nested_docker); the code is used"},
}

// refusedRead runs the run of a refusal of [readRefusals] with image and wall.
func refusedRead(t *testing.T, image, wall string) (codeRun, string) {
	t.Helper()
	return refusedDocuments(t, nil, nil, image, wall)
}

// refusedDocuments is [refusedRead] with the stack file changed by stackFile and the
// documents by change.
func refusedDocuments(t *testing.T, stackFile func(string) string, change func(map[string]any), image, wall string) (codeRun, string) {
	t.Helper()
	if image == "" {
		return runFromCode(t, stackFile, change), ""
	}
	return readRunDocuments(t, nil, stackFile, change, image, wall)
}

// checkStopped checks that r stopped before the join, exit 1, closed failed with reason
// and -1, made no call that starts anything, and that the fake docker's log, "" for no
// fake docker, shows no container started but the relay, which a real open starts
// before the compose.
func checkStopped(t *testing.T, r codeRun, log, reason string) {
	t.Helper()
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if want := []session.Outcome{{State: "failed", Reason: reason, ExitCode: -1}}; !slices.Equal(r.closes, want) {
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
			if (strings.HasPrefix(line, "run ") || strings.HasPrefix(line, "create ")) && !strings.Contains(line, "-relay ") {
				t.Errorf("docker started a container: %s", line)
			}
		}
	}
}

// checkGatewayRecord checks that the last run.exited of the gateway's record of r,
// events.jsonl, and of the session's, session.jsonl, is failed with reason and -1, and
// that neither record, nor r's output, contains batch_refused.
func checkGatewayRecord(t *testing.T, r codeRun, reason string) {
	t.Helper()
	dir := filepath.Join(r.open.RunsDir, r.open.RunID)
	for _, file := range []string{"events.jsonl", "session.jsonl"} {
		b, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := lastExited(t, b), "failed/"+reason+"/-1"; got != want {
			t.Errorf("%s ends the run %s, want %s\n%s", file, got, want, b)
		}
		lacks(t, string(b), "batch_refused")
	}
	lacks(t, r.out, "batch_refused")
}

// TestAReadWorkflowThatCannotBeReadOnlyStops is a run from a run code of a read
// workflow with no wall, with wall.setup inside, and on an image with nested Docker that
// its policy selects while wall.image is a pinned reference: each stops after the
// compose, before the image's fetch, the setup and the join, exit 1, with its line,
// closes failed with its reason and -1, and starts no container.
func TestAReadWorkflowThatCannotBeReadOnlyStops(t *testing.T) {
	for _, c := range readRefusals {
		t.Run(c.name, func(t *testing.T) {
			selectsImage(t, c.policy)
			r, log := refusedRead(t, c.image, c.wall)
			checkStopped(t, r, log, c.reason)
			wants(t, r.out+r.err.Error(), c.line)
		})
	}
}

// TestALoweredRunThatCannotBeReadOnlyStops is each run of
// [TestAReadWorkflowThatCannotBeReadOnlyStops] with its workflow declared write and
// access read in its documents: no wall, wall.setup inside and an image with nested
// Docker each stop it in the same way, with the same line and reason, and no agent or
// container starts.
func TestALoweredRunThatCannotBeReadOnlyStops(t *testing.T) {
	for _, c := range readRefusals {
		t.Run(c.name, func(t *testing.T) {
			selectsImage(t, c.policy)
			r, log := refusedDocuments(t, declares("write"), lowered, c.image, c.wall)
			checkStopped(t, r, log, c.reason)
			wants(t, r.out+r.err.Error(), c.line)
		})
	}
}

// TestAGatewayTakesTheCloseOfAReadWorkflowThatCannotBeReadOnly is each run of
// [TestAReadWorkflowThatCannotBeReadOnlyStops] opened and closed at a gateway of
// Forager's in this process, whose policy selects the image: the gateway accepts the
// close, so the last run.exited of the gateway's record, events.jsonl, and of the
// session's, session.jsonl, is failed with the reason and -1, and neither record
// contains batch_refused.
func TestAGatewayTakesTheCloseOfAReadWorkflowThatCannotBeReadOnly(t *testing.T) {
	atGateway(t)
	for _, c := range readRefusals {
		t.Run(c.name, func(t *testing.T) {
			selectsImage(t, c.policy)
			r, _ := refusedRead(t, c.image, c.wall)
			checkStopped(t, r, "", c.reason)
			wants(t, r.out+r.err.Error(), "("+c.reason+"); the code is used")
			checkGatewayRecord(t, r, c.reason)
		})
	}
}

// readImageRuns are the ways the image of a run from a run code is selected, each with
// its wall.image, its flags and the image its policy selects: run with an entry of
// [readImages] named entry, the other entry named other, which the selection must beat.
var readImageRuns = []struct {
	name  string
	image func(entry, other string) (wallImage string, args []string, policy string)
}{
	{"a policy over a pinned wall.image", func(entry, _ string) (string, []string, string) { return pinnedImage, nil, entry }},
	{"a policy over wall.image", func(entry, other string) (string, []string, string) { return other, nil, entry }},
	{"a policy over --image", func(entry, other string) (string, []string, string) {
		return pinnedImage, []string{"--image", other}, entry
	}},
	{"wall.image by name", func(entry, _ string) (string, []string, string) { return entry, nil, "" }},
	{"--image by name", func(entry, _ string) (string, []string, string) {
		return pinnedImage, []string{"--image", entry}, ""
	}},
	{"--image over wall.image", func(entry, other string) (string, []string, string) {
		return other, []string{"--image", entry}, ""
	}},
}

// TestAReadWorkflowStopsOnNestedDockerHoweverItsImageIsSelected is a run from a run
// code of a read workflow whose image is an entry of wall.images with docker: true,
// selected by the policy, --image or wall.image: with no gateway, and at a gateway of
// Forager's in this process, Image returns the whole entry, the run stops before the
// join, exit 1, closes failed with read_needs_no_nested_docker and -1, and starts no
// container. The line shows the entry's name.
func TestAReadWorkflowStopsOnNestedDockerHoweverItsImageIsSelected(t *testing.T) {
	const reason = "read_needs_no_nested_docker"
	for _, c := range readImageRuns {
		image, args, policy := c.image("nested", "plain")
		for where, gateway := range map[string]bool{"with no gateway": false, "at a gateway": true} {
			t.Run(c.name+" "+where, func(t *testing.T) {
				selectsImage(t, policy)
				if gateway {
					atGateway(t)
				}
				r, log := readRunArgs(t, args, nil, image, readImages)
				checkStopped(t, r, log, reason)
				wants(t, r.out+r.err.Error(), "image nested has nested Docker")
				if r.image != nestedEntry {
					t.Errorf("Image returned %+v, want %+v", r.image, nestedEntry)
				}
				if gateway {
					checkGatewayRecord(t, r, reason)
				}
			})
		}
	}
}

// TestAReadWorkflowOnAnImageWithoutNestedDockerRuns is a walled run from a run code of a
// read workflow whose image is an entry of wall.images with no nested Docker, selected
// by the policy, --image or wall.image, over an entry with nested Docker where one is
// set: with no gateway, and at a gateway of Forager's in this process, the run joins
// with the checkout read-only.
func TestAReadWorkflowOnAnImageWithoutNestedDockerRuns(t *testing.T) {
	for _, c := range readImageRuns {
		image, args, policy := c.image("plain", "nested")
		for where, gateway := range map[string]bool{"with no gateway": false, "at a gateway": true} {
			t.Run(c.name+" "+where, func(t *testing.T) {
				selectsImage(t, policy)
				if gateway {
					atGateway(t)
				}
				r, _ := readRunArgs(t, args, nil, image, readImages)
				if !r.joined {
					t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
				}
				if !r.spec.WorkspaceReadOnly {
					t.Error("the session does not bind the workspace read-only")
				}
				if got := checkoutMounts(r.spec); len(got) != 1 || got[0] != (wall.Mount{Path: r.spec.Dir, ReadOnly: true}) {
					t.Errorf("the mounts of the checkout are %+v, want %s read-only alone", got, r.spec.Dir)
				}
				if r.image != plainEntry {
					t.Errorf("Image returned %+v, want %+v", r.image, plainEntry)
				}
			})
		}
	}
}
