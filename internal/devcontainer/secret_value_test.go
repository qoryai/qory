package devcontainer_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// name is what a message prints in place of a name that contains secret.
const name = "(a name that contains an access key secret)"

// TestAMemberNameWithASecretIsNotPrinted refuses a devcontainer.json whose named
// command, or whose member written twice, has a name with a secret, and expands a named
// command with such a name that uses a value of the host: each error prints Forager's
// phrase in place of the name.
func TestAMemberNameWithASecretIsNotPrinted(t *testing.T) {
	const path = ".devcontainer/devcontainer.json"
	for _, c := range []struct{ doc, want string }{
		{`{"postCreateCommand": {"` + secret + `": 5}}`, path + ": postCreateCommand." + name + " is not "},
		{`{"containerEnv": {"` + secret + `": "a", "` + secret + `": "b"}}`, path + `: the object at /containerEnv names "` + name + `" twice`},
		{`{"containerEnv": {"` + secret + `": "\ud800"}}`, path + `: the string at /containerEnv/` + name + ` holds`},
	} {
		_, err := devcontainer.Parse(path, []byte(c.doc))
		if err == nil || strings.Contains(err.Error(), secret) || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.doc, err, c.want)
		}
	}
	cfg, err := devcontainer.Parse(path, []byte(`{"postCreateCommand": {"`+secret+`": "echo ${localEnv:HOME}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.PostCreate.Expand(devcontainer.Vars{})
	if want := "postCreateCommand's command " + name + " uses ${localEnv:…}, a value of the host; no value of the host reaches the container"; err == nil || err.Error() != want {
		t.Errorf("expand: %v, want %q", err, want)
	}
}
