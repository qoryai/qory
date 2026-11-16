package image_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// imageRoot is a directory laid out as an image's files: each path, relative to the
// root, is written with its content, and made executable when it is under a bin
// directory or is sh. Every folder has mode 0755, whatever the umask.
func imageRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.Contains(p, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return os.Chmod(p, 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// agentFiles are the files of an image that has what the wall needs.
func agentFiles() map[string]string {
	return map[string]string{
		"home/agent/":                       "",
		"etc/ssl/certs/ca-certificates.crt": "-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n",
		"bin/sh":                            "#!/bin/sh\n",
		"usr/bin/git":                       "",
		"usr/bin/cat":                       "",
		"usr/bin/chmod":                     "",
		"usr/bin/comm":                      "",
		"usr/bin/cp":                        "",
		"usr/bin/find":                      "",
		"usr/bin/grep":                      "",
		"usr/bin/mkdir":                     "",
		"usr/bin/mv":                        "",
		"usr/bin/readlink":                  "",
		"usr/bin/sort":                      "",
		"usr/bin/sleep":                     "",
		"usr/bin/stat":                      "",
		"usr/bin/tr":                        "",
		"usr/local/bin/gh":                  "",
		"usr/share/doc/git/copyright":       "",
	}
}

// versions stands in for the image's programs: each answers --version with the line
// given for its name, and a name it has none for fails. It records the paths it ran.
type versions struct {
	lines map[string]string
	ran   []string
}

func (v *versions) exec(_ context.Context, program string, args ...string) ([]byte, error) {
	v.ran = append(v.ran, program+" "+strings.Join(args, " "))
	line, ok := v.lines[filepath.Base(program)]
	if !ok {
		return []byte("segmentation fault\n"), errors.New("exit status 139")
	}
	return []byte(line + "\n"), nil
}

// goodVersions are the answers of an image that has what the wall needs.
func goodVersions() *versions {
	return &versions{lines: map[string]string{
		"git": "git version 2.47.3",
		"gh":  "gh version 2.101.0 (2026-09-15)\nhttps://github.com/cli/cli/releases/tag/v2.101.0",
	}}
}

// env is an environment of the image's.
func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// byName is the probe's lines by name.
func byName(r image.Report) map[string]image.Check {
	out := map[string]image.Check{}
	for _, c := range r.Checks {
		out[c.Name] = c
	}
	return out
}

// want fails the test unless the named line has the result and carries every string.
func want(t *testing.T, checks map[string]image.Check, name, result string, carries ...string) {
	t.Helper()
	c, ok := checks[name]
	if !ok {
		t.Errorf("no %s line", name)
		return
	}
	if c.Result != result {
		t.Errorf("%s: %s, want %s: %s", name, c.Result, result, c.Detail)
	}
	for _, s := range carries {
		if !strings.Contains(c.Detail, s) {
			t.Errorf("%s: %q does not carry %q", name, c.Detail, s)
		}
	}
}

// TestProbePassesAnImageWithWhatTheWallNeeds probes a root with HOME, a bundle, a shell,
// and git and gh on the PATH, and no runtime: every line passes, git and gh run from
// under the root at the paths the PATH finds, no line is about a runtime, and the image
// carries no Docker.
func TestProbePassesAnImageWithWhatTheWallNeeds(t *testing.T) {
	root := imageRoot(t, agentFiles())
	v := goodVersions()
	p := image.Probe{Root: root, Env: env(map[string]string{"HOME": "/home/agent", "PATH": "relative:/usr/local/bin:/usr/bin:/bin"}), Exec: v.exec, User: "65532:65532"}
	r := p.Report(context.Background())
	if r.Version != image.ReportVersion {
		t.Errorf("report version %d", r.Version)
	}
	checks := byName(r)
	want(t, checks, "home", image.Pass, "/home/agent", "65532:65532")
	want(t, checks, "authorities", image.Pass, "/etc/ssl/certs/ca-certificates.crt", "2 certificates")
	want(t, checks, "shell", image.Pass, "/bin/sh runs the runtime's hooks and the API-key approval")
	if c, ok := checks["claude"]; ok {
		t.Errorf("the probe has a runtime line: %+v", c)
	}
	want(t, checks, "git", image.Pass, "git version 2.47.3")
	want(t, checks, "gh", image.Pass, "gh version 2.101.0 (2026-09-15)")
	want(t, checks, "docker", image.Info, "no dockerd")
	if image.Failed(r.Checks) {
		t.Errorf("a line failed: %+v", r.Checks)
	}
	wantRan := []string{filepath.Join(root, "usr/bin/git") + " --version", filepath.Join(root, "usr/local/bin/gh") + " --version"}
	if strings.Join(v.ran, "\n") != strings.Join(wantRan, "\n") {
		t.Errorf("ran %q, want %q", v.ran, wantRan)
	}
	if left, _ := os.ReadDir(filepath.Join(root, "home", "agent")); len(left) != 0 {
		t.Errorf("the probe left %v in HOME", left)
	}
}

// TestProbeNamesWhatAnImageLacks probes a root with none of it: every line fails and
// says what to add.
func TestProbeNamesWhatAnImageLacks(t *testing.T) {
	root := imageRoot(t, sessionFiles("etc/ssl/cert.pem", "no certificate here\n"))
	p := image.Probe{Root: root, Env: env(map[string]string{"PATH": "/usr/bin"}), Exec: (&versions{}).exec, User: "65532:65532"}
	checks := byName(p.Report(context.Background()))
	want(t, checks, "home", image.Fail, "HOME is not set", "ENV HOME=")
	want(t, checks, "authorities", image.Fail, "no bundle of authorities", "ca-certificates")
	want(t, checks, "shell", image.Fail, "no /bin/sh; the runtime's hooks and the session's API-key approval run under sh")
	want(t, checks, "git", image.Fail, "/usr/bin/git --version failed: exit status 139: segmentation fault")
	want(t, checks, "gh", image.Fail, "no gh on the image's PATH")

	for _, c := range []struct {
		home, want string
	}{{"/", "HOME is /"}, {"home/agent", "not an absolute path"}, {"/nowhere", "cannot write in HOME, /nowhere"}} {
		p.Env = env(map[string]string{"HOME": c.home})
		want(t, byName(p.Report(context.Background())), "home", image.Fail, c.want)
	}
}

// TestProbeSaysWhetherTheImageCarriesADocker puts dockerd in a system directory, with
// and without what it runs there, with and without the docker command on the PATH, and
// in a directory of the PATH that is not a system one, where the wall never looks.
func TestProbeSaysWhetherTheImageCarriesADocker(t *testing.T) {
	files := agentFiles()
	for _, name := range []string{"dockerd", "docker", "containerd", "containerd-shim-runc-v2", "runc", "docker-init", "docker-proxy"} {
		files["usr/local/bin/"+name] = ""
	}
	files["usr/sbin/iptables"] = ""
	files["opt/docker/bin/dockerd"] = ""
	root := imageRoot(t, files)
	p := image.Probe{Root: root, Env: env(map[string]string{"HOME": "/home/agent", "PATH": "/usr/local/bin:/usr/bin"}), Exec: goodVersions().exec}
	want(t, byName(p.Report(context.Background())), "docker", image.Pass, "/usr/local/bin/dockerd", "iptables in the system's directories", "can carry nested Docker")

	if err := os.Rename(filepath.Join(root, "usr/local/bin/docker"), filepath.Join(root, "opt/docker/bin/docker")); err != nil {
		t.Fatal(err)
	}
	want(t, byName(p.Report(context.Background())), "docker", image.Fail, "the docker command is not on the image's PATH")
	p.Env = env(map[string]string{"HOME": "/home/agent", "PATH": "/opt/docker/bin:/usr/local/bin"})
	want(t, byName(p.Report(context.Background())), "docker", image.Pass)

	if err := os.Rename(filepath.Join(root, "usr/sbin/iptables"), filepath.Join(root, "opt/docker/bin/iptables")); err != nil {
		t.Fatal(err)
	}
	want(t, byName(p.Report(context.Background())), "docker", image.Fail, "iptables is not in /usr/local/sbin")

	if err := os.Remove(filepath.Join(root, "usr/local/bin/dockerd")); err != nil {
		t.Fatal(err)
	}
	want(t, byName(p.Report(context.Background())), "docker", image.Info, "no dockerd in /usr/local/sbin")
}

// TestProbeReportsEveryCheck has the report hold one line for each of the checks the
// outside counts on, in their order.
func TestProbeReportsEveryCheck(t *testing.T) {
	p := image.Probe{Root: imageRoot(t, agentFiles()), Env: env(nil), Exec: goodVersions().exec}
	var names []string
	for _, c := range p.Report(context.Background()).Checks {
		names = append(names, c.Name)
	}
	if want := "home authorities shell git gh docker"; strings.Join(names, " ") != want || strings.Join(image.ProbeChecks(), " ") != want {
		t.Errorf("lines %v, want %s", names, want)
	}
}

// TestTheGitLineChecksWhatTheGitSessionNeeds is an image whose git is too old, or is in no
// folder of the git session's PATH: the line fails with the fix first.
func TestTheGitLineChecksWhatTheGitSessionNeeds(t *testing.T) {
	old := goodVersions()
	old.lines["git"] = "git version 2.24.4"
	root := imageRoot(t, sessionFiles())
	p := image.Probe{Root: root, Env: env(map[string]string{"PATH": "/usr/bin"}), Exec: old.exec}
	checks := byName(p.Report(context.Background()))
	want(t, checks, "git", image.Fail, "install git 2.25 or later in /usr/local/bin, /usr/bin, /bin: ", "git version 2.24.4")

	root = imageRoot(t, map[string]string{"opt/git/bin/git": ""})
	p = image.Probe{Root: root, Env: env(map[string]string{"PATH": "/opt/git/bin"}), Exec: goodVersions().exec}
	checks = byName(p.Report(context.Background()))
	want(t, checks, "git", image.Fail, "install git in /usr/local/bin, /usr/bin, /bin: ")

	root = imageRoot(t, sessionFiles())
	p = image.Probe{Root: root, Env: env(map[string]string{"PATH": "/usr/bin"}), Exec: goodVersions().exec}
	checks = byName(p.Report(context.Background()))
	want(t, checks, "git", image.Pass, "git version 2.47.3")
}

// sessionFiles is an image's git and each program of the git session, and the files of extra, in
// pairs of a path and its content.
func sessionFiles(extra ...string) map[string]string {
	files := map[string]string{"usr/bin/git": ""}
	for _, tool := range image.SessionTools() {
		files["usr/bin/"+tool] = ""
	}
	for i := 0; i+1 < len(extra); i += 2 {
		files[extra[i]] = extra[i+1]
	}
	return files
}

// TestTheGitCheckNeedsEveryTool is an image without one program the script of the git session
// runs: the line starts with its install.
func TestTheGitCheckNeedsEveryTool(t *testing.T) {
	for _, tool := range image.SessionTools() {
		t.Run(tool, func(t *testing.T) {
			files := sessionFiles()
			delete(files, "usr/bin/"+tool)
			p := image.Probe{Root: imageRoot(t, files), Env: env(map[string]string{"PATH": "/usr/bin"}), Exec: goodVersions().exec}
			want(t, byName(p.Report(context.Background())), "git", image.Fail, "install "+tool+" in /usr/local/bin, /usr/bin, /bin: the git session of a walled run runs "+tool+" from these folders, to ")
		})
	}
}

// TestTheGitCheckNeedsFind is an image with git and no find in the folders of the git session.
func TestTheGitCheckNeedsFind(t *testing.T) {
	files := sessionFiles()
	delete(files, "usr/bin/find")
	root := imageRoot(t, files)
	p := image.Probe{Root: root, Env: env(map[string]string{"PATH": "/usr/bin"}), Exec: goodVersions().exec}
	checks := byName(p.Report(context.Background()))
	want(t, checks, "git", image.Fail, "install find in /usr/local/bin, /usr/bin, /bin: the git session of a walled run runs find from these folders, to list executable files, embedded repositories and files over 100 MiB")
}
