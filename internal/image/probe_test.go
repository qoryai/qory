package image_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// imageRoot is a directory laid out as an image's files: each path, relative to the
// root, is written with its content, and made executable when it is under a bin
// directory or is sh.
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
	}
	return root
}

// agentFiles are the files of an image that has what the wall needs.
func agentFiles() map[string]string {
	return map[string]string{
		"home/agent/":                       "",
		"etc/ssl/certs/ca-certificates.crt": "-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n",
		"bin/sh":                            "#!/bin/sh\n",
		"usr/local/bin/claude":              "",
		"usr/bin/git":                       "",
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
		"claude": "2.1.273 (Claude Code)",
		"git":    "git version 2.47.3",
		"gh":     "gh version 2.101.0 (2026-09-15)\nhttps://github.com/cli/cli/releases/tag/v2.101.0",
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
// and claude, git and gh on the PATH: every line passes, the programs run from under the
// root at the paths the PATH finds, and the image carries no Docker.
func TestProbePassesAnImageWithWhatTheWallNeeds(t *testing.T) {
	root := imageRoot(t, agentFiles())
	v := goodVersions()
	p := image.Probe{Root: root, Env: env(map[string]string{"HOME": "/home/agent", "PATH": "relative:/usr/local/bin:/usr/bin:/bin"}), Exec: v.exec, Claude: "2.1.273", User: "65532:65532"}
	r := p.Report(context.Background())
	if r.Version != image.ReportVersion {
		t.Errorf("report version %d", r.Version)
	}
	checks := byName(r)
	want(t, checks, "home", image.Pass, "/home/agent", "65532:65532")
	want(t, checks, "authorities", image.Pass, "/etc/ssl/certs/ca-certificates.crt", "2 certificates")
	want(t, checks, "shell", image.Pass)
	want(t, checks, "claude", image.Pass, "/usr/local/bin/claude is 2.1.273")
	want(t, checks, "git", image.Pass, "git version 2.47.3")
	want(t, checks, "gh", image.Pass, "gh version 2.101.0 (2026-09-15)")
	want(t, checks, "docker", image.Info, "no dockerd")
	if image.Failed(r.Checks) {
		t.Errorf("a line failed: %+v", r.Checks)
	}
	wantRan := []string{filepath.Join(root, "usr/local/bin/claude") + " --version", filepath.Join(root, "usr/bin/git") + " --version", filepath.Join(root, "usr/local/bin/gh") + " --version"}
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
	root := imageRoot(t, map[string]string{"usr/bin/git": "", "etc/ssl/cert.pem": "no certificate here\n"})
	p := image.Probe{Root: root, Env: env(map[string]string{"PATH": "/usr/bin"}), Exec: (&versions{}).exec, Claude: "2.1.273", User: "65532:65532"}
	checks := byName(p.Report(context.Background()))
	want(t, checks, "home", image.Fail, "HOME is not set", "ENV HOME=")
	want(t, checks, "authorities", image.Fail, "no bundle of authorities", "ca-certificates")
	want(t, checks, "shell", image.Fail, "no /bin/sh")
	want(t, checks, "claude", image.Fail, "no claude on the image's PATH, /usr/bin")
	want(t, checks, "git", image.Fail, "/usr/bin/git --version failed: exit status 139: segmentation fault")
	want(t, checks, "gh", image.Fail, "no gh on the image's PATH")

	for _, c := range []struct {
		home, want string
	}{{"/", "HOME is /"}, {"home/agent", "not an absolute path"}, {"/nowhere", "cannot write in HOME, /nowhere"}} {
		p.Env = env(map[string]string{"HOME": c.home})
		want(t, byName(p.Report(context.Background())), "home", image.Fail, c.want)
	}
}

// TestProbeRefusesAnotherClaude has claude report a version the descriptor is not
// written against: the line fails and names the package to install; with no version
// asked for, any passes.
func TestProbeRefusesAnotherClaude(t *testing.T) {
	root := imageRoot(t, agentFiles())
	v := goodVersions()
	v.lines["claude"] = "2.1.270 (Claude Code)"
	p := image.Probe{Root: root, Env: env(map[string]string{"HOME": "/home/agent", "PATH": "/usr/local/bin:/usr/bin"}), Exec: v.exec, Claude: "2.1.273"}
	want(t, byName(p.Report(context.Background())), "claude", image.Fail, "is 2.1.270", "written against 2.1.273", "@anthropic-ai/claude-code@2.1.273")
	p.Claude = ""
	want(t, byName(p.Report(context.Background())), "claude", image.Pass, "is 2.1.270")
}

// TestProbeSaysWhetherTheImageCarriesADocker puts dockerd in a system directory, with
// and without what it runs there, with and without the docker command on the PATH, and
// in a directory of the PATH that is not a system one, where the runner never looks.
func TestProbeSaysWhetherTheImageCarriesADocker(t *testing.T) {
	files := agentFiles()
	for _, name := range []string{"dockerd", "docker", "containerd", "containerd-shim-runc-v2", "runc", "docker-init", "docker-proxy"} {
		files["usr/local/bin/"+name] = ""
	}
	files["usr/sbin/iptables"] = ""
	files["opt/docker/bin/dockerd"] = ""
	root := imageRoot(t, files)
	p := image.Probe{Root: root, Env: env(map[string]string{"HOME": "/home/agent", "PATH": "/usr/local/bin:/usr/bin"}), Exec: goodVersions().exec}
	want(t, byName(p.Report(context.Background())), "docker", image.Pass, "/usr/local/bin/dockerd", "iptables in the system's directories", "can carry a Docker of the agent's own")

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
	if strings.Join(names, " ") != strings.Join(image.ProbeChecks, " ") {
		t.Errorf("lines %v, want %v", names, image.ProbeChecks)
	}
}
