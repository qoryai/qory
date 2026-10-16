package config

import (
	"fmt"
	"slices"

	"github.com/qoryai/forager/gateway"

	"github.com/qoryai/qory/internal/integration"
)

// ForgeGitHub is the one forge whose repository form qory knows: github.com.
const ForgeGitHub = "github.com"

// GitHubRepository is the pattern a label's value must match whole to name one
// repository on github.com: owner/name. The owner is 1 to 39 of letters, digits and
// dashes, starting with a letter or a digit; the name is 1 to 100 of letters, digits,
// underscores, dots and dashes, and never . or .., which GitHub forbids. Go's regular
// expressions have no lookahead, so the name is one of three forms: one that starts
// with no dot, one that starts with a single dot, and one that starts with two dots and
// has more after them. Forager matches it anchored, as ^(?:pattern)$.
const GitHubRepository = `[A-Za-z0-9][A-Za-z0-9-]{0,38}/(?:[A-Za-z0-9_-][A-Za-z0-9_.-]{0,99}|\.[A-Za-z0-9_-][A-Za-z0-9_.-]{0,98}|\.\.[A-Za-z0-9_.-]{1,98})`

// forgePatterns are the forges whose repository form qory knows, each with the pattern
// of one repository on it.
var forgePatterns = map[string]string{ForgeGitHub: GitHubRepository}

// selectsKey starts a refusal of gateway.selects.credentials.
const selectsKey = ForagerFileName + ": gateway.selects.credentials: "

// Selections is gateway.selects as Forager's gateway takes it, [gateway.Config.Selects]:
// each entry by name, and for a credential that takes an argument, the label, the forge
// and the pattern of one repository on that forge, which qory supplies. It checks each
// entry against r.Credentials, so it is for after [Forager.Expand]: the file's
// credentials and the ones the integrations define. An entry that names no credential,
// one for a credential that takes an argument without argument_label and forge, one for
// a credential that takes none with either, and a forge whose repository form qory does
// not know are refused, in words that name the entry's name and forge and no other
// value. A name given twice is left to Forager's gateway, which refuses it.
func (r *Forager) Selections() ([]gateway.Selection, error) {
	if r == nil || len(r.Selects) == 0 {
		return nil, nil
	}
	out := make([]gateway.Selection, 0, len(r.Selects))
	for _, s := range r.Selects {
		name := integration.Printable(s.Name)
		i := slices.IndexFunc(r.Credentials, func(c ForagerCredential) bool { return c.Name == s.Name })
		if i < 0 {
			return nil, fmt.Errorf("%s%s is no credential that gateway.credentials or gateway.integrations defines", selectsKey, name)
		}
		if r.Credentials[i].Argument == "" {
			if s.ArgumentLabel != "" || s.Forge != "" {
				return nil, fmt.Errorf("%s%s takes no argument, so argument_label and forge do not apply", selectsKey, name)
			}
			out = append(out, gateway.Selection{Name: s.Name})
			continue
		}
		if s.ArgumentLabel == "" || s.Forge == "" {
			return nil, fmt.Errorf("%s%s needs argument_label and forge", selectsKey, name)
		}
		pattern, ok := forgePatterns[s.Forge]
		if !ok {
			return nil, fmt.Errorf("%s%s: forge %s is not one qory knows the repository form of; use %s", selectsKey, name, integration.Printable(s.Forge), ForgeGitHub)
		}
		out = append(out, gateway.Selection{Name: s.Name, Label: s.ArgumentLabel, Forge: s.Forge, Pattern: pattern})
	}
	return out, nil
}

// orNone is v, or "(none)" when it is empty.
func orNone(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}
