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
	"github.com/qoryai/qory/internal/places"
)

// refusalOf is the approved refusal of key in the repository at dir.
func refusalOf(dir, key string) string {
	return "the git configuration of " + dir + " sets " + key + ", which qory does not write; qory runs no git there, since a run could have set it to make git on this machine run a program"
}

// recordCheckout records root as the checkout of a walled run, the way qory run does
// before its session: its git settings, its qory files and its harness.
func recordCheckout(t *testing.T, root string) {
	t.Helper()
	if err := cmd.RecordCheckout(root); err != nil {
		t.Fatal(err)
	}
}

// walledSection writes forager.yaml with a wall whose docker is the fake one, logging
// the records of the places each time it starts a container, and returns the log.
func walledSection(t *testing.T) string {
	t.Helper()
	docker, log := fakeDocker(t)
	placesDir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "places")
	wrapper := filepath.Join(t.TempDir(), "docker")
	writeFile(t, wrapper, "#!/bin/sh\ncase \"$1\" in run) echo \"places: $(ls "+placesDir+" 2>/dev/null | tr '\\n' ' ')\" >> "+log+" ;; esac\nexec "+docker+" \"$@\"\n")
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+wrapper+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	return log
}

// TestAWalledRunRecordsItsPlacesBeforeTheSession runs behind the fake wall with a
// read-write mount that is a repository and a read-only one: the checkout and the
// read-write mount are recorded before the first container starts, each with its
// repository's fingerprint and the run's id; the read-only mount is not recorded. A
// second run keeps the records as they were.
func TestAWalledRunRecordsItsPlacesBeforeTheSession(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	log := walledSection(t)
	mounted := tempDir(t)
	runGit(t, mounted, "init", "--quiet")
	readOnly := tempDir(t)
	if out, err := run(t, "run", "claude", "--mount", mounted, "--mount", readOnly+":ro", "--", "-p", "hi"); cmd.ExitCode(err) != 4 {
		t.Fatalf("%v\n%s", err, out)
	}
	dir, _ := events(t, root)
	runID := filepath.Base(dir)
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	first := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "places: ") {
			first = line
			break
		}
	}
	wants(t, first, places.Name(root), places.Name(mounted))
	lacks(t, first, places.Name(readOnly))
	checkout, err := places.Get(root)
	if err != nil || checkout == nil || checkout.Kind != places.KindCheckout || checkout.Run != runID || len(checkout.Git) != 1 || checkout.Git[0].Worktree != root {
		t.Fatalf("the checkout's record %+v, %v", checkout, err)
	}
	mount, err := places.Get(mounted)
	if err != nil || mount == nil || mount.Kind != places.KindMount || mount.Run != runID || len(mount.Git) != 1 {
		t.Fatalf("the mount's record %+v, %v", mount, err)
	}
	if r, err := places.Get(readOnly); err != nil || r != nil {
		t.Errorf("the read-only mount is recorded: %+v, %v", r, err)
	}
	record := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "places", places.Name(root))
	before, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(before), "tester@example.com", "Tester", "git.example.com")
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); cmd.ExitCode(err) != 4 {
		t.Fatalf("%v\n%s", err, out)
	}
	if after, err := os.ReadFile(record); err != nil || string(after) != string(before) {
		t.Errorf("a later run changed the record:\n%s", after)
	}
}

// homeLine is the line of a walled run whose checkout at root relates to the home folder
// by relation: "is your home folder", or "contains your home folder" and its path.
func homeLine(root, relation string) string {
	return "start the walled run from a repository folder: its checkout " + root + " " + relation + ", and a walled run may change any file in its checkout, so qory would stop running git in every repository under the checkout"
}

// stateLine is the line of a walled run whose checkout at root relates to qory's state
// directory by relation: "is qory's state directory", or "contains qory's state
// directory" and its path.
func stateLine(root, relation string) string {
	return "start the walled run from a repository folder: its checkout " + root + " " + relation + ", and a walled run may change any file in its checkout, the record of the places walled runs used among them"
}

// TestAWalledRunFromTheHomeFolderStopsFirst starts a walled run in a home folder that is
// a git checkout: it exits 2 with the line, before any container starts and before any
// place or run is recorded, and a repository under the home folder composes afterwards.
func TestAWalledRunFromTheHomeFolderStopsFirst(t *testing.T) {
	repo := newCheckout(t)
	copyFixture(t, "two-modules", repo)
	home := tempDir(t)
	t.Setenv("HOME", home)
	runGit(t, home, "init", "--quiet")
	copyFixture(t, "two-modules", home)
	composedForFake(t, repo, "claude")
	log := walledSection(t)
	t.Chdir(home)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil || err.Error() != homeLine(home, "is your home folder") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if data, err := os.ReadFile(log); err == nil && strings.Contains(string(data), "places: ") {
		t.Errorf("a container started:\n%s", data)
	}
	if recs, err := places.Load(); err != nil || len(recs) != 0 {
		t.Errorf("the stopped run left the records of places %+v, %v", recs, err)
	}
	if ids := recorded(t, home); len(ids) != 0 {
		t.Errorf("the stopped run left records %v", ids)
	}
	t.Chdir(repo)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Errorf("a repository under the home folder: %v\n%s", err, out)
	}
}

// TestAWalledRunCodeFromTheHomeFolderStopsFirst starts a walled run from a run code in a
// home folder that is the checkout: it exits 2 with the line, and the code is not sent.
func TestAWalledRunCodeFromTheHomeFolderStopsFirst(t *testing.T) {
	root := runKeyCheckout(t)
	walledSection(t)
	// harness.home puts the composed harness outside the checkout. Its default, under the
	// cache directory, is under HOME on macOS, and the check of a home inside the folder
	// the run runs in would stop the run first.
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	data, err := os.ReadFile(user)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, user, string(data)+"  home: "+filepath.Join(tempDir(t), "homes")+"\n")
	t.Setenv("HOME", root)
	g := newCodeRelay(t)
	file := codeFile(t, g.code())
	out, err := run(t, "run", "--run-code-file", file, "--gateway", g.URL)
	if err == nil || err.Error() != homeLine(root, "is your home folder") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if n := len(g.sent()); n != 0 {
		t.Errorf("the stopped run sent the code %d times", n)
	}
}

// TestAWalledRunUnderTheHomeFolderRuns starts a walled run in a repository under the
// home folder: the run starts, and its checkout is recorded.
func TestAWalledRunUnderTheHomeFolderRuns(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	t.Setenv("HOME", filepath.Dir(root))
	composedForFake(t, root, "claude")
	walledSection(t)
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); cmd.ExitCode(err) != 4 {
		t.Fatalf("%v\n%s", err, out)
	}
	if r, err := places.Get(root); err != nil || r == nil || r.Kind != places.KindCheckout {
		t.Errorf("the checkout's record %+v, %v", r, err)
	}
}

// mountHomeLine is the line of a walled run whose read-write mount at path relates to
// the home folder by relation: "is your home folder", or "contains your home folder" and
// its path.
func mountHomeLine(path, relation string) string {
	return "mount a narrower folder: the mount " + path + " " + relation + ", and a walled run may change any file in a read-write mount, so qory would stop running git in every repository under the mount"
}

// mountStateLine is the line of a walled run whose read-write mount at path relates to
// qory's state directory by relation: "is qory's state directory", or "contains qory's
// state directory" and its path.
func mountStateLine(path, relation string) string {
	return "mount a narrower folder: the mount " + path + " " + relation + ", and a walled run may change any file in a read-write mount, the record of the places walled runs used among them"
}

// TestAWalledRunWithAMountOfTheHomeFolderStopsFirst runs behind the fake wall with a
// read-write mount of the home folder: it exits 2 with the line, before any container
// starts and before any place or run is recorded. A read-only mount of the home folder
// passes that check, and is not recorded; Forager then stops the run with
// mount_contains_forager_files. A read-write mount of a repository under the home folder
// runs, and is recorded.
func TestAWalledRunWithAMountOfTheHomeFolderStopsFirst(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	log := walledSection(t)
	home := tempDir(t)
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "app")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "--quiet")
	out, err := run(t, "run", "claude", "--mount", home, "--", "-p", "hi")
	if err == nil || err.Error() != mountHomeLine(home, "is your home folder") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if data, err := os.ReadFile(log); err == nil && strings.Contains(string(data), "places: ") {
		t.Errorf("a container started:\n%s", data)
	}
	if recs, err := places.Load(); err != nil || len(recs) != 0 {
		t.Errorf("the stopped run left the records of places %+v, %v", recs, err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("the stopped run left records %v", ids)
	}
	if out, err := run(t, "run", "claude", "--mount", home+":ro", "--", "-p", "hi"); err == nil || cmd.ExitCode(err) != 1 || !strings.Contains(err.Error(), "(mount_contains_forager_files)") {
		t.Fatalf("a read-only mount of the home folder: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if r, err := places.Get(home); err != nil || r != nil {
		t.Errorf("the read-only mount of the home folder is recorded: %+v, %v", r, err)
	}
	if out, err := run(t, "run", "claude", "--mount", repo, "--", "-p", "hi"); cmd.ExitCode(err) != 4 {
		t.Fatalf("a mount of a repository under the home folder: %v\n%s", err, out)
	}
	if r, err := places.Get(repo); err != nil || r == nil || r.Kind != places.KindMount {
		t.Errorf("the mount's record %+v, %v", r, err)
	}
}

// TestWalledPlacesStopsAtTheHomeAndStateFolders checks the checkouts and the read-write
// mounts a walled run stops at: the home folder, an ancestor of it, the root, a link to
// the home folder, qory's state directory and an ancestor of it. A folder under the home
// folder, a folder inside the state directory, and a read-only mount of the home folder
// or the state directory pass.
func TestWalledPlacesStopsAtTheHomeAndStateFolders(t *testing.T) {
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
	for _, c := range []struct {
		path            string
		checkout, mount string
	}{
		{home, homeLine(home, "is your home folder"), mountHomeLine(home, "is your home folder")},
		{filepath.Dir(home), homeLine(filepath.Dir(home), "contains your home folder "+home), mountHomeLine(filepath.Dir(home), "contains your home folder "+home)},
		{"/", homeLine("/", "contains your home folder "+home), mountHomeLine("/", "contains your home folder "+home)},
		{link, homeLine(link, "is your home folder"), mountHomeLine(link, "is your home folder")},
		{state, stateLine(state, "is qory's state directory"), mountStateLine(state, "is qory's state directory")},
		{stateHome, stateLine(stateHome, "contains qory's state directory "+state), mountStateLine(stateHome, "contains qory's state directory "+state)},
		{app, "", ""},
		{inside, "", ""},
	} {
		for _, k := range []struct {
			what, want string
			err        error
		}{
			{"checkout", c.checkout, cmd.WalledPlaces(c.path, nil)},
			{"mount", c.mount, cmd.WalledPlaces(app, []wall.Mount{{Path: app}, {Path: c.path}})},
			{"read-only mount", "", cmd.WalledPlaces(app, []wall.Mount{{Path: app}, {Path: c.path, ReadOnly: true}})},
		} {
			if k.want == "" {
				if k.err != nil {
					t.Errorf("%s %s: %v", k.what, c.path, k.err)
				}
				continue
			}
			if k.err == nil || k.err.Error() != k.want || cmd.ExitCode(k.err) != cmd.ExitInput {
				t.Errorf("%s %s: %v (exit %d)\nwant %s", k.what, c.path, k.err, cmd.ExitCode(k.err), k.want)
			}
		}
	}
}

// TestWalledPlacesResolvesALinkAboveAStateDirectoryNotThereYet sets XDG_STATE_HOME to a
// folder below a link to the checkout, before the state directory exists: the checkout
// contains it where the link leads, and the run stops.
func TestWalledPlacesResolvesALinkAboveAStateDirectoryNotThereYet(t *testing.T) {
	emptyDir(t)
	root := tempDir(t)
	link := filepath.Join(tempDir(t), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	stateHome := filepath.Join(link, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	want := stateLine(root, "contains qory's state directory "+filepath.Join(stateHome, "qory"))
	if err := cmd.WalledPlaces(root, nil); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d)\nwant %s", err, cmd.ExitCode(err), want)
	}
}

// TestWalledPlacesComparesALinkInsideAsWritten sets HOME, and then XDG_STATE_HOME, to a
// link inside the checkout, and then inside a read-write mount, that leads out of it:
// the folder lies in the place as written, and the run stops.
func TestWalledPlacesComparesALinkInsideAsWritten(t *testing.T) {
	emptyDir(t)
	base := tempDir(t)
	co, mnt, app, out := filepath.Join(base, "co"), filepath.Join(base, "mnt"), filepath.Join(base, "app"), filepath.Join(base, "out")
	for _, d := range []string{co, mnt, app, out} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{co, mnt} {
		if err := os.Symlink(out, filepath.Join(d, "link")); err != nil {
			t.Fatal(err)
		}
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	for _, c := range []struct {
		home, state, want string
		err               func() error
	}{
		{filepath.Join(co, "link"), stateHome, homeLine(co, "contains your home folder "+filepath.Join(co, "link")), func() error { return cmd.WalledPlaces(co, nil) }},
		{filepath.Join(mnt, "link"), stateHome, mountHomeLine(mnt, "contains your home folder "+filepath.Join(mnt, "link")), func() error { return cmd.WalledPlaces(app, []wall.Mount{{Path: app}, {Path: mnt}}) }},
		{out, filepath.Join(co, "link"), stateLine(co, "contains qory's state directory "+filepath.Join(co, "link", "qory")), func() error { return cmd.WalledPlaces(co, nil) }},
		{out, filepath.Join(mnt, "link"), mountStateLine(mnt, "contains qory's state directory "+filepath.Join(mnt, "link", "qory")), func() error { return cmd.WalledPlaces(app, []wall.Mount{{Path: app}, {Path: mnt}}) }},
	} {
		t.Setenv("HOME", c.home)
		t.Setenv("XDG_STATE_HOME", c.state)
		if err := c.err(); err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("HOME=%s XDG_STATE_HOME=%s: %v (exit %d)\nwant %s", c.home, c.state, err, cmd.ExitCode(err), c.want)
		}
	}
}

// TestWalledPlacesSkipsARelativeHome sets HOME to a relative path: it is not compared,
// so a checkout that contains the folder the path leads to from the working folder
// passes.
func TestWalledPlacesSkipsARelativeHome(t *testing.T) {
	dir := emptyDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "rel", "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(tempDir(t), "state"))
	t.Setenv("HOME", filepath.Join("rel", "home"))
	if err := cmd.WalledPlaces(dir, nil); err != nil {
		t.Error(err)
	}
}

// TestEveryVerbStopsAtTheRefusal records the checkout, adds a key since, and has every
// verb that runs git there stop with the approved line and exit 1 before it does
// anything: the run starts no session and leaves no record of a run.
func TestEveryVerbStopsAtTheRefusal(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "stack")
	recordCheckout(t, root)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("the checkout as it was recorded: %v\n%s", err, out)
	}
	runGit(t, root, "config", "alias.x", "log")
	for _, args := range [][]string{
		{"harness", "compose"},
		{"harness", "inspect"},
		{"harness", "remove"},
		{"config"},
		{"worktree", "list"},
		{"worktree", "add", "feature", "--offline"},
		{"run", "claude", "--local"},
	} {
		out, err := run(t, args...)
		if err == nil || err.Error() != refusalOf(root, "alias.x") || cmd.ExitCode(err) != 1 {
			t.Errorf("%v: %v (exit %d)\n%s", args, err, cmd.ExitCode(err), out)
		}
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("the refused run left records %v", ids)
	}
}

// TestWorktreeRemoveDropsTheWorktreesRecord removes a worktree a walled run used, and
// its record with it; the main checkout's record stays.
func TestWorktreeRemoveDropsTheWorktreesRecord(t *testing.T) {
	root := worktreeRepo(t)
	if out, err := run(t, "worktree", "add", "feature"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	recordCheckout(t, wt)
	recordCheckout(t, root)
	if out, err := run(t, "worktree", "remove", "feature", "--delete-branch"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r, err := places.Get(wt); err != nil || r != nil {
		t.Errorf("the worktree's record is still there: %+v, %v", r, err)
	}
	if r, err := places.Get(root); err != nil || r == nil {
		t.Errorf("the main checkout's record went: %v", err)
	}
}

// TestAcceptCheckout lists what changed since a walled run began in the checkout, asks
// at a terminal and takes the checkout as it is on yes; refuses without a terminal and
// while a walled run uses the place; and says when there is nothing to accept. An
// include into the place stays refused after an accept.
func TestAcceptCheckout(t *testing.T) {
	root := newCheckout(t)
	if _, err := run(t, "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "no walled run has used "+root+"; there is nothing to accept" {
		t.Errorf("an unrecorded checkout: %v", err)
	}
	recordCheckout(t, root)
	out, err := run(t, "setup", "repo", "--accept-checkout")
	if err != nil || out != "nothing changed since a walled run began here\n" {
		t.Errorf("nothing changed: %v\n%s", err, out)
	}
	runGit(t, root, "config", "alias.x", "log")
	runGit(t, root, "config", "user.email", "someone@example.com")
	list := "changes since a walled run began here:\n  user.email\n  alias.x\n"

	out, err = runIn(t, "y\n", "setup", "repo", "--accept-checkout")
	if err == nil || err.Error() != "qory setup repo --accept-checkout asks before it accepts, and needs a terminal" || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("no terminal: %v", err)
	}
	wants(t, out, list)
	lacks(t, out, "someone@example.com", "log")

	defer cmd.SetTerminal(true)()
	live, err := places.Starting([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runIn(t, "y\n", "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "a walled run is using "+root+"; accept once it has ended" {
		t.Errorf("a live run: %v", err)
	}
	live.Release()

	out, err = runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil || out != list+"accept them? [y/N] accepted nothing\n" {
		t.Errorf("no: %v\n%q", err, out)
	}
	if _, err := run(t, "config"); err == nil || err.Error() != refusalOf(root, "user.email") {
		t.Errorf("after no: %v", err)
	}
	out, err = runIn(t, "y\n", "setup", "repo", "--accept-checkout")
	if err != nil || out != list+"accept them? [y/N] accepted; qory takes the checkout as it is now\n" {
		t.Errorf("yes: %v\n%q", err, out)
	}
	if out, err := run(t, "config"); err != nil {
		t.Errorf("after yes: %v\n%s", err, out)
	}
	if r, err := places.Get(root); err != nil || r == nil || r.Accepted == nil {
		t.Errorf("the record does not say it was accepted: %+v, %v", r, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
		t.Errorf("the accept wrote into the checkout: %v", entries)
	}

	writeFile(t, filepath.Join(root, "team.gitconfig"), "[alias]\n\tlg = log\n")
	runGit(t, root, "config", "include.path", filepath.Join(root, "team.gitconfig"))
	if out, err := runIn(t, "y\n", "setup", "repo", "--accept-checkout"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := run(t, "config"); err == nil || err.Error() != refusalOf(root, "include.path") {
		t.Errorf("an include into the place after an accept: %v", err)
	}
}

// changingReader is stdin that runs change when it is first read, as the person answers,
// and then reads as answer.
type changingReader struct {
	change func()
	answer *strings.Reader
	done   bool
}

func (r *changingReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.change()
	}
	return r.answer.Read(p)
}

// TestAcceptCheckoutRefusesWhileAnyHoldingPlaceIsInUse records the checkout and a mount
// around it: a walled run that uses the mount, the second place that holds the
// checkout, stops the accept as one that uses the checkout does.
func TestAcceptCheckoutRefusesWhileAnyHoldingPlaceIsInUse(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	around := filepath.Dir(root)
	recordMount(t, around)
	runGit(t, root, "config", "alias.x", "log")
	defer cmd.SetTerminal(true)()
	live, err := places.Starting([]string{around})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Release()
	if out, err := runIn(t, "y\n", "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "a walled run is using "+around+"; accept once it has ended" {
		t.Errorf("a live run in the mount around: %v\n%s", err, out)
	}
	if r, err := places.Get(root); err != nil || r == nil || r.Accepted != nil {
		t.Errorf("the record was accepted: %+v, %v", r, err)
	}
}

// TestAcceptCheckoutRefusesWhileAPlaceOfItsFilesIsInUse holds, in turn, a recorded
// place that holds none of the checkout but a stack file its qory.yaml extends, and the
// recorded main checkout whose git directory holds the configuration of a worktree the
// accept runs in: a walled run that uses either stops the accept, and nothing is taken.
func TestAcceptCheckoutRefusesWhileAPlaceOfItsFilesIsInUse(t *testing.T) {
	root := newCheckout(t)
	stacks := tempDir(t)
	writeFile(t, filepath.Join(stacks, "qory-stack.yaml"), "apiVersion: qory.dev/v1alpha1\nname: base\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends:\n    path: "+stacks+"\n")
	recordCheckout(t, root)
	recordMount(t, stacks)
	// The worktree is recorded before the key is set: a place that shares a
	// configuration a run changed is not recorded.
	runGit(t, root, "commit", "--quiet", "--allow-empty", "-m", "first")
	worktree := filepath.Join(tempDir(t), "w")
	runGit(t, root, "worktree", "add", "--quiet", "-b", "w", worktree)
	recordCheckout(t, worktree)
	runGit(t, root, "config", "alias.x", "log")
	defer cmd.SetTerminal(true)()
	live, err := places.Starting([]string{stacks})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runIn(t, "y\n", "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "a walled run is using "+stacks+"; accept once it has ended" {
		t.Errorf("a live run in the place of the extended stack: %v\n%s", err, out)
	}
	live.Release()
	if r, err := places.Get(root); err != nil || r == nil || r.Accepted != nil {
		t.Errorf("the record was accepted: %+v, %v", r, err)
	}

	t.Chdir(worktree)
	live, err = places.Starting([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Release()
	if out, err := runIn(t, "y\n", "setup", "repo", "--accept-checkout"); err == nil || err.Error() != "a walled run is using "+root+"; accept once it has ended" {
		t.Errorf("a live run in the main checkout, whose git directory holds the configuration: %v\n%s", err, out)
	}
	if r, err := places.Get(worktree); err != nil || r == nil || r.Accepted != nil {
		t.Errorf("the worktree's record was accepted: %+v, %v", r, err)
	}
}

// TestAcceptCheckoutReadsTheRecordsAgainOnceAnswered records a mount around the checkout
// and starts a walled run in it while the accept asks: the accept reads the records
// again under their lock, finds the place in use, and takes nothing.
func TestAcceptCheckoutReadsTheRecordsAgainOnceAnswered(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	runGit(t, root, "config", "alias.x", "log")
	defer cmd.SetTerminal(true)()
	around := filepath.Dir(root)
	var live *places.Hold
	defer func() { live.Release() }()
	c := cmd.Root()
	var out strings.Builder
	c.SetArgs([]string{"setup", "repo", "--accept-checkout"})
	c.SetIn(&changingReader{change: func() {
		recordMount(t, around)
		var err error
		if live, err = places.Starting([]string{around}); err != nil {
			t.Fatal(err)
		}
	}, answer: strings.NewReader("y\n")})
	c.SetOut(&out)
	c.SetErr(&out)
	if err := c.Execute(); err == nil || err.Error() != "a walled run is using "+around+"; accept once it has ended" {
		t.Errorf("a walled run begun while the accept asked: %v\n%s", err, out.String())
	}
	if r, err := places.Get(root); err != nil || r == nil || r.Accepted != nil {
		t.Errorf("the record was accepted: %+v, %v", r, err)
	}
}

// TestAcceptCheckoutTakesOnlyWhatItShowed adds a git key while the accept asks, after
// the list was shown: it accepts nothing, says so, and the key stays refused.
func TestAcceptCheckoutTakesOnlyWhatItShowed(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	runGit(t, root, "config", "alias.x", "log")
	defer cmd.SetTerminal(true)()
	c := cmd.Root()
	var out strings.Builder
	c.SetArgs([]string{"setup", "repo", "--accept-checkout"})
	c.SetIn(&changingReader{change: func() { runGit(t, root, "config", "alias.y", "show") }, answer: strings.NewReader("y\n")})
	c.SetOut(&out)
	c.SetErr(&out)
	err := c.Execute()
	want := "the checkout changed while qory setup repo --accept-checkout asked, so it accepted nothing; run it again to see the changes"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q\n%s", err, want, out.String())
	}
	lacks(t, out.String(), "alias.y", "accepted;")
	if r, err := places.Get(root); err != nil || r == nil || r.Accepted != nil {
		t.Errorf("the record was accepted: %+v, %v", r, err)
	}
	if _, err := run(t, "config"); err == nil || err.Error() != refusalOf(root, "alias.x") {
		t.Errorf("after the refused accept: %v", err)
	}
}

// TestAcceptCheckoutListsAnIncludeItDoesNotTakeApart adds an include of a file in the
// checkout and another key: the include is listed apart as not accepted, the accept
// says so, and the include stays refused. An include alone is listed, and nothing is
// asked or accepted.
func TestAcceptCheckoutListsAnIncludeItDoesNotTakeApart(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	writeFile(t, filepath.Join(root, "team.gitconfig"), "[alias]\n\tlg = log\n")
	runGit(t, root, "config", "include.path", filepath.Join(root, "team.gitconfig"))
	defer cmd.SetTerminal(true)()
	apart := "not accepted, since each includes a file a walled run could have written, or names it by a relative path:\n  include.path\n"
	out, err := runIn(t, "y\n", "setup", "repo", "--accept-checkout")
	if err != nil || out != apart+"accepted nothing\n" {
		t.Errorf("an include alone: %v\n%q", err, out)
	}
	runGit(t, root, "config", "alias.x", "log")
	out, err = runIn(t, "y\n", "setup", "repo", "--accept-checkout")
	want := "changes since a walled run began here:\n  alias.x\n" + apart + "accept them? [y/N] accepted; qory takes the checkout as it is now, but for the includes it does not accept\n"
	if err != nil || out != want {
		t.Errorf("an include and a key: %v\n%q\nwant %q", err, out, want)
	}
	if _, err := run(t, "config"); err == nil || err.Error() != refusalOf(root, "include.path") {
		t.Errorf("the include after the accept: %v", err)
	}
}

// TestAcceptCheckoutNamesAVariableWithoutItsValue adds a variable and a command to the
// checkout's qory.yaml: the accept lists the command with its value and the variables
// by their names alone, and no value of a variable appears in what it prints.
func TestAcceptCheckoutNamesAVariableWithoutItsValue(t *testing.T) {
	root := newCheckout(t)
	file := filepath.Join(root, "qory.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n")
	recordCheckout(t, root)
	appendTo(t, file, "env:\n  TOKEN: a-value-of-the-variable\nworktree:\n  run:\n    add: [make setup]\nharness:\n  launch:\n    claude:\n      command: claude\n      env:\n        LOG: another-value-of-a-variable\n")
	defer cmd.SetTerminal(true)()
	out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "  qory.yaml: env.TOKEN\n", "  qory.yaml: worktree.run.add [\"make setup\"]\n")
	lacks(t, out, "a-value-of-the-variable", "another-value-of-a-variable")
}

// TestAcceptCheckoutHidesAVariableInAnyForm writes env, and a launch's env, as a list
// and then as a scalar: each is listed by its key alone, and no value appears in what
// the accept prints.
func TestAcceptCheckoutHidesAVariableInAnyForm(t *testing.T) {
	for _, form := range []struct{ name, env, launchEnv string }{
		{"a list", "env: [TOKEN=a-value-of-the-variable]\n", "      env: [LOG=another-value-of-a-variable]\n"},
		{"a scalar", "env: TOKEN=a-value-of-the-variable\n", "      env: LOG=another-value-of-a-variable\n"},
	} {
		t.Run(form.name, func(t *testing.T) {
			root := newCheckout(t)
			file := filepath.Join(root, "qory.yaml")
			writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n")
			recordCheckout(t, root)
			appendTo(t, file, form.env+"harness:\n  launch:\n    claude:\n      command: claude\n"+form.launchEnv)
			defer cmd.SetTerminal(true)()
			out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			wants(t, out, "  qory.yaml: env\n", "  qory.yaml: harness.launch.claude.env\n")
			lacks(t, out, "a-value-of-the-variable", "another-value-of-a-variable")
		})
	}
}

// TestAcceptCheckoutQuotesAnActingKey adds a variable whose name holds a mark that turns
// the text's direction: the accept lists its key quoted.
func TestAcceptCheckoutQuotesAnActingKey(t *testing.T) {
	root := newCheckout(t)
	file := filepath.Join(root, "qory.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n")
	recordCheckout(t, root)
	appendTo(t, file, "env:\n  \"A\\u202eB\": a-value-of-the-variable\n")
	defer cmd.SetTerminal(true)()
	out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "  qory.yaml: \"env.A\\u202eB\"\n")
	lacks(t, out, "\u202e", "a-value-of-the-variable")
}

// TestAcceptCheckoutMasksTheUserOfARepositoryAndOfAKey sets a stack's extends.git and a
// module's source.git to URLs that name a user and a password, and a git key whose
// subsection is such a URL: the accept lists each with the user and the password
// masked, and the settings check's refusal names the key masked too.
func TestAcceptCheckoutMasksTheUserOfARepositoryAndOfAKey(t *testing.T) {
	root := newCheckout(t)
	file := filepath.Join(root, "qory.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n")
	recordCheckout(t, root)
	appendTo(t, file, "harness:\n  extends:\n    git: https://a-user:a-password@git.example.com/acme/base.git\n    ref: main\n  modules:\n    - name: lib\n      source:\n        git: an-user@git.example.com:acme/lib.git\n        ref: main\n")
	runGit(t, root, "config", "url.https://a-user:a-password@git.example.com/.insteadOf", "https://git.example.com/")
	defer cmd.SetTerminal(true)()
	out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out,
		"  url.https://***@git.example.com/.insteadof\n",
		"  qory.yaml: harness.extends.git \"https://***@git.example.com/acme/base.git\"\n",
		"  qory.yaml: harness.modules[0].source.git \"***@git.example.com:acme/lib.git\"\n")
	lacks(t, out, "a-user", "a-password", "an-user")
	if _, err := run(t, "config"); err == nil || !strings.Contains(err.Error(), " sets url.https://***@git.example.com/.insteadof, which qory does not write") || strings.Contains(err.Error(), "a-password") {
		t.Errorf("the refusal: %v", err)
	}
}

// TestAcceptCheckoutShowsWhereALinkLeadsNow records a checkout whose qory.yaml extends a
// path through a link in the checkout that leads out of it, then points the link at
// another folder: the value is the same, but the change is refused and listed with
// where the path leads now.
func TestAcceptCheckoutShowsWhereALinkLeadsNow(t *testing.T) {
	root := newCheckout(t)
	first, other := tempDir(t), tempDir(t)
	link := filepath.Join(root, "base")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends:\n    path: ./base\n")
	recordCheckout(t, root)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	defer cmd.SetTerminal(true)()
	out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "changes since a walled run began here:\n  qory.yaml: harness.extends.path \"./base\", which leads to "+other+"\n")
}

// TestAcceptCheckoutShowsARemotesURLWithoutItsUser sets origin's URL to one that names
// a user and a password, and then to git's scp-like form with a user: the accept lists
// each by its value, the user and the password masked, and neither appears in what it
// prints.
func TestAcceptCheckoutShowsARemotesURLWithoutItsUser(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	defer cmd.SetTerminal(true)()
	runGit(t, root, "remote", "set-url", "origin", "https://a-user:a-password@git.example.com/acme/other.git")
	out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "changes since a walled run began here:\n  remote.origin.url https://***@git.example.com/acme/other.git\n")
	lacks(t, out, "a-user", "a-password")
	runGit(t, root, "remote", "set-url", "origin", "a-user@git.example.com:acme/other.git")
	out, err = runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "changes since a walled run began here:\n  remote.origin.url ***@git.example.com:acme/other.git\n")
	lacks(t, out, "a-user")
	// A user and a password of odd characters, each set to what the form lets through to
	// the host git takes, in each form of URL git takes; and URLs whose @ git does not take
	// for the end of a user, shown with the host git reaches.
	httpUser, httpPassword := "us3r:@%2F \x01é", "pa55:@%2F \x01ñ"
	sshUser, sshPassword := "us3r:@%40?# \x01é", "pa55:@%3A?# \x01ñ"
	scpUser := "us3r@%2F?# \x01é"
	for _, c := range []struct{ url, want string }{
		{"https://" + httpUser + ":" + httpPassword + "@git.example.com/acme/other.git", "https://***@git.example.com/acme/other.git"},
		{"https://" + httpUser + ":" + httpPassword + "@git.example.com:8443/acme/other.git", "https://***@git.example.com:8443/acme/other.git"},
		{"ssh://" + sshUser + ":" + sshPassword + "@git.example.com:2222/acme/other", "ssh://***@git.example.com:2222/acme/other"},
		{"git://" + sshUser + ":" + sshPassword + "@git.example.com/acme/other.git", "git://***@git.example.com/acme/other.git"},
		{"file://" + sshUser + ":" + sshPassword + "@localhost/srv/git/other.git", "file://***@localhost/srv/git/other.git"},
		{scpUser + "@git.example.com:acme/other.git", "***@git.example.com:acme/other.git"},
	} {
		runGit(t, root, "remote", "set-url", "origin", c.url)
		out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		wants(t, out, "  remote.origin.url "+c.want+"\n")
		// The row is the one above; the rest of what it prints is fixed text.
		lacks(t, out, "us3r", "pa55", "%2F", "%40", "%3A", "é", "ñ", "\x01", `\x01`)
	}
	for _, c := range []struct{ url, want string }{
		{"prod-box:x@github.com:acme/other.git", "prod-box:x@github.com:acme/other.git"},
		{"ssh://evil.example%2F@github.com/acme/other.git", "ssh://evil.example/@github.com/acme/other.git"},
		{"https://evil.example?@github.com/acme/other.git", "https://evil.example?@github.com/acme/other.git"},
	} {
		runGit(t, root, "remote", "set-url", "origin", c.url)
		out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		wants(t, out, "  remote.origin.url "+c.want+"\n")
	}
}

// TestAcceptCheckoutComparesAKeysValue changes the value of a git key the accept lists
// by its name alone while it asks: it accepts nothing.
func TestAcceptCheckoutComparesAKeysValue(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	runGit(t, root, "config", "core.sshCommand", "ssh -o BatchMode=yes")
	defer cmd.SetTerminal(true)()
	c := cmd.Root()
	var out strings.Builder
	c.SetArgs([]string{"setup", "repo", "--accept-checkout"})
	c.SetIn(&changingReader{change: func() {
		runGit(t, root, "config", "core.sshCommand", "ssh -o ProxyCommand=none")
	}, answer: strings.NewReader("y\n")})
	c.SetOut(&out)
	c.SetErr(&out)
	err := c.Execute()
	want := "the checkout changed while qory setup repo --accept-checkout asked, so it accepted nothing; run it again to see the changes"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q\n%s", err, want, out.String())
	}
	if r, err := places.Get(root); err != nil || r == nil || r.Accepted != nil {
		t.Errorf("the record was accepted: %+v, %v", r, err)
	}
}

// TestAcceptCheckoutComparesTheWholeURL changes the password of origin's URL while the
// accept asks, which leaves the row it showed as it was: it accepts nothing.
func TestAcceptCheckoutComparesTheWholeURL(t *testing.T) {
	root := newCheckout(t)
	recordCheckout(t, root)
	runGit(t, root, "remote", "set-url", "origin", "https://a-user:a-password@git.example.com/acme/other.git")
	defer cmd.SetTerminal(true)()
	c := cmd.Root()
	var out strings.Builder
	c.SetArgs([]string{"setup", "repo", "--accept-checkout"})
	c.SetIn(&changingReader{change: func() {
		runGit(t, root, "remote", "set-url", "origin", "https://a-user:another-password@git.example.com/acme/other.git")
	}, answer: strings.NewReader("y\n")})
	c.SetOut(&out)
	c.SetErr(&out)
	err := c.Execute()
	want := "the checkout changed while qory setup repo --accept-checkout asked, so it accepted nothing; run it again to see the changes"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q\n%s", err, want, out.String())
	}
	if r, err := places.Get(root); err != nil || r == nil || r.Accepted != nil {
		t.Errorf("the record was accepted: %+v, %v", r, err)
	}
}

// TestSetupExampleStopsAtTheRefusal stops with the refusal at the first git command in a
// recorded repository whose git configuration holds a key since.
func TestSetupExampleStopsAtTheRefusal(t *testing.T) {
	setExample(t, exampleFromDisk(t))
	dir := emptyDir(t)
	runGit(t, dir, "init", "--quiet")
	recordCheckout(t, dir)
	runGit(t, dir, "config", "alias.x", "log")
	out, err := run(t, "setup", "example")
	if err == nil || err.Error() != refusalOf(dir, "alias.x") || cmd.ExitCode(err) != 1 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
}

// writtenOf runs qory run with args behind the fake wall, the harness's Claude Code
// settings in its home, and returns the SessionSpec.Written and the Args of its one session,
// with its output. The run must reach the container: exit 4.
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
		out = append(out, places.Real(p))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestAWalledRunIsGivenEveryRecordedPlace records the places of earlier walled runs: a
// checkout, a mount, and a mount whose folder is gone since. A walled run with a
// read-write and a read-only mount gives its session every one of them in SessionSpec.Written,
// with its checkout and its read-write mount; a place whose path now goes through a file,
// which Forager cannot resolve, is left out, and the run still starts. The harness's
// Claude Code settings go to the session once, inline, as their JSON.
func TestAWalledRunIsGivenEveryRecordedPlace(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	walledSection(t)
	other := tempDir(t)
	runGit(t, other, "init", "--quiet")
	recordCheckout(t, other)
	otherMount := tempDir(t)
	recordMount(t, otherMount)
	gone := filepath.Join(tempDir(t), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	recordMount(t, gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	now := filepath.Join(tempDir(t), "now-a-file")
	unresolved := filepath.Join(now, "place")
	if err := os.MkdirAll(unresolved, 0o755); err != nil {
		t.Fatal(err)
	}
	recordMount(t, unresolved)
	if err := os.RemoveAll(now); err != nil {
		t.Fatal(err)
	}
	writeFile(t, now, "")
	mounted, readOnly := tempDir(t), tempDir(t)
	written, args, out := writtenOf(t, root, "--mount", mounted, "--mount", readOnly+":ro", "--", "-p", "hi")
	want := realSet([]string{root, mounted, other, otherMount, gone})
	if got := realSet(written); !slices.Equal(got, want) {
		t.Errorf("SessionSpec.Written %q\nwant %q", written, want)
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

// TestAWalledRunWithNoRecordIsGivenItsPlaces runs walled with no place recorded before:
// SessionSpec.Written holds the run's checkout and its read-write mount, which qory recorded
// before the session, and nothing else; the read-only mount is none. Forager adds the
// writable binds of the other walled runs it lists and, behind the wall, this run's
// writable binds itself, outside SessionSpec.Written.
func TestAWalledRunWithNoRecordIsGivenItsPlaces(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	walledSection(t)
	forgetPlaces(t)
	mounted, readOnly := tempDir(t), tempDir(t)
	written, _, _ := writtenOf(t, root, "--mount", mounted, "--mount", readOnly+":ro", "--", "-p", "hi")
	if got, want := realSet(written), realSet([]string{root, mounted}); !slices.Equal(got, want) {
		t.Errorf("SessionSpec.Written %q\nwant %q", written, want)
	}
}

// TestAMountThroughALinkInARecordedPlaceIsRefused mounts a link an earlier walled run
// could have left in its checkout, to a folder outside it: the run is refused before it
// starts, mount_through_link, and names the recorded place as one a walled run has used.
func TestAMountThroughALinkInARecordedPlaceIsRefused(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	walledSection(t)
	other := tempDir(t)
	runGit(t, other, "init", "--quiet")
	recordCheckout(t, other)
	link := filepath.Join(other, "lib")
	if err := os.Symlink(tempDir(t), link); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "run", "claude", "--mount", link, "--", "-p", "hi")
	want := "the mount " + link + " is a link inside " + other + ", a place a walled run has used, which a walled agent can change, so the run does not start. List the link's target itself (mount_through_link)"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
		t.Errorf("%v (exit %d)\nwant %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	lacks(t, out, "inside the container")
}

// TestANewPlaceSharingARecordedConfigurationIsCheckedFirst records a checkout as an
// earlier walled run's, which then writes core.sshCommand into a git configuration in
// the checkout and ends: the checkout's, which a worktree inside it reads, or that of a
// separate repository inside it. A later walled run starts in the worktree, or in the
// separate repository, which no record contains as a place of its own: the start
// refuses the key with the settings check's line, and records neither the place nor
// its fingerprint.
func TestANewPlaceSharingARecordedConfigurationIsCheckedFirst(t *testing.T) {
	for _, nested := range []string{"worktree", "repository"} {
		t.Run(nested, func(t *testing.T) {
			root := newCheckout(t)
			writeFile(t, filepath.Join(root, "README.md"), "app\n")
			runGit(t, root, "add", "README.md")
			runGit(t, root, "commit", "-q", "-m", "first")
			place := filepath.Join(root, ".claude", "worktrees", "x")
			config := root
			if nested == "worktree" {
				runGit(t, root, "worktree", "add", "-q", "-b", "x", place)
			} else {
				place = filepath.Join(root, "sub")
				if err := os.Mkdir(place, 0o755); err != nil {
					t.Fatal(err)
				}
				runGit(t, place, "init", "-q")
				config = place
			}
			recordCheckout(t, root)
			runGit(t, config, "config", "core.sshCommand", "touch ran")
			hold, err := cmd.RecordPlaces(place, nil)
			if want := refusalOf(place, "core.sshcommand"); err == nil || err.Error() != want {
				hold.Release()
				t.Fatalf("got %v, want %q", err, want)
			}
			if r, err := places.Get(place); err != nil || r != nil {
				t.Errorf("the record of %s: %+v, %v", place, r, err)
			}
		})
	}
}

// TestARunInARecordedCheckoutRecordsAMountInsideIt records a checkout as an earlier
// walled run's, then starts a run there with a writable mount that is a repository
// inside the checkout. The start holds both places, and the check of the mount's
// repository leaves the checkout to that hold: the mount is recorded with its
// repository's fingerprint.
func TestARunInARecordedCheckoutRecordsAMountInsideIt(t *testing.T) {
	root := newCheckout(t)
	mount := filepath.Join(root, "data")
	if err := os.Mkdir(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mount, "init", "-q")
	recordCheckout(t, root)
	hold, err := cmd.RecordPlaces(root, []wall.Mount{{Path: mount}})
	if err != nil {
		t.Fatalf("the start: %v", err)
	}
	hold.Release()
	r, err := places.Get(mount)
	if err != nil || r == nil || len(r.Git) != 1 || r.Git[0].Worktree != mount {
		t.Errorf("the record of the mount: %+v, %v", r, err)
	}
}
