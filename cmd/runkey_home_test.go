package cmd_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/report"
)

// runKeyHome is the home of the checkout at root for a run given a run key with no
// --home and no harness.home: under qory/homes in the user's cache directory, named by
// [checkout.Key].
func runKeyHome(t *testing.T, root string) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "qory", "homes", checkout.Key(root))
}

// runKeyReport is the report of the compose of a run given a run key, beside
// [runKeyHome].
func runKeyReport(t *testing.T, root string) report.Report {
	t.Helper()
	rep, err := report.Read(runKeyHome(t, root) + "-report.json")
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// folderTree lists every path under dir but the run records' and git's objects, with
// each file's content and each link's target, so two listings differ when a run wrote
// anything into dir, .git/info/exclude included.
func folderTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() && (rel == filepath.Join(".git", "objects") || rel == filepath.Join(".git", "logs")) {
			return filepath.SkipDir
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "-> " + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameTree fails the test for every path the listing after has that before has not, or
// has otherwise.
func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for path, v := range after {
		if was, ok := before[path]; !ok {
			t.Errorf("%s: the run wrote %s into the folder", what, path)
		} else if was != v {
			t.Errorf("%s: the run changed %s in the folder", what, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			t.Errorf("%s: the run removed %s from the folder", what, path)
		}
	}
}

// walledByDocker writes the machine's forager.yaml with a wall of the fake docker.
func walledByDocker(t *testing.T) {
	t.Helper()
	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
}

// keyedFolder is the two-modules fixture in a folder made the working directory, a git
// checkout when git is true, with the fake runtime as Claude Code's launch, and nothing
// composed.
func keyedFolder(t *testing.T, git bool) string {
	t.Helper()
	var root string
	if git {
		root = newCheckout(t)
	} else {
		root = emptyDir(t)
	}
	copyFixture(t, "two-modules", root)
	fakeLaunch(t, fakeRuntime(t))
	return root
}

// TestARunKeyComposesOutsideTheFolder is a run given a run key with no --home and no
// harness.home, in a git checkout and in a folder that is not one, behind a wall and
// without one: the run composes into its home under qory/homes in the cache directory,
// goes on to the join, and writes nothing into the folder it runs in: no .qory, no link
// and no line of .git/info/exclude.
func TestARunKeyComposesOutsideTheFolder(t *testing.T) {
	for _, c := range []struct {
		name        string
		git, walled bool
	}{
		{"git", true, false},
		{"git, walled", true, true},
		{"not git", false, false},
		{"not git, walled", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := keyedFolder(t, c.git)
			if c.walled {
				walledByDocker(t)
			}
			before := folderTree(t, root)
			out, err, n := inCheckoutRun(t, "claude")
			if !n.joined {
				t.Fatalf("the run did not go on to the join: %v\n%s", err, out)
			}
			wants(t, out, "qory run: composed 8 entries from 2 modules for claude\n")
			sameTree(t, c.name, before, folderTree(t, root))
			home := runKeyHome(t, root)
			if _, err := os.Stat(filepath.Join(home, "claude")); err != nil {
				t.Errorf("the home %s: %v", home, err)
			}
			if rep := runKeyReport(t, root); rep.Links != report.NoLinks {
				t.Errorf("the report's links are %q, want %q", rep.Links, report.NoLinks)
			}
		})
	}
}

// TestARunKeyWithAHomeInsideTheFolderStops is a run given a run key whose home is inside
// the folder it runs in: by --home, by --home naming a composed home with its report,
// by --home through a link outside the folder that leads into it, and by harness.home,
// in a git checkout and in a folder that is not one, behind a wall and without one. Each stops with the line that says to set --home outside, exit 2,
// before the run opens, and writes nothing into the folder.
func TestARunKeyWithAHomeInsideTheFolderStops(t *testing.T) {
	for _, c := range []struct {
		name        string
		git, walled bool
	}{
		{"git", true, false},
		{"git, walled", true, true},
		{"not git", false, false},
		{"not git, walled", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := keyedFolder(t, c.git)
			if c.walled {
				walledByDocker(t)
			}
			composed := filepath.Join(root, ".qory", "harness")
			if c.git {
				// A person's compose wrote .qory/harness and its report.
				if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
			}
			// A link outside the folder that leads into it.
			link := filepath.Join(tempDir(t), "to-folder")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}
			for _, h := range []struct {
				what string
				args []string
				user string
				home string
			}{
				{"--home", []string{"--home", filepath.Join(root, "homes")}, "", filepath.Join(root, "homes")},
				{"--home through a link", []string{"--home", filepath.Join(link, "homes")}, "", filepath.Join(link, "homes")},
				{"--home .qory/harness", []string{"--home", composed}, "", composed},
				{"harness.home", nil, ".qory/harness", composed},
			} {
				if h.user != "" {
					machineConfig(t, "harness:\n  home: "+h.user+"\n")
				}
				before := folderTree(t, root)
				out, err, n := inCheckoutRun(t, append([]string{"claude"}, h.args...)...)
				want := "set --home to a folder outside " + root + ": " + h.home + " is inside " + root + ", and a run with a run key writes no harness into the folder it runs in"
				if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
					t.Errorf("%s: %v (exit %d), want %q\n%s", h.what, err, cmd.ExitCode(err), want, out)
				}
				if len(n.closes) != 0 {
					t.Errorf("%s: the run opened, and closed with %v; the check of the home comes before the open", h.what, n.closes)
				}
				lacks(t, out, "composed")
				sameTree(t, h.what, before, folderTree(t, root))
			}
		})
	}
}

// TestAPersonsRunComposesIntoTheCheckout is a run without a run key: it composes into
// the checkout's .qory/harness, links the checkout into it, and adds the exclude lines,
// as before; nothing goes to the home of a run given a run key.
func TestAPersonsRunComposesIntoTheCheckout(t *testing.T) {
	root := keyedFolder(t, true)
	t.Setenv("QORY_RUN_KEY", "")
	t.Setenv("QORY_TEST_EXIT", "0")
	if out, err := run(t, "run", "claude"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rep := readReport(t, root); rep.Links == report.NoLinks {
		t.Errorf("the report's links are %q", rep.Links)
	}
	home := filepath.Join(root, ".qory", "harness")
	links := 0
	for path, v := range folderTree(t, root) {
		if target, ok := strings.CutPrefix(v, "-> "); ok {
			if !filepath.IsAbs(target) {
				target = filepath.Join(root, filepath.Dir(path), target)
			}
			if strings.HasPrefix(filepath.Clean(target), home+string(filepath.Separator)) {
				links++
			}
		}
	}
	if links == 0 {
		t.Errorf("the checkout has no link into %s", home)
	}
	exclude := readText(t, filepath.Join(root, ".git", "info", "exclude"))
	wants(t, exclude, "/.qory")
	if _, err := os.Stat(runKeyHome(t, root)); err == nil {
		t.Errorf("a person's run composed into %s", runKeyHome(t, root))
	}
}

// TestARunKeyWithAHomeOfAnotherCheckoutInsideTheFolderStops is a run with a run key in
// one checkout, with --home naming the composed home of another checkout, which lies
// inside the folder the run runs in: the report beside the home is for the other
// checkout, and the run stops all the same, with the folder the run runs in as <dir>,
// before the run opens and with nothing written.
func TestARunKeyWithAHomeOfAnotherCheckoutInsideTheFolderStops(t *testing.T) {
	root := keyedFolder(t, true)
	other := tempDir(t)
	runGit(t, other, "init", "--quiet", "--initial-branch=main")
	copyFixture(t, "two-modules", other)
	t.Chdir(other)
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--home", filepath.Join(root, "homes")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Chdir(root)
	home := filepath.Join(root, "homes", checkout.Key(other))
	before := folderTree(t, root)
	out, err, n := inCheckoutRun(t, "claude", "--home", home)
	want := "set --home to a folder outside " + root + ": " + home + " is inside " + root + ", and a run with a run key writes no harness into the folder it runs in"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if len(n.closes) != 0 {
		t.Errorf("the run opened, and closed with %v", n.closes)
	}
	sameTree(t, "another checkout's home", before, folderTree(t, root))
}

// TestARunKeyNeedsACacheDirectoryForItsHome is qory run with a run key, no --home and no
// harness.home, HOME and XDG_CACHE_HOME unset: the lookup of the cache directory fails,
// and the run stops with exit 2 before it opens, on the line that starts with the fix.
func TestARunKeyNeedsACacheDirectoryForItsHome(t *testing.T) {
	root := keyedFolder(t, true)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	_, lookup := os.UserCacheDir()
	if lookup == nil {
		t.Skip("the cache directory is found without HOME and XDG_CACHE_HOME")
	}
	out, err, n := inCheckoutRun(t, "claude")
	want := "set HOME, or give --home a folder outside " + root + ": a run with a run key composes its harness under the cache directory, and the lookup of that directory failed: " + lookup.Error()
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if len(n.closes) != 0 {
		t.Errorf("the run opened, and closed with %v", n.closes)
	}
	if _, err := os.Stat(filepath.Join(cache, "qory", "homes")); err == nil {
		t.Errorf("the run composed into %s", cache)
	}
}

// TestARunKeyResendNeedsNoCacheDirectory is qory run resend with a run key, HOME and
// XDG_CACHE_HOME unset: resend composes nothing, so it looks up no cache directory and
// goes on to the run's record.
func TestARunKeyResendNeedsNoCacheDirectory(t *testing.T) {
	keyedFolder(t, true)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	out, err := run(t, "run", "resend", keyRunID, "--run-key-fd", descriptor(t, key))
	want := "no run " + keyRunID + " is recorded in this checkout"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %q\n%s", err, want, out)
	}
}
