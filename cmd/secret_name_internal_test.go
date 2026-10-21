package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// The phrases a message prints in place of a name that contains an access key secret
// or a run key.
const (
	accessKeyPhrase = "(a name that contains an access key secret)"
	runKeyPhrase    = "(a name that contains a run key)"
)

// isolate sets HOME and the XDG directories qory reads to directories of the test, so a
// test reads no file of the machine it runs on.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
}

// TestTheMachineLinesLeaveOutANameWithASecret is the lines qory run prints for an
// integration whose key contains an access key secret, which gateway.credentials
// shadows, and the refusals of a credential and a tool whose names contain an access
// key secret and a run key: each name, and the program's path that contains it, is
// the phrase. A plain key prints as before.
func TestTheMachineLinesLeaveOutANameWithASecret(t *testing.T) {
	isolate(t)
	r := &config.Forager{
		Integrations: []config.ForagerIntegration{{Key: "qak_x", Path: "/opt/bin/qory-qak_x", Version: "1.0"}},
		Credentials:  []config.ForagerCredential{{Name: "qak_x", Env: "X"}},
	}
	var report bytes.Buffer
	_, err := machineCredentials(context.Background(), r, config.Expansion{Only: func(string) bool { return false }}, "run", &report)
	want := "qory run: forager.yaml: gateway.credentials." + accessKeyPhrase + " defines the credential " + accessKeyPhrase + ", and gateway.integrations." + accessKeyPhrase + " defines none\n" +
		"qory run: integration " + accessKeyPhrase + ": " + accessKeyPhrase + " 1.0\n"
	if report.String() != want {
		t.Errorf("report %q, want %q", report.String(), want)
	}
	if want := "forager.yaml: credential " + accessKeyPhrase + ": a token from env or file needs the hosts it is for"; err == nil || err.Error() != want {
		t.Errorf("credential: %v, want %q", err, want)
	}
	_, err = machineTools(&config.Forager{Tools: []config.ForagerTool{{Name: "qrk_t"}}}, nil)
	if want := "forager.yaml: tool " + runKeyPhrase + ": the command is empty"; err == nil || err.Error() != want {
		t.Errorf("tool: %v, want %q", err, want)
	}
	_, err = machineTools(&config.Forager{Tools: []config.ForagerTool{{Name: "qak_a b", Command: []string{"/opt/tool"}, Serves: []string{"a.example"}}}}, nil)
	if want := "forager.yaml: the tool name " + accessKeyPhrase + " is not 1 to 64 of a-z, 0-9, underscore, dot and dash"; err == nil || err.Error() != want {
		t.Errorf("tool name with a space: %v, want %q", err, want)
	}
	if got, want := shadowed("github"), "forager.yaml: gateway.credentials.github defines the credential github, and gateway.integrations.github defines none"; got != want {
		t.Errorf("plain: %q, want %q", got, want)
	}
}
