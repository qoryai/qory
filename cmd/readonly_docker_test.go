//go:build unix

package cmd_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// readAgent is the program of the agent's container in
// [TestARealReadRunRemovesTheSetupContainerBeforeTheAgentStarts]: it prints whether the
// name qory-setup resolves, the size of ticks.log twice, 3 seconds apart, and the result
// of a write into the checkout.
const readAgent = `#!/bin/sh
if getent hosts qory-setup; then echo "agent: qory-setup resolves"; else echo "agent: qory-setup does not resolve"; fi
first=$(wc -c < ticks.log)
sleep 3
echo "agent: ticks.log has $first and then $(wc -c < ticks.log) bytes"
touch written-by-the-agent
`

// readTicks is the devcontainer.json of that test's checkout: postStartCommand leaves a
// process running that adds to ticks.log every second.
const readTicks = `{"image": "example.com/agent:1", "postStartCommand": "nohup sh -c 'while :; do echo tick >> ticks.log; sleep 1; done' >/dev/null 2>&1 & sleep 2"}`

// dockerOut runs docker with args and returns its output, trimmed.
func dockerOut(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	c := exec.Command("docker", args...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// stepsToTheJoin is the steps of the record file at path, each as its kind and name, in
// order, up to and with the first of kind join.
func stepsToTheJoin(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e struct {
			Type string `json:"type"`
			Data struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "dev.qory.run.step_started" {
			steps = append(steps, e.Data.Kind+" "+e.Data.Name)
			if e.Data.Kind == "join" {
				break
			}
		}
	}
	return steps
}

// realWallImage builds qory for this engine's machines, as the helper of a real wall, and an
// image on alpine with git from the distribution's packages and the program agent at
// /usr/local/bin/name, which the test removes at its end. It returns the helper's path and
// the image's reference.
func realWallImage(t *testing.T, name, agent string) (helper, image string) {
	t.Helper()
	var id [4]byte
	_, _ = rand.Read(id[:])
	dir := t.TempDir()
	_, arch, _ := strings.Cut(dockerOut(t, "", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}"), "/")
	helper = filepath.Join(dir, "qory-linux")
	build := exec.Command("go", "build", "-o", helper, ".")
	build.Dir = ".."
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(dir, "image", "agent"), agent)
	writeFile(t, filepath.Join(dir, "image", "Dockerfile"), "FROM public.ecr.aws/docker/library/alpine:3.22\nRUN apk add --no-cache git\nCOPY agent /usr/local/bin/"+name+"\nRUN chmod 755 /usr/local/bin/"+name+"\nUSER 1000:1000\n")
	image = "qory-test-" + name + ":" + hex.EncodeToString(id[:])
	dockerOut(t, "", "build", "--quiet", "--tag", image, filepath.Join(dir, "image"))
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", image).Run() })
	return helper, image
}

// TestARealReadRunRemovesTheSetupContainerBeforeTheAgentStarts is a read run behind a
// real wall on this machine's engine, whose checkout's postStartCommand leaves a process
// running. The record has the step "remove the setup container" after the setup and
// before the join. Inside the agent's container the name qory-setup does not resolve,
// the file that the process added to keeps its size, and a write into the checkout fails
// with Read-only file system. It runs only with QORY_TEST_DOCKER=1 and docker on the
// PATH.
func TestARealReadRunRemovesTheSetupContainerBeforeTheAgentStarts(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	helper, image := realWallImage(t, "read-agent", readAgent)

	atGateway(t)
	listsRepositories(t)
	codeReal = true
	t.Cleanup(func() { codeReal = false })
	r := runFromCodeLinks(t, func(string) []string {
		fakeLaunch(t, "/usr/local/bin/read-agent")
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, nil, nil, map[string]string{".devcontainer/devcontainer.json": readTicks}, nil)
	if !r.joined || cmd.ExitCode(r.err) != 1 {
		record, _ := os.ReadFile(filepath.Join(r.open.RunsDir, r.open.RunID, "session.jsonl"))
		t.Fatalf("%v (exit %d), want the join and the exit of the touch\n%s\n%s", r.err, cmd.ExitCode(r.err), r.out, record)
	}
	wants(t, r.out, "agent: qory-setup does not resolve", "Read-only file system")
	lacks(t, r.out, "agent: qory-setup resolves")
	sizes := regexp.MustCompile(`agent: ticks.log has (\d+) and then (\d+) bytes`).FindStringSubmatch(r.out)
	if sizes == nil || sizes[1] == "0" || sizes[1] != sizes[2] {
		t.Errorf("the sizes of ticks.log: %q, want one size above 0\n%s", sizes, r.out)
	}
	steps := stepsToTheJoin(t, filepath.Join(r.open.RunsDir, r.open.RunID, "session.jsonl"))
	if n := len(steps); n < 3 || steps[n-2] != "wall remove the setup container" || !strings.HasPrefix(steps[n-3], "setup ") || !strings.HasPrefix(steps[n-1], "join ") {
		t.Errorf("the steps: %s", fmt.Sprint(steps))
	}
}

// writeAgent is the program of the agent's container in
// [TestARealWriteRunRemovesTheSetupContainerBeforeThePush]: it leaves a file.
const writeAgent = `#!/bin/sh
echo left > left.txt
`

// TestARealWriteRunRemovesTheSetupContainerBeforeThePush is a write run behind a real wall
// on this machine's engine, whose postStartCommand leaves a process running. As the push
// step starts, the record has the step "remove the setup container", the run has no setup
// container in the engine, and ticks.log keeps its size. The push then carries the agent's
// file. It runs only with QORY_TEST_DOCKER=1 and docker on the PATH.
func TestARealWriteRunRemovesTheSetupContainerBeforeThePush(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	helper, image := realWallImage(t, "write-agent", writeAgent)
	p := newPushRepoWith(t, "write", map[string]string{
		".devcontainer/devcontainer.json": readTicks,
		".gitignore":                      "ticks.log\n",
	})
	p.real = true
	p.flags = []string{"--home", filepath.Join(tempDir(t), "home")}
	fakeLaunch(t, "/usr/local/bin/write-agent")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
	p.pushStarted = func() {
		record, err := os.ReadFile(p.record())
		if err != nil {
			t.Fatal(err)
		}
		wants(t, string(record), "remove the setup container")
		left := dockerOut(t, "", "ps", "--all", "--filter", "label=dev.qory.run="+p.runID, "--format", "{{.Names}}")
		lacks(t, left, "setup")
		ticks := filepath.Join(p.h.dir, "ticks.log")
		first, err := os.Stat(ticks)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		second, err := os.Stat(ticks)
		if err != nil {
			t.Fatal(err)
		}
		if first.Size() == 0 || first.Size() != second.Size() {
			t.Errorf("ticks.log has %d and then %d bytes", first.Size(), second.Size())
		}
	}
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	head := p.branchHead(t)
	if head == "" {
		t.Fatalf("the forge has no branch\n%s", r.out)
	}
	if got := proxygittest.Git(t, p.bare, "diff", "--name-only", p.first, head); got != "left.txt" {
		t.Errorf("the branch differs from the start in %q", got)
	}
}

// copyAgent is the program of the agent's container in
// [TestARealWriteRunInACopyBringsTheWorkBack]: it commits a file and leaves
// another uncommitted.
const copyAgent = `#!/bin/sh
echo left > left.txt
git add left.txt
git -c user.name=agent -c user.email=agent@example.invalid commit -q -m "Add the agent's file"
echo more > more.txt
`

// TestARealWriteRunInACopyBringsTheWorkBack is a local walled write run behind a real wall on
// this machine's engine. With the link kept, the git session runs in its container before the
// close, in the step copy work out. With the link lost, the join returns an error after the
// agent's session, as a join whose link to the gateway was lost does, and the git session runs
// in its container after the close. Both times the agent's commit and its uncommitted file are
// on the run's branch, and no container of the run is left. It runs only with
// QORY_TEST_DOCKER=1 and docker on the PATH.
func TestARealWriteRunInACopyBringsTheWorkBack(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	helper, image := realWallImage(t, "copy-agent", copyAgent)
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost %t", lost), func(t *testing.T) {
			root := newCheckout(t)
			copyFixture(t, "two-modules", root)
			composedForFake(t, root, "/usr/local/bin/copy-agent")
			runGit(t, root, "add", "qory.yaml", "modules")
			runGit(t, root, "commit", "--quiet", "-m", "first")
			writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
				"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
			t.Setenv("QORY_RUN_KEY", "")
			var argv []string
			t.Cleanup(cmd.GitSessionsInDocker(func(a []string) { argv = a }))
			if lost {
				t.Cleanup(cmd.LoseLinkAfterJoin(errors.New("the link to the gateway is lost")))
			}
			start := time.Now()
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			end := time.Now().Add(time.Second)
			t.Logf("the git container: %s", strings.Join(argv, " "))
			t.Logf("qory run:\n%s", out)
			if lost {
				if err == nil {
					t.Fatalf("the run passed:\n%s", out)
				}
				wants(t, out, "the link to the gateway is lost")
			} else if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			branch := runBranchOf(t, out)
			gitOutput(t, root, "cat-file", "-e", branch+":left.txt")
			gitOutput(t, root, "cat-file", "-e", branch+":more.txt")
			log := gitOutput(t, root, "log", "--format=%s", "main.."+branch)
			t.Logf("git log main..%s:\n%s", branch, log)
			if log != "Add the work the agent left uncommitted\nAdd the agent's file" {
				t.Errorf("the run's branch has the commits %q", log)
			}
			id := strings.TrimPrefix(branch, "qory/run-")
			if left := dockerOut(t, "", "ps", "--all", "--filter", "label=dev.qory.run="+id, "--format", "{{.Names}}"); left != "" {
				t.Errorf("containers of the run are left: %s", left)
			}
			events := dockerOut(t, "", "events", "--since", start.Format(time.RFC3339), "--until", end.Format(time.RFC3339),
				"--filter", "label=dev.qory.run="+id, "--filter", "type=container",
				"--format", "{{.Action}} {{.Actor.Attributes.name}} {{.Actor.Attributes.exitCode}}")
			t.Logf("the engine's events of the run's containers:\n%s", events)
			wants(t, events, "start qory-copy-"+id, "die qory-copy-"+id+" 0", "destroy qory-copy-"+id)
			agentGone, created, relayGone := strings.Index(events, "destroy qory-"+id+"-agent"), strings.Index(events, "create qory-copy-"+id), strings.Index(events, "destroy qory-"+id+"-relay")
			if agentGone < 0 || created < agentGone {
				t.Errorf("the git container was created before the agent's container was removed:\n%s", events)
			}
			// The close removes the relay: the git container comes after it only with the link lost.
			if lost != (relayGone < created) {
				t.Errorf("the git container, lost %t, against the close:\n%s", lost, events)
			}
			steps := strings.Join(stepsAndSessions(sessionRecord(t, root)), "\n")
			if strings.Contains(steps, "copy started") == lost {
				t.Errorf("the record's steps, lost %t:\n%s", lost, steps)
			}
			wants(t, strings.Join(argv, " "), "--network none", "--cap-drop ALL", "--user 1000:1000", "--label dev.qory.run="+id)
		})
	}
}

// plantingAgent is the program of the agent's container in
// [TestARealWriteRunRunsNoFilterPlantedInASubmodule]: it plants a clean filter in the
// configuration of the submodule libs/s that appends to planted-ran at the top of the copy, applies
// it to every file there, and changes lib.txt to other bytes of the same size. It also sets
// log.showSignature and gpg.program in the copy's configuration, to a program that marks work left
// behind in the git session's folder, and moves the run's branch to a commit with a forged
// signature, which HEAD's reflog then has.
const plantingAgent = `#!/bin/sh
top=$PWD
git -C libs/s config filter.planted.clean "sh -c 'echo ran >> $top/planted-ran; cat'"
echo '* filter=planted' > libs/s/.gitattributes
echo LIB > libs/s/lib.txt
echo left > left.txt
printf '#!/bin/sh\n: > "%s/../bundle/0.left"\ncat > /dev/null\n' "$top" > .git/check-signature
chmod +x .git/check-signature
git config log.showSignature true
git config gpg.program "$top/.git/check-signature"
forged=$(printf 'tree %s\nparent %s\nauthor agent <agent@example.invalid> 1700000000 +0000\ncommitter agent <agent@example.invalid> 1700000000 +0000\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEEforged\n -----END PGP SIGNATURE-----\n\nforged\n' \
	"$(git rev-parse 'HEAD^{tree}')" "$(git rev-parse HEAD)" | git hash-object -t commit -w --stdin)
git update-ref -m forged HEAD "$forged"
`

// TestARealWriteRunRunsNoFilterPlantedInASubmodule is a local walled write run behind a real wall
// whose agent plants a clean filter in a submodule's configuration: the git session in its
// container reads the submodule's changes with a configuration it makes, so the filter never
// runs, and the submodule's change comes back under the run's prefix in the person's git folder
// of the submodule. The planted signature check never runs either, so no work stays behind. It
// runs only with QORY_TEST_DOCKER=1 and docker on the PATH.
func TestARealWriteRunRunsNoFilterPlantedInASubmodule(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	helper, image := realWallImage(t, "planting-agent", plantingAgent)
	sub := filepath.Join(tempDir(t), "sub")
	writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
	runGit(t, sub, "init", "--quiet", "-b", "main")
	runGit(t, sub, "add", "lib.txt")
	runGit(t, sub, "-c", "user.name=person", "-c", "user.email=person@example.invalid", "commit", "--quiet", "-m", "lib")
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "/usr/local/bin/planting-agent")
	runGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", sub, "libs/s")
	runGit(t, root, "add", "qory.yaml", "modules")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
	t.Setenv("QORY_RUN_KEY", "")
	t.Cleanup(cmd.GitSessionsInDocker(func([]string) {}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	t.Logf("qory run:\n%s", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "did not come back") {
		t.Errorf("part of the run's work stayed behind, so the planted signature check ran:\n%s", out)
	}
	branch := runBranchOf(t, out)
	gitOutput(t, root, "cat-file", "-e", branch+":left.txt")
	if files := gitOutput(t, root, "ls-tree", "-r", "--name-only", branch); strings.Contains(files, "planted-ran") {
		t.Errorf("the planted filter ran: the run's branch has planted-ran:\n%s", files)
	}
	prefix := runRefsOf(t, out)
	gitDir := gitOutput(t, filepath.Join(root, "libs", "s"), "rev-parse", "--path-format=absolute", "--git-dir")
	if got := gitOutput(t, gitDir, "show", prefix+"/uncommitted:lib.txt"); got != "LIB" {
		t.Errorf("lib.txt of the submodule's uncommitted is %q", got)
	}
}
