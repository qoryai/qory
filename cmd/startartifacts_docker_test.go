//go:build unix

package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// startArtifactsAgent is the program of the agent's container in
// [TestARealRunKeepsTheAgentOutOfItsStartArtifacts]. It finds the run's inputs folder in the
// value of --add-dir, prints the first mount option of that folder, ro or rw, and the content
// of start-artifacts/index.json there. It then tries to write the new file planted.txt into
// start-artifacts and to append to index.json, and prints for each write whether it
// succeeded, with its error. Last it prints index.json again and whether planted.txt is there.
const startArtifactsAgent = `#!/bin/sh
in=
while [ $# -gt 0 ]; do
	if [ "$1" = --add-dir ]; then in=$2; fi
	shift
done
dir="$in/start-artifacts"
if [ -z "$in" ] || [ ! -f "$dir/index.json" ]; then echo "agent: no start-artifacts/index.json"; exit 0; fi
echo "agent: the inputs folder is mounted $(awk -v p="$in" '$5 == p { split($6, o, ","); print o[1] }' /proc/self/mountinfo)"
echo "agent: index.json before: $(cat "$dir/index.json")"
if err=$(sh -c 'echo planted > "$1"' sh "$dir/planted.txt" 2>&1); then
	echo "agent: the new file was written"
else
	echo "agent: the new file was not written: $err"
fi
if err=$(sh -c 'echo changed >> "$1"' sh "$dir/index.json" 2>&1); then
	echo "agent: index.json was changed"
else
	echo "agent: index.json was not changed: $err"
fi
echo "agent: index.json after: $(cat "$dir/index.json")"
if [ -e "$dir/planted.txt" ]; then echo "agent: planted.txt is there"; else echo "agent: planted.txt is not there"; fi
exit 0
`

// TestARealRunKeepsTheAgentOutOfItsStartArtifacts is a read run from a run code behind a real
// wall on this machine's engine. It proves that the agent cannot change the session's launch
// snapshot: in its container, in/start-artifacts of the run's inputs folder has index.json,
// the inputs folder is mounted ro, the write of a new file there and the append to
// index.json each fail with Read-only file system, and index.json keeps its content. It runs
// only with QORY_TEST_DOCKER=1 and docker on the PATH.
func TestARealRunKeepsTheAgentOutOfItsStartArtifacts(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	helper, image := realWallImage(t, "start-artifacts-agent", startArtifactsAgent)
	// The tests start Claude Code from a stand-in install, a copy of the launch's program on
	// this machine, so the launch names a file here and not the image's copy.
	agent := filepath.Join(t.TempDir(), "start-artifacts-agent")
	if err := os.WriteFile(agent, []byte(startArtifactsAgent), 0o755); err != nil {
		t.Fatal(err)
	}

	atGateway(t)
	listsRepositories(t)
	codeReal = true
	t.Cleanup(func() { codeReal = false })
	r := runFromCodeLinks(t, func(string) []string {
		fakeLaunch(t, agent)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: "+image+"\n")
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, nil, nil, nil, nil)
	t.Logf("qory run (exit %d):\n%s", cmd.ExitCode(r.err), r.out)
	if !r.joined || r.err != nil {
		record, _ := os.ReadFile(filepath.Join(r.open.RunsDir, r.open.RunID, "session.jsonl"))
		t.Fatalf("%v (exit %d), want the join and exit 0\n%s\n%s", r.err, cmd.ExitCode(r.err), r.out, record)
	}
	index := `{"artifacts":[]}`
	wants(t, r.out,
		"agent: the inputs folder is mounted ro\n",
		"agent: index.json before: "+index+"\n",
		"agent: the new file was not written: ",
		"planted.txt: Read-only file system",
		"agent: index.json was not changed: ",
		"index.json: Read-only file system",
		"agent: index.json after: "+index+"\n",
		"agent: planted.txt is not there")
	lacks(t, r.out, "agent: no start-artifacts/index.json", "agent: the new file was written", "agent: index.json was changed", "agent: planted.txt is there")
}
