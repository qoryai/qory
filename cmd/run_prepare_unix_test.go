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

// prepareRemote makes the working directory an empty git working tree, in the
// environment [emptyDir] sets, and the repository acme/shop on example.com with
// README.md and files in a second commit on main: a bare repository under a folder it
// returns, which [gitServer] serves, and which the test's gits reach through a url
// rewrite in the test's global configuration, which qory run prepare does not read. The
// variables name acme/shop, and the image's copy is looked for where there is none.
// [fetchedOnHost] then fetches acme/shop into the working directory, as qory run does
// before the session joins.
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

// fetchedOnHost fetches the head of main of acme/shop into dir, the working directory
// [prepareRemote] made, at depth one, checked out as main tracking origin, as qory run
// fetches the run's repository on this machine before the session joins. The file of
// the start commands' output goes into a temporary folder of the test's.
func fetchedOnHost(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("TMPDIR", tempDir(t))
	runGit(t, dir, "remote", "add", "origin", "https://example.com/acme/shop.git")
	runGit(t, dir, "fetch", "--quiet", "--depth", "1", "origin", "+refs/heads/main:refs/remotes/origin/main")
	runGit(t, dir, "checkout", "--quiet", "-b", "main", "--track", "origin/main")
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

// setupLogs matches the path in the line of qory run prepare that names the file of the
// start commands' output.
var setupLogs = regexp.MustCompile(`(?m)^qory run prepare: \S*/qory-setup-[0-9]+\.log in the container that runs the setup contains`)

// setupLogLine is that line, with LOG in place of the path, as [shownPrepare] has it.
const setupLogLine = "qory run prepare: LOG in the container that runs the setup contains the start commands' output, and what a program they leave running writes later\n"

// shownPrepare is out, the lines of qory run prepare, with X in place of each time and
// LOG in place of the path of the file of the start commands' output.
func shownPrepare(out string) string {
	return setupLogs.ReplaceAllString(prepareTimes.ReplaceAllString(out, " in Xs"), "qory run prepare: LOG in the container that runs the setup contains")
}

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

// TestRunPrepareStartsFromTheImagesCopy is a run in a prebuilt image, whose working
// folder qory run fetched the run's commit into: qory run prepare adds the untracked and
// ignored files of the image's copy, keeps the fetched files, takes none of the copy's
// .git, its hooks and configuration among it, then runs postCreateCommand and
// postStartCommand with the working folder's variables and the environment, and prints
// a line for each step with times.
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
	fetchedOnHost(t, dir)
	t.Setenv("GIVEN", "given")
	out, err := run(t, "run", "prepare")
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, out)
	}
	want := "qory run prepare: added from the image's copy the files that the commit of acme/shop does not track\n" +
		"create " + filepath.Base(dir) + " given container container-remote\nlib.js\n" +
		"start " + dir + "\n" +
		"qory run prepare: ran postCreateCommand and postStartCommand in Xs\n" + setupLogLine
	if got := shownPrepare(out); got != want {
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
	fetchedOnHost(t, dir)
	runGit(t, seed, "remote", "set-url", "origin", "https://example.com/acme/other.git")
	out, err := run(t, "run", "prepare")
	want := "qory run prepare: the image's copy in " + seed + " is not of acme/shop: its origin is not https://example.com/acme/shop\n"
	if out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if cmd.ExitCode(err) == 0 {
		t.Error("qory run prepare exited 0")
	}
	for _, name := range []string{"notes.txt", "deps"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("the copy's %s was added: %v", name, err)
		}
	}
}

// TestRunPrepareSaysWhichCommandFailed is a postCreateCommand that fails: the line
// names it, not its text, the exit is not 0, and postStartCommand does not run.
func TestRunPrepareSaysWhichCommandFailed(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postCreateCommand": ["sh", "-c", "exit 7"], "postStartCommand": "touch started"}`,
	})
	fetchedOnHost(t, dir)
	out, err := run(t, "run", "prepare")
	want := "qory run prepare: postCreateCommand exited with status 7\n"
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
// stdout and stderr pipes, as docker exec and the setup runner give it, with a
// postStartCommand that leaves a service running, which writes after the verb exited,
// beside a step that does nothing: the verb exits 0 at once, its pipes closed, not after
// the grace, and the service keeps running and writing, to the file of the start
// commands' output in the temporary folder, which the verb's last line names. Nothing
// reaches the verb's stdout.
func TestRunPrepareLeavesAServiceRunning(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postStartCommand": {"serve": "(sleep 1; echo serving; exec sleep 60) & echo $! > pid", "note": "true"}}`,
	})
	fetchedOnHost(t, dir)
	tmp := tempDir(t)
	var stdout, stderr bytes.Buffer
	c := exec.Command(os.Args[0], "-test.run=^$")
	c.Dir = dir
	c.Stdout, c.Stderr = &stdout, &stderr
	c.Env = append(os.Environ(), "QORY_TEST_PREPARE=1", "QORY_TEST_PREPARE_SEED="+filepath.Join(tempDir(t), "seed"), "TMPDIR="+tmp)
	start := time.Now()
	err := c.Run()
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, stderr.String())
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("qory run prepare took %s, held by the service", took)
	}
	printed := stderr.String()
	if want := "qory run prepare: ran postStartCommand in Xs\n" + setupLogLine; !strings.HasSuffix(shownPrepare(printed), want) {
		t.Errorf("printed %q, want it to end with %q", printed, want)
	}
	logs, _ := filepath.Glob(filepath.Join(tmp, "qory-setup-*.log"))
	if len(logs) != 1 || !strings.Contains(printed, logs[0]) {
		t.Fatalf("the files of the output: %v\n%s", logs, printed)
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
		if got, _ := os.ReadFile(logs[0]); strings.HasSuffix(string(got), "serving\n") {
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
	if strings.Contains(printed, "serving") || stdout.Len() != 0 {
		t.Errorf("the verb's stdout holds %q, its stderr %q", stdout.String(), printed)
	}
}

// TestRunPrepareWritesTheCommandsToStderr is a start command that writes to its stdout
// and its stderr: both reach the verb's stderr, in order, and nothing its stdout.
func TestRunPrepareWritesTheCommandsToStderr(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postCreateCommand": "echo to stdout; echo to stderr >&2"}`,
	})
	fetchedOnHost(t, dir)
	var stdout, stderr bytes.Buffer
	root := cmd.Root()
	root.SetArgs([]string{"run", "prepare"})
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, stderr.String())
	}
	want := "to stdout\nto stderr\nqory run prepare: ran postCreateCommand in Xs\n" + setupLogLine
	if got := shownPrepare(stderr.String()); got != want {
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
	fetchedOnHost(t, dir)
	out, err := run(t, "run", "prepare")
	if err != nil {
		t.Fatalf("qory run prepare: %v\n%s", err, out)
	}
	want := "qory run prepare: added from the image's copy the files that the commit of acme/shop does not track\n" +
		"qory run prepare: .devcontainer.json is not in the run's commit; no start command runs from it\n"
	if got := prepareTimes.ReplaceAllString(out, " in Xs"); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
		t.Errorf("the copy's configuration ran: %v", err)
	}
}
