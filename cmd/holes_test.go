package cmd_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	walled "github.com/qoryai/qory/internal/places"
)

// TestRemoveRuntimeDeletesNothingThroughALinkedHome composes for Claude Code, then puts
// a link to a folder elsewhere at .qory/harness: remove --runtime claude refuses the
// link, and the folder it leads to keeps its claude directory.
func TestRemoveRuntimeDeletesNothingThroughALinkedHome(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose", "--runtime", "claude"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	elsewhere := filepath.Join(tempDir(t), "elsewhere")
	if err := os.Rename(home, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, home); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "harness", "remove", "--runtime", "claude")
	want := home + " links to " + elsewhere + ", which qory did not write; qory does not replace it"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitForeign {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "claude")); err != nil {
		t.Errorf("the folder the link leads to lost its claude directory: %v", err)
	}
}

// TestRemoveWithAHomeTakesOnlyThatHome names, with --home, a home whose report says it
// is for a checkout that holds it, though not as its .qory/harness: remove takes that
// home and its report, as a home outside the checkout, and leaves the checkout's .qory
// as it is.
func TestRemoveWithAHomeTakesOnlyThatHome(t *testing.T) {
	root := newCheckout(t)
	kept := filepath.Join(root, ".qory", "kept")
	writeFile(t, kept, "the checkout's own\n")
	home := filepath.Join(root, "homes", "shop")
	writeFile(t, filepath.Join(home, "claude", "settings.json"), "{}\n")
	writeFile(t, home+"-report.json", `{"version": 1, "stack": "shop", "checkout": "`+root+`", "home": "`+home+`"}`+"\n")
	out, err := run(t, "harness", "remove", "--home", home)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the checkout's .qory went: %v", err)
	}
	for _, gone := range []string{home, home + "-report.json"} {
		if _, err := os.Lstat(gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
}

// TestTheExcludeLineGoesIntoTheExcludeFileItself composes where a link stands at
// .git/info/exclude: the compose is refused, the file the link leads to is left as it
// was, and its text never reaches the exclude file, the checkout or the output. With the
// link gone the compose writes the file and its line. A link at .git/info is refused,
// and nothing is written where it leads.
func TestTheExcludeLineGoesIntoTheExcludeFileItself(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	file := filepath.Join(root, ".git", "info", "exclude")
	elsewhere := filepath.Join(tempDir(t), "elsewhere")
	writeFile(t, elsewhere, "a text of the file the link leads to\n")
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, file); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "harness", "compose")
	want := file + " is a link to " + elsewhere + "; qory reads the exclude file only when it is no link"
	if err == nil || err.Error() != want {
		t.Errorf("a link at the exclude file: %v, want %q\n%s", err, want, out)
	}
	if data, _ := os.ReadFile(elsewhere); string(data) != "a text of the file the link leads to\n" {
		t.Errorf("the link's target is now %q", data)
	}
	lacks(t, out, "a text of the file the link leads to")
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "a text of the file the link leads to") {
			t.Errorf("%s holds the text of the link's target", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if info, err := os.Lstat(file); err != nil || !info.Mode().IsRegular() {
		t.Errorf("the exclude file is %v, %v", info, err)
	}
	data, _ := os.ReadFile(file)
	wants(t, string(data), "/.qory\n")
	lacks(t, string(data), "a text of the file the link leads to")

	if out, err := run(t, "harness", "remove"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	info := filepath.Join(root, ".git", "info")
	outside := tempDir(t)
	if err := os.RemoveAll(info); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, info); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "harness", "compose")
	want = info + " is a link to " + outside + "; qory writes the exclude file only in a folder that is no link"
	if err == nil || err.Error() != want {
		t.Errorf("a link at .git/info: %v, want %q\n%s", err, want, out)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("the compose wrote where .git/info leads: %v", entries)
	}
}

// TestAModuleLinkNameIsExcludedAsItIs links a module under a name that holds the
// characters an exclude pattern reads as wildcards: the line excludes that name alone,
// and the checkout's other files stay visible to git.
func TestAModuleLinkNameIsExcludedAsItIs(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, strings.Replace(linkedStack, "link: harness", "link: \"h*[a]?\"", 1))
	writeFile(t, filepath.Join(root, "hello.txt"), "the checkout's own\n")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	wants(t, string(data), "/h\\*\\[a]\\?\n")
	status, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(status), "hello.txt")
	lacks(t, string(status), "h*[a]?")
	if out, err := run(t, "harness", "remove"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	data, _ = os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	lacks(t, string(data), "h\\*")
}

// TestAModuleLinkNameHoldsNoControlCharacter refuses a link whose name holds a line
// break, which would write a second line into the exclude file.
func TestAModuleLinkNameHoldsNoControlCharacter(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, strings.Replace(linkedStack, "link: harness", "link: \"harness\\n*\"", 1))
	out, err := run(t, "harness", "compose")
	if err == nil || !strings.HasSuffix(err.Error(), `: the link name "harness\n*" holds a control character`) {
		t.Errorf("%v\n%s", err, out)
	}
}

// TestWorktreeAddBringsNothingThroughALinkOfTheCheckout links .env and copies
// config/local.json and tools into a new worktree, where .env in the main checkout is a
// link to a file elsewhere and config a link to a folder elsewhere: neither is brought,
// each named in a row, and a link inside tools is copied as the link it is, not as the
// file it leads to.
func TestWorktreeAddBringsNothingThroughALinkOfTheCheckout(t *testing.T) {
	root := worktreeRepo(t)
	file := filepath.Join(root, "qory.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, strings.Replace(string(data), "copy: [config/local.json]", "copy: [config/local.json, tools]", 1))
	elsewhere := tempDir(t)
	secret := filepath.Join(elsewhere, "secret")
	writeFile(t, secret, "not for the worktree\n")
	writeFile(t, filepath.Join(elsewhere, "config", "local.json"), "{\"from\": \"elsewhere\"}\n")
	for link, target := range map[string]string{".env": secret, "config": filepath.Join(elsewhere, "config")} {
		if err := os.RemoveAll(filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "tools", "run.sh"), "echo run\n")
	if err := os.Symlink(secret, filepath.Join(root, "tools", "key")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "wa", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	want := []string{
		".env  (.env in the main checkout is a link or lies under one; qory brings nothing through a link of the checkout)",
		"config/local.json  (config/local.json in the main checkout is a link or lies under one; qory brings nothing through a link of the checkout)",
	}
	if got := fieldRows(out)["refused"]; !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
	for _, gone := range []string{".env", filepath.Join("config", "local.json")} {
		if _, err := os.Lstat(filepath.Join(wt, gone)); err == nil {
			t.Errorf("%s was brought", gone)
		}
	}
	if info, err := os.Lstat(filepath.Join(wt, "tools", "key")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("tools/key in the worktree is %v, %v", info, err)
	}
	if data, err := os.ReadFile(filepath.Join(wt, "tools", "run.sh")); err != nil || string(data) != "echo run\n" {
		t.Errorf("tools/run.sh in the worktree: %q, %v", data, err)
	}
}

// recordMount records dir as a read-write mount a walled run had.
func recordMount(t *testing.T, dir string) {
	t.Helper()
	if err := walled.Update(dir, walled.KindMount, func(r *walled.Record) error {
		r.Run = "run-1"
		r.Qory = &walled.Qory{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestNoProgramFromAPlaceAWalledRunUsed records a folder as a read-write mount of an
// earlier walled run and puts an integration's program, a credential's adapter and a
// tool's program in it: qory config and qory run refuse each, as they refuse one in the
// checkout, before anything starts. An integration's relative <name>_file setting is
// refused, since it would move with the working folder.
func TestNoProgramFromAPlaceAWalledRunUsed(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	place := tempDir(t)
	recordMount(t, place)
	program := filepath.Join(place, "bin", "acme-tracker")
	writeFile(t, program, "#!/bin/sh\ntest \"$1\" = describe || exit 64\necho '{\"version\": 1, \"name\": \"tracker\", \"title\": \"Tracker\", \"program_version\": \"0.3.0\", \"settings\": {\"type\": \"object\"}, \"roles\": {\"credential\": {\"argument\": \"[A-Z]+\", \"hosts\": [\"tracker.acme.example\"]}}}'\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	real, err := filepath.EvalSymlinks(place)
	if err != nil {
		t.Fatal(err)
	}
	shown := filepath.Join(real, "bin", "acme-tracker")
	tail := ", inside " + real + ", which a run may write; qory runs %s from outside the checkout, the container's read-write mounts and the places a walled run has used"
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  integrations:\n    tracker: {program: "+program+"}\n")
	out, err := run(t, "config")
	if want := "gateway.integrations.tracker: " + shown + fmt.Sprintf(tail, "an integration"); err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("an integration: %v, want the suffix %q\n%s", err, want, out)
	}
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  credentials:\n    product:\n      adapter: ["+program+", \"${argument}\"]\n      argument: \"[a-z]+\"\n      hosts: [git.example.com]\n")
	out, err = run(t, "run", "claude", "--local")
	if want := "forager.yaml: gateway.credentials.product.adapter: " + program + fmt.Sprintf(tail, "an adapter"); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("an adapter: %v, want %q\n%s", err, want, out)
	}
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  tools:\n    files:\n      command: ["+program+"]\n      serves: [files.tools.internal]\n")
	out, err = run(t, "run", "claude", "--local")
	if want := "forager.yaml: gateway.tools.files.command: " + program + fmt.Sprintf(tail, "a tool"); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a tool: %v, want %q\n%s", err, want, out)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left records %v", ids)
	}

	forgetPlaces(t)
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\ngateway:\n  integrations:\n    tracker: {program: "+program+", settings: {project: SHOP, token_file: keys/token}}\n")
	out, err = run(t, "config")
	if want := "gateway.integrations.tracker: settings.token_file is a relative path; qory takes the file by its absolute path, since a relative one moves with the working folder"; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("a relative token_file: %v, want %q\n%s", err, want, out)
	}
}
