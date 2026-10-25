package compose

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// NameSources records the git source of p's extends, as [source.Named.Base], and the git
// source of each of p's modules, by the module's name, with n, so a text shows the
// phrase in place of the URL from the start of the compose, before the source is
// fetched.
func NameSources(p *stack.Stack, n *source.Named) {
	n.Add(p.Extends, n.Base())
	for _, l := range p.Modules {
		n.Add(l.Source, n.Module(l.Name))
	}
}

// CheckAddresses stops p, a stack whose modules a run fetches through its stack proxy,
// which sets no credential, before any module is fetched: a module whose git URL with a
// scheme has a user part, a user name or a password, or an @ between :// and the first
// / after it; one that url.Parse cannot read; and one whose host has a character outside
// ASCII. Each error shows the module's name and no part of the URL. harness is the
// harness as a text shows it, such as the workspace's harness: a module of the base is
// in the base of the harness.
func CheckAddresses(p *stack.Stack, harness string) error {
	for i, l := range p.Modules {
		if !strings.Contains(l.Source.Git, "://") {
			continue
		}
		module := fmt.Sprintf("modules[%d]", i)
		if l.Name != "" {
			module = "the module " + accesskey.ShownName(l.Name, false)
		}
		in := harness
		if l.Base {
			in = "the base of " + harness
		}
		u, err := url.Parse(l.Source.Git)
		switch {
		case stack.AtBeforePath(l.Source.Git) || err == nil && u.User != nil:
			return fmt.Errorf("remove the user name and password from the source of %s in %s: the run's stack proxy fetches the harness with no credential", module, in)
		case err != nil:
			return fmt.Errorf("correct the URL in the source of %s in %s: qory could not read it as a URL", module, in)
		case stack.NonASCIIHost(l.Source.Git):
			return fmt.Errorf("write the host in the source of %s in %s in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @", module, in)
		}
	}
	return nil
}
