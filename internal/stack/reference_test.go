package stack_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/stack"
)

// TestParseReference reads -f values: a file on this machine is no reference, a
// reference comes back as a git source with its folder cleaned, and each mistake is an
// error that starts with its fix.
func TestParseReference(t *testing.T) {
	for _, c := range []struct {
		value string
		ok    bool
		want  stack.Source
	}{
		{"qory-stack.yaml", false, stack.Source{}},
		{"./stacks/web/qory-stack.yaml", false, stack.Source{}},
		{"", false, stack.Source{}},
		{"https://github.com/example-org/harness#v1.2.0:stacks/web", true, stack.Source{Git: "https://github.com/example-org/harness", Ref: "v1.2.0", Path: "stacks/web"}},
		{"https://git.example.com:8443/example-org/harness.git#main:stacks/web/", true, stack.Source{Git: "https://git.example.com:8443/example-org/harness.git", Ref: "main", Path: "stacks/web"}},
		{"HTTPS://github.com/example-org/harness#0123456789abcdef0123456789abcdef01234567:./stacks//web", true, stack.Source{Git: "HTTPS://github.com/example-org/harness", Ref: "0123456789abcdef0123456789abcdef01234567", Path: "stacks/web"}},
		{"https://github.com/example-org/harness#v1:.", true, stack.Source{Git: "https://github.com/example-org/harness", Ref: "v1"}},
		{"https://github.com/example-org/harness#v1:a:b", true, stack.Source{Git: "https://github.com/example-org/harness", Ref: "v1", Path: "a:b"}},
	} {
		got, ok, err := stack.ParseReference(c.value)
		if err != nil || ok != c.ok || got != c.want {
			t.Errorf("%q: %+v, %v, %v; want %+v, %v", c.value, got, ok, err, c.want, c.ok)
		}
	}
}

// TestParseReferenceErrors is every mistake a reference can have: not https, no host, no
// ref, no folder, an absolute folder and a folder with a .. segment.
func TestParseReferenceErrors(t *testing.T) {
	for _, c := range []struct{ value, want string }{
		{"http://github.com/example-org/harness#v1:stacks/web", "use an https URL in -f http://github.com/example-org/harness#v1:stacks/web, as <https URL>#<ref>:<folder>: qory fetches a stack reference only over https, and http://github.com/example-org/harness is not an https URL with a host"},
		{"ssh://git@github.com/example-org/harness#v1:stacks/web", "use an https URL in -f "},
		{"file:///srv/harness#v1:stacks/web", "use an https URL in -f "},
		{"https:///example-org/harness#v1:stacks/web", "use an https URL in -f "},
		{"https://github.com/example-org/harness", "add the ref to -f https://github.com/example-org/harness, as <https URL>#<ref>:<folder>: the stack reference has no branch, tag or commit after #"},
		{"https://github.com/example-org/harness#:stacks/web", "add the ref to -f "},
		{"https://github.com/example-org/harness#v1", "add the folder to -f https://github.com/example-org/harness#v1, as <https URL>#<ref>:<folder>: the stack reference has no folder after the ref; the folder is the one that contains the qory-stack.yaml"},
		{"https://github.com/example-org/harness#v1:", "add the folder to -f "},
		{"https://github.com/example-org/harness#v1:/stacks/web", "write the folder in -f https://github.com/example-org/harness#v1:/stacks/web as a path from the repository's root: /stacks/web is an absolute path"},
		{"https://github.com/example-org/harness#v1:../web", "write the folder in -f https://github.com/example-org/harness#v1:../web without ..: ../web has a .. segment, and the folder is a path from the repository's root"},
		{"https://github.com/example-org/harness#v1:stacks/../web", "write the folder in -f "},
		{"https://example-org:s3cret@github.com/example-org/harness#v1", "add the folder to -f https://example-org:(hidden)@github.com/example-org/harness#v1"},
	} {
		_, ok, err := stack.ParseReference(c.value)
		if !ok || err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%q: %v, %v; want an error starting %q", c.value, ok, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: the error holds the URL's password: %v", c.value, err)
		}
	}
}

// TestAtBeforePathAndNonASCIIHost is the two checks of a harness's URLs that a run
// fetches through its stack proxy: an @ between :// and the first / after it, whatever
// comes before it, and a host with a character outside ASCII once its
// percent-encodings are decoded. A host in the xn-- form, a port and an @ in the path
// pass both.
func TestAtBeforePathAndNonASCIIHost(t *testing.T) {
	for _, c := range []struct {
		url          string
		at, nonASCII bool
	}{
		{"https://git.test/team/x", false, false},
		{"https://git.test:443/team/x@main", false, false},
		{"https://xn--bcher-kva.example/team/x", false, false},
		{"https://git.test?@evil.example/x", true, false},
		{"https://git.test#@evil.example/x", true, false},
		{"https://git.test%3F@evil.example/x", true, false},
		{`https://git.test\@evil.example/x`, true, false},
		{"https://git.test:443@evil.example/x", true, false},
		{"https://u%EF%BC%9Ap%EF%BC%A0git.test/x", false, true},
		{"https://u：p＠git.test/x", false, true},
		{"https://u﹫git.test/x", false, true},
		{"https://git.test%EF%BC%8F.evil.example/x", false, true},
		{"https://evil.example％40git.test/x", false, true},
	} {
		if got := stack.AtBeforePath(c.url); got != c.at {
			t.Errorf("AtBeforePath(%q) = %v, want %v", c.url, got, c.at)
		}
		if got := stack.NonASCIIHost(c.url); got != c.nonASCII {
			t.Errorf("NonASCIIHost(%q) = %v, want %v", c.url, got, c.nonASCII)
		}
	}
}
