package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/cmd"
)

// mountedInputs is a link to a folder with in/start-artifacts, which retarget points at
// the parent of the run's inputs folder in/ once the run opens: in/ is made after the
// flags are read, so a mount below the link is the only way to name it.
type mountedInputs struct {
	link, in string
	before   map[string]bool
}

// newMountedInputs is a [mountedInputs] whose link leads to a placeholder, so --mount
// finds the path, and which lists the inputs folders that exist before the run.
func newMountedInputs(t *testing.T) *mountedInputs {
	t.Helper()
	placeholder := filepath.Join(tempDir(t), "placeholder")
	if err := os.MkdirAll(filepath.Join(placeholder, "in", "start-artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := &mountedInputs{link: filepath.Join(tempDir(t), "inputs"), before: map[string]bool{}}
	if err := os.Symlink(placeholder, m.link); err != nil {
		t.Fatal(err)
	}
	for _, in := range inputsFolders(t) {
		m.before[in] = true
	}
	return m
}

// inputsFolders lists each qory-inputs-*/in in the temporary directory, links resolved.
func inputsFolders(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(os.TempDir(), "qory-inputs-*", "in"))
	if err != nil {
		t.Fatal(err)
	}
	for i, in := range found {
		if found[i], err = filepath.EvalSymlinks(in); err != nil {
			t.Fatal(err)
		}
	}
	return found
}

// retarget is an OnOpen that finds the one inputs folder the run made and points the
// link at its parent.
func (m *mountedInputs) retarget(t *testing.T) func(session.OpenSpec) {
	return func(session.OpenSpec) {
		var made []string
		for _, in := range inputsFolders(t) {
			if !m.before[in] {
				made = append(made, in)
			}
		}
		if len(made) != 1 {
			t.Errorf("the run made the inputs folders %q, want one", made)
			return
		}
		m.in = made[0]
		if err := os.Remove(m.link); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Dir(m.in), m.link); err != nil {
			t.Error(err)
		}
	}
}

// TestARunCodesMountAtOrInsideTheInputsEndsMachineSetupInvalid is a walled run from a
// run code whose --mount is the read-only inputs folder in/, or start-artifacts inside
// it, through cmd/run.go's check of the mounts before the session: the run closes failed
// with machine_setup_invalid, the output has the cause, the mount and the inputs folder,
// and the agent never runs.
func TestARunCodesMountAtOrInsideTheInputsEndsMachineSetupInvalid(t *testing.T) {
	for _, c := range []struct{ name, below string }{
		{"at in", "in"},
		{"inside in", filepath.Join("in", "start-artifacts")},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMountedInputs(t)
			mount := filepath.Join(m.link, c.below)
			docker, _ := fakeDocker(t)
			p := scopedRepo(t, docker)
			p.flags = append(p.flags, "--mount", mount)
			p.onOpen = m.retarget(t)
			joined := false
			r := p.run(t, func(string) { joined = true }, nil)
			if r.err == nil || errors.Is(r.err, cmd.ErrJoinedNowhere) {
				t.Fatalf("%v (exit %d), want the run to stop before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			want := session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodeMachineSetupInvalid), ExitCode: -1}
			if len(r.closes) != 1 || r.closes[0] != want {
				t.Errorf("the run closed with %+v, want %+v", r.closes, want)
			}
			if joined || slices.Contains(r.calls, "join") {
				t.Errorf("the agent ran; the calls are %q", r.calls)
			}
			wants(t, r.out, "qory run: A wall mount is at or inside the read-only inputs folder, so the session did not start.\n  Mount "+mount+".\n  Inputs folder "+m.in+".")
		})
	}
}

// TestARunKeysMountAtOrInsideTheInputsStopsWithTheFix is a person's walled run with a
// run key and no run code whose wall.mounts has, with :ro, the read-only inputs folder
// in/, or start-artifacts inside it, through cmd/run.go's check of the mounts before the
// session: the output starts with the fix and has the mount and the inputs folder, the
// run closes failed with no end code, and the agent never runs.
func TestARunKeysMountAtOrInsideTheInputsStopsWithTheFix(t *testing.T) {
	for _, c := range []struct{ name, below string }{
		{"at in", "in"},
		{"inside in", filepath.Join("in", "start-artifacts")},
	} {
		t.Run(c.name, func(t *testing.T) {
			keyedFolder(t, true)
			m := newMountedInputs(t)
			mount := filepath.Join(m.link, c.below)
			docker, _ := fakeDocker(t)
			writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  mounts: [\""+mount+":ro\"]\n")
			gw := newKeyedGateway(t)
			key := mintRunKey(t, keyRunID, gw.url())
			t.Setenv("QORY_RUN_KEY", "")
			closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{Proxy: "http://qory:nowhere@127.0.0.1:9", OnOpen: m.retarget(t)})
			defer restore()
			out, err := run(t, "run", "claude", "--run-key-fd", descriptor(t, key))
			if err == nil || errors.Is(err, cmd.ErrJoinedNowhere) {
				t.Fatalf("%v (exit %d), want the run to stop before the join\n%s", err, cmd.ExitCode(err), out)
			}
			want := session.Outcome{State: event.StateFailed, ExitCode: -1}
			if got := closes(); len(got) != 1 || got[0] != want {
				t.Errorf("the run closed with %+v, want %+v", got, want)
			}
			if got := calls(); slices.Contains(got, "join") {
				t.Errorf("the agent ran; the calls are %q", got)
			}
			wants(t, out, "qory run: Remove the mount at or inside the read-only inputs folder from wall.mounts or --mount.\n  Mount "+mount+".\n  Inputs folder "+m.in+".")
		})
	}
}
