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

	"github.com/qoryai/qory/cmd"
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

// TestARealReadRunRemovesTheSetupContainerBeforeTheAgentStarts is a read run behind a
// real wall on this machine's engine, whose checkout's postStartCommand leaves a process
// running. The record has the step "remove the setup container" after the setup and
// before the join. Inside the agent's container the name qory-setup does not resolve,
// the file that the process added to keeps its size, and a write into the checkout fails
// with Read-only file system. It runs only with QORY_TEST_DOCKER=1 and docker on the
// PATH. It builds an image on alpine with git from the distribution's packages, and
// removes it after the test.
func TestARealReadRunRemovesTheSetupContainerBeforeTheAgentStarts(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	var id [4]byte
	_, _ = rand.Read(id[:])
	dir := t.TempDir()
	_, arch, _ := strings.Cut(dockerOut(t, "", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}"), "/")
	helper := filepath.Join(dir, "qory-linux")
	build := exec.Command("go", "build", "-o", helper, ".")
	build.Dir = ".."
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(dir, "image", "agent"), readAgent)
	writeFile(t, filepath.Join(dir, "image", "Dockerfile"), "FROM public.ecr.aws/docker/library/alpine:3.22\nRUN apk add --no-cache git\nCOPY agent /usr/local/bin/read-agent\nRUN chmod 755 /usr/local/bin/read-agent\nUSER 1000:1000\n")
	image := "qory-test-read-agent:" + hex.EncodeToString(id[:])
	dockerOut(t, "", "build", "--quiet", "--tag", image, filepath.Join(dir, "image"))
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", image).Run() })

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
