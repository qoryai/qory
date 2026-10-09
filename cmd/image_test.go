package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/session/runtimes/catalog"

	"github.com/qoryai/qory/cmd"
)

// probeLines are the lines of a probe's report for each of its checks but claude, which
// pass, and the fact about Docker.
const probeLines = `{"name":"home","result":"pass","detail":"HOME is /home/agent, and the user 65532:65532 writes in it"},` +
	`{"name":"authorities","result":"pass","detail":"the authorities are in /etc/ssl/certs/ca-certificates.crt, 1 certificates"},` +
	`{"name":"shell","result":"pass","detail":"/bin/sh runs the runtime hooks and the API-key approval"},` +
	`{"name":"git","result":"pass","detail":"git version 2.47.3"},` +
	`{"name":"gh","result":"pass","detail":"gh version 2.101.0"},` +
	`{"name":"docker","result":"info","detail":"no dockerd: the image carries no Docker of the agent own"}`

// fakeImageDocker puts a program named docker first on the PATH. It logs every command
// line, runs an engine for linux/amd64 that holds every image but example.com/missing:1,
// exports a container's files as a tar stream of one program, and answers a run with a
// probe's report: one whose claude line fails for example.com/broken:1, one whose lines
// pass otherwise.
func fakeImageDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "docker.log")
	files := filepath.Join(dir, "files")
	writeFile(t, filepath.Join(files, "usr", "bin", "git"), "")
	writeFile(t, filepath.Join(dir, "docker"), `#!/bin/sh
echo "$*" >> `+log+`
for last; do :; done
case "$1" in
version) echo linux/amd64 ;;
image)
	case "$last" in
	example.com/missing:1) echo "Error response from daemon: No such image: $last" >&2; exit 1 ;;
	*) echo '{"Os":"linux","Architecture":"amd64","RepoDigests":[],"Config":{"Env":["PATH=/usr/bin","HOME=/home/agent"]}}' ;;
	esac ;;
create) echo c0ffee ;;
export) tar -cf - -C `+files+` usr ;;
rm) ;;
run)
	case "$*" in
	*example.com/broken:1*) echo '{"version":1,"checks":[`+probeLines+`,{"name":"claude","result":"fail","detail":"no claude on the image PATH, /usr/bin"}]}' ;;
	*) echo '{"version":1,"checks":[`+probeLines+`,{"name":"claude","result":"pass","detail":"claude at /usr/local/bin/claude is 2.1.273"}]}' ;;
	esac ;;
*) exit 64 ;;
esac
`)
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// imageForager writes the machine's forager.yaml with a wall whose helper is helper, and
// image as wall.image when it is not empty.
func imageForager(t *testing.T, helper, image string) {
	t.Helper()
	wall := "wall:\n  adapter: docker\n  helper: " + helper + "\n"
	if image != "" {
		wall += "  image: " + image + "\n"
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\n"+wall)
}

// TestImageCheckChecksTheMachinesImage checks wall.image of forager.yaml with no
// argument: the lines from outside and from the probe print, the fact that neither
// passes nor fails prints as a field, and the probe runs as the wall runs an agent, with
// the version Forager's descriptor for claude names.
func TestImageCheckChecksTheMachinesImage(t *testing.T) {
	emptyDir(t)
	log := fakeImageDocker(t)
	helper := staticELF(t)
	imageForager(t, helper, "example.com/agent:1")
	out, err := run(t, "image", "check")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out,
		"qory image check · engine linux/amd64",
		"probe   "+helper+", amd64",
		"example.com/agent:1\n✓ the engine holds it, for linux/amd64",
		"✓ the image sets HOME=/home/agent",
		"✓ HOME is /home/agent, and the user 65532:65532 writes in it",
		"✓ none of the image's 3 files is setuid or setgid or has capabilities of its own",
		"  reference  not pinned by digest, and the engine holds no digest of it: a local build",
		"  docker     no dockerd",
		"✓ example.com/agent:1 has what the wall needs",
	)
	rt, err := catalog.Lookup("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "create --pull never --network none --entrypoint /qory/qory example.com/agent:1\nexport c0ffee\nrm --force --volumes c0ffee\n", "run --rm --pull never --network none --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges --init --mount type=bind,src="+helper+",dst=/qory/qory,readonly --entrypoint /qory/qory example.com/agent:1 image probe --claude-version "+rt.Version()+"\n")
}

// TestImageCheckFailsWhenOneImageFails checks three images, one the engine does not hold
// and one whose probe finds no claude: each says what is wrong, the last line counts
// them, and the exit status is 1.
func TestImageCheckFailsWhenOneImageFails(t *testing.T) {
	emptyDir(t)
	fakeImageDocker(t)
	imageForager(t, staticELF(t), "")
	out, err := run(t, "image", "check", "example.com/agent:1", "example.com/missing:1", "example.com/broken:1")
	if cmd.ExitCode(err) != 1 {
		t.Fatalf("exit %d, %v\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out,
		"✗ the engine does not hold example.com/missing:1; docker pull example.com/missing:1",
		"✗ no claude on the image PATH",
		"✗ 2 of 3 images lack what the wall needs: example.com/missing:1, example.com/broken:1",
	)
}

// TestImageCheckRefusesWhatItCannotCheck pins the input errors, before the engine is
// asked anything: no image named or set, a word that is no reference, and a probe that
// is not a static Linux build.
func TestImageCheckRefusesWhatItCannotCheck(t *testing.T) {
	emptyDir(t)
	log := fakeImageDocker(t)
	imageForager(t, staticELF(t), "")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"image", "check"}, "name an image to check, or set wall.image"},
		{[]string{"image", "check", "--", "-v"}, `"-v" is not an image reference`},
	} {
		if out, err := run(t, c.args...); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (exit %d), want %q\n%s", c.args, err, cmd.ExitCode(err), c.want, out)
		}
	}
	script := filepath.Join(t.TempDir(), "qory")
	writeFile(t, script, "#!/bin/sh\n")
	imageForager(t, script, "example.com/agent:1")
	if _, err := run(t, "image", "check"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "not a Linux executable") {
		t.Errorf("a helper that is a script: %v", err)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("the engine was asked before the input was checked")
	}
}

// TestImageCheckRefusesABuildForAnotherEngine gives an arm64 build of qory for an engine
// that runs amd64: the probe would not run there, whatever the image, so the check is
// refused before any image is read.
func TestImageCheckRefusesABuildForAnotherEngine(t *testing.T) {
	emptyDir(t)
	log := fakeImageDocker(t)
	helper := staticELF(t)
	data, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	data[18] = 0xb7 // aarch64
	if err := os.WriteFile(helper, data, 0o755); err != nil {
		t.Fatal(err)
	}
	imageForager(t, helper, "example.com/agent:1")
	out, err := run(t, "image", "check")
	if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "is for arm64, and the engine runs linux/amd64; set wall.helper to the Linux build for amd64") {
		t.Fatalf("exit %d, %v\n%s", cmd.ExitCode(err), err, out)
	}
	lines, _ := os.ReadFile(log)
	lacks(t, string(lines), "image inspect", "run --rm")
}

// TestImageCheckReadsANameOfWallImages checks wall.image when it names an image of
// wall.images, and such a name given, as qory run reads them: as that image's ref, with
// the name in the heading and the last line.
func TestImageCheckReadsANameOfWallImages(t *testing.T) {
	emptyDir(t)
	log := fakeImageDocker(t)
	helper := staticELF(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), `apiVersion: qory.dev/v1alpha1
wall:
  adapter: docker
  helper: `+helper+`
  image: go
  images:
    go:
      ref: example.com/agent-go:1
    broken:
      ref: example.com/broken:1
`)
	out, err := run(t, "image", "check")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "go (example.com/agent-go:1)\n✓ the engine holds it, for linux/amd64", "✓ go has what the wall needs")
	out, err = run(t, "image", "check", "broken", "example.com/agent:1")
	if cmd.ExitCode(err) != 1 {
		t.Fatalf("exit %d, %v\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "broken (example.com/broken:1)\n", "✗ 1 of 2 images lack what the wall needs: broken")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "--entrypoint /qory/qory example.com/agent-go:1 image probe", "--entrypoint /qory/qory example.com/broken:1 image probe")
	lacks(t, string(data), " go\n", " broken\n")
}
