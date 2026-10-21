package prebuild

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// valuePhrase is what a message prints in place of a value that contains secret.
const valuePhrase = "(a value that contains an access key secret)"

// TestAPrebuildMessageHidesASecret refuses a Dockerfile path with a secret, expands a
// containerEnv variable whose name has one, and quotes a program's last line with a run
// key: each message prints Forager's phrase in place of the value or the name.
func TestAPrebuildMessageHidesASecret(t *testing.T) {
	cfg, err := devcontainer.Parse(".devcontainer/devcontainer.json", []byte(`{"build": {"dockerfile": "`+secret+`/Dockerfile"}}`))
	if err != nil {
		t.Fatal(err)
	}
	err = checkFrontend(t.TempDir(), cfg, ".devcontainer/"+secret+"/Dockerfile")
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "sets build.dockerfile to "+valuePhrase+", which cannot be read") {
		t.Errorf("dockerfile: %v", err)
	}
	_, err = expandEnv(devcontainer.Vars{}, "containerEnv", map[string]string{"A" + secret: "${localEnv:HOME}"})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.HasPrefix(err.Error(), "containerEnv.(a name that contains an access key secret) uses") {
		t.Errorf("expand: %v", err)
	}
	var l lastLine
	l.Write([]byte("pull https://u:token-123@registry.example/x refused qrk_m\n"))
	if want := "pull https://u:(hidden)@registry.example/x refused (a value that contains a run key)"; l.String() != want {
		t.Errorf("last line %q, want %q", l.String(), want)
	}
	if got := printables([]string{"A" + secret, "B"}); got != "(a name that contains an access key secret) and B" {
		t.Errorf("printables %q", got)
	}
}
