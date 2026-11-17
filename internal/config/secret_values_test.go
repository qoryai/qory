package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// The phrases a message prints in place of a value that contains an access key secret
// or a run key.
const (
	accessKeyValue = "(a value that contains an access key secret)"
	runKeyValue    = "(a value that contains a run key)"
)

// noSecret fails the test for each row whose key or value contains qak_ or qrk_ in any
// letter case.
func noSecret(t *testing.T, rows []config.Row) {
	t.Helper()
	for _, r := range rows {
		if s := strings.ToLower(r.Key + " " + r.Value); strings.Contains(s, "qak_") || strings.Contains(s, "qrk_") {
			t.Errorf("row %s %s contains a secret", r.Key, r.Value)
		}
	}
}

// TestTheConfigRowsPrintAPhraseForAValueWithASecret is a qory.yaml whose values contain
// an access key secret or a run key: the runtime, the model, the home, a launch entry's
// variables, command and arguments, the worktree keys and lists, a tier's model and
// effort, the git cache and a variable. Each such value is the value phrase, each such
// name the name phrase, and a plain value prints as before.
func TestTheConfigRowsPrintAPhraseForAValueWithASecret(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), `harness:
  runtime: [claude, qak_rt]
  model: qak_model
  effort: qrk_effort
  home: /opt/qak_home
  tiers:
    fast: {model: qrk_model, effort: qak_effort}
  launch:
    claude:
      command: /opt/qak_tool
      args: [--flag, [--token, qrk_x]]
      env: {API_KEY: qak_k, QAK_NAME: plain}
    qak_rt: {command: /opt/tool}
worktree:
  dir: /wt/qak_d
  name: "qak_{branch}"
  base: qak_base
  pr: "refs/qak_/{n}"
  link: [qak_l, {from: /opt/qak_f, to: x}, plain]
  run: {add: ["./setup --token qak_t", plain], remove: [qrk_r]}
git: {cache: /c/qak_c}
env: {API_TOKEN: qak_v, QRK_N: plain}
`)
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	rows := c.Rows()
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Value
	}
	for key, value := range map[string]string{
		"harness.runtime":                   "claude, " + accessKeyValue,
		"harness.model":                     accessKeyValue,
		"harness.effort":                    runKeyValue,
		"harness.home":                      accessKeyValue,
		"harness.tiers.fast":                runKeyValue + ", effort " + accessKeyValue,
		"harness.launch.claude":             "API_KEY=" + accessKeyValue + " " + accessKeyPhrase + "=plain " + accessKeyValue + " --flag --token " + runKeyValue,
		"harness.launch." + accessKeyPhrase: "/opt/tool",
		"worktree.dir":                      accessKeyValue,
		"worktree.name":                     accessKeyValue,
		"worktree.base":                     accessKeyValue,
		"worktree.pr":                       accessKeyValue,
		"worktree.link":                     accessKeyValue + ", " + accessKeyValue + " -> x, plain",
		"worktree.run.add":                  accessKeyValue + ", plain",
		"worktree.run.remove":               runKeyValue,
		"git.cache":                         accessKeyValue,
		"env.API_TOKEN":                     accessKeyValue,
		"env." + runKeyPhrase:               "plain",
	} {
		if got[key] != value {
			t.Errorf("row %s = %q, want %q", key, got[key], value)
		}
	}
	noSecret(t, rows)
}

// TestTheExportsRowsPrintAPhraseForAValueWithASecret is a checkout root's exports
// section whose directory contains an access key secret and one of whose modules has a
// name that contains a run key: the two directory rows are the value phrase, the
// module's name is the name phrase, and a plain name prints as before.
func TestTheExportsRowsPrintAPhraseForAValueWithASecret(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "exports:\n  dir: qak_h\n  modules: [core, qrk_m]\n")
	write(t, filepath.Join(root, "qak_h", "modules", "core", "qory-module.yaml"), "name: core\n")
	write(t, filepath.Join(root, "qak_h", "modules", "qrk_m", "qory-module.yaml"), "name: qrk_m\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	rows := c.Rows()
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Value
	}
	for key, value := range map[string]string{
		"exports.dir.stacks":  accessKeyValue,
		"exports.dir.modules": accessKeyValue,
		"exports.modules":     "core, " + runKeyPhrase,
	} {
		if got[key] != value {
			t.Errorf("row %s = %q, want %q", key, got[key], value)
		}
	}
	noSecret(t, rows)
}

// TestConfigRefusalsPrintAPhraseForASecret is each refusal of qory.yaml that prints a
// value or a name a file sets, with an access key secret or a run key in it: the
// runtime listed twice, harness.update, harness.links, git.timeout, a variable's name,
// a tier's name and model, the worktree keys, a link or copy entry, and a launch entry's
// runtime and variable names. The refusal prints the value phrase in place of a value
// and the name phrase in place of a name, and says the rest as before.
func TestConfigRefusalsPrintAPhraseForASecret(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ body, want string }{
		{"harness: {runtime: [qak_r, qak_r]}\n", "List " + accessKeyPhrase + " once in harness.runtime.\n  harness.runtime lists it twice.\n  harness.runtime is in {path}."},
		{"harness: {update: qak_u}\n", "Set harness.update to always or never.\n  The value is " + accessKeyValue + ".\n  harness.update is in {path}."},
		{"harness: {links: qrk_l}\n", "Set harness.links to checkout or none.\n  The value is " + runKeyValue + ".\n  harness.links is in {path}."},
		{"git: {timeout: qak_t}\n", "Set git.timeout to a duration above zero, such as 10m.\n  The value is " + accessKeyValue + ".\n  git.timeout is in {path}."},
		{"env: {QAK_X-1: v}\n", "Rename each variable in env to an environment variable name.\n  " + accessKeyPhrase + " is not one.\n  env is in {path}."},
		{"harness: {tiers: {qak_tier: m}}\n", "Use one of the tiers light, fast, and thinking.\n  " + accessKeyPhrase + " is not a tier.\n  The name is in harness.tiers." + accessKeyPhrase + ".\n  The file is {path}."},
		{"harness: {tiers: {fast: \"${qory:qak_x}\"}}\n", "Set harness.tiers.fast to a model id.\n  " + accessKeyValue + " is a tier placeholder, which goes only in an agent's model.\n  harness.tiers.fast is in {path}."},
		{"worktree: {name: qak_w}\n", "Add {branch} to worktree.name.\n  worktree.name is " + accessKeyValue + ", and without {branch} two worktrees share one directory.\n  worktree.name is in {path}."},
		{"worktree: {name: \"qak_/{branch}\"}\n", "Remove the slash from worktree.name.\n  worktree.name is " + accessKeyValue + ". It is one directory name under worktree.dir.\n  worktree.name is in {path}."},
		{"worktree: {pr: refs/qak_}\n", "Set worktree.pr to a ref under refs/ with {n}, as refs/pull/{n}/head.\n  worktree.pr is " + accessKeyValue + ", and {n} stands for the pull request number.\n  worktree.pr is in {path}."},
		{"worktree: {branch: qrk_b}\n", "Set worktree.branch to delete or keep.\n  The value is " + runKeyValue + ".\n  worktree.branch is in {path}."},
		{"worktree: {link: [/opt/qak_l]}\n", "Write the entry of worktree.link as {from: " + accessKeyValue + ", to: <path in the worktree>}.\n  worktree.link lists " + accessKeyValue + ", which is not a path inside the checkout.\n  worktree.link is in {path}."},
		{"worktree: {copy: [{from: /opt/qak_c}]}\n", "Add a to to the entry of worktree.copy, as {from: " + accessKeyValue + ", to: <path in the worktree>}.\n  worktree.copy lists " + accessKeyValue + " with no to, and a path from outside the checkout sets where it goes.\n  worktree.copy is in {path}."},
		{"worktree: {link: [../qak_l]}\n", "Set the entry of worktree.link to a path inside the checkout.\n  worktree.link lists " + accessKeyValue + ", which is not one.\n  worktree.link is in {path}."},
		{"worktree: {link: [{from: /opt/qak_f, to: ../qrk_t}]}\n", "Set the to of the entry of worktree.link to a path inside the worktree.\n  worktree.link maps " + accessKeyValue + " to " + runKeyValue + ", which is not one.\n  worktree.link is in {path}."},
		{"harness: {launch: {qak_rt: {command: \" \"}}}\n", "Set harness.launch." + accessKeyPhrase + ".command to the program, or remove it.\n  An empty command is no program. Leave it out for the runtime's program.\n  harness.launch." + accessKeyPhrase + " is in {path}."},
		{"harness: {launch: {qak_rt: {args: [[]]}}}\n", "Set harness.launch." + accessKeyPhrase + ".args[0] to a word or a list of words.\n  The group is empty.\n  harness.launch." + accessKeyPhrase + " is in {path}."},
		{"harness: {launch: {qak_rt: {env: {QRK_X-1: v}}}}\n", "Rename each variable in harness.launch." + accessKeyPhrase + ".env to an environment variable name.\n  " + runKeyPhrase + " is not one.\n  harness.launch." + accessKeyPhrase + " is in {path}."},
		{"harness: {launch: {qak_rt: {env: {QORY_HARNESS_HOME: v}}}}\n", "Remove QORY_HARNESS_HOME from harness.launch." + accessKeyPhrase + ".env.\n  Every launch sets it to the home.\n  harness.launch." + accessKeyPhrase + " is in {path}."},
	} {
		root := t.TempDir()
		path := filepath.Join(root, "qory.yaml")
		write(t, path, c.body)
		_, err := config.Load(root, true)
		if want := strings.ReplaceAll(c.want, "{path}", path); err == nil || err.Error() != want {
			t.Errorf("%q: %v, want %q", c.body, err, want)
		}
	}
}

// TestAHomeRefusalPrintsAPhraseForASecret is harness.home and a worktree.link entry
// under ~ that contain an access key secret, read when the home directory is unknown:
// the refusal prints the value phrase in place of the path.
func TestAHomeRefusalPrintsAPhraseForASecret(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	for _, c := range []struct{ body, want string }{
		{"harness: {home: ~/qak_h}\n", "Set HOME, or write harness.home as an absolute path.\n  harness.home is " + accessKeyValue + ", and the home directory is unknown: "},
		{"worktree: {link: [{from: ~/qak_l, to: x}]}\n", "Set HOME, or write the entry of worktree.link without ~.\n  worktree.link lists " + accessKeyValue + ", and the home directory is unknown: "},
	} {
		root := t.TempDir()
		path := filepath.Join(root, "qory.yaml")
		write(t, path, c.body)
		_, err := config.Load(root, true)
		if want := c.want; err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "qak_") {
			t.Errorf("%q: %v, want %q", c.body, err, want)
		}
		if cause := errors.Unwrap(err); cause == nil || !strings.Contains(cause.Error(), "HOME") {
			t.Errorf("%q: the refusal unwraps to %v, want the error of the home directory", c.body, cause)
		}
	}
}

// TestTheForagerRowsPrintAPhraseForAValueWithASecret is the rows qory config prints for
// a forager.yaml whose values contain an access key secret or a run key: an egress
// path, gateway.listen, the TLS paths, a credential's file and adapter, a tool's command
// and the hosts it serves, the default image as a reference, an image's reference and
// runtime, wall.images_allow, the wall's command, helper, user, mounts and limits, and
// the via's CA file. Each such value is the value phrase, each such name the name
// phrase, and a plain value prints as before.
func TestTheForagerRowsPrintAPhraseForAValueWithASecret(t *testing.T) {
	isolate(t)
	foragerFile(t, `gateway:
  egress:
    mode: observe
    paths: {api.example: [/qak_p, /plain]}
  listen: qak_h:8443
  tls: {certificate: /etc/qak_c.pem, key: /etc/qrk_k.pem}
  server:
    url: https://qory.example
    via: {url: https://via.example, ca_file: /etc/qak_ca.pem}
  credentials:
    f: {file: /etc/qak_f, hosts: [api.model.example], auth: {scheme: bearer}}
    a: {adapter: [/opt/qrk_a, x], hosts: [api.model.example]}
  tools:
    t: {command: [/opt/qak_t], serves: [qrk_s.example, a.example]}
wall:
  adapter: docker
  image: example/qak_i:1
  images: {img: {ref: "example/qak_img:1", runtime: qrk_rt}}
  images_allow: ["registry.example/qak_x/", "registry.example/plain/"]
  command: /opt/qak_cmd
  helper: /opt/qrk_helper
  user: qak_user
  mounts: ["/data/qak_m:ro", /data/plain]
  cpus: qak_1
  memory: qrk_1g
  shm_size: qak_64m
`)
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	rows := r.Rows()
	got := map[string]string{}
	for _, row := range rows {
		got[row.Key] = row.Value
	}
	for key, value := range map[string]string{
		"gateway.egress.paths":       "api.example " + accessKeyValue + ", /plain",
		"gateway.listen":             accessKeyValue,
		"gateway.tls.certificate":    accessKeyValue,
		"gateway.tls.key":            runKeyValue,
		"gateway.server.via.ca_file": accessKeyValue,
		"gateway.credentials.f":      "file " + accessKeyValue,
		"gateway.credentials.a":      "adapter " + runKeyValue,
		"gateway.tools.t":            accessKeyValue + ", serves " + runKeyPhrase + ", a.example",
		"wall.image":                 accessKeyValue + " (a reference)",
		"wall.images.img":            accessKeyValue + ", runtime " + runKeyValue,
		"wall.images_allow":          accessKeyValue + ", registry.example/plain/",
		"wall.command":               accessKeyValue,
		"wall.helper":                runKeyValue,
		"wall.user":                  accessKeyValue,
		"wall.mounts":                accessKeyValue + ":ro, /data/plain",
		"wall.cpus":                  accessKeyValue,
		"wall.memory":                runKeyValue,
		"wall.shm_size":              accessKeyValue,
	} {
		if got[key] != value {
			t.Errorf("row %s = %q, want %q", key, got[key], value)
		}
	}
	noSecret(t, rows)
}

// TestAMountWithASecretIsAPhrase is a --mount value that contains an access key secret
// or a run key and is not an absolute path: the refusal prints the value phrase in place
// of the mount. A plain mount prints quoted, as before.
func TestAMountWithASecretIsAPhrase(t *testing.T) {
	for v, shown := range map[string]string{"qak_m:ro": accessKeyValue, "data/QRK_m": runKeyValue, "data": `"data"`} {
		want := "Write the mount as an absolute path, with :ro after it for a read-only one.\n  The mount is " + shown + "."
		if _, err := config.ParseMount(v); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", v, err, want)
		}
	}
}

// TestAViaCAFileWithASecretIsAPhrase is a gateway.server.via.ca_file whose path
// contains an access key secret, missing and then holding no PEM certificate: each
// refusal prints the value phrase in place of the path, and the missing file's error
// still matches fs.ErrNotExist.
func TestAViaCAFileWithASecretIsAPhrase(t *testing.T) {
	isolate(t)
	ca := filepath.Join(t.TempDir(), "qak_ca.pem")
	path := foragerFile(t, "gateway:\n  server:\n    url: https://qory.example\n    via: {url: https://via.example, ca_file: "+ca+"}\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	err = r.CheckVia()
	if want := "Make the file of gateway.server.via.ca_file readable, or set another file.\n  The file " + accessKeyValue + " cannot be read: no such file or directory.\n  gateway.server.via.ca_file is in " + path + "."; err == nil || err.Error() != want || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v, want %q", err, want)
	}
	if err := os.WriteFile(ca, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if want := "Set gateway.server.via.ca_file to a file with a PEM certificate.\n  The file " + accessKeyValue + " contains no PEM certificate.\n  gateway.server.via.ca_file is in " + path + "."; r.CheckVia() == nil || r.CheckVia().Error() != want {
		t.Errorf("no PEM: %v, want %q", r.CheckVia(), want)
	}
}

// TestAProgramWithASecretInItsPathIsAPhrase is an adapter's or a tool's program whose
// name or path contains an access key secret, refused by config.Program: by an absolute
// path no user may run, by a name the PATH does not hold, inside a workspace directory,
// in a directory every user may write, and through a relative PATH entry. Each refusal prints the value phrase in place
// of the program and of each directory, and says the rest as before.
func TestAProgramWithASecretInItsPathIsAPhrase(t *testing.T) {
	isolate(t)
	onPath(t)
	workspace := filepath.Join(t.TempDir(), "qak_ws")
	inside := fakeIntegration(t, workspace, "tool", "{}")
	writable := filepath.Join(t.TempDir(), "qak_w")
	open := fakeIntegration(t, writable, "tool", "{}")
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		workspace []string
		want      string
	}{
		{"/opt/qak_tool", nil, accessKeyValue + " is not a program this user may run"},
		{"QRK_tool", nil, runKeyValue + " is not on the PATH; install it there, or give its absolute path"},
		{inside, []string{workspace}, accessKeyValue + ", inside " + accessKeyValue + ", which a run may write; qory runs a tool from outside the checkout and the container's read-write mounts"},
		{open, nil, accessKeyValue + ", and " + accessKeyValue + " may be written by every user; qory runs a program only root and its owner may change"},
	} {
		if _, err := config.Program(c.name, c.workspace, "a tool"); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	dir := t.TempDir()
	fakeIntegration(t, filepath.Join(dir, "qak_rel"), "reltool", "{}")
	t.Chdir(dir)
	t.Setenv("PATH", "qak_rel"+string(os.PathListSeparator)+os.Getenv("PATH"))
	want := "reltool is found as " + accessKeyValue + " through the PATH entry " + accessKeyValue + ", which is relative; qory runs a program from an absolute directory of the PATH; install it in one, or give its absolute path"
	if _, err := config.Program("reltool", nil, "a tool"); err == nil || err.Error() != want {
		t.Errorf("relative PATH entry: %v, want %q", err, want)
	}
}

// TestAnIntegrationProgramWithASecretIsAPhrase is an integration with a plain key whose
// program's path contains an access key secret: a program no user may run, one whose
// describe fails with a run key in its error output, and one that plays no role qory
// expands. Each refusal prints the value phrase in place of each word that contains the
// secret, and the key as before.
func TestAnIntegrationProgramWithASecretIsAPhrase(t *testing.T) {
	isolate(t)
	const fixProgram = "Fix the program of gateway.integrations.plain.\n  "
	path := foragerFile(t, "gateway:\n  integrations:\n    plain: {program: /opt/qak_prog}\n")
	where := "\n  gateway.integrations.plain is in " + path + "."
	if _, err := expand(t); err == nil || err.Error() != fixProgram+accessKeyValue+" is not a program this user may run."+where {
		t.Errorf("no program: %v", err)
	}
	program := filepath.Join(t.TempDir(), "qak_d", "prog")
	fakeIntegration(t, filepath.Dir(program), "prog", "{}")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho 'the token qrk_x is wrong' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path = foragerFile(t, "gateway:\n  integrations:\n    plain: {program: "+program+"}\n")
	where = "\n  gateway.integrations.plain is in " + path + "."
	if _, err := expand(t); err == nil || err.Error() != fixProgram+accessKeyValue+" describe: exit status 3: the token "+runKeyValue+" is wrong."+where {
		t.Errorf("describe fails: %v", err)
	}
	fakeIntegration(t, filepath.Dir(program), "prog", `{"version": 1, "name": "queue", "title": "Queue", "program_version": "1", "settings": {"type": "object"}, "roles": {"example_role": {}}}`)
	if _, err := expand(t); err == nil || err.Error() != "Remove gateway.integrations.plain, or give it a program that defines a credential.\n  "+accessKeyValue+" plays the role example_role, none of which qory expands, and defines nothing."+where {
		t.Errorf("plays no role: %v", err)
	}
}

// TestARelativeSettingsFileWithASecretInItsNameIsAPhrase is an integration with a plain
// key whose settings key ending in _file contains an access key secret and is set to a
// relative path: the refusal prints the name phrase in place of the key's name.
func TestARelativeSettingsFileWithASecretInItsNameIsAPhrase(t *testing.T) {
	isolate(t)
	dir := onPath(t)
	fakeIntegration(t, dir, "qory-plain", fixture(t, "acme-tracker.json"))
	path := foragerFile(t, "gateway:\n  integrations:\n    plain: {settings: {qak_key_file: keys/k.pem}}\n")
	want := "Set settings." + accessKeyPhrase + " to an absolute path.\n  A relative path moves with the working folder, and qory takes the file by its absolute path.\n  gateway.integrations.plain is in " + path + "."
	if _, err := expand(t); err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
