package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The results a [Check] has.
const (
	// Pass is a check the image meets.
	Pass = "pass"
	// Fail is a check the image does not meet; the detail says how to mend it.
	Fail = "fail"
	// Info is a fact reported that neither passes nor fails.
	Info = "info"
)

// Check is one line of a check's result.
type Check struct {
	// Name is what the line is about, one word: image, home, reference, files, probe, or
	// one of [ProbeChecks].
	Name string `json:"name"`
	// Result is [Pass], [Fail] or [Info].
	Result string `json:"result"`
	// Detail is the line as it prints: what was found, or what is missing and how to
	// mend it.
	Detail string `json:"detail"`
}

// Failed reports whether any of checks failed.
func Failed(checks []Check) bool {
	for _, c := range checks {
		if c.Result == Fail {
			return true
		}
	}
	return false
}

// ReportVersion is the version of the [Report] this build prints and reads.
const ReportVersion = 1

// Report is what the probe prints on its standard output, as one JSON document.
type Report struct {
	// Version is [ReportVersion].
	Version int `json:"version"`
	// Checks are the probe's lines, one of each of [ProbeChecks], in that order.
	Checks []Check `json:"checks"`
}

// ProbeChecks name the lines a [Report] holds, in the order they print. A report that
// lacks one did not check it, and the outside counts that as a failure.
var ProbeChecks = []string{"home", "authorities", "shell", "claude", "git", "gh", "docker"}

// Bundles are where the wall reads an image's authorities, the first that holds a
// certificate: Debian and Alpine, Red Hat, OpenSUSE, and OpenSSL's default. The wall's own
// list is unexported; this is a copy of the runner's at the version go.mod requires.
var Bundles = []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/ca-bundle.pem", "/etc/ssl/cert.pem"}

// SystemDirs are the directories the runner looks for dockerd in, and gives the daemon as
// its PATH, never the run's. A copy of the runner's list.
var SystemDirs = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// daemonRuns are the programs the daemon runs, which it finds in [SystemDirs]: containerd
// and what runs a container, the init and the port proxy it gives one, and iptables for
// its networks.
var daemonRuns = []string{"containerd", "containerd-shim-runc-v2", "runc", "docker-init", "docker-proxy", "iptables"}

// ProgramWait is how long one program the probe runs may take. A program that asks for
// the network waits for nothing, since the probe's container has none.
const ProgramWait = time.Minute

// Probe is the check from inside the image: what qory image probe runs as the entry
// point of a container of it.
type Probe struct {
	// Root is where the image's files are: / in the container, a directory in a test.
	Root string
	// Env reads the image's environment; nil is [os.Getenv].
	Env func(string) string
	// Exec runs a program of the image, by its path under Root, and returns what it
	// printed on both streams. Nil runs it with [ProgramWait] as its limit.
	Exec func(ctx context.Context, program string, args ...string) ([]byte, error)
	// Claude is the version claude --version must report, the runner's descriptor's
	// runtime_version; empty takes any.
	Claude string
	// User is the user the probe runs as, uid:gid, as the lines name it.
	User string
}

// Report runs every check of the probe, one line for each of [ProbeChecks], in that
// order.
func (p Probe) Report(ctx context.Context) Report {
	if p.Env == nil {
		p.Env = os.Getenv
	}
	checks := []Check{p.home(), p.authorities(), p.shell(), p.claude(ctx)}
	for _, name := range []string{"git", "gh"} {
		checks = append(checks, p.program(ctx, name))
	}
	checks = append(checks, p.docker())
	return Report{Version: ReportVersion, Checks: checks}
}

// host is the path under Root of a path inside the image.
func (p Probe) host(inside string) string {
	return filepath.Join(p.Root, filepath.FromSlash(inside))
}

// home checks that HOME is set, is not the root, and takes a file from this user.
func (p Probe) home() Check {
	home := p.Env("HOME")
	switch {
	case home == "":
		return Check{"home", Fail, "HOME is not set; set it in the image to a directory any user may write, such as ENV HOME=/home/agent with mode 1777"}
	case path.Clean(home) == "/":
		return Check{"home", Fail, "HOME is /, which the engine gives an image that sets none; set it to a directory any user may write, such as ENV HOME=/home/agent with mode 1777"}
	case !path.IsAbs(home):
		return Check{"home", Fail, "HOME is " + home + ", which is not an absolute path"}
	}
	f, err := os.CreateTemp(p.host(home), ".qory-image-check-")
	if err != nil {
		return Check{"home", Fail, fmt.Sprintf("the user %s cannot write in HOME, %s: %v; make it a directory any user may write, mode 1777", p.User, home, unwrapPath(err))}
	}
	f.Close()
	if err := os.Remove(f.Name()); err != nil {
		return Check{"home", Fail, fmt.Sprintf("the user %s wrote a file in HOME, %s, and cannot remove it: %v", p.User, home, unwrapPath(err))}
	}
	return Check{"home", Pass, fmt.Sprintf("HOME is %s, and the user %s writes in it", home, p.User)}
}

// unwrapPath is the error a [fs.PathError] carries, without the path the line names
// already.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// authorities checks for a bundle where the wall reads one: the first of [Bundles] that
// holds a certificate, followed through links as the wall's copy follows them.
func (p Probe) authorities() Check {
	for _, b := range Bundles {
		data, err := os.ReadFile(p.host(b))
		if err != nil {
			continue
		}
		if n := bytes.Count(data, []byte("BEGIN CERTIFICATE")); n > 0 {
			return Check{"authorities", Pass, fmt.Sprintf("the authorities are in %s, %d certificates", b, n)}
		}
	}
	return Check{"authorities", Fail, "no bundle of authorities in " + strings.Join(Bundles, ", ") + "; install the system's, ca-certificates on Debian, so the wall can add the run's authority after them"}
}

// shell checks for /bin/sh, which a runtime's hooks run under.
func (p Probe) shell() Check {
	info, err := os.Stat(p.host("/bin/sh"))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return Check{"shell", Fail, "no /bin/sh; the runtime's hooks run under sh -c"}
	}
	return Check{"shell", Pass, "/bin/sh runs the runtime's hooks"}
}

// version is the first version number in what a program printed: 2.1.273 in
// "2.1.273 (Claude Code)".
var version = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?`)

// claude checks that claude is on the image's PATH, where the wall's launch finds it,
// and reports the version the runner's descriptor is written against.
func (p Probe) claude(ctx context.Context) Check {
	out, where, err := p.run(ctx, "claude", "--version")
	if err != nil {
		return Check{"claude", Fail, err.Error()}
	}
	got := version.FindString(out)
	if p.Claude != "" && got != p.Claude {
		if got == "" {
			got = strconv.Quote(out)
		}
		return Check{"claude", Fail, fmt.Sprintf("claude at %s is %s, and the runner's descriptor is written against %s; install @anthropic-ai/claude-code@%s", where, got, p.Claude, p.Claude)}
	}
	if p.Claude == "" {
		return Check{"claude", Pass, fmt.Sprintf("claude at %s is %s", where, got)}
	}
	return Check{"claude", Pass, fmt.Sprintf("claude at %s is %s, the version the runner's descriptor is written against", where, got)}
}

// program checks that a program is on the image's PATH and answers --version.
func (p Probe) program(ctx context.Context, name string) Check {
	out, _, err := p.run(ctx, name, "--version")
	if err != nil {
		return Check{name, Fail, err.Error()}
	}
	return Check{name, Pass, out}
}

// run finds a program on the image's PATH and runs it, returning the first line it
// printed and where it was found. The error is the line that says what failed.
func (p Probe) run(ctx context.Context, name string, args ...string) (string, string, error) {
	where := p.lookPath(name, filepath.SplitList(p.Env("PATH")))
	if where == "" {
		return "", "", fmt.Errorf("no %s on the image's PATH, %s", name, p.Env("PATH"))
	}
	run := p.Exec
	if run == nil {
		run = execute
	}
	out, err := run(ctx, p.host(where), args...)
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil {
		if first != "" {
			return "", where, fmt.Errorf("%s %s failed: %v: %s", where, strings.Join(args, " "), err, first)
		}
		return "", where, fmt.Errorf("%s %s failed: %v", where, strings.Join(args, " "), err)
	}
	return first, where, nil
}

// execute runs a program on this machine, within [ProgramWait].
func execute(ctx context.Context, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ProgramWait)
	defer cancel()
	return exec.CommandContext(ctx, program, args...).CombinedOutput()
}

// lookPath is the path inside the image of the first executable file called name in
// dirs, "" for none. A directory that is not absolute is skipped, as the working
// directory is no place of the image's.
func (p Probe) lookPath(name string, dirs []string) string {
	for _, d := range dirs {
		if !path.IsAbs(d) {
			continue
		}
		inside := path.Join(d, name)
		if info, err := os.Stat(p.host(inside)); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return inside
		}
	}
	return ""
}

// docker reports whether the image can carry a Docker of the agent's own: dockerd and
// every program it runs in [SystemDirs], where the runner looks for the daemon and which
// it gives the daemon as its PATH, and the docker command on the image's PATH, where
// the agent finds it.
func (p Probe) docker() Check {
	dockerd := p.lookPath("dockerd", SystemDirs)
	if dockerd == "" {
		return Check{"docker", Info, "no dockerd in " + strings.Join(SystemDirs, ", ") + ": the image carries no Docker of the agent's own"}
	}
	var missing []string
	for _, name := range daemonRuns {
		if p.lookPath(name, SystemDirs) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Check{"docker", Fail, fmt.Sprintf("%s is there, and %s is not in %s, where the daemon finds what it runs", dockerd, strings.Join(missing, ", "), strings.Join(SystemDirs, ", "))}
	}
	if p.lookPath("docker", filepath.SplitList(p.Env("PATH"))) == "" {
		return Check{"docker", Fail, dockerd + " is there, and the docker command is not on the image's PATH, where the agent finds it"}
	}
	return Check{"docker", Pass, fmt.Sprintf("%s, with %s in the system's directories and docker on the PATH: the image can carry a Docker of the agent's own", dockerd, strings.Join(daemonRuns, ", "))}
}
