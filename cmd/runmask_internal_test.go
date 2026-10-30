package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/mask"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/secretmask"
)

// emptyMaskList makes the process's mask list an empty one for the test, and puts the
// one before it back when the test ends.
func emptyMaskList(t *testing.T) *secretmask.Set {
	t.Helper()
	s := secretmask.New()
	old := secretmask.SetDefault(s)
	t.Cleanup(func() { secretmask.SetDefault(old) })
	return s
}

// TestTheRunsValuesJoinTheMaskList is the values of --env and wall.env, and the values of
// the in-process gateway's credentials from a variable and from a file: each joins the
// mask list, and a credential qory cannot read adds nothing.
func TestTheRunsValuesJoinTheMaskList(t *testing.T) {
	s := emptyMaskList(t)
	maskVariables([]string{"SHOP_TOKEN=run-value-1", "EMPTY=", "NOVALUE"})
	maskVariables([]string{"MACHINE_TOKEN=machine-value-1"})
	t.Setenv("MODEL_TOKEN", "  model-value-1\n")
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("file-value-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	maskCredentials([]gateway.Credential{
		{Name: "model", Env: "MODEL_TOKEN"},
		{Name: "forge", File: file},
		{Name: "unset", Env: "UNSET_TOKEN_VARIABLE"},
		{Name: "missing", File: filepath.Join(t.TempDir(), "missing")},
		{Name: "adapter", Adapter: []string{"/bin/false"}},
	})
	want := []string{"run-value-1", "machine-value-1", "model-value-1", "file-value-1"}
	if got := s.Values(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the mask list is %q, want %q", got, want)
	}
}

// TestTheStreamsMaskQorysLinesAndLeaveTheSessionsAlone is a run's streams: qory's lines
// are masked, a value split at each byte included, and the session gets stdout and
// stderr as qory run got them, since Forager's session masks its output.
func TestTheStreamsMaskQorysLinesAndLeaveTheSessionsAlone(t *testing.T) {
	s := emptyMaskList(t)
	const value = "stream-secret-value"
	s.Add(value)
	text := "line " + value + " end\n"
	var stdout, stderr strings.Builder
	streams := newRunStreams(&stdout, &stderr)
	for i := 0; i <= len(text); i++ {
		for _, w := range []io.Writer{streams.out, streams.lines} {
			io.WriteString(w, text[:i])
			io.WriteString(w, text[i:])
		}
	}
	streams.flush()
	want := strings.Repeat("line *** end\n", len(text)+1)
	if stdout.String() != want {
		t.Errorf("stdout got %q", stdout.String())
	}
	if stderr.String() != want {
		t.Errorf("stderr got %q", stderr.String())
	}
	out, errOut := streams.session()
	if out != io.Writer(&stdout) || errOut != io.Writer(&stderr) {
		t.Errorf("the session got %T and %T, want stdout and stderr as they are", out, errOut)
	}
}

// loggedStep is a host step that keeps each line its Log got.
type loggedStep struct {
	mu    sync.Mutex
	lines []string
}

func (s *loggedStep) Log(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, string(b))
	return nil
}

func (s *loggedStep) Exit(string, int) error { return nil }

// TestAHostStepsLogIsMasked is a line of a host step with a value of the mask list: the
// step's log gets the line with the value as ***.
func TestAHostStepsLogIsMasked(t *testing.T) {
	s := emptyMaskList(t)
	const value = "step-secret-value"
	s.Add(value)
	step := &loggedStep{}
	w := &stepLines{stderr: io.Discard, step: step}
	io.WriteString(w, "fetched with "+value[:4])
	io.WriteString(w, value[4:]+"\nlast "+value)
	w.flush()
	if got := strings.Join(step.lines, ""); got != "fetched with ***\nlast ***" {
		t.Errorf("the step's log got %q", got)
	}
}

// TestTheAccessKeysSecretJoinsTheMaskList is the access key a command signs its requests
// with, read from --access-key-secret-fd and from the access-key-secret file: once
// signingKey returns it, its secret is *** in the process's mask list.
func TestTheAccessKeysSecretJoinsTheMaskList(t *testing.T) {
	for _, from := range []string{"descriptor", "file"} {
		t.Run(from, func(t *testing.T) {
			isolate(t)
			list := emptyMaskList(t)
			k, err := accesskey.Generate()
			if err != nil {
				t.Fatal(err)
			}
			var fd *accesskey.Key
			if from == "descriptor" {
				fd = k
			} else {
				dir, err := machineDir()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := dir.Ensure(); err != nil {
					t.Fatal(err)
				}
				if err := dir.WriteSecret(k); err != nil {
					t.Fatal(err)
				}
			}
			r := &config.Forager{Server: &config.ForagerServer{URL: "https://apiary.example", AccessKeyID: "ak_0123456789abcdef"}}
			if _, err := signingKey(r, fd); err != nil {
				t.Fatal(err)
			}
			if got := list.Replace("signed with " + k.Secret()); got != "signed with ***" {
				t.Errorf("the line is %q", got)
			}
		})
	}
}

// TestTheServersSecretsJoinTheMaskList is the server's variables that qory took when it
// started: the access key's secret, the workspace's secret and the run key are *** in
// the run's mask list, whether or not the run uses them.
func TestTheServersSecretsJoinTheMaskList(t *testing.T) {
	isolate(t)
	list := emptyMaskList(t)
	k, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	old := config.TakenServerVariables()
	t.Cleanup(func() { config.SetServerVariables(old) })
	const workspace, key = "workspace-secret-value", "qrk_run-key-value-for-the-test"
	config.SetServerVariables(config.ServerVariables{AccessKeyID: "ak_0123456789abcdef", AccessKeySecret: k.Secret() + "\n", WorkspaceSecret: workspace, RunKey: key})
	maskServerSecrets()
	line := strings.Join([]string{k.Secret(), workspace, key, "ak_0123456789abcdef"}, " ")
	if got := list.Replace(line); got != "*** *** *** ak_0123456789abcdef" {
		t.Errorf("the line is %q", got)
	}
}

// TestTheSessionsOutputIsMaskedOnce is the agent's output on pipes as Forager's session
// copies it, through a writer of Forager's mask package on the run's masker, to the
// session's stderr: a value of the mask list prints as *** once, and a forwarder's
// password prints as qory-proxy-password, intact, although the list also contains
// proxy-password. A second pass would make it qory-***.
func TestTheSessionsOutputIsMaskedOnce(t *testing.T) {
	list := emptyMaskList(t)
	const value = "split-secret-value"
	list.Add(value)
	list.Add("proxy-password")
	list.Masker().AddProxy("qory", "forwarder-password-1")
	var stderr strings.Builder
	streams := newRunStreams(io.Discard, &stderr)
	_, sessionErr := streams.session()
	copied := mask.NewWriter(sessionErr, list.Masker())
	io.WriteString(copied, "agent: "+value[:8])
	io.WriteString(streams.lines, "qory run: a line between\n")
	io.WriteString(copied, value[8:]+" via http://qory:forwarder-password-1@127.0.0.1:3128\n")
	copied.Flush()
	streams.flush()
	got := stderr.String()
	if strings.Contains(got, value[:8]) || strings.Contains(got, value[8:]) || strings.Contains(got, "forwarder-password-1") {
		t.Errorf("stderr got a value: %q", got)
	}
	if !strings.Contains(got, "*** via http://qory:qory-proxy-password@127.0.0.1:3128\n") || strings.Count(got, "qory run: a line between\n") != 1 {
		t.Errorf("stderr got %q", got)
	}
}
