package stack

import (
	"strings"
	"testing"
)

// TestFileAllowedMatchesByPathSegment is the prefix list of extending.files: a prefix
// with or without a trailing slash covers the segment and what is below it, never a
// longer segment, and a prefix naming a file covers that file only.
func TestFileAllowedMatchesByPathSegment(t *testing.T) {
	cases := []struct {
		name     string
		prefixes []string
		want     bool
	}{
		{"claude/rules/nextjs-15.md", []string{"claude/rules"}, true},
		{"claude/rules/nextjs-15.md", []string{"claude/rules/"}, true},
		{"claude/rules/a/b.md", []string{"claude/rules"}, true},
		{"claude/rules-private/x.md", []string{"claude/rules"}, false},
		{"claude/rules-private/x.md", []string{"claude/rules/"}, false},
		{"claude/rules/nextjs-15.md", []string{"claude"}, true},
		{"cursor/rules/x.mdc", []string{"claude"}, false},
		{"claude/rules/nextjs-15.md", []string{"claude/rules/nextjs-15.md"}, true},
		{"claude/rules/nextjs-15.md.bak", []string{"claude/rules/nextjs-15.md"}, false},
		{"claude/rules/nextjs-15.md", nil, false},
	}
	for _, c := range cases {
		if got := FileAllowed(c.name, c.prefixes); got != c.want {
			t.Errorf("FileAllowed(%q, %v) = %v", c.name, c.prefixes, got)
		}
	}
}

// TestLoadRefusesExtendingFilesShapes is a base naming files under extending.kinds, and
// a prefix that is not a relative <runtime>/<path>.
func TestLoadRefusesExtendingFilesShapes(t *testing.T) {
	head := "apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n"
	cases := []struct{ yaml, want string }{
		{head + "extending:\n  kinds: [files]\n", "extending.kinds names files; the paths an extending module's files may sit under go in extending.files"},
		{head + "extending:\n  files: [/claude/rules]\n", `extending.files names "/claude/rules", which is not a <runtime>/<path> prefix`},
		{head + "extending:\n  files: [claude/../rules]\n", `extending.files names "claude/../rules"`},
		{head + "extending:\n  files: [\"\"]\n", `extending.files names ""`},
	}
	for _, c := range cases {
		_, err := Load(write(t, c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.yaml, err, c.want)
		}
	}
	p, err := Load(write(t, head+"extending:\n  files: [claude/rules/, copilot/instructions]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Extending.Files) != 2 {
		t.Fatalf("files: %v", p.Extending.Files)
	}
}

// TestExtendRefusesAModuleNamedLikeABaseModule is a customer naming core, the base's
// module, in their own qory.yaml: the message says the module belongs to the base before any
// source is resolved.
func TestExtendRefusesAModuleNamedLikeABaseModule(t *testing.T) {
	base := &Stack{Target: Target{Runtimes: Runtimes{"claude"}}, Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p := &Stack{Extends: Source{Path: "../base"}, Modules: []Module{{Name: "core", Exclude: Selection{Kinds: map[string][]string{"skills": {"review"}}}}}, File: "/app/qory-stack.yaml"}
	_, err := Extend(base, p)
	want := "module core belongs to the base stack; a checkout's qory.yaml cannot name a base module, exclude from it or replace it"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
