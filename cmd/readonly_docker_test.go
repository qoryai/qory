//go:build unix

package cmd_test

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
