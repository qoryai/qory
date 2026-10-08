package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/runner/session"
	"github.com/qoryai/runner/wall"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
)

// TestNoRuntimeIsNamedAfterAVerbTheWallRuns holds the runtimes this qory renders apart
// from the verbs that carry [inWall]: qory run takes a runtime as its argument, and cobra
// finds a verb of qory run before it reads an argument, so a runtime named after one of
// them could never be started with qory run <name>. The verbs are found by walking the
// command tree and the runtimes are the ones registered, so a new verb or a new runtime
// is held to it without a change here.
func TestNoRuntimeIsNamedAfterAVerbTheWallRuns(t *testing.T) {
	var verbs []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Annotations[inWall] != "" {
			verbs = append(verbs, c.Name())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(Root())
	for _, want := range []string{"forward", "nest", "relay"} {
		if !slices.Contains(verbs, want) {
			t.Errorf("the walk found the verbs %v, without %s: it no longer sees what carries %s", verbs, want, inWall)
		}
	}
	runtimes := render.Names()
	if len(runtimes) == 0 {
		t.Fatal("no runtime is registered")
	}
	for _, name := range runtimes {
		if slices.Contains(verbs, name) {
			t.Errorf("runtime %s has the name of a verb the wall starts inside a container: qory run %s runs the verb, so the runtime could not be started with qory run %s", name, name, name)
		}
	}
}

// TestUnusedEnvSaysWhy is a line for each --env value the runner left out, and none for
// a value of another source that lost: the server's host when the server's value won,
// the deny list, the harness's fixed value, and the runner's own reason otherwise.
func TestUnusedEnvSaysWhy(t *testing.T) {
	var out strings.Builder
	unusedEnv(&out, "https://apiary.example.com/base")(session.Applied{
		{Name: "LOG_LEVEL", From: session.FromApiary, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyOverridden}}},
		{Name: "PATH", From: session.FromShell, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyDenied}}},
		{Name: "CODEX_HOME", From: session.FromFixed, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyFixed}}},
		{Name: "PROFILE", From: session.FromRun, Lost: []session.Loss{{From: session.FromHarness, Why: session.WhyOverridden}}},
		{Name: "ODD", Lost: []session.Loss{{From: session.FromRun, Why: "other"}}},
	})
	want := "qory run: LOG_LEVEL from --env is not used: apiary.example.com sets it\n" +
		"qory run: PATH from --env is not used: no source may set it\n" +
		"qory run: CODEX_HOME from --env is not used: the harness sets it\n" +
		"qory run: ODD from --env is not used: the runner left it out (other)\n"
	if out.String() != want {
		t.Errorf("lines\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	unusedEnv(&out, "")(session.Applied{{Name: "LOG_LEVEL", From: session.FromApiary, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyOverridden}}}})
	if got := out.String(); got != "qory run: LOG_LEVEL from --env is not used: the server sets it\n" {
		t.Errorf("no server: %q", got)
	}
}

// TestMountRefusedSaysHowTheMountStands is the runner's mount_contains_runner_files
// worded with Overlap's relation: a mount that is or contains the runner's directory
// reads the access key when access-key-secret is there; one that lies inside it, beside
// the key, could change one of the runner's files, with the key there or not, as could
// a mount of another of the runner's files, and read it through a read-only mount; a mount of a link qory passed as its place
// names the link, even when the link is the runner's directory, as leading to one of
// the runner's files, which a writable mount could point elsewhere; and any other error
// is left to the rest. The checkout root and the working directory are the
// workspace.
func TestMountRefusedSaysHowTheMountStands(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "locks")
	refusal := func(mount, path string) error {
		return fmt.Errorf("wrapped: %w", &session.Refusal{Code: "mount_contains_runner_files", Names: []string{mount, path}})
	}
	at := func(runnerDir string) passed { return passed{runnerDir: runnerDir, spec: &session.Spec{}} }
	if got, want := mountRefused(refusal(inner, dir), at(dir)).Error(), "the mount "+inner+" lies inside "+dir+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("lies inside, no access key: %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "access-key-secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, beside := range []string{inner, filepath.Join(dir, "runtimes")} {
		if got, want := mountRefused(refusal(beside, dir), at(dir)).Error(), "the mount "+beside+" lies inside "+dir+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
			t.Errorf("lies inside, beside the key: %q, want %q", got, want)
		}
	}
	parent := filepath.Dir(dir)
	for _, c := range []struct{ mount, how string }{{dir, "is"}, {parent, "contains"}} {
		if got, want := mountRefused(refusal(c.mount, dir), at(dir)).Error(), "the mount "+c.mount+" "+c.how+" "+dir+", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
			t.Errorf("%s: %q, want %q", c.how, got, want)
		}
	}
	// A link's place that shows as the runner's directory itself: the mount holds the
	// link, not the key, which the agent cannot reach through it; it could point a link
	// it can write elsewhere, and does nothing with one it cannot.
	place := linkPlace(dir)
	for _, c := range []struct {
		readOnly bool
		want     string
	}{
		{false, "the mount " + parent + " contains " + dir + ", which leads to one of the runner's files; the agent could point it elsewhere, so the run does not start. Mount a narrower path (mount_contains_runner_files)"},
		{true, "the mount " + parent + " contains " + dir + ", which leads to one of the runner's files, so the run does not start. Mount a narrower path (mount_contains_runner_files)"},
	} {
		p := passed{runnerDir: dir, spec: &session.Spec{Mounts: []wall.Mount{{Path: parent, ReadOnly: c.readOnly}}}, own: ownFiles{links: map[string]string{place: dir}}}
		if got := mountRefused(refusal(parent, place), p).Error(); got != c.want {
			t.Errorf("a link's place, read-only %v: %q, want %q", c.readOnly, got, c.want)
		}
	}
	other := filepath.Join(dir, "wall-files")
	if got, want := mountRefused(refusal(dir, other), at(filepath.Join(dir, "elsewhere"))).Error(), "the mount "+dir+" contains "+other+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("another file: %q, want %q", got, want)
	}
	// Through a read-only mount, the agent could read one of the runner's files, beside
	// the key or elsewhere, and the key itself where the mount is or contains it.
	for _, c := range []struct{ mount, path, want string }{
		{dir, other, "the mount " + dir + " contains " + other + ", which holds one of the runner's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"},
		{inner, dir, "the mount " + inner + " lies inside " + dir + ", which holds one of the runner's files; the agent could read it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"},
		{dir, dir, "the mount " + dir + " is " + dir + ", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_runner_files)"},
		{parent, dir, "the mount " + parent + " contains " + dir + ", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_runner_files)"},
	} {
		p := passed{runnerDir: dir, spec: &session.Spec{Mounts: []wall.Mount{{Path: c.mount, ReadOnly: true}}}}
		if got := mountRefused(refusal(c.mount, c.path), p).Error(); got != c.want {
			t.Errorf("read-only %s of %s: %q, want %q", c.mount, c.path, got, c.want)
		}
	}
	// The same refusals of the workspace: the checkout root as Mounts holds it, and the
	// working directory.
	root := filepath.Dir(dir)
	ws := passed{runnerDir: dir, root: root, spec: &session.Spec{Mounts: []wall.Mount{{Path: root}}, Dir: inner}}
	if got, want := mountRefused(refusal(root, dir), ws).Error(), "the workspace "+root+" contains "+dir+", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("the workspace's root: %q, want %q", got, want)
	}
	if got, want := mountRefused(refusal(inner, dir), ws).Error(), "the workspace "+inner+" lies inside "+dir+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("the working directory: %q, want %q", got, want)
	}
	if got, want := mountRefused(refusal(dir, other), ws).Error(), "the mount "+dir+" contains "+other+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("another mount beside the workspace: %q, want %q", got, want)
	}
	// The root is the workspace only as Mounts holds it.
	if got := mountRefused(refusal(root, dir), passed{runnerDir: dir, root: root, spec: &session.Spec{}}).Error(); !strings.HasPrefix(got, "the mount "+root+" ") {
		t.Errorf("a root Mounts does not hold: %q", got)
	}
	var ref *session.Refusal
	if err := mountRefused(refusal(dir, dir), at(dir)); !errors.As(err, &ref) || ref.Code != "mount_contains_runner_files" {
		t.Errorf("the refusal does not unwrap: %v", err)
	}
	for _, err := range []error{
		errors.New("cannot resolve the mount"),
		&session.Refusal{Code: "variable_reserved", Names: []string{"A", "B"}},
		&session.Refusal{Code: "mount_contains_runner_files", Names: []string{dir}},
		&session.Refusal{Code: "mount_mode_conflict", Names: []string{dir, dir, dir}},
		&session.Refusal{Code: "mount_shared_with_run", Names: []string{dir, "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"}},
	} {
		if got := mountRefused(err, at(dir)); got != nil {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// TestMountRefusedNamesTheRunRecords is mount_contains_runner_files of the runs
// directory, of qory's state directory and of a link on the way to them, for a mount and
// for the workspace: the agent could read the run records through a read-only mount,
// and change them through a writable one, the workspace, or a mount the run did not
// pass. A link on the way is named as itself, leading to the records.
func TestMountRefusedNamesTheRunRecords(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".local", "state", "qory")
	runs := filepath.Join(state, "runs", "app-0123456789ab")
	root := filepath.Join(home, "app")
	link := filepath.Join(home, "linked", "state")
	place := linkPlace(link)
	p := passed{stateDir: state, root: root, spec: &session.Spec{RunsDir: runs, Mounts: []wall.Mount{{Path: root}, {Path: home, ReadOnly: true}, {Path: filepath.Join(state, "runs")}, {Path: filepath.Dir(link), ReadOnly: true}}, Dir: root},
		own: ownFiles{links: map[string]string{place: link}, records: map[string]bool{place: true}}}
	for _, c := range []struct{ mount, path, want, what string }{
		{home, state, "the mount " + home + " contains " + state, "read"},
		{filepath.Join(state, "runs"), runs, "the mount " + filepath.Join(state, "runs") + " contains " + runs, "change"},
		{runs, runs, "the mount " + runs + " is " + runs, "change"},
		{filepath.Join(runs, "x"), runs, "the mount " + filepath.Join(runs, "x") + " lies inside " + runs, "change"},
		{root, state, "the workspace " + root + " overlaps " + state, "change"},
		{root, runs, "the workspace " + root + " overlaps " + runs, "change"},
	} {
		err := mountRefused(&session.Refusal{Code: "mount_contains_runner_files", Names: []string{c.mount, c.path}}, p)
		want := c.want + ", which holds qory's run records; the agent could " + c.what + " them, so the run does not start. Mount a narrower path (mount_contains_runner_files)"
		if err == nil || err.Error() != want {
			t.Errorf("%s and %s: %v, want %q", c.mount, c.path, err, want)
		}
	}
	// A link on the way leads to the records: a writable mount, the workspace or one the
	// run did not pass could point it elsewhere, and a read-only one does nothing with it.
	for _, c := range []struct{ mount, want string }{
		{filepath.Dir(link), "the mount " + filepath.Dir(link) + " contains " + link + ", which leads to qory's run records, so the run does not start."},
		{filepath.Join(home, "elsewhere"), "the mount " + filepath.Join(home, "elsewhere") + " overlaps " + link + ", which leads to qory's run records; the agent could point it elsewhere, so the run does not start."},
		{root, "the workspace " + root + " overlaps " + link + ", which leads to qory's run records; the agent could point it elsewhere, so the run does not start."},
	} {
		err := mountRefused(&session.Refusal{Code: "mount_contains_runner_files", Names: []string{c.mount, place}}, p)
		want := c.want + " Mount a narrower path (mount_contains_runner_files)"
		if err == nil || err.Error() != want {
			t.Errorf("%s and the link: %v, want %q", c.mount, err, want)
		}
	}
	// A runs directory inside the workspace, as XDG_STATE_HOME in the checkout makes it.
	inside := passed{stateDir: filepath.Join(root, "state", "qory"), root: root, spec: &session.Spec{RunsDir: filepath.Join(root, "state", "qory", "runs", "app-0123456789ab"), Mounts: []wall.Mount{{Path: root}}, Dir: root}}
	want := "the workspace " + root + " contains " + inside.stateDir + ", which holds qory's run records; the agent could change them, so the run does not start. Mount a narrower path (mount_contains_runner_files)"
	if err := mountRefused(&session.Refusal{Code: "mount_contains_runner_files", Names: []string{root, inside.stateDir}}, inside); err == nil || err.Error() != want {
		t.Errorf("the state directory in the workspace: %v, want %q", err, want)
	}
}

// TestMountRefusedSaysTheModes is mount_mode_conflict with the modes qory passed: a
// read-only mount inside a writable one, a writable one inside a read-only one, the
// workspace, writable, inside a read-only mount, one path passed with both modes, the
// root the run starts at passed read-only too, and a mount of one mode inside a path
// passed with both.
func TestMountRefusedSaysTheModes(t *testing.T) {
	root, sibling := t.TempDir(), t.TempDir()
	sub, docs := filepath.Join(root, "vendor"), filepath.Join(sibling, "docs")
	spec := &session.Spec{Dir: filepath.Join(sibling, "work"), Mounts: []wall.Mount{{Path: root}, {Path: sub, ReadOnly: true}, {Path: sibling, ReadOnly: true}, {Path: docs}, {Path: root, ReadOnly: true}}}
	p := passed{root: root, spec: spec}
	for _, c := range []struct{ inner, outer, want string }{
		{sub, root, "the mount " + sub + " (read-only) lies inside " + root + ", which is writable"},
		{docs, sibling, "the mount " + docs + " (writable) lies inside " + sibling + ", which is read-only"},
		{spec.Dir, sibling, "the mount " + spec.Dir + " (writable) lies inside " + sibling + ", which is read-only"},
		{root, root, "the mount " + root + " (read-only) lies inside " + root + ", which is writable"},
	} {
		err := mountRefused(&session.Refusal{Code: "mount_mode_conflict", Names: []string{c.inner, c.outer}}, p)
		want := c.want + ": a part of a mount can't have another mode, so the run does not start. Give both the same mode, or leave " + c.inner + " out (mount_mode_conflict)"
		if err == nil || err.Error() != want {
			t.Errorf("%s in %s: %v, want %q", c.inner, c.outer, err, want)
		}
	}
	// The workspace, writable, the same as a read-only mount: the runner counts it last.
	ro := passed{root: root, spec: &session.Spec{Dir: sub, Mounts: []wall.Mount{{Path: sub, ReadOnly: true}}}}
	want := "the mount " + sub + " (writable) lies inside " + sub + ", which is read-only: a part of a mount can't have another mode, so the run does not start. Give both the same mode, or leave " + sub + " out (mount_mode_conflict)"
	if err := mountRefused(&session.Refusal{Code: "mount_mode_conflict", Names: []string{sub, sub}}, ro); err == nil || err.Error() != want {
		t.Errorf("the workspace as a read-only mount: %v, want %q", err, want)
	}
	// The run started at the root, which the person passed read-only too: the runner
	// refuses the read-only entry, the first of the other mode.
	rootRO := passed{root: root, spec: &session.Spec{Dir: root, Mounts: []wall.Mount{{Path: root}, {Path: root, ReadOnly: true}}}}
	want = "the mount " + root + " (read-only) lies inside " + root + ", which is writable: a part of a mount can't have another mode, so the run does not start. Give both the same mode, or leave " + root + " out (mount_mode_conflict)"
	if err := mountRefused(&session.Refusal{Code: "mount_mode_conflict", Names: []string{root, root}}, rootRO); err == nil || err.Error() != want {
		t.Errorf("the root passed read-only too: %v, want %q", err, want)
	}
	// A writable mount inside a checkout passed read-only too: the inner's one mode
	// decides, and the outer is the other one.
	both := passed{root: root, spec: &session.Spec{Dir: root, Mounts: []wall.Mount{{Path: root}, {Path: sub}, {Path: root, ReadOnly: true}}}}
	want = "the mount " + sub + " (writable) lies inside " + root + ", which is read-only: a part of a mount can't have another mode, so the run does not start. Give both the same mode, or leave " + sub + " out (mount_mode_conflict)"
	if err := mountRefused(&session.Refusal{Code: "mount_mode_conflict", Names: []string{sub, root}}, both); err == nil || err.Error() != want {
		t.Errorf("a writable mount in a checkout passed with both modes: %v, want %q", err, want)
	}
	if err := mountRefused(&session.Refusal{Code: "mount_mode_conflict", Names: []string{"/elsewhere/a", "/elsewhere"}}, p); err != nil {
		t.Errorf("paths the run did not pass: %v", err)
	}
}

// TestMountRefusedNamesTheLinkOnTheWay is mount_through_link: a mount inside the
// workspace, the workspace inside a mount and a read-only place, each named with the
// link and the place that holds it; a place that is the link itself, passed clean or
// not, named as a link inside that place. Names of another length are left to the rest.
func TestMountRefusedNamesTheLinkOnTheWay(t *testing.T) {
	root := t.TempDir()
	vendor, shared := filepath.Join(root, "vendor"), filepath.Join(t.TempDir(), "shared")
	work := filepath.Join(shared, "app")
	tail := ", which a walled agent can change, so the run does not start. List the link's target itself (mount_through_link)"
	for _, c := range []struct {
		name               string
		spec               *session.Spec
		place, link, outer string
		want               string
	}{
		{"a mount inside the workspace", &session.Spec{Dir: root, Mounts: []wall.Mount{{Path: root}, {Path: vendor + "/lib"}}},
			vendor + "/lib", vendor, root,
			"the mount " + vendor + "/lib is reached through the link " + vendor + " inside the workspace " + root + tail},
		{"the workspace inside a mount", &session.Spec{Dir: work, Mounts: []wall.Mount{{Path: root}, {Path: shared}}},
			work, filepath.Join(shared, "current"), shared,
			"the workspace " + work + " is reached through the link " + filepath.Join(shared, "current") + " inside the mount " + shared + tail},
		{"a read-only place", &session.Spec{Dir: root, Mounts: []wall.Mount{{Path: root}, {Path: vendor, ReadOnly: true}}},
			vendor, filepath.Join(root, "third_party"), root,
			"the mount " + vendor + " is reached through the link " + filepath.Join(root, "third_party") + " inside the workspace " + root + tail},
		{"the place is the link", &session.Spec{Dir: root, Mounts: []wall.Mount{{Path: root}, {Path: vendor}}},
			vendor, vendor, root,
			"the mount " + vendor + " is a link inside the workspace " + root + tail},
		{"the place is the link, passed unclean", &session.Spec{Dir: root, Mounts: []wall.Mount{{Path: root}, {Path: root + "//vendor/"}}},
			root + "//vendor/", vendor, root,
			"the mount " + root + "//vendor/ is a link inside the workspace " + root + tail},
		{"the workspace is the link", &session.Spec{Dir: work, Mounts: []wall.Mount{{Path: root}, {Path: shared}}},
			work, work, shared,
			"the workspace " + work + " is a link inside the mount " + shared + tail},
	} {
		p := passed{root: root, spec: c.spec}
		err := mountRefused(&session.Refusal{Code: "mount_through_link", Names: []string{c.place, c.link, c.outer}}, p)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	p := passed{root: root, spec: &session.Spec{Dir: root, Mounts: []wall.Mount{{Path: root}}}}
	for _, names := range [][]string{{vendor, root}, {vendor, vendor, root, root}} {
		if err := mountRefused(&session.Refusal{Code: "mount_through_link", Names: names}, p); err != nil {
			t.Errorf("%d names: %v", len(names), err)
		}
	}
}

// TestMountRefusedNamesTheRunStillGoing is mount_shared_with_run, for a mount and for
// the workspace, each way the paths stand: a git worktree, whose .git is a file, is
// told where to make one; a checkout of its own, whose .git is a directory, is not. A
// refusal of this run's own records names them, each way they stand to the other run's
// path. Paths Overlap does not relate overlap.
func TestMountRefusedNamesTheRunStillGoing(t *testing.T) {
	const other = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	checkout := t.TempDir()
	worktree := filepath.Join(checkout, "wt-feature")
	clone := filepath.Join(checkout, "vendor", "lib")
	plain := filepath.Join(checkout, "notes")
	for _, d := range []string{worktree, filepath.Join(clone, ".git"), plain} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(checkout, ".git", "worktrees", "wt-feature")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail := ", which the run " + other + " also uses: it is still going on this machine, or its containers were left behind. One agent could read or change what the other uses, so the run does not start. Wait for " + other + " to end; if it has ended, remove its containers, which docker ps --all --filter label=dev.qory.run=" + other + " lists; or work in a checkout of its own"
	hint := "; make the worktree beside the checkout, not inside it"
	for _, c := range []struct {
		root, path, otherPath, want string
	}{
		{worktree, worktree, checkout, "the workspace " + worktree + " lies inside " + checkout + tail + hint},
		{"/srv/app", worktree, checkout, "the mount " + worktree + " lies inside " + checkout + tail + hint},
		{clone, clone, checkout, "the workspace " + clone + " lies inside " + checkout + tail},
		{"/srv/app", plain, checkout, "the mount " + plain + " lies inside " + checkout + tail},
		{checkout, checkout, plain, "the workspace " + checkout + " contains " + plain + tail},
		{"/srv/app", checkout, plain, "the mount " + checkout + " contains " + plain + tail},
		// Paths Overlap no longer relates.
		{"/srv/app", plain, "/elsewhere/app", "the mount " + plain + " overlaps /elsewhere/app" + tail},
	} {
		p := passed{root: c.root, spec: &session.Spec{Mounts: []wall.Mount{{Path: c.root}, {Path: c.path}}, Dir: c.root}}
		err := mountRefused(&session.Refusal{Code: "mount_shared_with_run", Names: []string{c.path, other, c.otherPath}}, p)
		want := c.want + " (mount_shared_with_run)"
		if err == nil || err.Error() != want {
			t.Errorf("%s and %s: %v, want %q", c.path, c.otherPath, err, want)
		}
	}
	// The runner names the runs directory for this run's own records.
	runs := filepath.Join(t.TempDir(), "runs", "app-0123456789ab")
	if err := os.MkdirAll(filepath.Join(runs, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := passed{root: checkout, spec: &session.Spec{RunsDir: runs, Mounts: []wall.Mount{{Path: checkout}}, Dir: checkout}}
	for _, c := range []struct{ otherPath, how string }{
		{filepath.Dir(runs), "lie inside"},
		{runs, "is"},
		{filepath.Join(runs, "x"), "contain"},
		{"/elsewhere/app", "overlap"},
	} {
		if c.how == "overlap" && session.Overlap(runs, c.otherPath) != "" {
			t.Fatalf("Overlap relates %s and %s", runs, c.otherPath)
		}
		want := "this run's records " + runs + " " + c.how + " " + c.otherPath + ", which the run " + other + " also uses: it is still going on this machine, or its containers were left behind. Its agent could read or change them, so the run does not start. Wait for " + other + " to end; if it has ended, remove its containers, which docker ps --all --filter label=dev.qory.run=" + other + " lists; or keep qory's state directory out of its mounts (mount_shared_with_run)"
		if err := mountRefused(&session.Refusal{Code: "mount_shared_with_run", Names: []string{runs, other, c.otherPath}}, p); err == nil || err.Error() != want {
			t.Errorf("the runs directory %s %s: %v, want %q", c.how, c.otherPath, err, want)
		}
	}
}

// TestRunsDirIsTheCheckoutsFolder is where a checkout's run records go: under
// $XDG_STATE_HOME/qory when it is absolute, else under ~/.local/state/qory, in a folder
// named after the checkout and the hash of where it really is, so a link to it leads to
// the same folder and another checkout of the same name to another.
func TestRunsDirIsTheCheckoutsFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fallback := filepath.Join(home, ".local", "state", "qory")
	for _, c := range []struct{ value, want string }{
		{filepath.Join(home, "xdg"), filepath.Join(home, "xdg", "qory")},
		{"", fallback},
		{"relative/state", fallback},
	} {
		t.Setenv("XDG_STATE_HOME", c.value)
		if got, err := stateDir(); err != nil || got != c.want {
			t.Errorf("XDG_STATE_HOME=%q: %q, %v; want %q", c.value, got, err, c.want)
		}
	}
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(real, "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(root))
	want := filepath.Join(fallback, "runs", "app-"+hex.EncodeToString(sum[:])[:12])
	for _, at := range []string{root, link, root + "/"} {
		if got, err := runsDir(fallback, at); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", at, got, err, want)
		}
	}
	another := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(another, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := runsDir(fallback, another); got == want || filepath.Base(filepath.Dir(got)) != "runs" || !strings.HasPrefix(filepath.Base(got), "app-") {
		t.Errorf("another checkout named app: %q", got)
	}
}

// TestSettingFilesAreTheFileSettingsOfTheDescribedIntegrations is the files the
// settings name: each <name>_file string of an integration described for the run,
// absolute from the working directory, once; a setting of another name, a value that is
// no string, and an integration the run did not describe add none. runnerFiles passes
// each after the rest, as where it leads.
func TestSettingFilesAreTheFileSettingsOfTheDescribedIntegrations(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	r := &config.Runner{Integrations: []config.RunnerIntegration{
		{Key: "tracker", Path: "/opt/acme/tracker", Settings: []byte(`{"project":"SHOP","token_file":"/keys/tracker-token","file":"/keys/plain","_file":"/keys/bare","count_file":3,"cert_file":"certs/tracker.pem"}`)},
		{Key: "chat", Settings: []byte(`{"token_file":"/keys/chat-token"}`)},
		{Key: "board", Path: "/opt/acme/board", Settings: []byte(`{"token_file":"/keys/tracker-token"}`)},
	}}
	want := []string{filepath.Join(cwd, "certs", "tracker.pem"), "/keys/tracker-token"}
	if got := settingFiles(r); !slices.Equal(got, want) {
		t.Errorf("settingFiles %q, want %q", got, want)
	}
	if got := settingFiles(nil); got != nil {
		t.Errorf("no runner file: %q", got)
	}
	state := "/state/qory"
	own := runnerFiles("", state, filepath.Join(state, "runs", "app-0123456789ab"), want)
	if !slices.Equal(own.files, append([]string{state}, want...)) {
		t.Errorf("files %q", own.files)
	}
}

// TestRunnerFilesPutsTheStateDirAfterTheConfigDir is the runner's files qory passes:
// the configuration directory, qory's state directory right after it, the links on the
// way to the run records, then where the configuration's links lead; with no
// configuration directory, the state directory and its links. The links on the way to
// the records are marked as the records', and each place shows as its link.
func TestRunnerFilesPutsTheStateDirAfterTheConfigDir(t *testing.T) {
	resolve := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	dir, other := resolve(t.TempDir()), resolve(t.TempDir())
	target := filepath.Join(other, "runner.yaml")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "runner.yaml")); err != nil {
		t.Fatal(err)
	}
	state := "/state/qory"
	runs := filepath.Join(state, "runs", "app-0123456789ab")
	if own := runnerFiles(dir, state, runs, nil); !slices.Equal(own.files, []string{dir, state, target}) || len(own.records) != 0 {
		t.Errorf("files %q, records %v", own.files, own.records)
	}
	if own := runnerFiles("", state, runs, nil); !slices.Equal(own.files, []string{state}) || len(own.links) != 0 {
		t.Errorf("no configuration directory: %q, %v", own.files, own.links)
	}
	base, real := resolve(t.TempDir()), resolve(t.TempDir())
	link := filepath.Join(base, "state")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	state = filepath.Join(link, "qory")
	runs = filepath.Join(state, "runs", "app-0123456789ab")
	if err := os.MkdirAll(filepath.Join(real, "qory", "runs", "app-0123456789ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	place := linkPlace(link)
	own := runnerFiles(dir, state, runs, nil)
	if !slices.Equal(own.files, []string{dir, state, place, target}) || !own.records[place] || own.records[target] || own.links[place] != link {
		t.Errorf("a linked state directory: files %q, records %v, links %v", own.files, own.records, own.links)
	}
}

// TestRecordLinksGuardTheLinksOnTheWay is recordLinks for a state directory whose path
// holds a link: the state home itself a link, a parent of it a link, and the runs
// directory a link out of the state directory. Each link on the way outside the state
// directory goes as its place, a mount of whose directory the runner refuses; a link
// inside it does not, and where it leads goes as it is. Without a link, nothing goes.
func TestRecordLinksGuardTheLinksOnTheWay(t *testing.T) {
	resolve := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	mkdir := func(d string) {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const folder = "app-0123456789ab"
	// The state home a link.
	base, real := resolve(t.TempDir()), resolve(t.TempDir())
	mkdir(filepath.Join(real, "qory", "runs", folder))
	home := filepath.Join(base, "state")
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	// A parent of it a link.
	above, realAbove := resolve(t.TempDir()), resolve(t.TempDir())
	mkdir(filepath.Join(realAbove, ".local", "state", "qory", "runs", folder))
	user := filepath.Join(above, "user")
	if err := os.Symlink(realAbove, user); err != nil {
		t.Fatal(err)
	}
	// The runs directory a link out of the state directory.
	plain, out := resolve(t.TempDir()), resolve(t.TempDir())
	mkdir(filepath.Join(plain, "qory"))
	mkdir(filepath.Join(out, folder))
	if err := os.Symlink(out, filepath.Join(plain, "qory", "runs")); err != nil {
		t.Fatal(err)
	}
	none := resolve(t.TempDir())
	mkdir(filepath.Join(none, "qory", "runs", folder))
	for _, c := range []struct {
		name, state, mount string
		want               []string
		link               string
	}{
		{"the state home a link", filepath.Join(home, "qory"), base, []string{linkPlace(home)}, home},
		{"a parent a link", filepath.Join(user, ".local", "state", "qory"), above, []string{linkPlace(user)}, user},
		{"the runs directory a link", filepath.Join(plain, "qory"), out, []string{filepath.Join(out, folder)}, ""},
		{"no link", filepath.Join(none, "qory"), "", nil, ""},
	} {
		files, shown := recordLinks(c.state, filepath.Join(c.state, "runs", folder))
		if !slices.Equal(files, c.want) {
			t.Errorf("%s: files %q, want %q", c.name, files, c.want)
		}
		if c.link != "" && shown[c.want[0]] != c.link {
			t.Errorf("%s: shown %v", c.name, shown)
		}
		if c.mount != "" {
			if how := session.Overlap(c.mount, c.want[0]); how == "" {
				t.Errorf("%s: a mount of %s does not hold %s", c.name, c.mount, c.want[0])
			}
		}
	}
}

// TestConfigLinksGuardsTheLinksOnTheWay is configLinks over a configuration directory
// whose descriptor is the first of two links: the link on the way goes as its place, a
// mount of whose directory the runner refuses and one beside it in that directory it
// does not; where the chain leads goes as it is; a descriptor whose chain loops is
// passed as it is, so the runner cannot resolve it and refuses the run; and a link in
// runtimes/ that is not a descriptor is not followed, while one whose .yaml is in upper
// case is, as a disk that ignores case opens it. A configuration directory that is a
// link goes as the link's place, though it holds none of the files yet, as does a link
// on the way to one that does not exist yet.
func TestConfigLinksGuardsTheLinksOnTheWay(t *testing.T) {
	resolve := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	dir, hop, last := resolve(t.TempDir()), resolve(t.TempDir()), resolve(t.TempDir())
	runtimes := filepath.Join(dir, DescriptorsDir)
	beside := filepath.Join(hop, "beside")
	for _, d := range []string{runtimes, beside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"goose.yaml", "codex.yaml"} {
		if err := os.WriteFile(filepath.Join(last, name), []byte("name: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range [][2]string{
		{filepath.Join(last, "goose.yaml"), filepath.Join(hop, "goose.yaml")},
		{filepath.Join(hop, "goose.yaml"), filepath.Join(runtimes, "goose.yaml")},
		{"loop.yaml", filepath.Join(runtimes, "loop.yaml")},
		{"notes", filepath.Join(runtimes, "notes")},
		{filepath.Join(last, "codex.yaml"), filepath.Join(runtimes, "Codex.YAML")},
	} {
		if err := os.Symlink(l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	files, shown := configLinks(dir)
	place := linkPlace(filepath.Join(hop, "goose.yaml"))
	if want := []string{filepath.Join(last, "codex.yaml"), place, filepath.Join(last, "goose.yaml"), filepath.Join(runtimes, "loop.yaml")}; !slices.Equal(files, want) {
		t.Errorf("files %q, want %q", files, want)
	}
	if got := shown[place]; got != filepath.Join(hop, "goose.yaml") {
		t.Errorf("the place %s shows %q", place, got)
	}
	if how := session.Overlap(hop, place); how != "contains" {
		t.Errorf("a mount of the link's directory %q the link", how)
	}
	if how := session.Overlap(beside, place); how != "" {
		t.Errorf("a mount beside the link %q it", how)
	}
	// A configuration directory that is a link, with none of the files in it yet: the
	// link is guarded all the same.
	linked := filepath.Join(resolve(t.TempDir()), "qory")
	if err := os.Symlink(resolve(t.TempDir()), linked); err != nil {
		t.Fatal(err)
	}
	if files, _ := configLinks(linked); !slices.Equal(files, []string{linkPlace(linked)}) {
		t.Errorf("a linked directory: files %q", files)
	}
	// A configuration directory that does not exist yet: a dangling link to it, and a
	// link above it to a directory without it, are guarded all the same, a mount of the
	// directory that holds either refused.
	above := resolve(t.TempDir())
	dangling := filepath.Join(above, "qory")
	if err := os.Symlink(filepath.Join(resolve(t.TempDir()), "missing", "qory"), dangling); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(above, ".config")
	if err := os.Symlink(resolve(t.TempDir()), config); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, link string }{
		{dangling, dangling},
		{filepath.Join(config, "qory"), config},
	} {
		files, shown := configLinks(c.dir)
		if want := []string{linkPlace(c.link)}; !slices.Equal(files, want) || shown[want[0]] != c.link {
			t.Errorf("%s: files %q, shown %q, want %q", c.dir, files, shown, want)
		}
		if how := session.Overlap(above, linkPlace(c.link)); how != "contains" {
			t.Errorf("%s: a mount of %s %q the link", c.dir, above, how)
		}
	}
	for _, name := range []string{"goose.yaml", "-a", "]a", "^a", `\a`, "[a", "a*b?c[d]"} {
		pattern := filepath.Base(linkPlace(filepath.Join(hop, name)))
		if ok, err := filepath.Match(pattern, name); !ok || err != nil || pattern == name || !strings.Contains(pattern, "[") {
			t.Errorf("%q as %q: %v, %v", name, pattern, ok, err)
		}
		if ok, _ := filepath.Match(pattern, name+"x"); ok {
			t.Errorf("%q matches %q", pattern, name+"x")
		}
	}
}

// TestUserHomeNeedsAConfigurationDirectory is a walled run whose harness.home a
// checkout's qory.yaml sets, on a machine with neither HOME nor XDG_CONFIG_HOME: there
// is no configuration directory whose file could say where the home goes, so the run is
// refused as the run records are with no state directory. A home no file sets passes.
func TestUserHomeNeedsAConfigurationDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("apiVersion: qory.dev/v1alpha1\nharness:\n  home: "+t.TempDir()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if config.UserDir() != "" {
		t.Skip("this system has a home directory without HOME")
	}
	conf, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	err = userHome(places{root: root}, conf)
	if want := "no configuration directory: set HOME or XDG_CONFIG_HOME"; err == nil || err.Error() != want || ExitCode(err) != ExitCode(errors.New("")) {
		t.Errorf("no configuration directory: %v (exit %d), want %q", err, ExitCode(err), want)
	}
	if err := userHome(places{root: root}, config.Defaults()); err != nil {
		t.Errorf("a home no file sets: %v", err)
	}
}

// TestEngineUnreachableIsWorded is the runner's engine_unreachable, a walled run that
// cannot ask the container engine whether an earlier walled run is still going, with
// that run's id its one name: the text leaves the id out. With the path of that run's
// registry entry as a second name, the text gives the docker ps command that lists that
// run's containers, and says to delete the entry when it lists none or that Docker is
// gone for good. Any other number of names falls through to the runner's own words.
func TestEngineUnreachableIsWorded(t *testing.T) {
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	p := passed{spec: &session.Spec{}}
	err := mountRefused(&session.Refusal{Code: "engine_unreachable", Names: []string{id}}, p)
	if want := "Docker could not be asked whether an earlier walled run is still going, so the run does not start (engine_unreachable)"; err == nil || err.Error() != want {
		t.Errorf("engine_unreachable: %v, want %q", err, want)
	}
	entry := "/var/state/qory/walled/" + id + ".json"
	err = mountRefused(&session.Refusal{Code: "engine_unreachable", Names: []string{id, entry}}, p)
	if want := "Docker could not be asked whether an earlier walled run is still going, so the run does not start. If docker ps --all --filter label=dev.qory.run=" + id + " lists no container, or that Docker is gone for good, delete " + entry + " (engine_unreachable)"; err == nil || err.Error() != want {
		t.Errorf("engine_unreachable with the entry: %v, want %q", err, want)
	}
	for _, names := range [][]string{nil, {id, entry, entry}} {
		if err := mountRefused(&session.Refusal{Code: "engine_unreachable", Names: names}, p); err != nil {
			t.Errorf("engine_unreachable with names %v: %v, want it to fall through", names, err)
		}
	}
}
