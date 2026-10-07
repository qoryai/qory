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
// a mount of another of the runner's files, or of a link qory passed as its place, which
// is named as the link even when the link is the runner's directory; and any other
// error is left to the rest. The checkout root and the working directory are the
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
	// link, not the key, which the agent cannot reach through it.
	place := linkPlace(dir)
	if got, want := mountRefused(refusal(parent, place), passed{runnerDir: dir, spec: &session.Spec{}, links: map[string]string{place: dir}}).Error(), "the mount "+parent+" contains "+dir+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("a link's place: %q, want %q", got, want)
	}
	other := filepath.Join(dir, "wall-files")
	if got, want := mountRefused(refusal(dir, other), at(filepath.Join(dir, "elsewhere"))).Error(), "the mount "+dir+" contains "+other+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("another file: %q, want %q", got, want)
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
// directory and of qory's state directory, for a mount and for the workspace: the agent
// could change the run records.
func TestMountRefusedNamesTheRunRecords(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".local", "state", "qory")
	runs := filepath.Join(state, "runs", "app-0123456789ab")
	root := filepath.Join(home, "app")
	p := passed{stateDir: state, root: root, spec: &session.Spec{RunsDir: runs, Mounts: []wall.Mount{{Path: root}}, Dir: root}}
	for _, c := range []struct{ mount, path, want string }{
		{home, state, "the mount " + home + " contains " + state},
		{filepath.Join(state, "runs"), runs, "the mount " + filepath.Join(state, "runs") + " contains " + runs},
		{runs, runs, "the mount " + runs + " is " + runs},
		{filepath.Join(runs, "x"), runs, "the mount " + filepath.Join(runs, "x") + " lies inside " + runs},
		{root, state, "the workspace " + root + " overlaps " + state},
		{root, runs, "the workspace " + root + " overlaps " + runs},
	} {
		err := mountRefused(&session.Refusal{Code: "mount_contains_runner_files", Names: []string{c.mount, c.path}}, p)
		want := c.want + ", which holds qory's run records; the agent could change them, so the run does not start. Mount a narrower path (mount_contains_runner_files)"
		if err == nil || err.Error() != want {
			t.Errorf("%s and %s: %v, want %q", c.mount, c.path, err, want)
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
// workspace, writable, inside a read-only mount, and one path passed with both modes.
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
	if err := mountRefused(&session.Refusal{Code: "mount_mode_conflict", Names: []string{"/elsewhere/a", "/elsewhere"}}, p); err != nil {
		t.Errorf("paths the run did not pass: %v", err)
	}
}

// TestMountRefusedNamesTheRunStillGoing is mount_shared_with_run, for a mount and for
// the workspace, each way the paths stand: a git worktree, whose .git is a file, is
// told where to make one; a checkout of its own, whose .git is a directory, is not.
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
	tail := ", which the run " + other + ", still going on this machine, can write: one agent could change what the other mounts, so the run does not start. Wait for " + other + " to end, or work in a checkout of its own"
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
	} {
		p := passed{root: c.root, spec: &session.Spec{Mounts: []wall.Mount{{Path: c.root}, {Path: c.path}}, Dir: c.root}}
		err := mountRefused(&session.Refusal{Code: "mount_shared_with_run", Names: []string{c.path, other, c.otherPath}}, p)
		want := c.want + " (mount_shared_with_run)"
		if err == nil || err.Error() != want {
			t.Errorf("%s and %s: %v, want %q", c.path, c.otherPath, err, want)
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

// TestRunnerFilesPutsTheStateDirAfterTheConfigDir is the runner's files qory passes:
// the configuration directory, qory's state directory right after it, then where the
// configuration's links lead; with no configuration directory, the state directory.
func TestRunnerFilesPutsTheStateDirAfterTheConfigDir(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(other, "runner.yaml")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "runner.yaml")); err != nil {
		t.Fatal(err)
	}
	state := "/state/qory"
	if files, _ := runnerFiles(dir, state); !slices.Equal(files, []string{dir, state, target}) {
		t.Errorf("files %q", files)
	}
	if files, links := runnerFiles("", state); !slices.Equal(files, []string{state}) || links != nil {
		t.Errorf("no configuration directory: %q, %v", files, links)
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
