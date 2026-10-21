package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// printsNoKey fails the test when the error or the output contains qak_ or qrk_ in any
// letter case.
func printsNoKey(t *testing.T, out string, err error) {
	t.Helper()
	text := strings.ToLower(out)
	if err != nil {
		text += strings.ToLower(err.Error())
	}
	if strings.Contains(text, "qak_") || strings.Contains(text, "qrk_") {
		t.Errorf("the output contains a secret: %v\n%s", err, out)
	}
}

// nameShown and valueShown are Forager's phrases for a name and a value that contain an
// access key secret.
const (
	nameShown  = "(a name that contains an access key secret)"
	valueShown = "(a value that contains an access key secret)"
)

// TestComposeAndInspectPrintNoSecretInNames is a stack whose name, module, link,
// description and entries contain an access key secret or a run key: compose,
// compose --verbose, inspect and remove print each as Forager's phrase.
func TestComposeAndInspectPrintNoSecretInNames(t *testing.T) {
	root := secretSourceCheckout(t)
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\nname: qrk_app\ndescription: token-qak_d\ntarget:\n  runtime: claude\n  model: qak_model\nmodules:\n  - name: qak_m\n    source: {path: ./harness/qak_m}\n    link: qak_link\n")
	dir := filepath.Join(root, "harness", "qak_m")
	writeManifest(t, dir, "qak_m")
	writeFile(t, filepath.Join(dir, "skills", "qak_s", "SKILL.md"), "---\nname: qak_s\ndescription: S.\n---\nS.\n")
	out, err := run(t, "hc", "--verbose")
	printsNoKey(t, out, err)
	wants(t, out, "(a name that contains a run key) · claude "+valueShown, nameShown+"  "+nameShown, "link    "+nameShown+"  (module "+nameShown+")")
	out, err = run(t, "hi")
	printsNoKey(t, out, err)
	wants(t, out, "about     "+valueShown, nameShown+"  "+valueShown+"  working-tree")
	out, err = run(t, "hr")
	printsNoKey(t, out, err)
	wants(t, out, "removed "+nameShown)
}

// TestACollisionPrintsNoSecret is two modules whose names, and the skill both ship,
// contain an access key secret: the collision prints each name as Forager's phrase, in
// the table and in the fix.
func TestACollisionPrintsNoSecret(t *testing.T) {
	root := secretSourceCheckout(t)
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: qak_a\n    source: {path: ./harness/qak_a}\n  - name: qak_b\n    source: {path: ./harness/qak_b}\n")
	for _, m := range []string{"qak_a", "qak_b"} {
		dir := filepath.Join(root, "harness", m)
		writeManifest(t, dir, m)
		writeFile(t, filepath.Join(dir, "skills", "qak_s", "SKILL.md"), "---\nname: qak_s\ndescription: S.\n---\nS.\n")
	}
	// The collision is printed by the command, and the main package prints nothing of
	// the error it returns, which matches cmd.ErrReported.
	out, err := run(t, "hc")
	if !errors.Is(err, cmd.ErrReported) {
		t.Fatalf("err = %v, want the collision reported\n%s", err, out)
	}
	printsNoKey(t, out, nil)
	wants(t, out, nameShown+" is provided by 2 modules", nameShown+"  "+valueShown, "- name: "+nameShown, "skills: ["+nameShown+"]")
}

// TestAnUnknownHarnessVerbPrintsNoSecret is qory harness with a verb that contains an
// access key secret: the refusal prints it as Forager's phrase and exits 2.
func TestAnUnknownHarnessVerbPrintsNoSecret(t *testing.T) {
	newCheckout(t)
	out, err := run(t, "harness", "qak_verb")
	printsNoKey(t, out, err)
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput || !strings.HasPrefix(err.Error(), "unknown command "+valueShown) {
		t.Errorf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestTheBaseRowPrintsNoPassword is compose -f on a checkout whose qory.yaml extends a
// git URL with a password and a ref with an access key secret: the base row prints the
// password as (hidden) and the ref as Forager's phrase.
func TestTheBaseRowPrintsNoPassword(t *testing.T) {
	_, base := runnerTree(t)
	fleetCheckout(t, "qory.yaml", "  extends: {git: https://u:token-123@git.example.com/acme/harness, ref: qak_v9, path: nextjs-15}\n", consumerTarget)
	out, err := run(t, "harness", "compose", "-f", base)
	if err != nil {
		t.Fatalf("compose -f: %v\n%s", err, out)
	}
	printsNoKey(t, out, err)
	lacks(t, out, "token-123")
	wantsRow(t, out, "base", base+"  (set by -f, in place of extends https://u:(hidden)@git.example.com/acme/harness#"+valueShown+":nextjs-15)")
}

// TestTheBaseRowPrintsNoSecretPath is compose -f on a base stack under a directory whose
// name contains an access key secret: the base row prints Forager's phrase in place of
// the directory.
func TestTheBaseRowPrintsNoSecretPath(t *testing.T) {
	dir, _ := runnerTree(t)
	link := filepath.Join(tempDir(t), "qak_trees")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	fleetCheckout(t, "qory.yaml", consumerTarget)
	out, err := run(t, "harness", "compose", "-f", filepath.Join(link, "nextjs-15", "qory-stack.yaml"))
	if err != nil {
		t.Fatalf("compose -f: %v\n%s", err, out)
	}
	printsNoKey(t, out, err)
	parent, err := filepath.EvalSymlinks(filepath.Dir(link))
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "base", filepath.Join(parent, nameShown, "nextjs-15", "qory-stack.yaml")+"  (set by -f)")
}

// TestWorktreeRowsPrintNoSecret is a worktree added and removed on a branch whose name
// contains an access key secret, with run commands and a link that contain one: add
// with --verbose, list and remove print each as Forager's value phrase.
func TestWorktreeRowsPrintNoSecret(t *testing.T) {
	root := newCheckout(t)
	isolateTheRest(t, root)
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\n")
	configure(t, root, nil, []string{"worktree:", "  link: [qak_env]", "  run:", "    add: ['true token-qak_add']", "    remove: ['true token-qak_remove']"})
	writeFile(t, filepath.Join(root, "qak_env"), "X=1\n")
	runGit(t, root, "add", "qory.yaml")
	runGit(t, root, "commit", "-q", "-m", "config")
	localOrigin(t, root)
	runGit(t, root, "push", "--quiet", "origin", "main")
	runGit(t, root, "remote", "set-head", "origin", "main")
	out, err := run(t, "wa", "qak_feature", "--no-compose", "-v")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	printsNoKey(t, out, err)
	wantsRow(t, out, "path", valueShown)
	wantsRow(t, out, "branch", valueShown+"  (new off origin/main)")
	wantsRow(t, out, "pushes to", valueShown)
	wantsRow(t, out, "linked", valueShown+"  (from the main checkout)")
	wantsRow(t, out, "ran", valueShown)
	out, err = run(t, "wl")
	printsNoKey(t, out, err)
	wants(t, out, valueShown)
	out, err = run(t, "wr", "qak_feature", "--force", "--delete-branch")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	printsNoKey(t, out, err)
	wantsRow(t, out, "ran", valueShown)
	wantsRow(t, out, "removed", valueShown)
	out, err = run(t, "wr", "qak_gone")
	printsNoKey(t, out, err)
	if err == nil || !strings.Contains(err.Error(), "no worktree is at "+valueShown) {
		t.Errorf("err = %v", err)
	}
}

// TestImageCheckPrintsNoSecret checks an image named in wall.images by a name that
// contains an access key secret, with a reference that contains one, and a reference
// that contains one given as the argument: each prints as Forager's phrase.
func TestImageCheckPrintsNoSecret(t *testing.T) {
	emptyDir(t)
	fakeImageDocker(t)
	helper := staticELF(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  helper: "+helper+"\n  images:\n    qak_go:\n      ref: example.com/qak_go:1\n")
	out, err := run(t, "image", "check", "qak_go", "example.com/qak_agent:1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	printsNoKey(t, out, err)
	wants(t, out, nameShown+" ("+valueShown+")\n✓ the engine holds it", "\n"+valueShown+"\n✓ the engine holds it")
}

// TestAcceptCheckoutPrintsNoSecret changes acting keys whose names and values contain
// an access key secret, in qory.yaml and in git's configuration, since a walled run
// began: the accept lists each as Forager's phrase.
func TestAcceptCheckoutPrintsNoSecret(t *testing.T) {
	root := newCheckout(t)
	file := filepath.Join(root, "qory.yaml")
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n")
	recordCheckout(t, root)
	appendTo(t, file, "env:\n  qak_TOKEN: a-value\nworktree:\n  run:\n    add: [./setup token-qak_y]\n")
	runGit(t, root, "config", "branch.qak_x.description", "d")
	defer cmd.SetTerminal(true)()
	out, err := runIn(t, "n\n", "setup", "repo", "--accept-checkout")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	printsNoKey(t, out, err)
	wants(t, out, "  qory.yaml: "+nameShown+"\n", "  qory.yaml: worktree.run.add "+valueShown+"\n", "  "+nameShown+"\n")
}

// TestAModuleThatSetsAModelVariablePrintsNoSecret is a module whose name contains an
// access key secret and which exports a model variable, and a CLAUDE_CONFIG_DIR whose
// value contains one: the refusal prints the module's name and the value as Forager's
// phrases.
func TestAModuleThatSetsAModelVariablePrintsNoSecret(t *testing.T) {
	root := secretSourceCheckout(t)
	dir := filepath.Join(root, "harness", "qak_m")
	writeFile(t, filepath.Join(dir, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: qak_m\nenv:\n  CLAUDE_CODE_SUBAGENT_MODEL: .\n")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "# m\n")
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: qak_m\n    source: {path: ./harness/qak_m}\n")
	out, err := run(t, "harness", "compose")
	printsNoKey(t, out, err)
	if err == nil || !strings.HasPrefix(err.Error(), "Remove env.CLAUDE_CODE_SUBAGENT_MODEL from qory-module.yaml in module "+nameShown+".\n") {
		t.Errorf("err = %v\n%s", err, out)
	}
	machineConfig(t, "env:\n  CLAUDE_CONFIG_DIR: qak_dir\n")
	out, err = run(t, "harness", "compose")
	printsNoKey(t, out, err)
	if err == nil || !strings.Contains(err.Error(), "and cannot check "+valueShown+".") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

// TestSetupShellPrintsNoSecret is qory setup shell under a SHELL whose name contains an
// access key secret: the refusal prints it as Forager's phrase.
func TestSetupShellPrintsNoSecret(t *testing.T) {
	emptyDir(t)
	t.Setenv("SHELL", "/bin/qak_sh")
	out, _, err := runSplit(t, "n\n", "setup", "shell")
	printsNoKey(t, out, err)
	if err == nil || !strings.HasPrefix(err.Error(), "the "+valueShown+" shell is not supported") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

// TestSetupCompletionPrintsNoSecret is qory setup completion for a shell whose name
// contains an access key secret: the refusal prints it as Forager's phrase.
func TestSetupCompletionPrintsNoSecret(t *testing.T) {
	emptyDir(t)
	out, err := run(t, "setup", "completion", "qak_sh")
	printsNoKey(t, out, err)
	if err == nil || !strings.HasPrefix(err.Error(), "the "+valueShown+" shell is not supported") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("err = %v\n%s", err, out)
	}
}

// TestGatewayListenPrintsNoSecret is qory gateway with a --listen that contains an
// access key secret and is not host:port: the refusal prints it as Forager's phrase.
func TestGatewayListenPrintsNoSecret(t *testing.T) {
	emptyDir(t)
	serverFile(t, newFakeServer(t, ""), "  listen: 127.0.0.1:0\n")
	out, err := run(t, "gateway", "--listen", "qak_host")
	printsNoKey(t, out, err)
	if err == nil || err.Error() != "--listen "+valueShown+" is not host:port, such as 0.0.0.0:8443" || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("err = %v\n%s", err, out)
	}
}

// TestAReportOfAnotherVersionPrintsNoSecret is a harness composed into a --home whose
// path contains an access key secret, with its report rewritten to another version:
// inspect refuses it and prints the report's path as Forager's value phrase.
func TestAReportOfAnotherVersionPrintsNoSecret(t *testing.T) {
	root := secretSourceCheckout(t)
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {path: ./harness/core}\n")
	writeManifest(t, filepath.Join(root, "harness", "core"), "core")
	homes := filepath.Join(tempDir(t), "qak_homes")
	if out, err := run(t, "harness", "compose", "--home", homes); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	reports, err := filepath.Glob(filepath.Join(homes, "*-report.json"))
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports %v, %v", reports, err)
	}
	data, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, reports[0], strings.Replace(string(data), `"version": 1`, `"version": 99`, 1))
	out, err := run(t, "harness", "inspect", "--home", homes)
	printsNoKey(t, out, err)
	if err == nil || err.Error() != valueShown+": report version 99 is not one this qory reads; versions: 1" {
		t.Errorf("err = %v\n%s", err, out)
	}
}
