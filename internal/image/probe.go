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
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/exports"
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
	// one of [ProbeChecks], a runtime's name among them.
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
	// Checks are the probe's lines, one of each of [ProbeChecks] for the runtimes it
	// checked, in that order.
	Checks []Check `json:"checks"`
}

// ProbeChecks are the names of the lines a [Report] of a probe for runtimes contains, in
// the order they print: one line per runtime after shell. A report that lacks one did
// not check it, and the outside counts that as a failure.
func ProbeChecks(runtimes []string) []string {
	return slices.Concat([]string{"home", "authorities", "shell"}, runtimes, []string{"git", "gh", "docker"})
}

// Runtime is one runtime the probe checks the image for: its program on the image's
// PATH, at the version Forager's descriptor sets.
type Runtime struct {
	// Name is the runtime's name, the name of its line.
	Name string
	// Command is the program the runtime's launch starts, as found on the PATH; empty
	// for a runtime this qory does not render, whose line fails.
	Command string
	// Version is the version <Command> --version must report, Forager's descriptor's
	// runtime_version; empty takes any.
	Version string
	// Install is the package at Version that a failed line has as its fix, such as
	// @anthropic-ai/claude-code@2.1.316; empty for a runtime with no package.
	Install string
}

// Bundles are where the wall reads an image's authorities, the first that holds a
// certificate: Debian and Alpine, Red Hat, OpenSUSE, and OpenSSL's default. The wall's own
// list is unexported; this is a copy of Forager's at the version go.mod requires.
var Bundles = []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/ca-bundle.pem", "/etc/ssl/cert.pem"}

// SystemDirs are the directories Forager looks for dockerd in, and gives the daemon as
// its PATH, never the run's. A copy of Forager's list.
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
	// Runtimes are the runtimes to check, Forager's list in its order.
	Runtimes []Runtime
	// User is the user the probe runs as, uid:gid, as the lines name it.
	User string
}

// Report runs every check of the probe, one line for each of [ProbeChecks], in that
// order.
func (p Probe) Report(ctx context.Context) Report {
	if p.Env == nil {
		p.Env = os.Getenv
	}
	checks := []Check{p.home(), p.authorities(), p.shell()}
	for _, rt := range p.Runtimes {
		checks = append(checks, p.runtime(ctx, rt))
	}
	checks = append(checks, p.git(ctx), p.program(ctx, "gh"))
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
		return Check{"home", Fail, "HOME is " + exports.Value(home, false) + ", which is not an absolute path"}
	}
	f, err := os.CreateTemp(p.host(home), ".qory-image-check-")
	if err != nil {
		return Check{"home", Fail, fmt.Sprintf("the user %s cannot write in HOME, %s: %v; make it a directory any user may write, mode 1777", p.User, exports.Value(home, false), unwrapPath(err))}
	}
	f.Close()
	if err := os.Remove(f.Name()); err != nil {
		return Check{"home", Fail, fmt.Sprintf("the user %s wrote a file in HOME, %s, and cannot remove it: %v", p.User, exports.Value(home, false), unwrapPath(err))}
	}
	return Check{"home", Pass, fmt.Sprintf("HOME is %s, and the user %s writes in it", exports.Value(home, false), p.User)}
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

// shell checks for /bin/sh, which a runtime's hooks run under, as sh -c, and the
// session's approval of an API key's placeholder for an interactive Claude Code, as
// /bin/sh and the script it writes into the run directory.
func (p Probe) shell() Check {
	info, err := os.Stat(p.host("/bin/sh"))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return Check{"shell", Fail, "no /bin/sh; the runtime's hooks and the session's API-key approval run under sh"}
	}
	return Check{"shell", Pass, "/bin/sh runs the runtime's hooks and the API-key approval"}
}

// version is the first version number in what a program printed: 2.1.316 in
// "2.1.316 (Claude Code)".
var version = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?`)

// runtime checks that rt's program is on the image's PATH, where the wall's launch
// finds it, and reports the version Forager's descriptor sets.
func (p Probe) runtime(ctx context.Context, rt Runtime) Check {
	if rt.Command == "" {
		return Check{rt.Name, Fail, "set wall.helper to the Linux build of this qory: the probe's build renders no runtime " + rt.Name}
	}
	out, where, err := p.run(ctx, rt.Command, "--version")
	if err != nil {
		return Check{rt.Name, Fail, err.Error()}
	}
	got := version.FindString(out)
	if rt.Version != "" && got != rt.Version {
		if got == "" {
			got = strconv.Quote(out)
		}
		fix := "install that version"
		if rt.Install != "" {
			fix = "install " + rt.Install
		}
		return Check{rt.Name, Fail, fmt.Sprintf("%s at %s is %s, and Forager's descriptor sets %s; %s", rt.Command, exports.Value(where, false), got, rt.Version, fix)}
	}
	if rt.Version == "" {
		return Check{rt.Name, Pass, fmt.Sprintf("%s at %s is %s", rt.Command, exports.Value(where, false), got)}
	}
	return Check{rt.Name, Pass, fmt.Sprintf("%s at %s is %s, the version Forager's descriptor sets", rt.Command, exports.Value(where, false), got)}
}

// program checks that a program is on the image's PATH and answers --version.
func (p Probe) program(ctx context.Context, name string) Check {
	out, _, err := p.run(ctx, name, "--version")
	if err != nil {
		return Check{name, Fail, err.Error()}
	}
	return Check{name, Pass, out}
}

// gitSessionDirs are the folders of the PATH of a walled run's git session.
var gitSessionDirs = []string{"/usr/local/bin", "/usr/bin", "/bin"}

// sessionTools are the programs the script of a walled run's git session runs besides git, with
// what it runs each for.
var sessionTools = []struct{ name, why string }{
	{"cat", "to read its lists of commits and git's lines"},
	{"chmod", "to give itself read access to the files and folders the run made"},
	{"comm", "to compare the run's refs and commits with what the bundle has"},
	{"cp", "to copy the person's ignore rules to its git folder"},
	{"find", "to list executable files, embedded repositories and files over 100 MiB"},
	{"grep", "to find a sparse checkout and what the setup left in a submodule"},
	{"mkdir", "to make its git folder"},
	{"mv", "to put the finished bundle in place"},
	{"readlink", "to read a link the setup left"},
	{"sh", "to run its script"},
	{"sort", "to compare the run's refs and commits with what the bundle has"},
	{"tr", "to print a folder's name with no control character"},
}

// SessionTools is the name of each of [sessionTools].
func SessionTools() []string {
	names := make([]string, 0, len(sessionTools))
	for _, tool := range sessionTools {
		names = append(names, tool.name)
	}
	return names
}

// noSessionGit is the fix for an image with no git in [gitSessionDirs].
func noSessionGit() string {
	return "install git in " + strings.Join(gitSessionDirs, ", ") + ": the git session of a walled run runs git from there, and the image has none in them"
}

// noSessionTool is the fix for an image with no program name in [gitSessionDirs], which the git
// session runs for why.
func noSessionTool(name, why string) string {
	return "install " + name + " in " + strings.Join(gitSessionDirs, ", ") + ": the git session of a walled run runs " + name + " from these folders, " + why
}

// sessionGitFailed is the fix for an image whose git at the path at fails with cause.
func sessionGitFailed(at, cause string) string {
	return "install git 2.25 or later in " + strings.Join(gitSessionDirs, ", ") + ": " + at + " --version failed: " + cause
}

// sessionGitOld is the fix for an image whose git at the path at prints line, older than 2.25.
func sessionGitOld(at, line string) string {
	return "install git 2.25 or later in " + strings.Join(gitSessionDirs, ", ") + ": the git session of a walled run needs it, and " + at + " prints " + line
}

// SessionShellFix is the line for a setup step that could not start /bin/sh.
func SessionShellFix() string {
	for _, tool := range sessionTools {
		if tool.name == "sh" {
			return noSessionTool(tool.name, tool.why)
		}
	}
	return ""
}

// SessionCheckScript is the sh script of a walled write run's setup step that checks, before the
// agent, what [Probe.git] checks of the image: each of [sessionTools] and git 2.25 or later, on
// the PATH of the git session. It prints nothing and exits 0 when all of it is there. Otherwise it
// prints the line of [Probe.git] for each failure on stderr, the fix first, and exits 1. It runs
// only the shell's builtins and git, under dash and busybox ash as well.
func SessionCheckScript() string {
	return sessionCheckScript(strings.Join(gitSessionDirs, ":"))
}

// sessionCheckScript is [SessionCheckScript] with path as the session's PATH.
func sessionCheckScript(path string) string {
	var b strings.Builder
	b.WriteString("PATH=" + shellQuoted(path) + "\nexport PATH\nfailed=0\n")
	b.WriteString(sessionCheckFunctions)
	for _, tool := range sessionTools {
		b.WriteString("need " + tool.name + " " + shellQuoted(noSessionTool(tool.name, tool.why)) + "\n")
	}
	b.WriteString("if found git; then\n")
	b.WriteString("\tline=$(\"$at\" --version 2>/dev/null)\n\tstatus=$?\n\tline=${line%%\n*}\n")
	b.WriteString("\tif test \"$status\" -ne 0; then\n")
	b.WriteString("\t\tfail " + shellQuoted(sessionGitFailed("@AT@", "exit status @STATUS@")) + "\n")
	b.WriteString("\telif ! at_least_225 \"$line\"; then\n")
	b.WriteString("\t\tfail " + shellQuoted(sessionGitOld("@AT@", "@LINE@")) + "\n")
	b.WriteString("\tfi\nelse\n")
	b.WriteString("\tfail " + shellQuoted(noSessionGit()) + "\n")
	b.WriteString("fi\nexit \"$failed\"\n")
	return b.String()
}

// sessionCheckFunctions are the functions of [SessionCheckScript]. found sets at to the first
// executable file with that name in a folder of the PATH, where dash's command -v returns the
// first file, executable or not. A builtin of /bin/sh counts as well, since the session's script
// runs under the same shell. fail
// prints a line with what git's run gave in place of @AT@, @STATUS@ and @LINE@. at_least_225 is
// [gitAtLeast225].
const sessionCheckFunctions = `found() {
	dirs=$PATH:
	while test -n "$dirs"; do
		at=${dirs%%:*}/$1
		dirs=${dirs#*:}
		if test -x "$at" && test ! -d "$at"; then
			return 0
		fi
	done
	at=$(command -v "$1" 2>/dev/null) || at=
	case $at in
	'' | /*) return 1 ;;
	esac
}
fail() {
	text=$1
	case $text in *@AT@*) text=${text%%@AT@*}$at${text#*@AT@} ;; esac
	case $text in *@STATUS@*) text=${text%%@STATUS@*}$status${text#*@STATUS@} ;; esac
	case $text in *@LINE@*) text=${text%%@LINE@*}$line${text#*@LINE@} ;; esac
	printf '%s\n' "$text" >&2
	failed=1
}
need() {
	found "$1" || fail "$2"
}
at_least_225() {
	case $1 in
	'git version '[0-9]*.[0-9]*) ;;
	*) return 1 ;;
	esac
	rest=${1#git version }
	major=${rest%%.*}
	case $major in *[!0-9]*) return 1 ;; esac
	minor=${rest#*.}
	minor=${minor%%[!0-9]*}
	case $minor in '') return 1 ;; esac
	test "$major" -gt 2 || { test "$major" -eq 2 && test "$minor" -ge 25; }
}
`

// shellQuoted is s as one word of sh, in single quotes.
func shellQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// maxSessionGitLine is the longest line about git that [SessionCheckLine] takes, in bytes. The
// line for git version 2.24.0 in /usr/local/bin has 151 bytes.
const maxSessionGitLine = 200

// SessionCheckLine is line, one line of what [SessionCheckScript] printed, as qory prints it, and
// whether the script can print it. The image runs the script, so the line can contain anything: a
// line about a program is one of the fixed lines, and a line about git's version has one of the
// script's forms, at most [maxSessionGitLine] bytes of valid UTF-8 with no control or format
// character.
func SessionCheckLine(line string) (string, bool) {
	if line == noSessionGit() {
		return line, true
	}
	for _, tool := range sessionTools {
		if line == noSessionTool(tool.name, tool.why) {
			return line, true
		}
	}
	if len(line) > maxSessionGitLine || !utf8.ValidString(line) || strings.IndexFunc(line, unprintable) >= 0 {
		return "", false
	}
	for _, dir := range gitSessionDirs {
		at := dir + "/git"
		if strings.HasPrefix(line, sessionGitOld(at, "")) {
			return exports.HideValues(line), true
		}
		if status, ok := strings.CutPrefix(line, sessionGitFailed(at, "exit status ")); ok && isStatus(status) {
			return line, true
		}
	}
	return "", false
}

// SessionCheckFixes is what qory prints of text, the output of [SessionCheckScript]: each line
// [SessionCheckLine] takes, and at most as many as the script can print.
func SessionCheckFixes(text string) []string {
	var fixes []string
	for line := range strings.Lines(text) {
		if fix, ok := SessionCheckLine(strings.TrimRight(line, "\n")); ok {
			fixes = append(fixes, fix)
			if len(fixes) == len(sessionTools)+1 {
				break
			}
		}
	}
	return fixes
}

// unprintable reports whether r is a control or format character, which a terminal can act on.
func unprintable(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }

// isStatus reports whether s is an exit status: one to three digits.
func isStatus(s string) bool {
	if s == "" || len(s) > 3 {
		return false
	}
	return strings.Trim(s, "0123456789") == ""
}

// git checks that git runs, and what the git session of a walled run that may write needs of
// it: git 2.25 or later, for --pathspec-from-file, and [sessionTools], in [gitSessionDirs].
func (p Probe) git(ctx context.Context) Check {
	line, at, err := p.run(ctx, "git", "--version")
	if err != nil {
		return Check{"git", Fail, err.Error()}
	}
	session := p.lookPath("git", gitSessionDirs)
	if session == "" {
		return Check{"git", Fail, noSessionGit()}
	}
	for _, tool := range sessionTools {
		if p.lookPath(tool.name, gitSessionDirs) != "" {
			continue
		}
		return Check{"git", Fail, noSessionTool(tool.name, tool.why)}
	}
	shown := line
	if session != at {
		run := p.Exec
		if run == nil {
			run = execute
		}
		out, err := run(ctx, p.host(session), "--version")
		if err != nil {
			return Check{"git", Fail, sessionGitFailed(exports.Value(session, false), exports.HideValueError(err, session).Error())}
		}
		shown, _, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
		shown = exports.HideValues(shown)
	}
	if !gitAtLeast225(shown) {
		return Check{"git", Fail, sessionGitOld(exports.Value(session, false), shown)}
	}
	return Check{"git", Pass, line}
}

// gitAtLeast225 reports whether line, as git --version prints it, has git 2.25 or later.
func gitAtLeast225(line string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(line, "git version %d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 2 || major == 2 && minor >= 25
}

// run finds a program on the image's PATH and runs it, returning the first line it
// printed and where it was found. The error is the line that says what failed.
func (p Probe) run(ctx context.Context, name string, args ...string) (string, string, error) {
	where := p.lookPath(name, filepath.SplitList(p.Env("PATH")))
	if where == "" {
		return "", "", fmt.Errorf("no %s on the image's PATH, %s", name, exports.Value(p.Env("PATH"), false))
	}
	run := p.Exec
	if run == nil {
		run = execute
	}
	out, err := run(ctx, p.host(where), args...)
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	first = exports.HideValues(first)
	if err != nil {
		if first != "" {
			return "", where, fmt.Errorf("%s %s failed: %v: %s", exports.Value(where, false), strings.Join(args, " "), exports.HideValueError(err, where), first)
		}
		return "", where, fmt.Errorf("%s %s failed: %v", exports.Value(where, false), strings.Join(args, " "), exports.HideValueError(err, where))
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

// docker reports whether the image can carry a nested Docker for the agent: dockerd and
// every program it runs in [SystemDirs], where Forager looks for the daemon and which
// it gives the daemon as its PATH, and the docker command on the image's PATH, where
// the agent finds it.
func (p Probe) docker() Check {
	dockerd := p.lookPath("dockerd", SystemDirs)
	if dockerd == "" {
		return Check{"docker", Info, "no dockerd in " + strings.Join(SystemDirs, ", ") + ": the image carries no nested Docker"}
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
	return Check{"docker", Pass, fmt.Sprintf("%s, with %s in the system's directories and docker on the PATH: the image can carry nested Docker", dockerd, strings.Join(daemonRuns, ", "))}
}
