package stack

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// Constraint is the range of qory versions a document accepts, from its qory key:
//
//	qory: ">=0.3.0"
//	qory: ">=0.3.0 <0.4.0"
//
// One or more comparators separated by spaces, each an operator, >=, >, <=, < or =,
// followed by a version with or without a leading v. Every comparator has to hold. A bare
// version is refused, because one reader takes it for a minimum and another for an exact
// pin. The zero Constraint accepts every version and prints as "".
type Constraint struct {
	comparators []comparator
}

// comparator is one operator and the version it compares against, the version in the
// canonical form semver reads.
type comparator struct {
	op      string
	version string
}

// operators are the comparator operators, the two-character ones first so that a prefix
// match finds >= before >.
var operators = []string{">=", "<=", ">", "<", "="}

// ParseConstraint reads the value of a qory key. The error says what a value looks like.
func ParseConstraint(s string) (Constraint, error) {
	var c Constraint
	if strings.TrimSpace(s) == "" {
		return c, errors.New("qory is empty; it is one or more comparators such as >=0.3.0")
	}
	for _, field := range strings.Fields(s) {
		var op string
		for _, o := range operators {
			if strings.HasPrefix(field, o) {
				op = o
				break
			}
		}
		if op == "" {
			return Constraint{}, fmt.Errorf("qory %q is not one or more comparators such as >=0.3.0", s)
		}
		v := withV(strings.TrimPrefix(field, op))
		// A comparator names all three numbers and no build metadata, so that two
		// people read the same range: "0.3" is refused, "0.3.0-rc.1" is read.
		if !semver.IsValid(v) || semver.Canonical(v) != v {
			return Constraint{}, fmt.Errorf("qory %q: %q is not a version such as 0.3.0", s, strings.TrimPrefix(field, op))
		}
		c.comparators = append(c.comparators, comparator{op, v})
	}
	return c, nil
}

// withV returns v with the leading v semver wants.
func withV(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// Allows reports whether version, with or without a leading v, is inside the range. Build
// metadata on the version, +dirty say, does not count. A version that does not parse is
// outside every non-empty range.
func (c Constraint) Allows(version string) bool {
	if len(c.comparators) == 0 {
		return true
	}
	v := withV(version)
	if !semver.IsValid(v) {
		return false
	}
	for _, cmp := range c.comparators {
		d := semver.Compare(v, cmp.version)
		var ok bool
		switch cmp.op {
		case ">=":
			ok = d >= 0
		case ">":
			ok = d > 0
		case "<=":
			ok = d <= 0
		case "<":
			ok = d < 0
		case "=":
			ok = d == 0
		}
		if !ok {
			return false
		}
	}
	return true
}

// Empty reports whether the constraint accepts every version: no qory key was given.
func (c Constraint) Empty() bool { return len(c.comparators) == 0 }

// String writes the range as a qory key holds it, the versions without the leading v.
func (c Constraint) String() string {
	parts := make([]string, 0, len(c.comparators))
	for _, cmp := range c.comparators {
		parts = append(parts, cmp.op+strings.TrimPrefix(cmp.version, "v"))
	}
	return strings.Join(parts, " ")
}

// Join returns a constraint that holds when both do.
func (c Constraint) Join(other Constraint) Constraint {
	return Constraint{comparators: append(append([]comparator{}, c.comparators...), other.comparators...)}
}

// UnmarshalYAML reads the qory key. A value that is not a string, or that
// [ParseConstraint] refuses, is the error the document's reader prefixes with the path.
func (c *Constraint) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return errors.New("qory is one or more comparators such as >=0.3.0, as a string")
	}
	parsed, err := ParseConstraint(s)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// MarshalYAML writes the range as [Constraint.String] does.
func (c Constraint) MarshalYAML() (any, error) { return c.String(), nil }

// IsZero reports whether the key is left out when the document is written.
func (c Constraint) IsZero() bool { return c.Empty() }
