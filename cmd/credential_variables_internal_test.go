package cmd

import (
	"slices"
	"testing"

	"github.com/qoryai/forager/contracts"
	"github.com/qoryai/forager/session/runtimes"
	"github.com/qoryai/forager/session/runtimes/catalog"
)

// TestCredentialVariablesReadTheModelKeys is the variables Claude Code reads a model key
// from, which its descriptor lists in model.keys alone: credentialVariables has each of
// them, and so modelCredentialPassed stops each, from --env and from wall.env.
func TestCredentialVariablesReadTheModelKeys(t *testing.T) {
	rt, err := catalog.Lookup("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	var secrets []string
	if s, ok := rt.(runtimes.Secrets); ok {
		d := s.Secrets()
		secrets = slices.Clone(d.Reserves)
		for _, dec := range d.Declares {
			secrets = append(secrets, dec.Name)
		}
	}
	names := credentialVariables(rt)
	for _, v := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if slices.Contains(secrets, v) {
			t.Errorf("%s is among the descriptor's secrets; the test means to prove model.keys", v)
		}
		if !slices.Contains(names, v) {
			t.Errorf("credentialVariables %v lacks %s", names, v)
		}
		for _, c := range []struct {
			env, wallEnv []string
			from         string
		}{{[]string{v + "=x"}, nil, "--env"}, {nil, []string{v}, "wall.env in forager.yaml"}} {
			want := "qory run: " + v + " is the model's credential, which a walled run given a run key takes only through the gateway; remove it from " + c.from
			if err := modelCredentialPassed(rt, c.env, c.wallEnv); err == nil || err.Error() != want {
				t.Errorf("%s from %s: %v, want %q", v, c.from, err, want)
			}
		}
	}
	if err := modelCredentialPassed(runtimes.Bare("other"), []string{"ANTHROPIC_API_KEY"}, nil); err != nil {
		t.Errorf("a runtime that describes nothing: %v", err)
	}
}

// TestAForgeTokenIsStoppedInAWalledRunGivenARunKey is each forge token's variable of
// Forager's deny list, in --env, as NAME and as NAME=value, and in wall.env: each stops
// the run with an input error that starts with its fix, and contains the name, never the
// value.
func TestAForgeTokenIsStoppedInAWalledRunGivenARunKey(t *testing.T) {
	names := contracts.ForgeTokenNames()
	for _, want := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GITLAB_TOKEN", "GL_TOKEN", "BITBUCKET_TOKEN"} {
		if !slices.Contains(names, want) {
			t.Errorf("Forager's forge token names %v lack %s", names, want)
		}
	}
	rt := runtimes.Bare("other")
	for _, name := range names {
		for _, c := range []struct {
			env, wallEnv []string
			from         string
		}{
			{[]string{name}, nil, "--env"},
			{[]string{name + "=forge-token-value"}, nil, "--env"},
			{nil, []string{name}, "wall.env in forager.yaml"},
		} {
			err := modelCredentialPassed(rt, c.env, c.wallEnv)
			want := "remove " + name + " from " + c.from + ": a walled run given a run key takes a forge token only through the gateway"
			if err == nil || err.Error() != want || ExitCode(err) != 2 {
				t.Errorf("%s from %s: %v (exit %d), want %q, exit 2", name, c.from, err, ExitCode(err), want)
			}
		}
	}
	if err := modelCredentialPassed(rt, []string{"SHOP_TOKEN=x"}, []string{"HTTP_TIMEOUT"}); err != nil {
		t.Errorf("a variable that is no forge token: %v", err)
	}
}
