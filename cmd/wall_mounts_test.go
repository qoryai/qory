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
	"github.com/qoryai/qory/internal/realpath"
)

// walledSection writes forager.yaml with a wall whose docker is the fake one, logging a
// line each time it starts a container, and returns the log.
func walledSection(t *testing.T) string {
	t.Helper()
	docker, log := fakeDocker(t)
	wrapper := filepath.Join(t.TempDir(), "docker")
	writeFile(t, wrapper, "#!/bin/sh\ncase \"$1 $2\" in \"run \"*|\"start --attach\") echo \"container started\" >> "+log+" ;; esac\nexec "+docker+" \"$@\"\n")
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+wrapper+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	return log
}

// mountLine is the line of a walled run whose read-write mount at path relates to the
// home folder or qory's state directory by relation: "is your home folder", or
// "contains your home folder" and its path, and the same for "qory's state directory".
func mountLine(path, relation string) string {
	return "make the mount " + path + " read-only, with :ro on --mount or in wall.mounts: it " + relation + ", and a walled run may change any file in a read-write mount"
}

// TestAWalledRunWithAMountOfTheHomeFolderStopsFirst runs behind the fake wall with a
// read-write mount of the home folder: it exits 2 with the line, before any container
// starts and before any run is recorded. A read-only mount of the home folder passes
// that check; Forager then stops the run with mount_contains_forager_files. A read-write
// mount of a repository under the home folder runs.
func TestAWalledRunWithAMountOfTheHomeFolderStopsFirst(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	log := walledSection(t)
	home := tempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("DOCKER_CONFIG", "")
	repo := filepath.Join(home, "app")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "--quiet")
	out, err := run(t, "run", "claude", "--mount", home, "--", "-p", "hi")
	if err == nil || err.Error() != mountLine(home, "is your home folder") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if data, err := os.ReadFile(log); err == nil && strings.Contains(string(data), "container started") {
		t.Errorf("a container started:\n%s", data)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("the stopped run left records %v", ids)
	}
	if out, err := run(t, "run", "claude", "--mount", home+":ro", "--", "-p", "hi"); err == nil || cmd.ExitCode(err) != 1 || !strings.Contains(err.Error(), "(mount_contains_forager_files)") {
		t.Fatalf("a read-only mount of the home folder: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if out, err := run(t, "run", "claude", "--mount", repo+":ro", "--", "-p", "hi"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a read-only mount of a repository under the home folder: %v\n%s", err, out)
	}
}

// TestReservedMountsStopsAtTheHomeAndStateFolders checks the read-write mounts a walled
// run stops at: the home folder, an ancestor of it, the root, a link to the home folder,
// qory's state directory and an ancestor of it. A folder under the home folder, a folder
// inside the state directory, a read-only mount of any of them, and the checkout's
// mount of any of them pass.
func TestReservedMountsStopsAtTheHomeAndStateFolders(t *testing.T) {
	emptyDir(t)
	base := tempDir(t)
	home := filepath.Join(base, "users", "me")
	stateHome := filepath.Join(base, "state")
	state := filepath.Join(stateHome, "qory")
	link := filepath.Join(base, "link")
	app := filepath.Join(home, "app")
	inside := filepath.Join(state, "pool", "run")
	for _, d := range []string{app, inside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	for _, c := range []struct{ path, want string }{
		{home, mountLine(home, "is your home folder")},
		{filepath.Dir(home), mountLine(filepath.Dir(home), "contains your home folder "+home)},
		{"/", mountLine("/", "contains your home folder "+home)},
		{link, mountLine(link, "is your home folder")},
		{state, mountLine(state, "is qory's state directory")},
		{stateHome, mountLine(stateHome, "contains qory's state directory "+state)},
		{app, ""},
		{inside, ""},
	} {
		err := cmd.ReservedMounts(app, []wall.Mount{{Path: app}, {Path: c.path}})
		switch {
		case c.want == "" && err != nil:
			t.Errorf("mount %s: %v", c.path, err)
		case c.want != "" && (err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput):
			t.Errorf("mount %s: %v (exit %d)\nwant %s", c.path, err, cmd.ExitCode(err), c.want)
		}
		if err := cmd.ReservedMounts(app, []wall.Mount{{Path: app}, {Path: c.path, ReadOnly: true}}); err != nil {
			t.Errorf("read-only mount %s: %v", c.path, err)
		}
		if err := cmd.ReservedMounts(c.path, []wall.Mount{{Path: c.path}}); err != nil {
			t.Errorf("checkout %s: %v", c.path, err)
		}
	}
}

// TestReservedMountsResolvesALinkAboveAStateDirectoryNotThereYet sets XDG_STATE_HOME to
// a folder below a link to a mount, before the state directory exists: the mount
// contains it where the link leads, and the run stops.
func TestReservedMountsResolvesALinkAboveAStateDirectoryNotThereYet(t *testing.T) {
	emptyDir(t)
	mount := tempDir(t)
	link := filepath.Join(tempDir(t), "link")
	if err := os.Symlink(mount, link); err != nil {
		t.Fatal(err)
	}
	stateHome := filepath.Join(link, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	want := mountLine(mount, "contains qory's state directory "+filepath.Join(stateHome, "qory"))
	if err := cmd.ReservedMounts("", []wall.Mount{{Path: mount}}); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d)\nwant %s", err, cmd.ExitCode(err), want)
	}
}

// TestReservedMountsComparesALinkInsideAsWritten sets HOME, and then XDG_STATE_HOME, to
// a link inside a read-write mount that leads out of it: the folder lies in the mount as
// written, and the run stops.
func TestReservedMountsComparesALinkInsideAsWritten(t *testing.T) {
	emptyDir(t)
	base := tempDir(t)
	mnt, out := filepath.Join(base, "mnt"), filepath.Join(base, "out")
	for _, d := range []string{mnt, out} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(out, filepath.Join(mnt, "link")); err != nil {
		t.Fatal(err)
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	for _, c := range []struct{ home, state, want string }{
		{filepath.Join(mnt, "link"), stateHome, mountLine(mnt, "contains your home folder "+filepath.Join(mnt, "link"))},
		{out, filepath.Join(mnt, "link"), mountLine(mnt, "contains qory's state directory "+filepath.Join(mnt, "link", "qory"))},
	} {
		t.Setenv("HOME", c.home)
		t.Setenv("XDG_STATE_HOME", c.state)
		if err := cmd.ReservedMounts("", []wall.Mount{{Path: mnt}}); err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("HOME=%s XDG_STATE_HOME=%s: %v (exit %d)\nwant %s", c.home, c.state, err, cmd.ExitCode(err), c.want)
		}
	}
}

// TestReservedMountsSkipsARelativeHome sets HOME to a relative path: it is not compared,
// so a mount that contains the folder the path leads to from the working folder passes.
func TestReservedMountsSkipsARelativeHome(t *testing.T) {
	dir := emptyDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "rel", "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempDir(t), "state"))
	t.Setenv("HOME", filepath.Join("rel", "home"))
	if err := cmd.ReservedMounts("", []wall.Mount{{Path: dir}}); err != nil {
		t.Error(err)
	}
}

// writtenOf runs qory run with args behind the fake wall, the harness's Claude Code
// settings in its home, and returns the SessionSpec.Written and the Args of its one
// session, with its output. The run must reach the container: exit 4.
func writtenOf(t *testing.T, root string, args ...string) (written, launch []string, out string) {
	t.Helper()
	var specs []session.SessionSpec
	defer cmd.SetJoinSession(func(s session.SessionSpec) { specs = append(specs, s) })()
	out, err := run(t, append([]string{"run", "claude", "--model", "a-model-of-the-settings"}, args...)...)
	if cmd.ExitCode(err) != 4 || len(specs) != 1 {
		t.Fatalf("%d sessions, %v (exit %d)\n%s", len(specs), err, cmd.ExitCode(err), out)
	}
	return specs[0].Written, specs[0].Args, out
}

// realSet is each path made real, sorted, each once.
func realSet(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, realpath.Real(p))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestAWalledRunIsGivenItsPlaces runs walled with a read-only mount: SessionSpec.Written
// contains the run's copy and the folder of the runs' copies, and nothing else; the
// read-only mount and the person's checkout are none. Forager adds the writable binds of the other walled
// runs it lists and, behind the wall, this run's writable binds itself, outside
// SessionSpec.Written. The harness's Claude Code settings go to the session once,
// inline, as their JSON.
func TestAWalledRunIsGivenItsPlaces(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	walledSection(t)
	readOnly := tempDir(t)
	written, args, out := writtenOf(t, root, "--mount", readOnly+":ro", "--", "-p", "hi")
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(cache, "qory", "runs")
	if len(written) != 2 || !slices.Contains(written, runs) {
		t.Errorf("SessionSpec.Written %q, want one copy and %s", written, runs)
	}
	for _, place := range written {
		if strings.Contains(realpath.Real(place), realpath.Real(root)) || strings.Contains(realpath.Real(place), realpath.Real(readOnly)) {
			t.Errorf("SessionSpec.Written has %s, of the person's folders", place)
		}
	}
	wants(t, out, "inside the container")
	settings := 0
	for i, a := range args {
		if a == "--settings" {
			settings++
			if i+1 >= len(args) || !strings.HasPrefix(args[i+1], "{") || !strings.Contains(args[i+1], "a-model-of-the-settings") {
				t.Errorf("--settings is not the settings' JSON: %q", args)
			}
		}
	}
	if settings != 1 {
		t.Errorf("the session got --settings %d times: %q", settings, args)
	}
}
