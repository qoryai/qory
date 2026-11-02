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
	head := "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n"
	cases := []struct{ yaml, want string }{
		{head + "extending:\n  kinds: [files]\n", "extending.kinds lists files; the paths an extending module's files may sit under go in extending.files"},
		{head + "extending:\n  files: [/claude/rules]\n", `extending.files lists "/claude/rules", which is not a <runtime>/<path> prefix`},
		{head + "extending:\n  files: [claude/../rules]\n", `extending.files lists "claude/../rules"`},
		{head + "extending:\n  files: [\"\"]\n", `extending.files lists ""`},
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

// TestExtendTakesTheExcludeOfABaseModule is a checkout's qory.yaml listing core, the
// base's module, by its name with an exclude of a skill: the exclude goes on the base's
// core, and the checkout's entry is no module of the result. A base module listed by its
// export is reached by the export's name, and a checkout module by a path alone beside a
// base module by a path alone is appended.
func TestExtendTakesTheExcludeOfABaseModule(t *testing.T) {
	base := &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{Kinds: []string{"skills"}}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p := &Stack{Extends: Source{Path: "../base"}, Modules: []Module{{Name: "core", Exclude: Selection{Kinds: map[string][]string{"skills": {"review"}}}}, {Name: "app"}}, File: "/app/qory-stack.yaml"}
	out, err := Extend(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Modules) != 2 || out.Modules[0].Name != "core" || !out.Modules[0].Base || out.Modules[1].Name != "app" {
		t.Fatalf("modules = %+v, want the base's core, then app", out.Modules)
	}
	if got := out.Modules[0].CheckoutExclude.Kinds["skills"]; len(got) != 1 || got[0] != "review" {
		t.Fatalf("core's checkout exclude = %+v, want skills/review", out.Modules[0].CheckoutExclude)
	}
	if !out.Modules[0].Exclude.Empty() {
		t.Fatalf("core's exclude = %+v, want the base's, none", out.Modules[0].Exclude)
	}
	exported := &Stack{Modules: []Module{{Source: Source{Git: "https://example.com/acme/harness", Ref: "v1", Module: "core"}}}, Extending: &Extending{Kinds: []string{"skills"}}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	out, err = Extend(exported, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Modules) != 2 || out.Modules[0].CheckoutExclude.Empty() {
		t.Fatalf("a base module by its export: modules = %+v, want core with the checkout's exclude", out.Modules)
	}
	byPath := &Stack{Modules: []Module{{Source: Source{Path: "modules/core"}}}, Extending: &Extending{Kinds: []string{"skills"}}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	out, err = Extend(byPath, &Stack{Extends: Source{Path: "../base"}, Modules: []Module{{Source: Source{Path: "./app"}}}, File: "/app/qory-stack.yaml"})
	if err != nil || len(out.Modules) != 2 {
		t.Fatalf("a base module and a checkout module, each by a path alone: %v, %+v", err, out)
	}
}

// TestExtendRefusesWhatABaseModuleCannotExclude is a checkout's qory.yaml listing core,
// the base's module, with more than an exclude of entries its modules may ship: each
// message starts with the fix and states that the module belongs to the base.
func TestExtendRefusesWhatABaseModuleCannotExclude(t *testing.T) {
	base := &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{Kinds: []string{"skills", "agents"}, Files: []string{"claude/rules/"}}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	skills := Selection{Kinds: map[string][]string{"skills": {"review"}}}
	for _, c := range []struct {
		name string
		l    Module
		want string
	}{
		{"a source", Module{Name: "core", Source: Source{Path: "./core"}}, "list module core with its name and exclude only: it belongs to the base stack, and a checkout's qory.yaml cannot change its source, only, variant or link"},
		{"an only", Module{Name: "core", Only: skills}, "list module core with its name and exclude only: it belongs to the base stack, and a checkout's qory.yaml cannot change its source, only, variant or link"},
		{"a variant", Module{Name: "core", Exclude: skills, Variant: "codex"}, "list module core with its name and exclude only"},
		{"a link", Module{Name: "core", Exclude: skills, Link: "core"}, "list module core with its name and exclude only"},
		{"no exclude", Module{Name: "core"}, "add an exclude to module core, or remove its entry: it belongs to the base stack, and a checkout's qory.yaml lists a base module only to exclude entries from it"},
		{"the instructions", Module{Name: "core", Exclude: Selection{Instructions: true}}, "remove instructions from the exclude of module core: it belongs to the base stack, and a checkout's qory.yaml excludes entries from a base module, not its instructions, settings or env"},
		{"the settings", Module{Name: "core", Exclude: Selection{Settings: Part{All: true}}}, "remove settings from the exclude of module core: it belongs to the base stack"},
		{"a variable", Module{Name: "core", Exclude: Selection{Env: Part{Names: []string{"PROFILE"}}}}, "remove env from the exclude of module core: it belongs to the base stack"},
		{"a hook", Module{Name: "core", Exclude: Selection{Kinds: map[string][]string{"hooks": {"guard.sh"}}}}, "remove hooks/guard.sh from the exclude of module core: a checkout excludes from a base module only entries of the kinds an extending module may ship, and the base stack lets an extending module ship skills, agents"},
		{"a file elsewhere", Module{Name: "core", Exclude: Selection{Kinds: map[string][]string{"files": {"claude/agents/x.md"}}}}, "remove files/claude/agents/x.md from the exclude of module core: a checkout excludes from a base module only files where an extending module may ship them, and the base stack lets an extending module ship files under claude/rules/"},
	} {
		p := &Stack{Extends: Source{Path: "../base"}, Modules: []Module{c.l}, File: "/app/qory-stack.yaml"}
		_, err := Extend(base, p)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	p := &Stack{Extends: Source{Path: "../base"}, Modules: []Module{{Name: "core", Exclude: Selection{Kinds: map[string][]string{"files": {"claude/rules/web.md"}, "agents": {"reviewer"}}}}}, File: "/app/qory-stack.yaml"}
	if _, err := Extend(base, p); err != nil {
		t.Errorf("a rule file and an agent: %v", err)
	}
	closed := &Stack{Modules: base.Modules, Extending: &Extending{}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p = &Stack{Extends: Source{Path: "../base"}, Modules: []Module{{Name: "core", Exclude: skills}}, File: "/app/qory-stack.yaml"}
	if _, err := Extend(closed, p); err == nil || !strings.HasSuffix(err.Error(), "and the base stack lets an extending module ship no entries") {
		t.Errorf("a base that lets no entry in: err = %v", err)
	}
}

// TestExtendMergesTheBindings is a checkout's qory.yaml binding a role over its base: the
// base's bindings carry, the checkout's add to them and rebind a role of the base's.
func TestExtendMergesTheBindings(t *testing.T) {
	base := &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Bind: map[string]string{"agents/coder": "base-coder", "agents/reviewer-role": "reviewer"}, Extending: &Extending{}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p := &Stack{Extends: Source{Path: "../base"}, Bind: map[string]string{"agents/coder": "rails-coder", "skills/entry": "implement"}, File: "/app/qory-stack.yaml"}
	out, err := Extend(base, p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"agents/coder": "rails-coder", "agents/reviewer-role": "reviewer", "skills/entry": "implement"}
	if len(out.Bind) != len(want) {
		t.Fatalf("bind %v, want %v", out.Bind, want)
	}
	for role, to := range want {
		if out.Bind[role] != to {
			t.Errorf("bind %s = %q, want %q", role, out.Bind[role], to)
		}
	}
	if base.Bind["agents/coder"] != "base-coder" {
		t.Error("Extend wrote into the base's bindings")
	}
}

// TestExtendKeepsTheDocumentsTarget is a checkout's qory.yaml naming a target beside
// extends: the composed stack carries that target, since a delivered stack has none,
// and a document without one composes with an empty target for the machine to fill.
func TestExtendKeepsTheDocumentsTarget(t *testing.T) {
	base := &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p := &Stack{Extends: Source{Path: "../base"}, Target: Target{Runtimes: Runtimes{"claude", "codex"}, Model: "opus"}, File: "/app/qory.yaml"}
	out, err := Extend(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if out.Target.Runtimes.String() != "claude, codex" || out.Target.Model != "opus" {
		t.Errorf("target %+v, want the document's", out.Target)
	}
	if len(out.Modules) != 1 || !out.Modules[0].Base {
		t.Errorf("modules %+v", out.Modules)
	}
	p = &Stack{Extends: Source{Path: "../base"}, File: "/app/qory.yaml"}
	if out, err = Extend(base, p); err != nil || len(out.Target.Runtimes) != 0 || out.Target.Model != "" {
		t.Errorf("no target: %+v, %v", out.Target, err)
	}
}
