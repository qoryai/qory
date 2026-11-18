package pushcheck_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/pushcheck"
)

// mountSrc is the source of the --mount of create whose destination is dst, "" for none.
func mountSrc(create []string, dst string) string {
	for i, a := range create {
		if i > 0 && create[i-1] == "--mount" && strings.Contains(a, ",dst="+dst) {
			return strings.TrimPrefix(strings.Split(a, ",")[1], "src=")
		}
	}
	return ""
}

// flagValue is the word after flag in create, "" for none.
func flagValue(create []string, flag string) string {
	if i := slices.Index(create, flag); i >= 0 && i+1 < len(create) {
		return create[i+1]
	}
	return ""
}

// proxyServer is a TCP server standing in for the push proxy: it answers each connection's
// first read with "ok <that read>".
func proxyServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 256)
				n, _ := c.Read(buf)
				fmt.Fprintf(c, "ok %s", buf[:n])
			}()
		}
	}()
	return l.Addr().String()
}

// programFixture is a checker whose engine runs run as the program's container, with a proxy
// at proxyURL and a CA bundle with the text "ca".
type programFixture struct {
	*fixture
	proxy, bundle string
	// seen is what the container saw at its start: the create's line, and the files of the
	// work and net folders by name, taken before run is called.
	create []string
}

// newProgramFixture is a [programFixture] whose "program" is run, which gets the create's
// command line and the three folders of the container, and returns the exit status.
func newProgramFixture(t *testing.T, run func(f *programFixture, work, netDir string) (stdout, stderr string, code int)) *programFixture {
	t.Helper()
	f := &programFixture{fixture: newFixture(t, "#!/usr/bin/env node")}
	f.proxy = "http://qory:s3cret-pass@" + proxyServer(t)
	f.bundle = filepath.Join(t.TempDir(), "bundle.pem")
	if err := os.WriteFile(f.bundle, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.c.Dir = t.TempDir()
	f.e.start = func(ctx context.Context, stdout, stderr io.Writer) error {
		f.create = f.e.created(t)
		if run == nil {
			return nil
		}
		out, errOut, code := run(f, mountSrc(f.create, pushcheck.ProgramWorkDir), mountSrc(f.create, pushcheck.ProgramNetDir))
		io.WriteString(stdout, out)
		io.WriteString(stderr, errOut)
		f.e.state = fmt.Sprintf(`{"Status":"exited","ExitCode":%d,"Error":""}`, code)
		if code != 0 {
			return errors.New("exit status")
		}
		return nil
	}
	return f
}

// program is a run of the program with the submission content.
func (f *programFixture) program(content string) pushcheck.Program {
	return pushcheck.Program{
		Command:    []string{f.program_(), "--flag"},
		Submission: []byte(content),
		ProxyURL:   f.proxy,
		CABundle:   f.bundle,
	}
}

// program_ is the path of the module's program in the home.
func (f *programFixture) program_() string { return f.fixture.program }

// TestTheProgramContainerIsClosedOff is the create of the submit program's container: no
// network, the run's user, no capabilities, no pull, the wall's label and limits, the home and
// the net folder read-only, the work folder writable, and no variable and no password on the
// command line.
func TestTheProgramContainerIsClosedOff(t *testing.T) {
	f := newProgramFixture(t, nil)
	f.c.Label = "dev.qory.run=run-1"
	if _, err := f.c.RunProgram(context.Background(), f.program("{}")); err != nil {
		t.Fatal(err)
	}
	line := strings.Join(f.create, " ")
	for _, want := range []string{
		"--network none", "--user " + f.c.User, "--cap-drop ALL", "--security-opt no-new-privileges", "--init",
		"--pull never", "--label dev.qory.run=run-1", "--pids-limit 512", "--workdir " + pushcheck.ProgramWorkDir,
		"--entrypoint " + f.program_() + " " + f.c.Image + " --flag",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the create lacks %q:\n%s", want, line)
		}
	}
	for _, flag := range []string{"-e", "--env", "--privileged", "--volume", "-v"} {
		if slices.Contains(f.create, flag) {
			t.Errorf("the create has %s:\n%s", flag, line)
		}
	}
	if strings.Contains(line, "s3cret-pass") {
		t.Errorf("the password is on the command line:\n%s", line)
	}
	if got := f.e.verbs(); !slices.Equal(got, []string{"create", "start", "inspect", "rm"}) {
		t.Errorf("the engine ran %v", got)
	}
	var modes []string
	for i, a := range f.create {
		if i > 0 && f.create[i-1] == "--mount" {
			modes = append(modes, strings.Split(a, ",")[2]+" "+fmt.Sprint(strings.HasSuffix(a, ",readonly")))
		}
	}
	want := []string{"dst=" + f.home + " true", "dst=" + pushcheck.ProgramWorkDir + " false", "dst=" + pushcheck.ProgramNetDir + " true"}
	if !slices.Equal(modes, want) {
		t.Errorf("the mounts are %v, want %v", modes, want)
	}
}

// TestTheProgramGetsItsInputsAndNoSecretOnItsFiles is what the container sees: the
// submission file at its path, the environment in a file that is no mount, a read-only copy
// of the CA bundle and the relay's socket in the net folder; and the folders are private to
// this user and gone after the run.
func TestTheProgramGetsItsInputsAndNoSecretOnItsFiles(t *testing.T) {
	var work, netDir, envPath, root string
	var seen map[string]string
	var envMode, workMode, netMode, socketMode, fileMode fs.FileMode
	f := newProgramFixture(t, func(f *programFixture, w, n string) (string, string, int) {
		work, netDir = w, n
		root = filepath.Dir(w)
		envPath = flagValue(f.create, "--env-file")
		data, _ := os.ReadFile(envPath)
		seen = map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			k, v, _ := strings.Cut(line, "=")
			seen[k] = v
		}
		stat := func(p string) fs.FileMode {
			info, err := os.Lstat(p)
			if err != nil {
				t.Errorf("stat %s: %v", p, err)
				return 0
			}
			return info.Mode()
		}
		envMode, workMode, netMode = stat(envPath).Perm(), stat(w).Perm(), stat(n).Perm()
		socketMode, fileMode = stat(filepath.Join(n, "proxy.sock")), stat(filepath.Join(w, "submission.json")).Perm()
		return "", "", 0
	})
	if _, err := f.c.RunProgram(context.Background(), f.program(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"QORY_SUBMISSION_FILE": "/qory-submit/work/submission.json", "QORY_PROXY_SOCKET": "/qory-submit/net/proxy.sock",
		"QORY_PROXY_PASSWORD": "s3cret-pass", "SSL_CERT_FILE": "/qory-submit/net/ca.pem",
	} {
		if seen[k] != v {
			t.Errorf("%s is %q, want %q", k, seen[k], v)
		}
	}
	if len(seen) != 4 {
		t.Errorf("the environment is %v, want 4 variables", seen)
	}
	if envMode != 0o600 || workMode != 0o700 || netMode != 0o700 || fileMode != 0o600 {
		t.Errorf("modes: env %o, work %o, net %o, submission %o, want 600, 700, 700, 600", envMode, workMode, netMode, fileMode)
	}
	if socketMode&fs.ModeSocket == 0 || socketMode.Perm() != 0o600 {
		t.Errorf("the socket's mode is %v, want a socket, 0600", socketMode)
	}
	if mountSrc(f.create, filepath.Dir(envPath)) != "" || strings.Contains(strings.Join(f.create, " "), "src="+root+",") {
		t.Errorf("the container mounts the folder with the environment file:\n%v", f.create)
	}
	for _, p := range []string{root, work, netDir} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is left after the run: %v", p, err)
		}
	}
}

// TestTheCABundleIsACopy is the bundle in the net folder: the same bytes, and the path of
// the run's bundle is not mounted.
func TestTheCABundleIsACopy(t *testing.T) {
	var got string
	f := newProgramFixture(t, func(_ *programFixture, _, n string) (string, string, int) {
		data, _ := os.ReadFile(filepath.Join(n, "ca.pem"))
		got = string(data)
		return "", "", 0
	})
	if _, err := f.c.RunProgram(context.Background(), f.program("{}")); err != nil {
		t.Fatal(err)
	}
	if got != "ca" {
		t.Errorf("the copy is %q, want ca", got)
	}
	if strings.Contains(strings.Join(f.create, " "), f.bundle) {
		t.Errorf("the create names the run's bundle:\n%v", f.create)
	}
	// A run with no bundle sets no SSL_CERT_FILE.
	var env string
	f = newProgramFixture(t, func(f *programFixture, _, _ string) (string, string, int) {
		data, _ := os.ReadFile(flagValue(f.create, "--env-file"))
		env = string(data)
		return "", "", 0
	})
	p := f.program("{}")
	p.CABundle = ""
	if _, err := f.c.RunProgram(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env, "SSL_CERT_FILE") {
		t.Errorf("the environment has SSL_CERT_FILE with no bundle:\n%s", env)
	}
}

// TestTheRelayCarriesTheProgramsRequestsAndEndsWithTheProgram is a program that connects to
// the socket twice and gets the proxy's answer each time. After the run the socket is gone,
// and a connection finds nothing.
func TestTheRelayCarriesTheProgramsRequestsAndEndsWithTheProgram(t *testing.T) {
	var path string
	var answers []string
	f := newProgramFixture(t, func(_ *programFixture, _, n string) (string, string, int) {
		path = filepath.Join(n, "proxy.sock")
		for _, text := range []string{"one", "two"} {
			c, err := net.Dial("unix", path)
			if err != nil {
				t.Errorf("dial: %v", err)
				return "", "", 1
			}
			c.SetDeadline(time.Now().Add(2 * time.Second))
			io.WriteString(c, text)
			got, _ := io.ReadAll(c)
			c.Close()
			answers = append(answers, string(got))
		}
		return "", "", 0
	})
	if _, err := f.c.RunProgram(context.Background(), f.program("{}")); err != nil {
		t.Fatal(err)
	}
	if want := []string{"ok one", "ok two"}; !slices.Equal(answers, want) {
		t.Errorf("the answers are %q, want %q", answers, want)
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		t.Error("the relay takes a connection after the program ended")
	}
}

// TestTheProgramsFileAndOutputComeBack is the file that the program rewrote, its exit
// status and what it printed.
func TestTheProgramsFileAndOutputComeBack(t *testing.T) {
	for _, code := range []int{0, 1, 2} {
		f := newProgramFixture(t, func(_ *programFixture, w, _ string) (string, string, int) {
			os.WriteFile(filepath.Join(w, "submission.json"), []byte(`{"changed":true}`), 0o600)
			return "https://example.com/pull/1 opened\n", "a line of the fix\n", code
		})
		end, err := f.c.RunProgram(context.Background(), f.program("{}"))
		if err != nil {
			t.Fatal(err)
		}
		if end.Exit != code || end.TimedOut || end.StartErr != "" || string(end.Submission) != `{"changed":true}` ||
			string(end.Stdout) != "https://example.com/pull/1 opened\n" || string(end.Stderr) != "a line of the fix\n" {
			t.Errorf("exit %d: the end is %+v", code, end)
		}
	}
}

// TestAProgramPastItsLimitIsStopped is a program that never ends: the end is timed out, the
// relay is closed, the container is removed, and the file as it was is returned.
func TestAProgramPastItsLimitIsStopped(t *testing.T) {
	var path string
	f := newProgramFixture(t, nil)
	f.c.ProgramLimit = 50 * time.Millisecond
	f.e.start = func(ctx context.Context, _, _ io.Writer) error {
		path = filepath.Join(mountSrc(f.e.created(t), pushcheck.ProgramNetDir), "proxy.sock")
		<-ctx.Done()
		return ctx.Err()
	}
	end, err := f.c.RunProgram(context.Background(), f.program(`{"a":1}`))
	if err != nil || !end.TimedOut || string(end.Submission) != `{"a":1}` {
		t.Fatalf("the end is %+v, %v, want timed out with the file", end, err)
	}
	if want := []string{"create", "start", "rm"}; !slices.Equal(f.e.verbs(), want) {
		t.Errorf("the engine ran %v, want %v", f.e.verbs(), want)
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		t.Error("the relay takes a connection after the time limit")
	}
}

// TestAnEndedContextIsAnError is a step that ended while the program ran: an error, not an
// end of the program, with the container removed.
func TestAnEndedContextIsAnError(t *testing.T) {
	f := newProgramFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	f.e.start = func(ctx context.Context, _, _ io.Writer) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := f.c.RunProgram(ctx, f.program("{}")); err == nil || ctx.Err() == nil {
		t.Errorf("RunProgram = %v, want an error of the ended context", err)
	}
	if !slices.Contains(f.e.verbs(), "rm") {
		t.Errorf("the engine ran %v, want an rm", f.e.verbs())
	}
}

// TestAFileThatIsNotAFileOfTheFolderIsNotRead is a program that leaves a link to a file of
// this machine, one that is too large and none: the file is not read, with the cause.
func TestAFileThatIsNotAFileOfTheFolderIsNotRead(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("not for the submission"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		leave func(path string)
		cause string
	}{
		{"a link", func(p string) { os.Remove(p); os.Symlink(secret, p) }, "the file cannot be opened"},
		{"a folder", func(p string) { os.Remove(p); os.Mkdir(p, 0o700) }, "the file is not a regular file"},
		{"too large", func(p string) { os.WriteFile(p, make([]byte, 4<<20+1), 0o600) }, "the file has more than 4194304 bytes"},
		{"none", func(p string) { os.Remove(p) }, "the file is not there"},
	} {
		f := newProgramFixture(t, func(_ *programFixture, w, _ string) (string, string, int) {
			c.leave(filepath.Join(w, "submission.json"))
			return "", "", 0
		})
		end, err := f.c.RunProgram(context.Background(), f.program("{}"))
		if err != nil || end.Submission != nil || end.FileErr != c.cause {
			t.Errorf("%s: the end is %q %q, %v, want no file and %q", c.name, end.Submission, end.FileErr, err, c.cause)
		}
	}
}

// TestAProgramThatCannotStartIsAnEndWithTheLine is a node that the image lacks: an exit 127
// that the init explains, the end has the line.
func TestAProgramThatCannotStartIsAnEndWithTheLine(t *testing.T) {
	f := newProgramFixture(t, func(*programFixture, string, string) (string, string, int) {
		return "", "/usr/bin/env: 'node': No such file or directory\n", 127
	})
	end, err := f.c.RunProgram(context.Background(), f.program("{}"))
	if err != nil || end.StartErr != "/usr/bin/env: 'node': No such file or directory" {
		t.Errorf("the end is %+v, %v, want the line of the env", end, err)
	}
}

// TestAnEngineThatFailsIsAnError is a create that fails: an error that names the submit
// program's container, with the folders removed.
func TestAnEngineThatFailsIsAnError(t *testing.T) {
	f := newProgramFixture(t, nil)
	f.e.create = func(stderr io.Writer) error {
		io.WriteString(stderr, "Cannot connect to the Docker daemon\n")
		return errors.New("exit status 1")
	}
	_, err := f.c.RunProgram(context.Background(), f.program("{}"))
	if err == nil || !strings.Contains(err.Error(), "the engine did not create the submit program's container: Cannot connect to the Docker daemon") {
		t.Errorf("RunProgram = %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "qory-submit-*")); len(left) != 0 {
		t.Errorf("%v is left after the run", left)
	}
}

// TestAnotherUserGetsFoldersItCanUse is a run's user that is not this process's: the root
// passes it through without a listing, it writes the work folder, and reads the net folder
// and the socket.
func TestAnotherUserGetsFoldersItCanUse(t *testing.T) {
	var modes = map[string]fs.FileMode{}
	f := newProgramFixture(t, func(_ *programFixture, w, n string) (string, string, int) {
		for name, p := range map[string]string{"root": filepath.Dir(w), "work": w, "net": n, "socket": filepath.Join(n, "proxy.sock"), "file": filepath.Join(w, "submission.json")} {
			if info, err := os.Lstat(p); err == nil {
				modes[name] = info.Mode().Perm()
			}
		}
		return "", "", 0
	})
	f.c.User = fmt.Sprintf("%d:%d", os.Getuid()+1, os.Getgid())
	if _, err := f.c.RunProgram(context.Background(), f.program("{}")); err != nil {
		t.Fatal(err)
	}
	want := map[string]fs.FileMode{"root": 0o711, "work": 0o777, "net": 0o755, "socket": 0o666, "file": 0o666}
	for name, mode := range want {
		if modes[name] != mode {
			t.Errorf("%s has mode %o, want %o", name, modes[name], mode)
		}
	}
}

// TestAProxyAddressThatIsNoAddressIsAnError is a proxy URL with no host or no password.
func TestAProxyAddressThatIsNoAddressIsAnError(t *testing.T) {
	f := newProgramFixture(t, nil)
	for _, bad := range []string{"", "http://127.0.0.1:9", "not a url"} {
		p := f.program("{}")
		p.ProxyURL = bad
		if _, err := f.c.RunProgram(context.Background(), p); err == nil {
			t.Errorf("proxy %q: no error", bad)
		}
	}
	if len(f.e.commands) != 0 {
		t.Errorf("the engine ran %v", f.e.verbs())
	}
}
