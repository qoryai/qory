package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConstraintReadsComparatorsOnly pins the grammar of the qory key: comparators with
// full versions read, and a bare version, a two-number version and an unknown operator
// are refused with the message that shows the shape.
func TestConstraintReadsComparatorsOnly(t *testing.T) {
	good := map[string]string{
		">=0.3.0":         ">=0.3.0",
		">=v0.3.0 <0.4.0": ">=0.3.0 <0.4.0",
		"  =0.3.1  ":      "=0.3.1",
		">0.3.0-rc.1":     ">0.3.0-rc.1",
	}
	for in, want := range good {
		c, err := ParseConstraint(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if c.String() != want {
			t.Errorf("%q prints as %q, want %q", in, c.String(), want)
		}
	}
	bad := map[string]string{
		"0.3.0":        `qory "0.3.0" is not one or more comparators such as >=0.3.0`,
		">=0.3":        `qory ">=0.3": "0.3" is not a version such as 0.3.0`,
		"~0.3.0":       `qory "~0.3.0" is not one or more comparators such as >=0.3.0`,
		">=0.3.0+meta": `qory ">=0.3.0+meta": "0.3.0+meta" is not a version such as 0.3.0`,
		"":             "qory is empty; it is one or more comparators such as >=0.3.0",
	}
	for in, want := range bad {
		_, err := ParseConstraint(in)
		if err == nil || err.Error() != want {
			t.Errorf("%q: err = %v, want %q", in, err, want)
		}
	}
}

// TestConstraintAllows is the check itself: every comparator has to hold, a leading v and
// build metadata on the version do not matter, a pseudo-version orders after the tag it
// follows, and the empty constraint allows everything.
func TestConstraintAllows(t *testing.T) {
	c, err := ParseConstraint(">=0.3.0 <0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	for v, want := range map[string]bool{
		"0.3.0":                               true,
		"v0.3.9":                              true,
		"0.3.1+dirty":                         true,
		"0.3.1-0.20260911000000-abcdefabcdef": true,
		"0.4.0":                               false,
		"0.3.0-rc.1":                          false,
		"0.2.1":                               false,
		"dev":                                 false,
		"":                                    false,
	} {
		if got := c.Allows(v); got != want {
			t.Errorf("%s allows %q = %v, want %v", c, v, got, want)
		}
	}
	var none Constraint
	if !none.Allows("0.0.1") || !none.Empty() || none.String() != "" {
		t.Errorf("the zero constraint refuses, or prints as %q", none.String())
	}
	both := c.Join(mustParse(t, "=0.3.5"))
	if both.Allows("0.3.4") || !both.Allows("0.3.5") || both.String() != ">=0.3.0 <0.4.0 =0.3.5" {
		t.Errorf("joined constraint %s misbehaves", both)
	}
}

// TestConstraintJoinKeepsASharedComparatorOnce is a stack repeating its repository's
// range: the comparator both name prints once, the others are kept in order, and
// joining the empty constraint either way changes nothing.
func TestConstraintJoinKeepsASharedComparatorOnce(t *testing.T) {
	c := mustParse(t, ">=0.4.3 <0.6.0")
	if got := c.Join(mustParse(t, ">=0.5.0 <0.6.0")).String(); got != ">=0.4.3 <0.6.0 >=0.5.0" {
		t.Errorf("joined %q", got)
	}
	if got := c.Join(c).String(); got != c.String() {
		t.Errorf("joined with itself %q", got)
	}
	var none Constraint
	if got := none.Join(c).String(); got != c.String() {
		t.Errorf("the empty constraint joined %q", got)
	}
	if got := c.Join(none).String(); got != c.String() {
		t.Errorf("joined with the empty constraint %q", got)
	}
}

// TestStackReadsTheQoryKey is the key in a stack file: read into [Stack.Qory], and a
// bad value refused with the file named and no line number in the way.
func TestStackReadsTheQoryKey(t *testing.T) {
	path := write(t, "apiVersion: qory.dev/v1alpha1\nqory: \">=0.3.0\"\nmodules: [{name: core}]\n")
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Qory.String() != ">=0.3.0" {
		t.Errorf("qory = %q", p.Qory.String())
	}
	path = write(t, "apiVersion: qory.dev/v1alpha1\nqory: [0.3.0]\nmodules: [{name: core}]\n")
	_, err = Load(path)
	if err == nil || !strings.HasPrefix(err.Error(), path+": qory is one or more comparators such as >=0.3.0, as a string") {
		t.Errorf("err = %v", err)
	}
}

// TestLoadJoinsTheRepositoryRange is the qory key of the qory.yaml at the root of the
// git repository a stack sits in, below the root: a stack naming no range is held to
// the repository's, one naming its own is held to both, a comparator both name prints
// once, and a bad value at the root is refused naming the root's file.
func TestLoadJoinsTheRepositoryRange(t *testing.T) {
	// git reports the toplevel with symlinks resolved, as the temporary directory is
	// not on every system.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	rootFile := filepath.Join(root, "qory.yaml")
	path := filepath.Join(root, "stacks", "nextjs", FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootFile, []byte("apiVersion: qory.dev/v1alpha1\nqory: \">=0.5.0\"\nexports: {stacks: [nextjs]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ own, want string }{
		{"", ">=0.5.0"},
		{"qory: \">=0.4.3\"\n", ">=0.4.3 >=0.5.0"},
		{"qory: \">=0.5.0\"\n", ">=0.5.0"},
	} {
		if err := os.WriteFile(path, []byte("apiVersion: qory.dev/v1alpha1\n"+c.own+"modules: [{name: core}]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := Load(path)
		if err != nil {
			t.Fatalf("%q: %v", c.own, err)
		}
		if p.Root != root || p.Qory.String() != c.want {
			t.Errorf("%q: root %s, qory %q, want %q", c.own, p.Root, p.Qory.String(), c.want)
		}
	}
	if err := os.WriteFile(rootFile, []byte("apiVersion: qory.dev/v1alpha1\nqory: \"0.5.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	if want := rootFile + `: qory "0.5.0" is not one or more comparators such as >=0.3.0`; err == nil || err.Error() != want {
		t.Errorf("a bad root key: err = %v, want %q", err, want)
	}
	// A root without the key, or without a file, leaves the stack's own range alone.
	if err := os.Remove(rootFile); err != nil {
		t.Fatal(err)
	}
	if p, err := Load(path); err != nil || p.Qory.String() != ">=0.5.0" {
		t.Errorf("no root file: qory %q, %v", p.Qory.String(), err)
	}
}

func mustParse(t *testing.T, s string) Constraint {
	t.Helper()
	c, err := ParseConstraint(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
