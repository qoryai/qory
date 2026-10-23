//go:build unix

package preparation

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// committed makes a git repository in a new folder with files in its one commit, and
// returns the folder, its links resolved.
func committed(t *testing.T, files map[string]string) string {
	t.Helper()
	needGit(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(dir, "README.md"), "shop\n")
	for name, content := range files {
		write(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "--quiet", "-m", "first")
	return dir
}

// started runs [Start] in dir with environ, and returns what the commands and Start
// wrote to the output, without the line of the time, and that line, with its time as
// Xs.
func started(t *testing.T, dir string, environ []string) (string, string, error) {
	t.Helper()
	var output strings.Builder
	err := Start(context.Background(), dir, environ, &output)
	out, last := output.String(), ""
	if i := strings.LastIndex(strings.TrimSuffix(out, "\n"), "\n"); strings.HasPrefix(out[i+1:], "qory run prepare: ran ") {
		out, last = out[:i+1], strings.TrimSuffix(out[i+1:], "\n")
	}
	return out, times.ReplaceAllString(last, " in Xs"), err
}

// TestStartRunsTheStartCommands is a configuration with the four lifecycle commands:
// postCreateCommand runs, then postStartCommand, and the two the build ran do not.
// The commands get the working folder's variables, and the environment given, then
// containerEnv, then remoteEnv, with the variables in their values replaced.
func TestStartRunsTheStartCommands(t *testing.T) {
	dir := committed(t, map[string]string{".devcontainer/devcontainer.json": `{
		// The build ran these two.
		"onCreateCommand": "echo onCreate",
		"updateContentCommand": "echo updateContent",
		"postCreateCommand": "echo create ${containerWorkspaceFolder} ${containerWorkspaceFolderBasename} ${containerEnv:A} $A $C $R $BASE; cat",
		"postStartCommand": ["printf", "start %s\n", "${containerEnv:C}"],
		"containerEnv": {"A": "container", "C": "${containerEnv:BASE}-c"},
		"remoteEnv": {"A": "remote", "R": "${containerEnv:A}-r"},
	}`})
	stdout, stderr, err := started(t, dir, []string{"PATH=/usr/bin:/bin", "BASE=base", "A=given"})
	if err != nil {
		t.Fatal(err)
	}
	want := "create " + dir + " " + filepath.Base(dir) + " container remote base-c container-r base\nstart base-c\n"
	if stdout != want {
		t.Errorf("the commands wrote %q, want %q", stdout, want)
	}
	if want := "qory run prepare: ran postCreateCommand and postStartCommand in Xs"; stderr != want {
		t.Errorf("printed %q, want %q", stderr, want)
	}
}

// TestStartSaysWhichRan is the line with one start command, and no line and nothing
// run with neither or with no configuration.
func TestStartSaysWhichRan(t *testing.T) {
	for _, c := range []struct{ config, want string }{
		{`{"postStartCommand": "true"}`, "qory run prepare: ran postStartCommand in Xs"},
		{`{"postCreateCommand": ["true"], "postStartCommand": {}}`, "qory run prepare: ran postCreateCommand in Xs"},
		{`{"onCreateCommand": "echo onCreate"}`, ""},
		{"", ""},
	} {
		files := map[string]string{}
		if c.config != "" {
			files[".devcontainer.json"] = c.config
		}
		dir := committed(t, files)
		stdout, stderr, err := started(t, dir, []string{"PATH=/usr/bin:/bin"})
		if err != nil || stdout != "" || stderr != c.want {
			t.Errorf("%s: %q, %q, %v; want %q", c.config, stdout, stderr, err, c.want)
		}
	}
}

// TestStartStopsAtTheFirstFailure is a postCreateCommand that fails: Start says which,
// without its text, and runs no postStartCommand and prints no time.
func TestStartStopsAtTheFirstFailure(t *testing.T) {
	dir := committed(t, map[string]string{".devcontainer.json": `{
		"postCreateCommand": "echo secret-text >&2; exit 3",
		"postStartCommand": "touch started"}`})
	output, line, err := started(t, dir, []string{"PATH=/usr/bin:/bin"})
	if want := "qory run prepare: postCreateCommand exited with status 3"; err == nil || err.Error() != want {
		t.Errorf("%v, want %s", err, want)
	}
	if output != "secret-text\n" || line != "" {
		t.Errorf("output %q, line %q", output, line)
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !os.IsNotExist(err) {
		t.Errorf("postStartCommand ran: %v", err)
	}
}

// TestStartRefusesBeforeRunning is a configuration Start cannot use: a variable of the
// host in remoteEnv or in a command, and a configuration that is not JSON. Nothing
// runs, and the error names where, never a value.
func TestStartRefusesBeforeRunning(t *testing.T) {
	for _, c := range []struct{ config, want string }{
		{`{"postCreateCommand": "touch ran", "remoteEnv": {"TOKEN": "${localEnv:SECRET}"}}`,
			"qory run prepare: remoteEnv's variable TOKEN uses ${localEnv:…}, a value of the host; no value of the host reaches the container"},
		{`{"postCreateCommand": "touch ran", "postStartCommand": "echo ${localWorkspaceFolder}"}`,
			"qory run prepare: postStartCommand uses ${localWorkspaceFolder}, a value of the host; no value of the host reaches the container"},
		{`{"postCreateCommand": "touch ran",`,
			"qory run prepare: .devcontainer.json is not JSON with comments: the error is at line 1, column 35"},
	} {
		dir := committed(t, map[string]string{".devcontainer.json": c.config})
		_, _, err := started(t, dir, []string{"PATH=/usr/bin:/bin", "SECRET=value"})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.config, err, c.want)
		}
		if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
			t.Errorf("%s: postCreateCommand ran: %v", c.config, err)
		}
	}
}

func TestSetEnv(t *testing.T) {
	got := setEnv([]string{"A=1", "B=2", "A=3", "C=4"}, []string{"A=x", "D=y", "B=z"})
	if want := "C=4 A=x D=y B=z"; strings.Join(got, " ") != want {
		t.Errorf("%v, want %s", got, want)
	}
}

// TestStartRunsOnlyATrackedConfiguration is a configuration the working tree has and the
// commit does not, untracked or ignored, as the image's copy may leave one: it is never
// run, a line says so, and a configuration the commit holds at a later place runs
// instead. A configuration changed in the working tree runs as the commit holds it.
func TestStartRunsOnlyATrackedConfiguration(t *testing.T) {
	untracked := "qory run prepare: .devcontainer.json is not in the run's commit; no start command runs from it\n"
	dir := committed(t, nil)
	write(t, filepath.Join(dir, ".devcontainer.json"), `{"postCreateCommand": "touch ran"}`)
	output, line, err := started(t, dir, []string{"PATH=/usr/bin:/bin"})
	if err != nil || output != untracked || line != "" {
		t.Errorf("untracked: %q, %q, %v; want %q", output, line, err, untracked)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
		t.Errorf("untracked: the configuration ran: %v", err)
	}

	dir = committed(t, map[string]string{
		".gitignore":         ".devcontainer/\n",
		".devcontainer.json": `{"postCreateCommand": "echo committed"}`,
	})
	write(t, filepath.Join(dir, ".devcontainer", "devcontainer.json"), `{"postCreateCommand": "echo ignored"}`)
	output, line, err = started(t, dir, []string{"PATH=/usr/bin:/bin"})
	want := "qory run prepare: .devcontainer/devcontainer.json is not in the run's commit; no start command runs from it\ncommitted\n"
	if err != nil || output != want || line != "qory run prepare: ran postCreateCommand in Xs" {
		t.Errorf("ignored: %q, %q, %v; want %q", output, line, err, want)
	}

	dir = committed(t, map[string]string{".devcontainer.json": `{"postCreateCommand": "echo committed"}`})
	write(t, filepath.Join(dir, ".devcontainer.json"), `{"postCreateCommand": "echo changed"}`)
	if output, _, err := started(t, dir, []string{"PATH=/usr/bin:/bin"}); err != nil || output != "committed\n" {
		t.Errorf("changed: %q, %v", output, err)
	}
}

// TestStartRefusesWhatTheCommitHolds is a commit whose configuration is a link, or
// behind one, behind a file, or not a regular file: each is refused, as a working
// tree's would be.
func TestStartRefusesWhatTheCommitHolds(t *testing.T) {
	needGit(t)
	for _, c := range []struct {
		name  string
		setup func(dir string)
		want  string
	}{
		{"a link", func(dir string) {
			write(t, filepath.Join(dir, "config.json"), `{"postCreateCommand": "touch ran"}`)
			if err := os.Symlink("config.json", filepath.Join(dir, ".devcontainer.json")); err != nil {
				t.Fatal(err)
			}
		}, "qory run prepare: .devcontainer.json is a symbolic link; the development container configuration is read only from files and folders of the repository"},
		{"a folder link", func(dir string) {
			write(t, filepath.Join(dir, "elsewhere", "devcontainer.json"), `{"postCreateCommand": "touch ran"}`)
			if err := os.Symlink("elsewhere", filepath.Join(dir, ".devcontainer")); err != nil {
				t.Fatal(err)
			}
		}, "qory run prepare: .devcontainer is a symbolic link; the development container configuration is read only from files and folders of the repository"},
		{"a file for a folder", func(dir string) {
			write(t, filepath.Join(dir, ".devcontainer"), "a file\n")
		}, "qory run prepare: .devcontainer is not a folder; the development container configuration is read only from files and folders of the repository"},
		{"a folder for a file", func(dir string) {
			write(t, filepath.Join(dir, ".devcontainer.json", "inner"), "a file\n")
		}, "qory run prepare: .devcontainer.json is not a regular file; the development container configuration is read only from a regular file"},
		{"too big", func(dir string) {
			write(t, filepath.Join(dir, ".devcontainer.json"), `{"image": "`+strings.Repeat("a", 1<<20)+`"}`)
		}, "qory run prepare: .devcontainer.json is more than 1 MiB; a development container configuration is a few kilobytes"},
	} {
		dir := committed(t, nil)
		c.setup(dir)
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "--quiet", "-m", "second")
		if _, _, err := started(t, dir, []string{"PATH=/usr/bin:/bin"}); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
		if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
			t.Errorf("%s: the configuration ran: %v", c.name, err)
		}
	}
}

// TestStartTakesTheNamesOfVariables refuses a containerEnv or remoteEnv variable whose
// name is empty or holds = or a NUL byte, naming the member and never the value, and
// runs nothing; and unsets a remoteEnv variable whose value is null.
func TestStartTakesTheNamesOfVariables(t *testing.T) {
	for _, c := range []struct{ env, want string }{
		{`"containerEnv": {"": "secret-value"}`, "qory run prepare: containerEnv has a variable whose name is empty or holds = or a NUL byte, which no environment variable's name can"},
		{`"containerEnv": {"A=B": "secret-value"}`, "qory run prepare: containerEnv has a variable whose name is empty or holds = or a NUL byte, which no environment variable's name can"},
		{`"remoteEnv": {"A\u0000B": "secret-value"}`, "qory run prepare: remoteEnv has a variable whose name is empty or holds = or a NUL byte, which no environment variable's name can"},
		{`"remoteEnv": {"=": null}`, "qory run prepare: remoteEnv has a variable whose name is empty or holds = or a NUL byte, which no environment variable's name can"},
	} {
		dir := committed(t, map[string]string{".devcontainer.json": `{"postCreateCommand": "touch ran", ` + c.env + `}`})
		_, _, err := started(t, dir, []string{"PATH=/usr/bin:/bin"})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.env, err, c.want)
		}
		if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
			t.Errorf("%s: postCreateCommand ran: %v", c.env, err)
		}
	}
	dir := committed(t, map[string]string{".devcontainer.json": `{
		"postCreateCommand": "echo \"${GIVEN-unset} ${CONTAINER-unset} $KEPT\"",
		"containerEnv": {"CONTAINER": "set"},
		"remoteEnv": {"GIVEN": null, "CONTAINER": null, "KEPT": "${containerEnv:CONTAINER}"}}`})
	output, _, err := started(t, dir, []string{"PATH=/usr/bin:/bin", "GIVEN=given"})
	if err != nil || output != "unset unset set\n" {
		t.Errorf("null: %q, %v", output, err)
	}
}

// TestStartLoggedCopiesTheOutputAsItComes is a postCreateCommand that writes a line,
// waits, and writes another: StartLogged copies the first line to the output before the
// command ends, then the second, the line that says which ran and the line that names
// the file, which contains both lines. A configuration with no commands leaves no file.
func TestStartLoggedCopiesTheOutputAsItComes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := committed(t, map[string]string{".devcontainer.json": `{"postCreateCommand": "echo first; sleep 1; echo second"}`})
	var output safeBuilder
	errs := make(chan error, 1)
	go func() { errs <- StartLogged(context.Background(), dir, []string{"PATH=/usr/bin:/bin"}, &output) }()
	deadline := time.Now().Add(900 * time.Millisecond)
	for output.String() != "first\n" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := output.String(); got != "first\n" {
		t.Errorf("before the command ended: %q, want the first line", got)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	logs, _ := filepath.Glob(filepath.Join(tmp, "qory-setup-*.log"))
	if len(logs) != 1 {
		t.Fatalf("the files: %v", logs)
	}
	want := "first\nsecond\nqory run prepare: ran postCreateCommand in Xs\nqory run prepare: " + logs[0] + " in the container that runs the setup contains the start commands' output, and what a program they leave running writes later\n"
	if got := regexp.MustCompile(` in [0-9]+\.[0-9]s\n`).ReplaceAllString(output.String(), " in Xs\n"); got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if data, err := os.ReadFile(logs[0]); err != nil || !strings.HasPrefix(string(data), "first\nsecond\n") {
		t.Errorf("the file contains %q, %v", data, err)
	}

	none := committed(t, map[string]string{".devcontainer.json": `{"image": "example.com/agent:1"}`})
	if err := os.Remove(logs[0]); err != nil {
		t.Fatal(err)
	}
	var quiet safeBuilder
	if err := StartLogged(context.Background(), none, []string{"PATH=/usr/bin:/bin"}, &quiet); err != nil || quiet.String() != "" {
		t.Errorf("no commands: printed %q, %v", quiet.String(), err)
	}
	if logs, _ := filepath.Glob(filepath.Join(tmp, "qory-setup-*.log")); len(logs) != 0 {
		t.Errorf("no commands left the files %v", logs)
	}
}

// safeBuilder is a strings.Builder that a test reads while another goroutine writes it.
type safeBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
