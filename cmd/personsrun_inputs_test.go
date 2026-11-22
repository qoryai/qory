package cmd_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// TestAPersonsUnwalledRunGetsNoInputs is a run with no run key and no run code, and no wall:
// it composes into the checkout's .qory/harness, as before the copy of inputs.json, and the
// home has no inputs.json while the agent runs and after.
func TestAPersonsUnwalledRunGetsNoInputs(t *testing.T) {
	root, _ := committedFixture(t)
	home := filepath.Join(root, ".qory", "harness")
	var at string
	var copied error
	agentInCopy(t, func(s session.SessionSpec) {
		at = s.HarnessHome
		_, copied = os.Lstat(filepath.Join(s.HarnessHome, "inputs.json"))
	})
	if out, err := run(t, "run", "claude", "--wall", "none", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	sameFolder(t, "the home", at, home)
	if !os.IsNotExist(copied) {
		t.Errorf("the home had inputs.json while the agent ran: %v", copied)
	}
	if _, err := os.Lstat(filepath.Join(home, "inputs.json")); !os.IsNotExist(err) {
		t.Errorf("the home has inputs.json after the run: %v", err)
	}
}

// TestAPersonsWalledRunsPushChecksGetNoInputs is a walled run against a server with no run
// key and no run code, which has a control plane, whose harness declares a push check for spec: it composes into a home for that run
// under qory/homes in the cache, and its check's container starts with the pushed files and
// no --inputs, and mounts no inputs.json. The home has no inputs.json, unless the harness has
// a submit program: then it has the file with the member qory alone, and the check still gets
// no --inputs.
func TestAPersonsWalledRunsPushChecksGetNoInputs(t *testing.T) {
	for _, program := range []bool{false, true} {
		t.Run(fmt.Sprintf("submit program %v", program), func(t *testing.T) {
			personsWalledRunsPushCheck(t, program)
		})
	}
}

// personsWalledRunsPushCheck is [TestAPersonsWalledRunsPushChecksGetNoInputs] with a harness
// that has a submit program, or none.
func personsWalledRunsPushCheck(t *testing.T, program bool) {
	root := checksCheckout(t, "")
	if program {
		for name, content := range programModule() {
			writeFile(t, filepath.Join(root, name), content)
		}
		stack := filepath.Join(root, "qory.yaml")
		writeFile(t, stack, strings.Replace(readText(t, stack), "    - name: checks\n", "    - name: forge\n      source: {path: modules/forge}\n    - name: checks\n", 1))
		runGit(t, root, "add", "-A")
		runGit(t, root, "commit", "--quiet", "-m", "third")
	}
	docker, log := checkDocker(t, 0, "")
	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"observe"}}`)
	serverFile(t, srv, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	t.Setenv("QORY_RUN_KEY", "")
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	api := newPushAPI(t)
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		RunAPI: api.base(),
		OnOpen: func(spec session.OpenSpec) {
			api.mu.Lock()
			defer api.mu.Unlock()
			api.runID = spec.RunID
		},
		SetRunAPICheck: func(c func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
			check = c
			return nil
		},
	})
	defer restore()
	var home string
	var copied error
	var refusal *session.RunAPIRefusal
	var checkErr error
	agentInCopy(t, func(s session.SessionSpec) {
		home = s.HarnessHome
		_, copied = os.Lstat(filepath.Join(s.HarnessHome, "inputs.json"))
		if check == nil {
			t.Error("the walled run's open has no RunAPICheck")
			return
		}
		refusal, checkErr = check(context.Background(), session.RunAPIRequest{Path: "/artifacts", Body: specPush(t)})
	})
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if checkErr != nil || refusal != nil {
		t.Fatalf("the push of spec got %+v, %v, want it to pass\n%s", refusal, checkErr, checkLog(t, log))
	}
	sameFolder(t, "the home's folder", filepath.Dir(home), runHomesFolder(t, root))
	if program && copied != nil {
		t.Errorf("the home had no inputs.json while the agent ran: %v", copied)
	}
	if !program && !os.IsNotExist(copied) {
		t.Errorf("the home had inputs.json while the agent ran: %v", copied)
	}
	created := ""
	for _, line := range strings.Split(checkLog(t, log), "\n") {
		if strings.HasPrefix(line, "create ") {
			created = line
		}
	}
	if created == "" {
		t.Fatalf("no check's container was created:\n%s", checkLog(t, log))
	}
	if !regexp.MustCompile(` push spec /qory-push-check/files/spec\.md$`).MatchString(created) {
		t.Errorf("the check's program does not end with push spec and the pushed file:\n%s", created)
	}
	for _, not := range []string{"--inputs", "inputs.json"} {
		if strings.Contains(created, not) {
			t.Errorf("the check's container has %s:\n%s", not, created)
		}
	}
}

// checksCheckout is a committed git checkout, made the working directory, whose harness has
// the module hello and [checksModule], which declares push checks for branch and spec, and
// whose stack has the extra lines given, such as an extensions block.
func checksCheckout(t *testing.T, extra string) string {
	t.Helper()
	hello := helloFiles(t)
	root := newCheckout(t)
	for name, content := range checksModule {
		writeFile(t, filepath.Join(root, name), content)
	}
	if err := os.Chmod(filepath.Join(root, "modules", "checks", "bin", "check"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "modules", "hello", "qory-module.yaml"), hello["examples/hello/modules/hello/qory-module.yaml"])
	writeFile(t, filepath.Join(root, "modules", "hello", "commands", "hello.md"), hello["examples/hello/modules/hello/commands/hello.md"])
	writeFile(t, filepath.Join(root, "qory.yaml"), ownStack("apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: hello\n    source: {path: modules/hello}\n"+
		"  - name: checks\n    source: {path: modules/checks}\nworkflows:\n  hello:\n    access: write\n"+extra))
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "--quiet", "-m", "second")
	return root
}
