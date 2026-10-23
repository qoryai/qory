package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// TestAProgramNotFoundPrintsNoSecret is a credential and a tool whose names and
// programs contain an access key secret, with programs the PATH does not find: each
// refusal prints the name and the program as Forager's phrases and is an input error.
func TestAProgramNotFoundPrintsNoSecret(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	workspace := []string{t.TempDir()}
	r := &config.Forager{
		Credentials: []config.ForagerCredential{{Name: "qak_product", Adapter: []string{"qak_acme", "${argument}"}, Argument: "[a-z]+", Hosts: []string{"git.example.com"}}},
		Tools:       []config.ForagerTool{{Name: "qak_files", Command: []string{"qak_acme", "serve"}, Serves: []string{"files.tools.internal"}}},
	}
	_, err := machineCredentials(context.Background(), r, config.Expansion{Workspace: workspace}, "run", io.Discard)
	wantsHidden(t, err, "forager.yaml: gateway.credentials.(a name that contains an access key secret).adapter: ")
	_, err = machineTools(r, workspace)
	wantsHidden(t, err, "forager.yaml: gateway.tools.(a name that contains an access key secret).command: ")
}

// wantsHidden fails the test unless err is an input error that starts with prefix,
// says the program is not on the PATH, and contains no qak_ in any letter case.
func wantsHidden(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil || ExitCode(err) != ExitInput || !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), "is not on the PATH") || strings.Contains(strings.ToLower(err.Error()), "qak_") {
		t.Errorf("err = %v, exit %d, want the prefix %q and no secret", err, ExitCode(err), prefix)
	}
}

// TestRunFlagsPrintNoSecret is --label and --policy with names and values that
// contain an access key secret: each refusal prints them as Forager's phrases.
func TestRunFlagsPrintNoSecret(t *testing.T) {
	name := "(a name that contains an access key secret)"
	value := "(a value that contains an access key secret)"
	for _, c := range []struct {
		err  error
		want string
	}{
		{second(parseLabels([]string{"qak_label"})), "--label " + value + " is not key=value"},
		{second(parseLabels([]string{"qak_k=1", "qak_k=2"})), "--label " + name + " is set twice"},
		{second(runPolicy("/work/app/qak_policy.yaml", "/work/app", nil)), "--policy " + value + " is inside the checkout, where the agent it constrains could write it; keep it outside"},
	} {
		if c.err == nil || c.err.Error() != c.want || ExitCode(c.err) != ExitInput {
			t.Errorf("err = %v, want %q", c.err, c.want)
		}
	}
}

// second is the error of a call that returns a value and an error.
func second[T any](_ T, err error) error { return err }

// TestTierRefusalsPrintNoSecret is --tier, --tier-effort and --model with values that contain an
// access key secret: each refusal prints the value as Forager's phrase.
func TestTierRefusalsPrintNoSecret(t *testing.T) {
	value := "(a value that contains an access key secret)"
	_, err := tierFlags([]string{"qak_light"}, nil)
	wantsText(t, err, "--tier "+value+": write --tier <tier>=<model id>, such as --tier light=claude-haiku-5-5")
	_, err = tierFlags([]string{"qak_x=m"}, nil)
	wantsText(t, err, "--tier "+value+": use one of the tiers light, fast and thinking; (a name that contains an access key secret) is not a tier")
	_, err = tierFlags([]string{"light=${qory:qak_x}"}, nil)
	wantsText(t, err, "--tier "+value+": write a model id; "+value+" is a tier placeholder, which goes only in an agent's model")
	_, err = tierFlags(nil, []string{"qak_light"})
	wantsText(t, err, "--tier-effort "+value+": write --tier-effort <tier>=<effort>, such as --tier-effort thinking=high")
	_, err = tierFlags(nil, []string{"qak_x=low"})
	wantsText(t, err, "--tier-effort "+value+": use one of the tiers light, fast and thinking; (a name that contains an access key secret) is not a tier")
	_, err = tierFlags(nil, []string{"light=qrk_low"})
	wantsText(t, err, "--tier-effort (a value that contains a run key): give --tier light=<model id> as well; the effort is for the model that --tier maps the tier to")
	err = mainModelTier(&stack.Stack{File: "/work/app/qory.yaml"}, config.Config{}, "${qory:qak_x}")
	wantsText(t, err, "--model "+value+" is a tier placeholder; set the main model by its id, and a tier only in an agent's model")
}

// wantsText fails the test unless err's text is want.
func wantsText(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// TestFlagsAndLabelsPrintNoSecret is --subject, --details, a checkout's labels, a
// harness home and the settings of a launch with values that contain an access key
// secret: each prints the value as Forager's phrase.
func TestFlagsAndLabelsPrintNoSecret(t *testing.T) {
	value := "(a value that contains an access key secret)"
	_, err := parseSubject("type=qak_t")
	wantsText(t, err, "--subject "+value+" has no ref")
	_, err = parseSubject("type=t,qak_k=1")
	wantsText(t, err, "--subject "+value+": (a name that contains an access key secret) is not type, ref, url or title")
	_, err = readDetails("/nonexistent/qak_details.json", nil, false)
	if err == nil || !strings.HasPrefix(err.Error(), "--details "+value+": ") || strings.Contains(err.Error(), "qak_") {
		t.Errorf("err = %v", err)
	}
	if got, want := labelWords(map[string]string{"forge": "git.example", "repository": "acme/qak_app"}), "forge git.example, repository "+value; got != want {
		t.Errorf("labels %q, want %q", got, want)
	}
	wantsText(t, homeInFolder("/work/qak_app", "/work/qak_app/homes"), "set --home to a folder outside "+value+": "+value+" is inside "+value+", and a run with a run key writes no harness into the folder it runs in")
	_, err = inlineSettings([]string{"claude", "--settings", "/elsewhere/qak_settings.json"}, nil, "/work/home")
	if err == nil || !strings.HasPrefix(err.Error(), "the Claude Code settings "+value+" lie outside the harness's home /work/home") {
		t.Errorf("err = %v", err)
	}
}

// TestEndAndViaFlagsPrintNoSecret is --outcome, --reason and --via-ca-file with values
// that contain an access key secret: each refusal prints the value as Forager's phrase
// and is an input error.
func TestEndAndViaFlagsPrintNoSecret(t *testing.T) {
	value := "(a value that contains an access key secret)"
	_, err := endOutcome("qak_done", "")
	wantsInput(t, err, "--outcome "+value+" is not completed, failed or cancelled")
	_, err = endOutcome("completed", "QAK_x")
	wantsInput(t, err, "--reason "+value+" is not a code: a lower-case letter, then lower-case letters, digits and _, at most 64")
	dir := filepath.Join(t.TempDir(), "qak_trust")
	err = (&enrolVia{url: "https://gateway.example", caFile: filepath.Join(dir, "ca.pem")}).check()
	wantsInput(t, err, "--via-ca-file "+value+": no such file or directory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = (&enrolVia{url: "https://gateway.example", caFile: filepath.Join(dir, "ca.pem")}).check()
	wantsInput(t, err, "--via-ca-file "+value+" holds no PEM certificate")
}

// wantsInput fails the test unless err is an input error whose text is want.
func wantsInput(t *testing.T, err error, want string) {
	t.Helper()
	wantsText(t, err, want)
	if ExitCode(err) != ExitInput {
		t.Errorf("err = %v, exit %d, want %d", err, ExitCode(err), ExitInput)
	}
}
