package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// hermetic points HOME and the configuration directory at temporary paths, so no test
// reads the machine's own qory.yaml.
func hermetic(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("apiVersion: qory.dev/v1alpha1\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadWithoutAFileIsTheDefaults is a checkout with no qory.yaml anywhere: every value
// is its default and says so.
func TestLoadWithoutAFileIsTheDefaults(t *testing.T) {
	hermetic(t)
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime != nil || c.Model != "" || c.Force || c.Update || c.Git.Timeout != 10*time.Minute || c.Git.Cache != "" || len(c.Env) != 0 || len(c.Files) != 0 {
		t.Errorf("defaults: %+v", c)
	}
	for _, r := range c.Rows() {
		if r.Origin != config.Default {
			t.Errorf("%s comes from %s, want %s", r.Key, r.Origin, config.Default)
		}
	}
	rows := c.Rows()
	if rows[0].Value != "(stack)" || rows[3].Value != "never" || rows[4].Value != config.DefaultHome || rows[5].Value != "(by home)" || rows[6].Value != ".." || rows[7].Value != "wt-{branch}" || rows[8].Value != "(remote HEAD)" || rows[9].Value != "delete" || rows[10].Value != "10m0s" {
		t.Errorf("rows: %+v", rows)
	}
}

// TestNearerFilesWin is the user's file, an ancestor's and the checkout's, each setting
// the runtime: the checkout's wins, a key only the user's file sets keeps that value, and
// every file is listed in the order it applied.
func TestNearerFilesWin(t *testing.T) {
	home := hermetic(t)
	user := filepath.Join(home, ".config", "qory", "qory.yaml")
	write(t, user, "harness: {runtime: gemini, force: true}\nworktree: {dir: /tmp/wt, link: [.env]}\nenv: {A: user, B: user}\n")
	base := t.TempDir()
	root := filepath.Join(base, "code", "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(base, "code", "qory.yaml")
	write(t, ancestor, "harness: {runtime: codex, update: always}\nworktree: {name: \"{repo}-{branch}\"}\nenv: {B: ancestor}\n")
	own := filepath.Join(root, "qory.yaml")
	write(t, own, "harness: {runtime: [claude, codex]}\nworktree: {link: [.env.local], run: {add: [pnpm install]}}\ngit: {timeout: 30s, cache: cache}\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime.String() != "claude, codex" || !c.Force || !c.Update || c.Git.Timeout != 30*time.Second || c.Git.Cache != filepath.Join(root, "cache") {
		t.Errorf("effective: %+v", c)
	}
	if c.Env["A"] != "user" || c.Env["B"] != "ancestor" {
		t.Errorf("env: %v", c.Env)
	}
	if w := c.Worktree; w.Dir != "/tmp/wt" || w.Name != "{repo}-{branch}" || len(w.Link) != 1 || w.Link[0] != (config.Path{From: ".env.local", To: ".env.local"}) || strings.Join(w.Add, ",") != "pnpm install" || len(w.Copy) != 0 {
		t.Errorf("worktree: %+v", w)
	}
	if strings.Join(c.Files, " ") != user+" "+ancestor+" "+own {
		t.Errorf("files: %v", c.Files)
	}
	origins := map[string]string{}
	for _, r := range c.Rows() {
		origins[r.Key] = r.Origin
	}
	if origins["harness.runtime"] != own || origins["harness.force"] != user || origins["harness.update"] != ancestor || origins["env.B"] != ancestor || origins["harness.model"] != config.Default || origins["worktree.link"] != own || origins["worktree.dir"] != user {
		t.Errorf("origins: %v", origins)
	}
}

// TestLoadRefusesAMistake is every value a file cannot carry: an unknown key, a wrong
// kind, an update that is neither always nor never, a timeout that is not a duration, an
// empty cache, a variable that is not a name, qory's own variable, and a YAML alias,
// plain, as a merge key, or chained to expand as far as it likes.
func TestLoadRefusesAMistake(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"runtim: claude\n", `line 2: key "runtim" is not one qory.yaml reads`},
		{"harness: {runtim: claude}\n", `line 2: key "runtim" is not one qory.yaml reads`},
		{"harness: {update: sometimes}\n", `harness.update "sometimes" is not always or never`},
		{"worktree: {name: wt}\n", `worktree.name "wt" contains no {branch}`},
		{"worktree: {name: \"a/{branch}\"}\n", `worktree.name "a/{branch}" contains a slash`},
		{"worktree: {dir: \"\"}\n", "worktree.dir is empty"},
		{"worktree: {link: [../secrets]}\n", `worktree.link lists "../secrets", which is not a path inside the checkout`},
		{"worktree: {copy: [/etc/hosts]}\n", `worktree.copy lists "/etc/hosts", which is not a path inside the checkout; a path from outside goes as {from: /etc/hosts, to: <path in the worktree>}`},
		{"worktree: {link: [{from: /etc/hosts}]}\n", `worktree.link lists /etc/hosts with no to; a path from outside the checkout sets where it goes, as {from: /etc/hosts, to: <path in the worktree>}`},
		{"worktree: {link: [{from: ~/secrets/app.env}]}\n", `worktree.link lists ~/secrets/app.env with no to`},
		{"worktree: {link: [{from: /etc/hosts, to: /etc/hosts}]}\n", `worktree.link maps /etc/hosts to "/etc/hosts", which is not a path inside the worktree`},
		{"worktree: {copy: [{from: /etc/hosts, to: ../hosts}]}\n", `worktree.copy maps /etc/hosts to "../hosts", which is not a path inside the worktree`},
		{"worktree: {copy: [{from: /etc/hosts, to: .}]}\n", `worktree.copy maps /etc/hosts to ".", which is not a path inside the worktree`},
		{"worktree: {link: [{from: ../shared/.env, to: .env}]}\n", `worktree.link lists "../shared/.env", which is not a path inside the checkout`},
		{"worktree: {link: [{to: .env}]}\n", `worktree.link lists an entry with no path`},
		{"worktree: {link: [\"\"]}\n", `worktree.link lists an entry with no path`},
		{"worktree: {link: [{from: .env, to: ../.env}]}\n", `worktree.link maps .env to "../.env", which is not a path inside the worktree`},
		{"worktree: {pr: \"pull/{n}\"}\n", `worktree.pr "pull/{n}" is not a ref under refs/ with {n} for the pull request number`},
		{"git: {timeout: soon}\n", `git.timeout "soon" is not a duration above zero, such as 10m`},
		{"git: {timeout: 0s}\n", `git.timeout "0s" is not a duration above zero`},
		{"git: {cache: \"\"}\n", "git.cache is empty"},
		{"env: {1A: x}\n", "env: 1A is not an environment variable name"},
		{"env: {QORY_HARNESS_HOME: x}\n", "env.QORY_HARNESS_HOME is qory's own"},
		{"harness: {runtime: []}\n", "harness.runtime is empty"},
		{"harness: {model: &m opus}\nworktree: {base: *m}\n", "line 3: *m is a YAML alias, which qory.yaml may not contain"},
		{"env: &e {A: x}\nharness:\n  extensions:\n    ci:\n      <<: *e\n", "line 6: *e is a YAML alias"},
		{"a: &a [x, x, x, x]\nb: &b [*a, *a, *a, *a]\nc: [*b, *b, *b, *b]\n", "line 3: *a is a YAML alias"},
	} {
		root := t.TempDir()
		write(t, filepath.Join(root, "qory.yaml"), c.body)
		_, err := config.Load(root, true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), root) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}

// TestAnAnchorAloneReads is a qory.yaml naming an anchor no alias uses: the anchor
// copies nothing, so the file is read.
func TestAnAnchorAloneReads(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "harness: {model: &m opus}\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "opus" {
		t.Errorf("model %q, want opus", c.Model)
	}
}

// TestAnEmptyFileSetsNothing is a qory.yaml holding only its apiVersion: it is
// read, listed, and changes no value.
func TestAnEmptyFileSetsNothing(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Files) != 1 || c.Git.Timeout != config.DefaultTimeout {
		t.Errorf("got %+v", c)
	}
}

// TestOwnFileKeepsItsWorktreeSectionUnderExtends is the checkout's own file read with
// own false, as a compose under extends reads it: its worktree keys apply and its
// harness, git and env keys do not.
func TestOwnFileKeepsItsWorktreeSectionUnderExtends(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "harness: {runtime: codex, force: true}\nworktree: {base: develop, link: [.env]}\ngit: {timeout: 30s}\nenv: {A: own}\n")
	c, err := config.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime != nil || c.Force || c.Git.Timeout != config.DefaultTimeout || len(c.Env) != 0 {
		t.Errorf("machine keys were read: %+v", c)
	}
	if c.Worktree.Base != "develop" || len(c.Worktree.Link) != 1 || c.Worktree.Link[0].String() != ".env" {
		t.Errorf("worktree keys were not read: %+v", c.Worktree)
	}
}

// TestAFileWithoutAnAPIVersionReadsAsTheNewest is a qory.yaml naming no apiVersion: it
// is read as the format this qory reads, through Load and as a document through
// LoadStack, while one naming another version is refused as before.
func TestAFileWithoutAnAPIVersionReadsAsTheNewest(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	path := filepath.Join(root, "qory.yaml")
	writeRaw(t, path, "harness: {force: true}\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Force || len(c.Files) != 1 {
		t.Fatalf("got %+v", c)
	}
	writeRaw(t, path, "harness:\n  extends: {path: ../base}\n  modules:\n    - name: app\n")
	p, err := config.LoadStack(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != stack.APIVersion || p.Extends.Path != "../base" {
		t.Fatalf("document: %+v", p)
	}
	writeRaw(t, path, "apiVersion: qory.dev/v2\nharness: {force: true}\n")
	_, err = config.Load(root, true)
	want := path + `: apiVersion "qory.dev/v2" is not one this qory reads; versions: qory.dev/v1alpha1`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestWorktreePathsFromOutsideTheCheckout is worktree.link and worktree.copy in their
// two forms: a path alone goes to the same path, {from, to} with a relative from goes to
// its to, and an absolute or ~ from is taken as it is, the ~ expanded to the home
// directory, and printed as from -> to.
func TestWorktreePathsFromOutsideTheCheckout(t *testing.T) {
	home := hermetic(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "worktree:\n  link: [.env, {from: ~/secrets/app.env, to: .env.local}, {from: config/dev.json, to: config/local.json}]\n  copy: [{from: /var/lib/app/seed.sql, to: db/seed.sql}]\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	wantLink := []config.Path{{From: ".env", To: ".env"}, {From: filepath.Join(home, "secrets", "app.env"), To: ".env.local"}, {From: "config/dev.json", To: "config/local.json"}}
	if len(c.Worktree.Link) != len(wantLink) {
		t.Fatalf("link: %+v", c.Worktree.Link)
	}
	for i := range wantLink {
		if c.Worktree.Link[i] != wantLink[i] {
			t.Errorf("link[%d] = %+v, want %+v", i, c.Worktree.Link[i], wantLink[i])
		}
	}
	if len(c.Worktree.Copy) != 1 || c.Worktree.Copy[0] != (config.Path{From: "/var/lib/app/seed.sql", To: "db/seed.sql"}) {
		t.Errorf("copy: %+v", c.Worktree.Copy)
	}
	rows := map[string]string{}
	for _, r := range c.Rows() {
		rows[r.Key] = r.Value
	}
	if want := ".env, " + filepath.Join(home, "secrets", "app.env") + " -> .env.local, config/dev.json -> config/local.json"; rows["worktree.link"] != want {
		t.Errorf("worktree.link row %q, want %q", rows["worktree.link"], want)
	}
	if want := "/var/lib/app/seed.sql -> db/seed.sql"; rows["worktree.copy"] != want {
		t.Errorf("worktree.copy row %q, want %q", rows["worktree.copy"], want)
	}
}

// TestAFileNamingARetiredAPIVersionReads is a qory.yaml naming a retired apiVersion:
// Load reads it and records the file and the spelling under Retired, and as a
// document through LoadStack it carries the spelling beside the version it is read as,
// so a compose can say the line wants rewriting. Nothing is recorded for a file naming
// the current version or none.
func TestAFileNamingARetiredAPIVersionReads(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	path := filepath.Join(root, "qory.yaml")
	writeRaw(t, path, "apiVersion: qory.ai/v1alpha1\nharness: {force: true}\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Force || len(c.Retired) != 1 || c.Retired[0].File != path || c.Retired[0].APIVersion != "qory.ai/v1alpha1" {
		t.Fatalf("got force %v, retired %+v", c.Force, c.Retired)
	}
	writeRaw(t, path, "apiVersion: qory.ai/v1alpha1\nharness:\n  extends: {path: ../base}\n  modules:\n    - name: app\n  extensions:\n    consumer: {team: web}\n")
	p, err := config.LoadStack(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != stack.APIVersion || p.RetiredAPIVersion != "qory.ai/v1alpha1" || p.Extensions["consumer"] == nil {
		t.Fatalf("document: apiVersion %q, retired %q, extensions %v", p.APIVersion, p.RetiredAPIVersion, p.Extensions)
	}
	if got, err := config.Document(root); err != nil || got != path {
		t.Fatalf("Document: %q, %v", got, err)
	}
	writeRaw(t, path, "harness: {force: true}\n")
	if c, err = config.Load(root, true); err != nil || len(c.Retired) != 0 {
		t.Fatalf("no apiVersion: retired %+v, %v", c.Retired, err)
	}
}
