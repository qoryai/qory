package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/places"
)

// leftID is the run id of the walled run whose entry the tests below leave in Forager's
// registry of walled runs.
const leftID = "0191f2a4-0000-7000-8000-00000000a1e1"

// leftBind is a bind of the walled run leftRun writes: a real path, and whether the run
// could write it.
type leftBind struct {
	path     string
	writable bool
}

// leftRun writes the entry of the walled run leftID to the registry of walled runs, as a
// qory run that was killed leaves it: its lock free, the binds, and the engine of a
// Docker whose command is a script in place of docker. The script answers as containers
// is set: "running" lists the run's container, "stopped" lists it only for ps --all,
// "removed" lists none, and "unreachable" exits 1 for every command. leftRun returns
// the entry's path and the log of the commands the script ran.
func leftRun(t *testing.T, containers string, binds ...leftBind) (entry, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "docker.log")
	answer := map[string]string{
		"running":     `ps) echo c0ffee ;;`,
		"stopped":     `ps) case "$*" in *--all*) echo c0ffee ;; esac ;;`,
		"removed":     `ps) ;;`,
		"unreachable": `*) echo "Cannot connect to the Docker daemon" >&2; exit 1 ;;`,
	}[containers]
	docker := filepath.Join(dir, "docker")
	writeFile(t, docker, "#!/bin/sh\necho \"$*\" >> "+log+"\ncase \"$1\" in\n"+answer+"\ninfo) echo engine-1 ;;\nesac\n")
	if err := os.Chmod(docker, 0o755); err != nil {
		t.Fatal(err)
	}
	var bs []map[string]any
	for _, b := range binds {
		bs = append(bs, map[string]any{"path": b.path, "resolved": b.path, "lookups": []string{}, "writable": b.writable})
	}
	data, err := json.Marshal(map[string]any{
		"run_id": leftID,
		"pid":    1,
		"binds":  bs,
		"engine": map[string]any{"wall": "docker", "command": docker, "id": "engine-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return writeEntry(t, data), log
}

// writeEntry writes data as the entry of the walled run leftID, its lock free, and
// returns the entry's path.
func writeEntry(t *testing.T, data []byte) string {
	t.Helper()
	registry := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory-forager", "walled")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(registry, leftID)
	if err := os.WriteFile(entry, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return entry
}

// inUseLine is the approved refusal of git in dir while a walled run uses it.
func inUseLine(dir string) string {
	return dir + " is in use by a walled run; qory runs no git there until the run ends, since the run could change git's configuration between qory's check and git's read"
}

// TestAKilledRunWithItsContainerLeftHoldsThePlace leaves the entry of a walled run whose
// qory was killed, its lock free, while Docker still lists the run's container: the
// checkout is in use, so qory runs no git there and accepts nothing, and the entry
// stays.
func TestAKilledRunWithItsContainerLeftHoldsThePlace(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	entry, _ := leftRun(t, "running", leftBind{root, true})
	if _, err := run(t, "config"); err == nil || err.Error() != inUseLine(root) {
		t.Errorf("git with the run's container left: %v", err)
	}
	if _, err := run(t, "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "a walled run is using "+root+"; accept once it has ended" {
		t.Errorf("an accept with the run's container left: %v", err)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Errorf("the entry of a run whose container is left is gone: %v", err)
	}
}

// TestAStoppedContainerHoldsThePlace leaves the entry of a walled run whose container
// is stopped and not removed, which Docker lists only when asked for every state: it
// could be started again with its binds, so the checkout is in use.
func TestAStoppedContainerHoldsThePlace(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	leftRun(t, "stopped", leftBind{root, true})
	if _, err := run(t, "config"); err == nil || err.Error() != inUseLine(root) {
		t.Errorf("git with the run's container stopped: %v", err)
	}
	if _, err := run(t, "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "a walled run is using "+root+"; accept once it has ended" {
		t.Errorf("an accept with the run's container stopped: %v", err)
	}
}

// TestADockerThatCannotBeAskedRefusesWithTheEntry leaves the entry of a walled run whose
// Docker cannot be reached: qory runs no git in the checkout and accepts nothing, and
// says how to tell whether the run is over and which entry to delete then.
func TestADockerThatCannotBeAskedRefusesWithTheEntry(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	entry, _ := leftRun(t, "unreachable", leftBind{root, true})
	ending := " If docker ps --all --filter label=dev.qory.run=" + leftID + " lists no container, or the Docker engine of that run no longer exists, delete " + entry
	git := "The check for a container of the walled run " + leftID + " failed. That run used " + root + ", so qory runs no git there." + ending
	if _, err := run(t, "config"); err == nil || err.Error() != git {
		t.Errorf("git with Docker unreachable: %v\nwant %s", err, git)
	}
	accept := "The check for a container of the walled run " + leftID + " failed. That run used " + root + ", so qory does not accept the checkout yet." + ending
	if _, err := run(t, "setup", "repo", "--accept-checkout"); err == nil || err.Error() != accept {
		t.Errorf("an accept with Docker unreachable: %v\nwant %s", err, accept)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Errorf("the entry of a run Docker could not be asked about is gone: %v", err)
	}
}

// TestARemovedContainerFreesThePlace leaves the entry of a walled run whose container
// is removed: once Docker, asked for the run's containers in every state, lists none,
// the run is over, its entry is removed, and git runs in the checkout.
func TestARemovedContainerFreesThePlace(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	entry, log := leftRun(t, "removed", leftBind{root, true})
	if out, err := run(t, "config"); err != nil {
		t.Errorf("git with the run's container removed: %v\n%s", err, out)
	}
	asked, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(asked), "ps --all --quiet --filter label=dev.qory.run="+leftID+"\n") {
		t.Errorf("Docker was not asked for the run's containers: %v\n%s", err, asked)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Errorf("the entry of a run whose container is removed is still there: %v", err)
	}
	if out, err := run(t, "setup", "repo", "--accept-checkout"); err != nil || out != "nothing changed since a walled run began here\n" {
		t.Errorf("an accept with the run's container removed: %v\n%s", err, out)
	}
}

// TestAReadOnlyBindOfARunLeavesThePlaceFree writes the entry of a walled run whose
// container is running and binds the recorded checkout read-only: the run cannot write
// the checkout, so git runs there and accept-checkout goes on.
func TestAReadOnlyBindOfARunLeavesThePlaceFree(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	leftRun(t, "running", leftBind{root, false})
	if out, err := run(t, "config"); err != nil {
		t.Errorf("git with a read-only bind of the checkout: %v\n%s", err, out)
	}
	if out, err := run(t, "setup", "repo", "--accept-checkout"); err != nil || out != "nothing changed since a walled run began here\n" {
		t.Errorf("an accept with a read-only bind of the checkout: %v\n%s", err, out)
	}
}

// TestAWritableBindBesideAReadOnlyOneHoldsThePlace writes the entry of a walled run
// whose container is running, binds the recorded checkout read-only and a folder in the
// checkout read-write: the run can write the checkout, so git and accept-checkout return
// the in-use refusals.
func TestAWritableBindBesideAReadOnlyOneHoldsThePlace(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	leftRun(t, "running", leftBind{root, false}, leftBind{src, true})
	if _, err := run(t, "config"); err == nil || err.Error() != inUseLine(root) {
		t.Errorf("git with a writable bind in the checkout: %v", err)
	}
	if _, err := run(t, "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "a walled run is using "+root+"; accept once it has ended" {
		t.Errorf("an accept with a writable bind in the checkout: %v", err)
	}
}

// TestAnUnreadableEntryStopsGit writes an entry of a walled run that is no JSON, its
// lock free: qory cannot read which places the run binds, so git returns the refusal
// that lists the entry, the run's id and the docker ps command.
func TestAnUnreadableEntryStopsGit(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	entry := writeEntry(t, []byte("{"))
	want := "qory cannot read " + entry + ", the record of the walled run " + leftID + ", so it cannot tell whether that run used " + root + " or is still going, and runs no git there. If docker ps --all --filter label=dev.qory.run=" + leftID + " lists no container, delete " + entry
	if _, err := run(t, "config"); err == nil || err.Error() != want {
		t.Errorf("git with an unreadable entry: %v\nwant %s", err, want)
	}
}

// TestAnUnreadableEntryStopsTheAccept writes an entry of a walled run that is no JSON,
// its lock free: qory cannot read which places the run binds, so accept-checkout returns
// the refusal that lists the entry, the run's id and the docker ps command.
func TestAnUnreadableEntryStopsTheAccept(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	entry := writeEntry(t, []byte("{"))
	want := "qory cannot read " + entry + ", the record of the walled run " + leftID + ", so it cannot tell whether that run used " + root + " or is still going, and does not accept the checkout yet. If docker ps --all --filter label=dev.qory.run=" + leftID + " lists no container, delete " + entry
	if _, err := run(t, "setup", "repo", "--accept-checkout"); err == nil || err.Error() != want {
		t.Errorf("an accept with an unreadable entry: %v\nwant %s", err, want)
	}
}

// TestAPlaceAGoingRunBindsIsNotRecordedForANewRun leaves the entry of a walled run whose
// container is running, with a writable bind of the folder that contains a mount of a
// new run. The new run's start refuses the mount with the line that it is in use, and
// writes no record and no fingerprint, neither of the mount nor of the checkout.
func TestAPlaceAGoingRunBindsIsNotRecordedForANewRun(t *testing.T) {
	root := newCheckout(t)
	shared := filepath.Join(tempDir(t), "shared")
	mount := filepath.Join(shared, "data")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mount, "init", "--quiet")
	leftRun(t, "running", leftBind{shared, true})
	hold, err := cmd.RecordPlaces(root, []wall.Mount{{Path: mount}})
	if err == nil || err.Error() != inUseLine(mount) {
		hold.Release()
		t.Fatalf("got %v, want %q", err, inUseLine(mount))
	}
	for _, p := range []string{root, mount} {
		if r, err := places.Get(p); err != nil || r != nil {
			t.Errorf("the record of %s: %+v, %v", p, r, err)
		}
	}
	if r, err := places.Starting([]string{root, mount}); err != nil {
		t.Errorf("the places stay held: %v", err)
	} else {
		r.Release()
	}
}

// TestTheRunThisProcessOpensDoesNotHoldThePlace leaves the entry of a walled run whose
// container is running, with a writable bind of a recorded checkout. In the process
// that opens that run, as qory run does before its compose, git in the checkout runs:
// the entry is that run's. Once the process has set back the run before, qory stops
// git there again with the line that the checkout is in use.
func TestTheRunThisProcessOpensDoesNotHoldThePlace(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	leftRun(t, "running", leftBind{root, true})
	restore := places.SetThisRun(leftID)
	_, err := run(t, "config")
	restore()
	if err != nil {
		t.Errorf("git in a place of the run this process opens: %v", err)
	}
	if _, err := run(t, "config"); err == nil || err.Error() != inUseLine(root) {
		t.Errorf("git in a place of another run: %v", err)
	}
}
