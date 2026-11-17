package stack

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// withWorkflows is a valid stack with workflows set to body.
func withWorkflows(body string) string {
	return valid + "workflows:\n" + body
}

// TestLoadReadsTheWorkflows reads each workflow's time_limit as a duration, and a
// workflow with no time_limit, or none listed, as no limit.
func TestLoadReadsTheWorkflows(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  hello:\n    time_limit: 7h\n  review-pr:\n    time_limit: 45m\n  plain: {}\n")))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]time.Duration{"hello": 7 * time.Hour, "review-pr": 45 * time.Minute, "plain": 0, "none": 0} {
		if got := p.TimeLimitOf(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	p, err = Load(write(t, valid))
	if err != nil || p.Workflows != nil || p.TimeLimitOf("hello") != 0 {
		t.Errorf("no workflows: %+v, %v", p, err)
	}
}

// TestLoadReadsTheAccess reads each workflow's access as written, and a workflow with
// no access, or none listed, as read.
func TestLoadReadsTheAccess(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  fix:\n    access: write\n  stage:\n    access: write-no-push\n  review:\n    access: read\n  plain:\n    time_limit: 45m\n")))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"fix": AccessWrite, "review": AccessRead, "plain": AccessRead, "none": AccessRead, "stage": AccessWriteNoPush} {
		if got := p.AccessOf(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if p.Workflows["plain"].Access != "" {
		t.Errorf("plain: access %q as written, want none", p.Workflows["plain"].Access)
	}
	p, err = Load(write(t, valid))
	if err != nil || p.AccessOf("fix") != AccessRead {
		t.Errorf("no workflows: %+v, %v", p, err)
	}
}

// TestLowerAccessTakesTheLowerOfTwoLevels is every pair of the three levels in the order
// read, write-no-push, write. The empty level does not lower the other, and any other
// value, such as Read or bogus, counts as read.
func TestLowerAccessTakesTheLowerOfTwoLevels(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{AccessRead, AccessRead, AccessRead},
		{AccessRead, AccessWriteNoPush, AccessRead},
		{AccessRead, AccessWrite, AccessRead},
		{AccessWriteNoPush, AccessRead, AccessRead},
		{AccessWriteNoPush, AccessWriteNoPush, AccessWriteNoPush},
		{AccessWriteNoPush, AccessWrite, AccessWriteNoPush},
		{AccessWrite, AccessRead, AccessRead},
		{AccessWrite, AccessWriteNoPush, AccessWriteNoPush},
		{AccessWrite, AccessWrite, AccessWrite},
		{AccessWrite, "", AccessWrite},
		{AccessWriteNoPush, "", AccessWriteNoPush},
		{"", AccessRead, AccessRead},
		{"", "", ""},
		{AccessWrite, "Read", AccessRead},
		{"Read", AccessWrite, AccessRead},
		{AccessWriteNoPush, "bogus", AccessRead},
		{"bogus", "Write", AccessRead},
	} {
		if got := LowerAccess(c.a, c.b); got != c.want {
			t.Errorf("LowerAccess(%q, %q) is %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// TestLoadRefusesABrokenWorkflow refuses a workflow name outside the contract's form, a
// time_limit that is no duration or not above zero, an access other than read,
// write-no-push or write,
// and a key a workflow does not have.
func TestLoadRefusesABrokenWorkflow(t *testing.T) {
	isolate(t)
	for _, c := range []struct {
		body, want string
	}{
		{"  Hello: {}\n", "Rename the workflow \"Hello\".\n  A workflow name is a lower-case letter, then up to 54 lower-case letters, digits, and hyphens, 55 characters at most.\n  workflows is in {path}."},
		{"  hello_there: {}\n", "Rename the workflow \"hello_there\".\n  A workflow name is a lower-case letter, then up to 54 lower-case letters, digits, and hyphens, 55 characters at most.\n  workflows is in {path}."},
		{"  hello:\n    time_limit: 7 hours\n", "to a duration above zero, such as 7h or 45m.\n  The value is \"7 hours\"."},
		{"  hello:\n    time_limit: 0s\n", "to a duration above zero, such as 7h or 45m.\n  The value is \"0s\"."},
		{"  hello:\n    time_limit: -1h\n", "to a duration above zero, such as 7h or 45m.\n  The value is \"-1h\"."},
		{"  hello:\n    access: admin\n", "Set workflows.hello.access to a valid level.\n  The levels are read, write-no-push, and write.\n  The value is \"admin\".\n  workflows.hello is in {path}."},
		{"  hello:\n    access: Write\n", "Set workflows.hello.access to a valid level.\n  The levels are read, write-no-push, and write.\n  The value is \"Write\".\n  workflows.hello is in {path}."},
		{"  hello:\n    access: write\n    time_limit: 0s\n", "Set workflows.hello.time_limit to a duration above zero"},
		{"  hello:\n    prompt: x\n", `key "prompt" is not one qory-stack.yaml reads`},
	} {
		path := write(t, withWorkflows(c.body))
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), strings.ReplaceAll(c.want, "{path}", path)) {
			t.Errorf("%q: %v, want %q", c.body, err, c.want)
		}
	}
}

// TestExtendTakesTheBaseWorkflows is a checkout's qory.yaml extending a stack: the
// stack that composes has the base's workflows.
func TestExtendTakesTheBaseWorkflows(t *testing.T) {
	base := &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{},
		Workflows: map[string]Workflow{"hello": {TimeLimitText: "7h", TimeLimit: 7 * time.Hour, Access: AccessWrite}}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p := &Stack{Extends: Source{Path: "../base"}, File: "/app/qory-stack.yaml"}
	out, err := Extend(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if out.TimeLimitOf("hello") != 7*time.Hour || out.AccessOf("hello") != AccessWrite {
		t.Errorf("workflows %+v", out.Workflows)
	}
}

// TestTheSchemaTakesTheWorkflows validates stacks with workflows against
// stack.schema.json: a time_limit in Go's duration form above zero passes, and a
// workflow name, a time_limit or an access that Load refuses, a zero time_limit among
// them, fails.
func TestTheSchemaTakesTheWorkflows(t *testing.T) {
	s, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"  hello:\n    time_limit: 7h\n", true},
		{"  hello:\n    time_limit: 1h30m\n", true},
		{"  hello: {}\n", true},
		{"  Hello: {}\n", false},
		{"  hello:\n    time_limit: 7 hours\n", false},
		{"  hello:\n    time_limit: 0s\n", false},
		{"  hello:\n    time_limit: 0h0m\n", false},
		{"  hello:\n    time_limit: 0.5s\n", true},
		{"  hello:\n    access: read\n", true},
		{"  hello:\n    access: write\n    time_limit: 7h\n", true},
		{"  hello:\n    access: write-no-push\n", true},
		{"  hello:\n    access: admin\n", false},
		{"  hello:\n    access: Write\n", false},
		{"  hello:\n    prompt: x\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(withWorkflows(c.body)), &doc); err != nil {
			t.Fatal(err)
		}
		if err := s.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %t", c.body, err, c.valid)
		}
	}
}

// TestNewComposeReadsTheWorkflowsOfADefinedStack is the harness section of a checkout's
// qory.yaml that defines a stack, with a target and modules: it may list workflows,
// read and checked as a stack file's are, with the same errors.
func TestNewComposeReadsTheWorkflowsOfADefinedStack(t *testing.T) {
	defined := func(w map[string]Workflow) *Stack {
		return &Stack{Target: Target{Runtimes: Runtimes{"claude"}}, Modules: []Module{{Name: "app"}}, Workflows: w}
	}
	p, err := compose(t, defined(map[string]Workflow{"fix": {Access: AccessWrite, TimeLimitText: "45m"}, "review": {}}))
	if err != nil {
		t.Fatal(err)
	}
	if p.AccessOf("fix") != AccessWrite || p.TimeLimitOf("fix") != 45*time.Minute || p.AccessOf("review") != AccessRead || p.TimeLimitOf("review") != 0 {
		t.Errorf("workflows %+v", p.Workflows)
	}
	for _, c := range []struct {
		w    Workflow
		want string
	}{
		{Workflow{Access: "admin"}, "to a valid level.\n  The levels are read, write-no-push, and write.\n  The value is \"admin\".\n  workflows.fix is in "},
		{Workflow{TimeLimitText: "7 hours"}, "to a duration above zero, such as 7h or 45m.\n  The value is \"7 hours\".\n  workflows.fix is in "},
		{Workflow{TimeLimitText: "0s"}, "to a duration above zero, such as 7h or 45m.\n  The value is \"0s\".\n  workflows.fix is in "},
	} {
		if _, err := compose(t, defined(map[string]Workflow{"fix": c.w})); err == nil || !strings.HasPrefix(err.Error(), "Set workflows.fix.") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.w, err, c.want)
		}
	}
}

// TestNewComposeRefusesWorkflowsBesideExtends is the harness section of a checkout's
// qory.yaml that extends a stack and lists workflows: it is refused, since the section
// takes the workflows of its base.
func TestNewComposeRefusesWorkflowsBesideExtends(t *testing.T) {
	const want = "\n  A harness section that extends a stack takes the workflows of its base.\n  Both keys are in "
	_, err := compose(t, &Stack{Extends: Source{Path: "../base"}, Modules: []Module{{Name: "app"}}, Workflows: map[string]Workflow{"fix": {Access: AccessWrite}}})
	if err == nil || !strings.HasPrefix(err.Error(), "Remove workflows or extends.") || !strings.Contains(err.Error(), want) {
		t.Errorf("%v, want %q", err, want)
	}
}

// TestAWorkflowNameHasAtMost55Characters is the stack check at the limit: a name of 55
// characters loads and [WorkflowEntry] makes one of 64, and a name of 56 stops, with the
// limit and the name in the line.
func TestAWorkflowNameHasAtMost55Characters(t *testing.T) {
	isolate(t)
	limit := strings.Repeat("a", 55)
	if _, err := Load(write(t, withWorkflows("  "+limit+": {}\n"))); err != nil {
		t.Errorf("55 characters: %v", err)
	}
	if got := len(WorkflowEntry(limit)); got != 64 {
		t.Errorf("the prefixed name has %d characters, want 64", got)
	}
	over := limit + "a"
	want := `Rename the workflow "` + over + "\".\n  A workflow name is a lower-case letter, then up to 54 lower-case letters, digits, and hyphens, 55 characters at most."
	if _, err := Load(write(t, withWorkflows("  "+over+": {}\n"))); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("56 characters: %v\nwant %s", err, want)
	}
}

// TestWorkflowEntriesListThePrefixedNameFirst is the one rule the run's lookup and the
// compose's check read.
func TestWorkflowEntriesListThePrefixedNameFirst(t *testing.T) {
	if got := WorkflowEntries("deploy"); !slices.Equal(got, []string{"workflow-deploy", "deploy"}) {
		t.Errorf("got %q", got)
	}
}

// TestTheAccessRefusalFirstLineFitsWithTheLongestName is a workflow named with 55
// characters, the longest name, and an access that is no level: the first line of the
// refusal has at most 100 characters and starts with the fix.
func TestTheAccessRefusalFirstLineFitsWithTheLongestName(t *testing.T) {
	isolate(t)
	name := "a" + strings.Repeat("b", 54)
	_, err := Load(write(t, withWorkflows("  "+name+":\n    access: admin\n")))
	if err == nil {
		t.Fatal("no refusal")
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	if want := "Set workflows." + name + ".access to a valid level."; first != want || len(first) > 100 {
		t.Errorf("first line %q (%d characters), want %q within 100", first, len(first), want)
	}
	if len(WorkflowEntry(name)) != 64 {
		t.Errorf("the entry name has %d characters, want 64", len(WorkflowEntry(name)))
	}
}
