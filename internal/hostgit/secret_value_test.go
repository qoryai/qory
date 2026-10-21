package hostgit_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// TestARefusalHidesASecretInAKeyAndAPath is the settings check refusing a key of a
// remote whose name contains a secret, and a link in the git directory whose name and
// target contain one: each refusal prints Forager's phrase in place of the name.
func TestARefusalHidesASecretInAKeyAndAPath(t *testing.T) {
	const name = "(a name that contains an access key secret)"
	for _, c := range []struct {
		err  *hostgit.RefusedError
		want string
	}{
		{&hostgit.RefusedError{Dir: "/w/repo", Key: "remote." + secret + ".url", Reason: hostgit.RefusedRemote}, "sets " + name + " to a URL"},
		{&hostgit.RefusedError{Dir: "/w/repo", Key: "core." + secret, Reason: hostgit.RefusedSetting}, "sets " + name + ", which"},
		{&hostgit.RefusedError{Dir: "/w/repo", Key: "/w/repo/.git/refs/" + secret, Target: "/elsewhere/" + secret + "/refs", Reason: hostgit.RefusedRefLink}, "holds /w/repo/.git/refs/" + name + ", a link to /elsewhere/" + name + "/refs;"},
		{&hostgit.RefusedError{Dir: "/w/repo", Key: "/w/repo/.git/remotes/" + secret, Reason: hostgit.RefusedRemoteFile}, "holds /w/repo/.git/remotes/" + name + ", which"},
	} {
		got := c.err.Error()
		if strings.Contains(got, secret) || !strings.Contains(got, c.want) {
			t.Errorf("refusal %q, want it to contain %q", got, c.want)
		}
	}
}
