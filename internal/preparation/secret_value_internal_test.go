package preparation

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// valuePhrase is what a message prints in place of a value that contains secret.
const valuePhrase = "(a value that contains an access key secret)"

// TestARepositoryWithASecretIsNotPrinted refuses a list of repositories that names one
// with a secret twice, and one of another owner, and fails a fetch of one with git's
// line quoting its URL: each message prints Forager's phrase in place of the path and
// (hidden) in place of a URL's password.
func TestARepositoryWithASecretIsNotPrinted(t *testing.T) {
	_, err := Check("git.example", "o/"+secret+",o/"+secret)
	if want := "qory run: list " + valuePhrase + " once in " + EnvRepositories + ", which lists it twice"; err == nil || err.Error() != want {
		t.Errorf("twice: %v, want %q", err, want)
	}
	_, err = Check("github.com", "o/a,"+secret+"/b")
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "only repositories of owner o: entry 2 is of owner "+valuePhrase+",") {
		t.Errorf("other owner: %v", err)
	}
	err = notFetched("o/"+secret, "git.example", &gitError{"fatal: repository 'https://u:token-123@git.example/o/" + secret + "/' not found", 128})
	want := "qory run: add a credential for git.example that reads " + valuePhrase + " to the run configuration, then run again. The fetch of " + valuePhrase + " through the run's gateway failed: fatal: repository " + valuePhrase + " not found"
	if err == nil || err.Error() != want {
		t.Errorf("not fetched: %v\nwant %q", err, want)
	}
	err = notFetched("o/a", "git.example", &gitError{"fatal: repository 'https://u:token-123@git.example/o/a/' not found", 128})
	if want := "qory run: add a credential for git.example that reads o/a to the run configuration, then run again. The fetch of o/a through the run's gateway failed: fatal: repository 'https://u:(hidden)@git.example/o/a/' not found"; err == nil || err.Error() != want {
		t.Errorf("not fetched: %v\nwant %q", err, want)
	}
}

// TestAnEnvNameWithASecretIsNotPrinted expands a remoteEnv variable whose name contains
// a secret and whose value uses a value of the host: the error prints Forager's phrase
// in place of the name.
func TestAnEnvNameWithASecretIsNotPrinted(t *testing.T) {
	_, err := expandEnv("remoteEnv", map[string]string{"A" + secret: "${localEnv:HOME}"}, devcontainer.Vars{})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "remoteEnv's variable (a name that contains an access key secret) uses") {
		t.Errorf("expand: %v", err)
	}
}
