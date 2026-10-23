package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// changedLine is the refusal of a qory file whose key changed since a walled run began
// in its place.
func changedLine(file, key string) string {
	return file + " changes " + key + " since a walled run began here; qory does not act on it, since the run could have written it. qory setup repo --accept-checkout accepts it"
}

// harnessLine is the approved refusal of a home or report that is not what the last
// compose wrote.
func harnessLine(root string) string {
	return "the harness of " + root + " is not the one qory composed; compose again with qory harness compose"
}

// appendTo adds text at the end of the file at path.
func appendTo(t *testing.T, path, text string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data)+text)
}

// TestAChangedActingKeyIsRefusedUntilAccepted records a checkout, then adds a command
// to its qory.yaml: every verb that reads the file refuses it with the approved line,
// before it acts; a change to a key qory does not act on passes. The accept command lists
// the key with its new value and, on yes, takes the file as it is.
func TestAChangedActingKeyIsRefusedUntilAccepted(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	recordCheckout(t, root)
	file := filepath.Join(root, "qory.yaml")
	appendTo(t, file, "# a note\n")
	if out, err := run(t, "config"); err != nil {
		t.Fatalf("a change qory does not act on: %v\n%s", err, out)
	}
	appendTo(t, file, "worktree:\n  run:\n    add: [make setup]\n")
	for _, args := range [][]string{{"config"}, {"harness", "compose"}, {"harness", "inspect"}, {"worktree", "add", "feature", "--offline"}, {"run", "claude", "--local"}} {
		out, err := run(t, args...)
		if err == nil || err.Error() != changedLine(file, "worktree.run.add") || cmd.ExitCode(err) != 1 {
			t.Errorf("%v: %v (exit %d)\n%s", args, err, cmd.ExitCode(err), out)
		}
	}
	defer cmd.SetTerminal(true)()
	out, err := runIn(t, "y\n", "setup", "repo", "--accept-checkout")
	want := "changes since a walled run began here:\n  qory.yaml: worktree.run.add [\"make setup\"]\naccept them? [y/N] accepted; qory takes the checkout as it is now\n"
	if err != nil || out != want {
		t.Fatalf("accept: %v\n%q", err, out)
	}
	if out, err := run(t, "config"); err != nil {
		t.Errorf("after the accept: %v\n%s", err, out)
	}
}

// TestAHarnessQoryDidNotComposeIsRefused records a composed checkout, then changes its
// report and, after a compose, a file of its home: inspect and remove refuse each with
// the approved line, and a compose takes the harness as qory wrote it again. A run whose
// open fails, for a wall with no image, composes nothing; the compose of a run that
// opens takes the harness as qory wrote it, before the agent starts.
func TestAHarnessQoryDidNotComposeIsRefused(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	recordCheckout(t, root)
	if out, err := run(t, "harness", "inspect"); err != nil {
		t.Fatalf("as composed: %v\n%s", err, out)
	}
	report := filepath.Join(root, ".qory", "harness-report.json")
	appendTo(t, report, " ")
	refusedAll := func(what string) {
		t.Helper()
		for _, args := range [][]string{{"harness", "inspect"}, {"harness", "remove"}} {
			out, err := run(t, args...)
			if err == nil || err.Error() != harnessLine(root) || cmd.ExitCode(err) != 1 {
				t.Errorf("%s, %v: %v (exit %d)\n%s", what, args, err, cmd.ExitCode(err), out)
			}
		}
		if _, err := os.Stat(filepath.Join(root, ".qory", "harness")); err != nil {
			t.Errorf("%s: a refused remove removed the home: %v", what, err)
		}
	}
	refusedAll("the report")
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("compose again: %v\n%s", err, out)
	}
	if out, err := run(t, "harness", "inspect"); err != nil {
		t.Fatalf("composed again: %v\n%s", err, out)
	}
	appendTo(t, filepath.Join(root, ".qory", "harness", "claude", "settings.json"), "\n")
	refusedAll("a file of the home")
	out, err := run(t, "run", "claude", "--local", "--wall", "docker")
	if err == nil || !strings.Contains(err.Error(), "a wall needs the container's image") {
		t.Errorf("a run on a home qory did not compose: %v\n%s", err, out)
	}
	refusedAll("after a run that did not open")
	// A wall with an image opens; the agent's arguments set --settings, so the run stops
	// after its compose.
	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	if out, err := run(t, "run", "claude", "--local", "--", "--settings"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "the agent's arguments set --settings") {
		t.Errorf("a run that opens: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if out, err := run(t, "harness", "inspect"); err != nil {
		t.Fatalf("after the run's compose: %v\n%s", err, out)
	}
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("compose again: %v\n%s", err, out)
	}
	if out, err := run(t, "harness", "remove"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links"); err != nil {
		t.Fatalf("compose after a remove: %v\n%s", err, out)
	}
	if out, err := run(t, "harness", "inspect"); err != nil {
		t.Errorf("after a remove and a compose: %v\n%s", err, out)
	}
}

// TestAFolderQoryDerivesThatLeadsOutIsRefused records a checkout whose qory.yaml lists a
// module by its name alone, and one whose qory.yaml reads a module from the export it
// lists, then points the folder qory derives for each at a folder of a repository outside
// the checkout through a link: compose refuses each with the approved line, and copies
// nothing.
func TestAFolderQoryDerivesThatLeadsOutIsRefused(t *testing.T) {
	for _, c := range []struct{ name, fixture, from, folder string }{
		{"a module by its name alone", "two-modules", "      source: {path: modules/core}\n", "modules/core"},
		{"a module of an export", "exports-resolve", "", "harness/modules/core"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := newCheckout(t)
			copyFixture(t, c.fixture, root)
			file := filepath.Join(root, "qory.yaml")
			if c.from != "" {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), c.from) {
					t.Fatalf("qory.yaml lacks %q:\n%s", c.from, data)
				}
				writeFile(t, file, strings.Replace(string(data), c.from, "", 1))
			}
			recordCheckout(t, root)
			folder := filepath.Join(root, filepath.FromSlash(c.folder))
			outside := filepath.Join(outsideRepo(t), "core")
			if err := os.Rename(folder, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, folder); err != nil {
				t.Fatal(err)
			}
			out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links")
			want := "module core: " + folder + " leads through a link out of " + root + ", a place a walled run has used; qory does not read a module or a stack from it, since the run could have pointed the link elsewhere. Remove the link to go on"
			if err == nil || err.Error() != want {
				t.Fatalf("%v, want %q\n%s", err, want, out)
			}
			if _, err := os.Stat(filepath.Join(root, ".qory", "harness")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the compose wrote the home: %v", err)
			}
		})
	}
}

// TestAFolderABaseOnDiskDerivesThatLeadsOutIsRefused records a checkout whose qory.yaml
// extends a base stack in the checkout that lists a module by its name alone, then
// points the folder qory derives for it at a folder of a repository outside the checkout
// through a link: compose refuses it with the approved line, and copies nothing.
func TestAFolderABaseOnDiskDerivesThatLeadsOutIsRefused(t *testing.T) {
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "base", "qory-stack.yaml"), "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\nextending:\n  kinds: [skills]\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ./base}\n  target: {runtime: claude}\n")
	outside := filepath.Join(outsideRepo(t), "core")
	writeManifest(t, outside, "core")
	writeFile(t, filepath.Join(outside, "skills", "review", "SKILL.md"), "# review\n")
	recordCheckout(t, root)
	folder := filepath.Join(root, "modules", "core")
	if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, folder); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links")
	want := "module core: " + folder + " leads through a link out of " + root + ", a place a walled run has used; qory does not read a module or a stack from it, since the run could have pointed the link elsewhere. Remove the link to go on"
	if err == nil || err.Error() != want {
		t.Fatalf("%v, want %q\n%s", err, want, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".qory", "harness")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the compose wrote the home: %v", err)
	}
}

// TestAFolderThatLeadsOutOfAPlaceIsRefusedWhoeverNamesIt records a checkout, a, then
// makes a folder of a module or a stack in it a link out of it, and composes in a
// checkout in no place, b, that reaches that folder: by a module a exports, named
// through a link to a; by an extends path into a, for a base that lists a module by its
// name or for a stack a exports; and by a link of b's into a's folder. Compose refuses
// each with the approved line, naming the folder as written, and copies nothing.
func TestAFolderThatLeadsOutOfAPlaceIsRefusedWhoeverNamesIt(t *testing.T) {
	const stackExport = "apiVersion: qory.dev/v1alpha1\nexports:\n  dir: harness\n  stacks: [web]\n"
	const byName = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\nextending:\n  kinds: [skills]\n"
	for _, c := range []struct {
		name string
		// a writes a's tree and returns the folder of a that becomes a link out.
		a func(t *testing.T, a string) string
		// b writes b's qory.yaml, in which compose runs; alias is a link to a.
		b func(t *testing.T, b, alias string)
		// folder and prefix are the folder the refusal names and what comes before it.
		folder func(a, b, alias string) string
		prefix func(alias string) string
	}{
		{
			name: "b names a module a exports, through a link to a",
			a: func(t *testing.T, a string) string {
				copyFixture(t, "exports-resolve", a)
				return filepath.Join(a, "harness", "modules", "core")
			},
			b: func(t *testing.T, b, alias string) {
				writeFile(t, filepath.Join(b, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target: {runtime: claude}\n  modules:\n    - name: core\n      source: {path: "+alias+", module: core}\n")
			},
			folder: func(_, _, alias string) string { return filepath.Join(alias, "harness", "modules", "core") },
			prefix: func(string) string { return "module core: " },
		},
		{
			name: "b extends a base in a that lists a module by its name",
			a: func(t *testing.T, a string) string {
				writeFile(t, filepath.Join(a, "base", "qory-stack.yaml"), byName)
				return filepath.Join(a, "modules", "core")
			},
			b: func(t *testing.T, b, alias string) {
				writeFile(t, filepath.Join(b, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: "+filepath.Join(alias, "base")+"}\n  target: {runtime: claude}\n")
			},
			folder: func(a, _, _ string) string { return filepath.Join(a, "modules", "core") },
			prefix: func(string) string { return "module core: " },
		},
		{
			name: "b extends a stack a exports",
			a: func(t *testing.T, a string) string {
				writeFile(t, filepath.Join(a, "qory.yaml"), stackExport)
				writeFile(t, filepath.Join(a, "harness", "stacks", "web", "qory-stack.yaml"), byName)
				return filepath.Join(a, "harness", "stacks", "web")
			},
			b: func(t *testing.T, b, alias string) {
				writeFile(t, filepath.Join(b, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: "+alias+", stack: web}\n  target: {runtime: claude}\n")
			},
			folder: func(_, _, alias string) string { return filepath.Join(alias, "harness", "stacks", "web") },
			prefix: func(alias string) string { return "extends " + alias + " stack web: " },
		},
		{
			name: "b's link into a's folder",
			a: func(t *testing.T, a string) string {
				return filepath.Join(a, "modules", "core")
			},
			b: func(t *testing.T, b, alias string) {
				writeFile(t, filepath.Join(b, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target: {runtime: claude}\n  modules:\n    - name: core\n")
				if err := os.MkdirAll(filepath.Join(b, "modules"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(alias, "modules", "core"), filepath.Join(b, "modules", "core")); err != nil {
					t.Fatal(err)
				}
			},
			folder: func(_, b, _ string) string { return filepath.Join(b, "modules", "core") },
			prefix: func(string) string { return "module core: " },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newCheckout(t)
			alias := filepath.Join(tempDir(t), "alias")
			if err := os.Symlink(a, alias); err != nil {
				t.Fatal(err)
			}
			folder := c.a(t, a)
			recordCheckout(t, a)
			outside := filepath.Join(outsideRepo(t), "folder")
			writeManifest(t, outside, "core")
			writeFile(t, filepath.Join(outside, "qory-stack.yaml"), byName)
			writeFile(t, filepath.Join(outside, "skills", "review", "SKILL.md"), "# review\n")
			if err := os.RemoveAll(folder); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, folder); err != nil {
				t.Fatal(err)
			}
			b := tempDir(t)
			runGit(t, b, "init", "-q", "-b", "main")
			c.b(t, b, alias)
			t.Chdir(b)
			out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links")
			want := c.prefix(alias) + c.folder(a, b, alias) + " leads through a link out of " + a + ", a place a walled run has used; qory does not read a module or a stack from it, since the run could have pointed the link elsewhere. Remove the link to go on"
			if err == nil || err.Error() != want {
				t.Fatalf("%v, want %q\n%s", err, want, out)
			}
			for _, dir := range []string{a, b} {
				if _, err := os.Stat(filepath.Join(dir, ".qory", "harness")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the compose wrote the home in %s: %v", dir, err)
				}
			}
		})
	}
}

// TestAQoryFileReadThroughALinkIsCheckedAsAtThePlace records a checkout whose qory.yaml
// reads a module from an export of the checkout by a relative path, then composes it by
// --file through a link to the checkout: the file sets what it set when the place was
// recorded, so compose goes on. A path the file then changes to, read through the same
// link, is refused with the line that names the file as given.
func TestAQoryFileReadThroughALinkIsCheckedAsAtThePlace(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "exports-resolve", root)
	recordCheckout(t, root)
	alias := filepath.Join(tempDir(t), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(alias, "qory.yaml")
	if out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--file", file); err != nil {
		t.Fatalf("as recorded, through the link: %v\n%s", err, out)
	}
	outside := filepath.Join(outsideRepo(t), "extra")
	writeManifest(t, outside, "extra")
	data, err := os.ReadFile(filepath.Join(root, "qory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	from := "source: {path: harness/modules/extra}"
	if !strings.Contains(string(data), from) {
		t.Fatalf("qory.yaml lacks %q:\n%s", from, data)
	}
	writeFile(t, filepath.Join(root, "qory.yaml"), strings.Replace(string(data), from, "source: {path: "+outside+"}", 1))
	out, err := run(t, "harness", "compose", "--runtime", "claude", "--no-links", "--file", file)
	want := file + " changes harness.modules[1].source.path since a walled run began here; qory does not act on it, since the run could have written it. qory setup repo --accept-checkout accepts it"
	if err == nil || err.Error() != want {
		t.Fatalf("%v, want %q\n%s", err, want, out)
	}
}

// outsideRepo is a git repository outside the checkout, where a link out of it could
// lead: git there runs without the settings check, as in any folder of the person's.
func outsideRepo(t *testing.T) string {
	t.Helper()
	dir := tempDir(t)
	runGit(t, dir, "init", "-q", "-b", "main")
	return dir
}
