//go:build unix

package cmd_test

import (
	"bytes"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// TestRunPrepareStopsGitOnSIGTERM sends SIGTERM to qory run prepare alone, as Forager's
// preparation script does, while git waits on a proxy that never answers: the verb
// ends git's whole process group, git-remote-https among them, so the proxy's
// connection closes, and the verb exits within seconds with one line that says git was
// stopped.
func TestRunPrepareStopsGitOnSIGTERM(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conns := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- c
		}
	}()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	proxy := "http://" + ln.Addr().String()
	var stderr bytes.Buffer
	c := exec.Command(os.Args[0], "-test.run=^$")
	c.Dir = dir
	c.Stderr = &stderr
	c.Env = append(os.Environ(),
		"QORY_TEST_PREPARE=1",
		"QORY_RUN_SYSTEM=git.example.com",
		"QORY_RUN_REPOSITORIES=acme/shop",
		"HTTPS_PROXY="+proxy, "https_proxy="+proxy, "ALL_PROXY=", "all_proxy=",
		"NO_PROXY=", "no_proxy=",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	t.Cleanup(func() { _ = c.Process.Kill() })

	var held net.Conn
	select {
	case held = <-conns:
		defer held.Close()
	case err := <-done:
		t.Fatalf("qory run prepare exited before git reached the proxy: %v\n%s", err, stderr.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("git did not reach the proxy\n%s", stderr.String())
	}
	if err := c.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	select {
	case err := <-done:
		if err == nil {
			t.Error("qory run prepare exited 0 after SIGTERM")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("qory run prepare still runs 10s after SIGTERM\n%s", stderr.String())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("qory run prepare took %s to stop", took)
	}
	want := "qory run prepare: acme/shop could not be fetched from git.example.com: git ls-remote was stopped before it finished\n"
	if got := stderr.String(); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	// The connection git-remote-https held is closed once what git sent is read: no
	// process of git's is left.
	_ = held.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, held); err != nil && !strings.Contains(err.Error(), "reset") {
		t.Errorf("the proxy's connection is still open after the verb exited: %v", err)
	}
}

// prepareRemote makes the working directory an empty git working tree, in the
// environment [emptyDir] sets, and the repository acme/shop on example.com with
// README.md and files in a second commit on main: a bare repository under a folder it
// returns, which [gitServer] serves to qory run prepare, and which the test's gits
// reach through a url rewrite in the test's global configuration, which qory run
// prepare does not read. The variables name acme/shop, and the image's copy is looked
// for where there is none.
func prepareRemote(t *testing.T, files map[string]string) (dir, base string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir = emptyDir(t)
	runGit(t, dir, "init", "--quiet", "-b", "master")
	for _, kv := range []string{"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com", "GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com"} {
		name, value, _ := strings.Cut(kv, "=")
		t.Setenv(name, value)
	}
	base = tempDir(t)
	writeFile(t, os.Getenv("GIT_CONFIG_GLOBAL"), "[url \"file://"+base+"/\"]\n\tinsteadOf = https://example.com/\n")
	gitServer(t, base)
	work := tempDir(t)
	runGit(t, work, "init", "--quiet", "-b", "main")
	writeFile(t, filepath.Join(work, "first.txt"), "first\n")
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "first")
	writeFile(t, filepath.Join(work, "README.md"), "acme/shop\n")
	for name, content := range files {
		writeFile(t, filepath.Join(work, filepath.FromSlash(name)), content)
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "second")
	runGit(t, base, "clone", "--quiet", "--bare", work, filepath.Join(base, "acme", "shop.git"))
	t.Setenv("QORY_RUN_SYSTEM", "example.com")
	t.Setenv("QORY_RUN_REPOSITORIES", "acme/shop")
	t.Cleanup(cmd.SetPrepareSeed(filepath.Join(tempDir(t), "seed")))
	return dir, base
}

// gitServer serves the bare repositories under base over https, with git's
// http-backend, as example.com, the name the test server's certificate holds: the
// variables of the environment point git at a proxy that tunnels each connection to
// the server, and at the server's certificate as the authority, as the wall gives git
// its proxy and its authority.
func gitServer(t *testing.T, base string) {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not installed")
	}
	srv := httptest.NewTLSServer(&cgi.Handler{
		Path:       backend,
		Env:        []string{"GIT_PROJECT_ROOT=" + base, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull},
		InheritEnv: []string{"PATH"},
	})
	t.Cleanup(srv.Close)
	ca := filepath.Join(tempDir(t), "ca.pem")
	writeFile(t, ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		up, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			up.Close()
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		go func() {
			_, _ = io.Copy(up, buf)
			up.Close()
		}()
		_, _ = io.Copy(conn, up)
		conn.Close()
	}))
	t.Cleanup(proxy.Close)
	for _, name := range []string{"HTTPS_PROXY", "https_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	for _, name := range []string{"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("GIT_SSL_CAINFO", ca)
}

// pushNewer pushes a commit to acme/shop that adds newer.txt, and returns it.
func pushNewer(t *testing.T) string {
	t.Helper()
	work := tempDir(t)
	runGit(t, work, "clone", "--quiet", "https://example.com/acme/shop.git", ".")
	writeFile(t, filepath.Join(work, "newer.txt"), "newer\n")
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "--quiet", "-m", "newer")
	runGit(t, work, "push", "--quiet", "origin", "main")
	return gitOut(t, work, "rev-parse", "HEAD")
}

// prepareTimes are the times in the lines of qory run prepare.
var prepareTimes = regexp.MustCompile(`(?m) in [0-9]+\.[0-9]s$`)

// imageCopy makes the image's copy of acme/shop: a clone at depth one, with an ignored
// dependency, an untracked file, hooks and a file system monitor that write to marker,
// and a configuration with keys that run programs. qory run prepare looks for it there.
func imageCopy(t *testing.T, marker string) string {
	t.Helper()
	seed := filepath.Join(tempDir(t), "seed")
	runGit(t, tempDir(t), "clone", "--quiet", "--depth", "1", "https://example.com/acme/shop.git", seed)
	writeFile(t, filepath.Join(seed, "deps", "lib.js"), "dependency\n")
	writeFile(t, filepath.Join(seed, "notes.txt"), "untracked\n")
	script := "#!/bin/sh\necho \"$0\" >> '" + marker + "'\n"
	for _, name := range []string{"hooks/post-checkout", "hooks/reference-transaction", "monitor"} {
		file := filepath.Join(seed, ".git", name)
		writeFile(t, file, script)
		if err := os.Chmod(file, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(seed, ".git", "config")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(data)+"[core]\n\tfsmonitor = "+filepath.Join(seed, ".git", "monitor")+
		"\n\tsshCommand = /nonexistent/ssh\n\tgitProxy = /nonexistent/proxy\n[credential]\n\thelper = /nonexistent/helper\n"+
		"[filter \"lfs\"]\n\tsmudge = /nonexistent/smudge\n[url \"https://elsewhere.example.com/\"]\n\tinsteadOf = https://\n"+
		"[include]\n\tpath = /nonexistent/included\n")
	t.Cleanup(cmd.SetPrepareSeed(seed))
	return seed
}

// TestRunPrepareStartsFromTheImagesCopy is a run in a prebuilt image: qory run prepare
// copies the working tree of the image's copy, fetches into the runner's repository and
// checks out the run's commit over the files, keeping the untracked and ignored ones,
// takes none of the copy's .git, its hooks and configuration among it, then runs postCreateCommand and postStartCommand with the working
// folder's variables and the environment, and prints a line for each step with times.
func TestRunPrepareStartsFromTheImagesCopy(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".gitignore": "deps/\n",
		".devcontainer/devcontainer.json": `{
			"onCreateCommand": "touch onCreate",
			"postCreateCommand": "echo create ${containerWorkspaceFolderBasename} $GIVEN $MODE $EXTRA; ls deps",
			"postStartCommand": {"serve": "echo start ${containerWorkspaceFolder}"},
			"containerEnv": {"MODE": "container", "EXTRA": "container"},
			"remoteEnv": {"EXTRA": "${containerEnv:MODE}-remote"}
		}`,
	})
	runGit(t, dir, "config", "qory.made", "by the runner")
	marker := filepath.Join(tempDir(t), "ran")
	imageCopy(t, marker)
	head := pushNewer(t)
	t.Setenv("GIVEN", "given")
	out, err := run(t, "run", "prepare")
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, out)
	}
	want := "qory run prepare: copied the working tree of acme/shop from the image\n" +
		"qory run prepare: fetched acme/shop in Xs\n" +
		"create " + filepath.Base(dir) + " given container container-remote\nlib.js\n" +
		"start " + dir + "\n" +
		"qory run prepare: ran postCreateCommand and postStartCommand in Xs\n"
	if got := prepareTimes.ReplaceAllString(out, " in Xs"); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD is %s, want %s", got, head)
	}
	if got := gitOut(t, dir, "status", "--porcelain", "--ignored"); got != "?? notes.txt\n!! deps/" {
		t.Errorf("git status: %q", got)
	}
	for name, want := range map[string]string{"newer.txt": "newer\n", "notes.txt": "untracked\n", "deps/lib.js": "dependency\n"} {
		if got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))); err != nil || string(got) != want {
			t.Errorf("%s holds %q, %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "onCreate")); !os.IsNotExist(err) {
		t.Errorf("onCreateCommand ran: %v", err)
	}
	if got, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Errorf("a hook or the monitor of the copy ran: %v\n%s", err, got)
	}
	if got := gitOut(t, dir, "config", "qory.made"); got != "by the runner" {
		t.Errorf("the working folder's repository is not the runner's: qory.made is %q", got)
	}
	keys := gitOut(t, dir, "config", "--file", filepath.Join(".git", "config"), "--name-only", "--list")
	for _, key := range []string{"fsmonitor", "sshcommand", "gitproxy", "credential", "filter", "insteadof", "include"} {
		if strings.Contains(keys, key) {
			t.Errorf("the configuration holds a %s key of the copy's:\n%s", key, keys)
		}
	}
	for _, hook := range []string{"post-checkout", "reference-transaction"} {
		if _, err := os.Lstat(filepath.Join(dir, ".git", "hooks", hook)); !os.IsNotExist(err) {
			t.Errorf("the copy's %s hook was taken: %v", hook, err)
		}
	}
}

// TestRunPrepareRefusesACopyOfAnotherRepository is an image's copy whose origin is not
// the run's repository: one line, an exit that is not 0, and nothing copied.
func TestRunPrepareRefusesACopyOfAnotherRepository(t *testing.T) {
	dir, _ := prepareRemote(t, nil)
	seed := imageCopy(t, filepath.Join(tempDir(t), "ran"))
	runGit(t, seed, "remote", "set-url", "origin", "https://example.com/acme/other.git")
	out, err := run(t, "run", "prepare")
	want := "qory run prepare: the image's copy in " + seed + " is not of acme/shop: its origin is not https://example.com/acme/shop\n"
	if out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if cmd.ExitCode(err) == 0 {
		t.Error("qory run prepare exited 0")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the working folder holds %d entries", len(entries))
	}
}

// TestRunPrepareRefusesAGitFile is a working folder whose .git is a file, as a
// worktree's is: one line and an exit that is not 0.
func TestRunPrepareRefusesAGitFile(t *testing.T) {
	dir, _ := prepareRemote(t, nil)
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".git"), "gitdir: /nonexistent/worktrees/shop\n")
	out, err := run(t, "run", "prepare")
	want := "qory run prepare: the working folder's .git is a file or a link, which points at a repository elsewhere; the run's repository is fetched only into an empty git working tree\n"
	if out != want || cmd.ExitCode(err) == 0 {
		t.Errorf("printed %q, exit %d; want %q", out, cmd.ExitCode(err), want)
	}
}

// TestRunPrepareSaysWhichCommandFailed is a postCreateCommand that fails: the line
// names it, not its text, the exit is not 0, and postStartCommand does not run.
func TestRunPrepareSaysWhichCommandFailed(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postCreateCommand": ["sh", "-c", "exit 7"], "postStartCommand": "touch started"}`,
	})
	out, err := run(t, "run", "prepare")
	want := "qory run prepare: fetched acme/shop in Xs\nqory run prepare: postCreateCommand exited with status 7\n"
	if got := prepareTimes.ReplaceAllString(out, " in Xs"); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if !errors.Is(err, cmd.ErrReported) || cmd.ExitCode(err) == 0 {
		t.Errorf("%v exits %d", err, cmd.ExitCode(err))
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !os.IsNotExist(err) {
		t.Errorf("postStartCommand ran: %v", err)
	}
}

// TestRunPrepareLeavesAServiceRunning runs qory run prepare as a separate process, its
// stdout and stderr files, with a postStartCommand that leaves a service running, which
// writes to its stdout after the verb exited, beside a step that does nothing: the verb
// exits 0 at once, not after the grace, and the service keeps running and writing, to
// the verb's stderr. Nothing reaches the verb's stdout.
func TestRunPrepareLeavesAServiceRunning(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postStartCommand": {"serve": "(sleep 1; echo serving; exec sleep 60) & echo $! > pid", "note": "true"}}`,
	})
	files := tempDir(t)
	stdout, err := os.Create(filepath.Join(files, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(files, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	c := exec.Command(os.Args[0], "-test.run=^$")
	c.Dir = dir
	c.Stdout, c.Stderr = stdout, stderr
	c.Env = append(os.Environ(), "QORY_TEST_PREPARE=1", "QORY_TEST_PREPARE_SEED="+filepath.Join(tempDir(t), "seed"))
	start := time.Now()
	err = c.Run()
	printed, _ := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, printed)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("qory run prepare took %s, held by the service", took)
	}
	if !regexp.MustCompile(`\nqory run prepare: ran postStartCommand in [0-9]+\.[0-9]s\n`).Match(printed) {
		t.Errorf("printed %q", printed)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, _ := os.ReadFile(stderr.Name()); strings.HasSuffix(string(got), "s\nserving\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the service wrote nothing after the verb exited")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the service stopped: %v", err)
	}
	if got, _ := os.ReadFile(stdout.Name()); len(got) != 0 {
		t.Errorf("the verb's stdout holds %q", got)
	}
}

// TestRunPrepareWritesTheCommandsToStderr is a start command that writes to its stdout
// and its stderr: both reach the verb's stderr, in order, and nothing its stdout.
func TestRunPrepareWritesTheCommandsToStderr(t *testing.T) {
	prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postCreateCommand": "echo to stdout; echo to stderr >&2"}`,
	})
	var stdout, stderr bytes.Buffer
	root := cmd.Root()
	root.SetArgs([]string{"run", "prepare"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, stderr.String())
	}
	want := "qory run prepare: fetched acme/shop in Xs\nto stdout\nto stderr\nqory run prepare: ran postCreateCommand in Xs\n"
	if got := prepareTimes.ReplaceAllString(stderr.String(), " in Xs"); got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout %q", stdout.String())
	}
}

// TestRunPrepareRunsNoConfigurationOfTheCopy is an image's copy that holds a
// configuration the run's commit does not: no start command runs from it, and a line
// says so.
func TestRunPrepareRunsNoConfigurationOfTheCopy(t *testing.T) {
	dir, _ := prepareRemote(t, nil)
	seed := imageCopy(t, filepath.Join(tempDir(t), "ran"))
	writeFile(t, filepath.Join(seed, ".devcontainer.json"), `{"postCreateCommand": "touch ran"}`)
	out, err := run(t, "run", "prepare")
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, out)
	}
	want := "qory run prepare: copied the working tree of acme/shop from the image\n" +
		"qory run prepare: fetched acme/shop in Xs\n" +
		"qory run prepare: .devcontainer.json is not in the run's commit; no start command runs from it\n"
	if got := prepareTimes.ReplaceAllString(out, " in Xs"); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
		t.Errorf("the copy's configuration ran: %v", err)
	}
}
