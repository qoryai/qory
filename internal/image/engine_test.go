package image_test

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// fakeEngine stands in for the docker command: it answers the version with its
// platform, an inspect with the image it holds, and a run with the report, and records
// every command line.
type fakeEngine struct {
	platform string
	held     map[string]string // reference to what inspect prints
	report   string
	runErr   string // what a run prints on standard error, and exits 1, when set
	lines    []string
}

func (f *fakeEngine) run(_ context.Context, argv []string) ([]byte, []byte, error) {
	f.lines = append(f.lines, strings.Join(argv, " "))
	switch {
	case argv[1] == "version":
		return []byte(f.platform + "\n"), nil, nil
	case argv[1] == "image" && argv[2] == "inspect":
		ref := argv[len(argv)-1]
		if doc, ok := f.held[ref]; ok {
			return []byte(doc + "\n"), nil, nil
		}
		return nil, []byte("Error response from daemon: No such image: " + ref + "\n"), errors.New("exit status 1")
	case argv[1] == "run":
		if f.runErr != "" {
			return nil, []byte(f.runErr + "\n"), errors.New("exit status 1")
		}
		return []byte(f.report + "\n"), nil, nil
	}
	return nil, []byte("unknown\n"), errors.New("exit status 1")
}

// arm64Image is what inspect prints of an image for linux/arm64 that sets HOME.
const arm64Image = `{"Os":"linux","Architecture":"arm64","RepoDigests":["example.com/agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"],"Config":{"Env":["PATH=/usr/bin","HOME=/home/agent"]}}`

// passingReport is a probe's report whose lines pass, with one fact.
const passingReport = `{"version":1,"checks":[{"name":"home","result":"pass","detail":"HOME is /home/agent"},{"name":"docker","result":"info","detail":"no dockerd"}]}`

// TestEngineProbesTheImageAsTheWallRunsAnAgent checks an image the engine holds: the
// outside lines pass, the reference is reported unpinned with the digest that names it,
// and the probe runs as a user the image does not know, with no capability, no way to
// gain one, no network and nothing pulled, qory's Linux build mounted read-only at the
// wall's helper path as the entry point.
func TestEngineProbesTheImageAsTheWallRunsAnAgent(t *testing.T) {
	f := &fakeEngine{platform: "linux/arm64", held: map[string]string{"example.com/agent:1": arm64Image}, report: passingReport}
	e := image.Engine{Command: "podman", Run: f.run}
	platform, err := e.Platform(context.Background())
	if err != nil || platform != "linux/arm64" {
		t.Fatalf("platform %q, %v", platform, err)
	}
	checks := e.Check(context.Background(), platform, image.Target{Ref: "example.com/agent:1", Helper: "/opt/qory/qory-linux", HelperArch: "arm64", Claude: "2.1.273"})
	var got []string
	for _, c := range checks {
		got = append(got, c.Name+" "+c.Result)
	}
	if strings.Join(got, ", ") != "image pass, home pass, reference info, home pass, docker info" {
		t.Errorf("lines %v", checks)
	}
	if !strings.Contains(checks[2].Detail, "example.com/agent@sha256:1111") {
		t.Errorf("reference %q", checks[2].Detail)
	}
	wantRun := "podman run --rm --pull never --network none --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges --init --mount type=bind,src=/opt/qory/qory-linux,dst=/qory/qory,readonly --entrypoint /qory/qory example.com/agent:1 image probe --claude-version 2.1.273"
	if f.lines[len(f.lines)-1] != wantRun {
		t.Errorf("ran\n%s\nwant\n%s", f.lines[len(f.lines)-1], wantRun)
	}
	if image.Failed(checks) {
		t.Error("a line failed")
	}
}

// TestEngineNamesWhatIsWrongFromOutside checks an image the engine does not hold, one of
// another platform with no HOME, and one whose architecture is not the probe's, which is
// not run.
func TestEngineNamesWhatIsWrongFromOutside(t *testing.T) {
	f := &fakeEngine{platform: "linux/arm64", report: passingReport, held: map[string]string{
		"example.com/amd64:1": `{"Os":"linux","Architecture":"amd64","Config":{"Env":["PATH=/usr/bin"]}}`,
		"example.com/agent:1@sha256:2222222222222222222222222222222222222222222222222222222222222222": arm64Image,
	}}
	e := image.Engine{Run: f.run}

	checks := e.Check(context.Background(), "linux/arm64", image.Target{Ref: "example.com/missing:1", HelperArch: "arm64"})
	if len(checks) != 1 || checks[0].Result != image.Fail || !strings.Contains(checks[0].Detail, "docker pull example.com/missing:1") {
		t.Errorf("a missing image: %+v", checks)
	}

	checks = e.Check(context.Background(), "linux/arm64", image.Target{Ref: "example.com/amd64:1", Helper: "/opt/qory/qory-linux", HelperArch: "arm64"})
	lines := map[string]image.Check{}
	for _, c := range checks {
		lines[c.Name] = c
	}
	for name, carries := range map[string]string{
		"image": "for linux/amd64, and the engine runs linux/arm64",
		"home":  "sets no HOME",
		"probe": "qory's Linux build for arm64, and the image is for amd64",
	} {
		if c := lines[name]; c.Result != image.Fail || !strings.Contains(c.Detail, carries) {
			t.Errorf("%s: %+v, want a failure carrying %q", name, c, carries)
		}
	}
	if lines["reference"].Detail != "not pinned by digest, and the image has none: a local build" {
		t.Errorf("reference %q", lines["reference"].Detail)
	}
	for _, l := range f.lines {
		if strings.Contains(l, " run ") {
			t.Errorf("a probe of another architecture ran: %s", l)
		}
	}

	checks = e.Check(context.Background(), "linux/arm64", image.Target{Ref: "example.com/agent:1@sha256:2222222222222222222222222222222222222222222222222222222222222222", Helper: "/q", HelperArch: "arm64"})
	if checks[2].Detail != "pinned by digest" {
		t.Errorf("a pinned reference: %+v", checks[2])
	}
}

// TestEngineSaysWhyTheProbeDidNotRun has the probe fail as an older qory does, with no
// such verb, and print what does not read.
func TestEngineSaysWhyTheProbeDidNotRun(t *testing.T) {
	f := &fakeEngine{platform: "linux/arm64", held: map[string]string{"a:1": arm64Image}, runErr: `Error: unknown command "probe" for "qory image"`}
	e := image.Engine{Run: f.run}
	checks := e.Check(context.Background(), "linux/arm64", image.Target{Ref: "a:1", Helper: "/opt/old-qory", HelperArch: "arm64"})
	if last := checks[len(checks)-1]; last.Result != image.Fail || !strings.Contains(last.Detail, "/opt/old-qory has no image probe") {
		t.Errorf("an older probe: %+v", last)
	}
	f.runErr, f.report = "", "exec /qory/qory: exec format error"
	checks = e.Check(context.Background(), "linux/arm64", image.Target{Ref: "a:1", Helper: "/q", HelperArch: "arm64"})
	if last := checks[len(checks)-1]; last.Result != image.Fail || !strings.Contains(last.Detail, "printed what does not read") {
		t.Errorf("a probe that printed nonsense: %+v", last)
	}
	e = image.Engine{Run: func(context.Context, []string) ([]byte, []byte, error) {
		return nil, []byte("Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n"), errors.New("exit status 1")
	}}
	if _, err := e.Platform(context.Background()); err == nil || !strings.Contains(err.Error(), "reaches no engine: Cannot connect") {
		t.Errorf("no engine: %v", err)
	}
}

// TestCheckRefRefusesWhatIsNoReference refuses a word the docker command would read as a
// flag, and takes a reference pinned by digest.
func TestCheckRefRefusesWhatIsNoReference(t *testing.T) {
	for _, bad := range []string{"-v", "--rm", "", "a b", "a;b"} {
		if image.CheckRef(bad) == nil {
			t.Errorf("%q taken", bad)
		}
	}
	for _, good := range []string{"agent", "ghcr.io/qoryai/agent:0.13.0@sha256:0123", "localhost:5000/a_b/c-d.e"} {
		if err := image.CheckRef(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}

// elfFile writes an ELF header for machine, with one program header of type prog when
// prog is not zero, the smallest file debug/elf reads.
func elfFile(t *testing.T, machine uint16, prog uint32) string {
	t.Helper()
	h := make([]byte, 64)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(h[16:], 2) // an executable
	binary.LittleEndian.PutUint16(h[18:], machine)
	binary.LittleEndian.PutUint32(h[20:], 1)
	binary.LittleEndian.PutUint16(h[52:], 64) // the header's size
	binary.LittleEndian.PutUint16(h[54:], 56) // a program header's
	binary.LittleEndian.PutUint16(h[58:], 64) // a section header's
	if prog != 0 {
		binary.LittleEndian.PutUint64(h[32:], 64) // the program headers follow
		binary.LittleEndian.PutUint16(h[56:], 1)
		p := make([]byte, 56)
		binary.LittleEndian.PutUint32(p, prog)
		h = append(h, p...)
	}
	path := filepath.Join(t.TempDir(), "qory-linux")
	if err := os.WriteFile(path, h, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHelperArchReadsTheBuildsArchitecture reads amd64 and arm64 from the ELF header and
// refuses what is no Linux executable and a dynamically linked one.
func TestHelperArchReadsTheBuildsArchitecture(t *testing.T) {
	for machine, want := range map[uint16]string{62: "amd64", 183: "arm64"} {
		if got, err := image.HelperArch(elfFile(t, machine, 0)); err != nil || got != want {
			t.Errorf("machine %d: %q, %v; want %s", machine, got, err, want)
		}
	}
	if _, err := image.HelperArch(elfFile(t, 62, 3)); err == nil || !strings.Contains(err.Error(), "CGO_ENABLED=0") {
		t.Errorf("a dynamically linked build: %v", err)
	}
	script := filepath.Join(t.TempDir(), "qory")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := image.HelperArch(script); err == nil || !strings.Contains(err.Error(), "not a Linux executable") {
		t.Errorf("a script: %v", err)
	}
}
