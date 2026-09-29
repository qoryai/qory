package image_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// tarFile is one entry of an image's file system as docker export writes it.
type tarFile struct {
	name       string
	mode       int64
	capability bool
}

// exported is the tar stream of an image's file system with files, a directory first.
func exported(t *testing.T, files ...tarFile) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		h := &tar.Header{Name: f.name, Typeflag: tar.TypeReg, Mode: f.mode, Format: tar.FormatPAX}
		if f.capability {
			h.PAXRecords = map[string]string{"SCHILY.xattr.security.capability": "\x01\x00\x00\x02"}
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeEngine stands in for the docker command: it answers the version with its
// platform, an inspect with the image it holds, a create with a container's id, an
// export with its file system, and a run with the report, and records every command
// line.
type fakeEngine struct {
	platform string
	held     map[string]string // reference to what inspect prints
	files    []byte            // what export writes
	report   string
	runErr   string // what a run prints on standard error, and exits 1, when set
	lines    []string
}

func (f *fakeEngine) run(_ context.Context, argv []string, stdout io.Writer) ([]byte, error) {
	f.lines = append(f.lines, strings.Join(argv, " "))
	switch {
	case argv[1] == "version":
		io.WriteString(stdout, f.platform+"\n")
		return nil, nil
	case argv[1] == "image" && argv[2] == "inspect":
		ref := argv[len(argv)-1]
		if doc, ok := f.held[ref]; ok {
			io.WriteString(stdout, doc+"\n")
			return nil, nil
		}
		return []byte("Error response from daemon: No such image: " + ref + "\n"), errors.New("exit status 1")
	case argv[1] == "create":
		io.WriteString(stdout, "c0ffee\n")
		return nil, nil
	case argv[1] == "export":
		_, err := stdout.Write(f.files)
		return nil, err
	case argv[1] == "rm":
		return nil, nil
	case argv[1] == "run":
		if f.runErr != "" {
			return []byte(f.runErr + "\n"), errors.New("exit status 1")
		}
		io.WriteString(stdout, f.report+"\n")
		return nil, nil
	}
	return []byte("unknown\n"), errors.New("exit status 1")
}

// arm64Image is what inspect prints of an image for linux/arm64 that sets HOME.
const arm64Image = `{"Os":"linux","Architecture":"arm64","RepoDigests":["example.com/agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"],"Config":{"Env":["PATH=/usr/bin","HOME=/home/agent"]}}`

// passingReport is a probe's report with a line for each check, which pass, and a fact.
const passingReport = `{"version":1,"checks":[` +
	`{"name":"home","result":"pass","detail":"HOME is /home/agent"},` +
	`{"name":"authorities","result":"pass","detail":"the authorities"},` +
	`{"name":"shell","result":"pass","detail":"/bin/sh"},` +
	`{"name":"claude","result":"pass","detail":"claude 2.1.273"},` +
	`{"name":"git","result":"pass","detail":"git version 2.47.3"},` +
	`{"name":"gh","result":"pass","detail":"gh version 2.101.0"},` +
	`{"name":"docker","result":"info","detail":"no dockerd"}]}`

// lineNames are the name and result of each line, as one string.
func lineNames(checks []image.Check) string {
	var got []string
	for _, c := range checks {
		got = append(got, c.Name+" "+c.Result)
	}
	return strings.Join(got, ", ")
}

// TestEngineChecksTheImageAsTheWallRunsAnAgent checks an image the engine holds: the
// outside lines pass, the reference is reported unpinned with the digest the engine
// holds, which it does not call a registry's; the files are read from a container that
// is created, exported and removed; and the probe runs as a user the image does not
// know, with no capability, no way to gain one, no network and nothing pulled, qory's
// Linux build mounted read-only at the wall's helper path as the entry point.
func TestEngineChecksTheImageAsTheWallRunsAnAgent(t *testing.T) {
	f := &fakeEngine{platform: "linux/arm64", held: map[string]string{"example.com/agent:1": arm64Image}, report: passingReport, files: exported(t, tarFile{name: "usr/bin/git", mode: 0o755})}
	e := image.Engine{Command: "podman", Run: f.run}
	platform, err := e.Platform(context.Background())
	if err != nil || platform != "linux/arm64" {
		t.Fatalf("platform %q, %v", platform, err)
	}
	checks := e.Check(context.Background(), platform, image.Target{Ref: "example.com/agent:1", Helper: "/opt/qory/qory-linux", Claude: "2.1.273"})
	if got := lineNames(checks); got != "image pass, home pass, reference info, files pass, home pass, authorities pass, shell pass, claude pass, git pass, gh pass, docker info" {
		t.Errorf("lines %s", got)
	}
	if want := "not pinned by digest; the engine holds the image as sha256:1111111111111111111111111111111111111111111111111111111111111111, which a registry serves only if the image was pulled from it or pushed to it"; checks[2].Detail != want {
		t.Errorf("reference %q", checks[2].Detail)
	}
	if !strings.Contains(checks[3].Detail, "none of the image's 2 files is setuid") {
		t.Errorf("files %q", checks[3].Detail)
	}
	for _, want := range []string{
		"podman create --pull never --network none --entrypoint /qory/qory example.com/agent:1",
		"podman export c0ffee",
		"podman rm --force --volumes c0ffee",
		"podman run --rm --pull never --network none --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges --init --mount type=bind,src=/opt/qory/qory-linux,dst=/qory/qory,readonly --entrypoint /qory/qory example.com/agent:1 image probe --claude-version 2.1.273",
	} {
		if !strings.Contains(strings.Join(f.lines, "\n")+"\n", want+"\n") {
			t.Errorf("did not run %s; ran:\n%s", want, strings.Join(f.lines, "\n"))
		}
	}
	if image.Failed(checks) {
		t.Error("a line failed")
	}
}

// TestEngineFindsSetuidFilesAndCapabilities reads an image whose files include a setuid
// program, a setgid one, a setuid directory, which is no program, and a program with
// capabilities of its own: the line names the three files and how to mend them.
func TestEngineFindsSetuidFilesAndCapabilities(t *testing.T) {
	f := &fakeEngine{platform: "linux/arm64", held: map[string]string{"a:1": arm64Image}, report: passingReport, files: exported(t,
		tarFile{name: "usr/bin/passwd", mode: 0o4755},
		tarFile{name: "usr/bin/chage", mode: 0o2755},
		tarFile{name: "usr/bin/ping", mode: 0o755, capability: true},
		tarFile{name: "usr/bin/git", mode: 0o755},
	)}
	checks := image.Engine{Run: f.run}.Check(context.Background(), "linux/arm64", image.Target{Ref: "a:1", Helper: "/q"})
	want := "setuid or setgid: /usr/bin/passwd, /usr/bin/chage; capabilities of its own: /usr/bin/ping; remove them in the image: find / -xdev -type f -perm /6000 -exec chmod ug-s {} +, and setcap -r on each"
	if c := checks[3]; c.Name != "files" || c.Result != image.Fail || c.Detail != want {
		t.Errorf("files %+v, want %q", c, want)
	}

	f.files = []byte("not a tar stream, but long enough to be read as a header of one; " + strings.Repeat("x", 600))
	checks = image.Engine{Run: f.run}.Check(context.Background(), "linux/arm64", image.Target{Ref: "a:1", Helper: "/q"})
	if c := checks[3]; c.Result != image.Fail || !strings.Contains(c.Detail, "the image's files were not read to the end") {
		t.Errorf("a stream that does not read: %+v", c)
	}
}

// TestEngineNamesWhatIsWrongFromOutside checks an image the engine does not hold, and
// one of another platform than the engine's, with no HOME and no digest, which is still
// probed: the probe is built for the engine and runs as the engine runs it.
func TestEngineNamesWhatIsWrongFromOutside(t *testing.T) {
	f := &fakeEngine{platform: "linux/arm64", report: passingReport, files: exported(t), held: map[string]string{
		"example.com/amd64:1": `{"Os":"linux","Architecture":"amd64","Config":{"Env":["PATH=/usr/bin"]}}`,
		"example.com/agent:1@sha256:2222222222222222222222222222222222222222222222222222222222222222": arm64Image,
	}}
	e := image.Engine{Run: f.run}

	checks := e.Check(context.Background(), "linux/arm64", image.Target{Ref: "example.com/missing:1"})
	if len(checks) != 1 || checks[0].Result != image.Fail || !strings.Contains(checks[0].Detail, "docker pull example.com/missing:1") {
		t.Errorf("a missing image: %+v", checks)
	}

	checks = e.Check(context.Background(), "linux/arm64", image.Target{Ref: "example.com/amd64:1", Helper: "/opt/qory/qory-linux"})
	lines := map[string]image.Check{}
	for _, c := range checks {
		if _, seen := lines[c.Name]; !seen {
			lines[c.Name] = c
		}
	}
	for name, carries := range map[string]string{
		"image": "for linux/amd64, and the engine runs linux/arm64; pull or build it for linux/arm64",
		"home":  "sets no HOME",
	} {
		if c := lines[name]; c.Result != image.Fail || !strings.Contains(c.Detail, carries) {
			t.Errorf("%s: %+v, want a failure carrying %q", name, c, carries)
		}
	}
	if lines["reference"].Detail != "not pinned by digest, and the engine holds no digest of it: a local build" {
		t.Errorf("reference %q", lines["reference"].Detail)
	}
	if c, ok := lines["claude"]; !ok || c.Result != image.Pass {
		t.Errorf("the image of another platform was not probed: %s", lineNames(checks))
	}

	checks = e.Check(context.Background(), "linux/arm64", image.Target{Ref: "example.com/agent:1@sha256:2222222222222222222222222222222222222222222222222222222222222222", Helper: "/q"})
	if checks[2].Detail != "pinned by digest" {
		t.Errorf("a pinned reference: %+v", checks[2])
	}
}

// TestEngineSaysWhyTheProbeDidNotRun has the probe fail as an older qory does, with no
// such verb, print what does not read, leave out checks, and print a result this qory
// does not know.
func TestEngineSaysWhyTheProbeDidNotRun(t *testing.T) {
	f := &fakeEngine{platform: "linux/arm64", held: map[string]string{"a:1": arm64Image}, files: exported(t), runErr: `Error: unknown command "probe" for "qory image"`}
	e := image.Engine{Run: f.run}
	checks := e.Check(context.Background(), "linux/arm64", image.Target{Ref: "a:1", Helper: "/opt/old-qory"})
	if last := checks[len(checks)-1]; last.Result != image.Fail || !strings.Contains(last.Detail, "/opt/old-qory has no image probe") {
		t.Errorf("an older probe: %+v", last)
	}
	f.runErr, f.report = "", "exec /qory/qory: exec format error"
	checks = e.Check(context.Background(), "linux/arm64", image.Target{Ref: "a:1", Helper: "/q"})
	if last := checks[len(checks)-1]; last.Result != image.Fail || !strings.Contains(last.Detail, "printed what does not read") {
		t.Errorf("a probe that printed nonsense: %+v", last)
	}
	f.report = `{"version":1,"checks":[{"name":"home","result":"pass","detail":"HOME"},{"name":"shell","result":"maybe","detail":"/bin/sh"}]}`
	checks = e.Check(context.Background(), "linux/arm64", image.Target{Ref: "a:1", Helper: "/q"})
	if !image.Failed(checks) {
		t.Errorf("a report that lacks checks passed: %+v", checks)
	}
	if last := checks[len(checks)-1]; !strings.Contains(last.Detail, "no line for authorities, claude, git, gh, docker") {
		t.Errorf("the lacking checks: %+v", last)
	}
	if c := checks[len(checks)-2]; c.Result != image.Fail || !strings.Contains(c.Detail, `the result "maybe"`) {
		t.Errorf("an unknown result: %+v", c)
	}
	e = image.Engine{Run: func(context.Context, []string, io.Writer) ([]byte, error) {
		return []byte("Cannot connect to the Docker daemon at unix:///var/run/docker.sock.\n"), errors.New("exit status 1")
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
