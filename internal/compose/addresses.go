package compose

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/exports"
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

// CheckBase is [CheckAddresses] for the git source of the stack p extends, before it is
// fetched: a URL with a scheme that has a user part or an @ before its path, that
// url.Parse cannot read, or whose host has a character outside ASCII. Each error shows
// the base as "the base of <harness>" and no part of the URL. A source with no scheme
// passes here: the rule of https sources stops it.
func CheckBase(p *stack.Stack, harness string) error {
	base := "the base of " + harness
	switch addressFault(p.Extends.Git) {
	case faultUser:
		return exports.Refuse("Remove the user name and password from the URL of "+base+".", userCause)
	case faultParse:
		return exports.Refuse("Correct the URL of "+base+".", parseCause)
	case faultHost:
		return exports.Refuse("Write the host in the URL of "+base+" in ASCII.", hostFix, hostCause)
	}
	return nil
}

// CheckReference is [CheckBase] for ref, the stack reference that -f is set to, before
// it is fetched. Each error shows -f and no part of the URL.
func CheckReference(ref stack.Source) error {
	switch addressFault(ref.Git) {
	case faultUser:
		return exports.Refuse("Remove the user name and password from the URL in -f.", userCause)
	case faultParse:
		return exports.Refuse("Correct the URL in -f.", parseCause)
	case faultHost:
		return exports.Refuse("Write the host of the URL in -f in ASCII.", hostFix, hostCause)
	}
	return nil
}

// The cause and fix lines that the refusals of a git URL share.
const (
	userCause  = "The run's stack proxy sets the harness credentials of the run configuration, so a source's URL needs none."
	parseCause = "qory could not read it as a URL."
	hostFix    = "Use the xn-- form for an international name."
	hostCause  = "The host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @."
)

// The faults of a git URL that [CheckBase] and [CheckReference] stop.
const (
	faultNone = iota
	faultUser
	faultParse
	faultHost
)

// addressFault is the fault of git, a git URL: faultUser for a URL with a scheme that
// has a user part or an @ before its path, faultParse for one that url.Parse cannot
// read, faultHost for one whose host has a character outside ASCII, and faultNone for
// any other, a source with no scheme among them.
func addressFault(git string) int {
	if !strings.Contains(git, "://") {
		return faultNone
	}
	u, err := url.Parse(git)
	switch {
	case stack.AtBeforePath(git) || err == nil && u.User != nil:
		return faultUser
	case err != nil:
		return faultParse
	case stack.NonASCIIHost(git):
		return faultHost
	}
	return faultNone
}

// CheckAddresses stops p, a stack whose modules a run fetches through its stack proxy,
// which sets the run configuration's harness credentials, before any module is
// fetched: a module whose git URL with a scheme has a user part, a user name or a
// password, or an @ between :// and the first / after it; one that url.Parse cannot
// read; and one whose host has a character outside ASCII. Each error shows the module's
// name and no part of the URL. harness is the harness as a text shows it, such as the
// workspace's harness: a module of the base is in the base of the harness.
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
		where := "The source of " + module + " is in " + in + "."
		u, err := url.Parse(l.Source.Git)
		switch {
		case stack.AtBeforePath(l.Source.Git) || err == nil && u.User != nil:
			return exports.Refuse("Remove the user name and password from the URL in the source of "+module+".", userCause, where)
		case err != nil:
			return exports.Refuse("Correct the URL in the source of "+module+".", parseCause, where)
		case stack.NonASCIIHost(l.Source.Git):
			return exports.Refuse("Write the host in the source of "+module+" in ASCII.", hostFix, hostCause, where)
		}
	}
	return nil
}
