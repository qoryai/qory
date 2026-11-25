package stack

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// withWorkflows is a valid stack with workflows set to body.
func withWorkflows(body string) string {
	return valid + "workflows:\n" + body
}

// TestLoadReadsTheWorkflows reads each workflow listed by its name, and a stack with
// none listed as no workflows.
func TestLoadReadsTheWorkflows(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  hello:\n    access: write\n  review-pr: {}\n  plain: {}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := slices.Sorted(maps.Keys(p.Workflows)), []string{"hello", "plain", "review-pr"}; !slices.Equal(got, want) {
		t.Errorf("workflows %q, want %q", got, want)
	}
	p, err = Load(write(t, valid))
	if err != nil || p.Workflows != nil {
		t.Errorf("no workflows: %+v, %v", p, err)
	}
}

// TestLoadReadsTheAccess reads each workflow's access as written, and a workflow with
// no access, or none listed, as read.
func TestLoadReadsTheAccess(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  fix:\n    access: write\n"+
		"  stage:\n    access: write-no-push\n  review:\n    access: read\n"+
		"  plain: {}\n")))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"fix": AccessWrite, "review": AccessRead, "plain": AccessRead, "none": AccessRead,
		"stage": AccessWriteNoPush,
	} {
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
		{"", "bogus", AccessRead},
		{"bogus", "", AccessRead},
	} {
		if got := LowerAccess(c.a, c.b); got != c.want {
			t.Errorf("LowerAccess(%q, %q) is %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// TestLoadReadsJudges reads judges as written: false, true, and nil for a workflow that
// leaves it out; a value that is no boolean is refused, and a key that is not read is
// still refused.
func TestLoadReadsJudges(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  a:\n    judges: false\n  b:\n    judges: true\n  c: {}\n")))
	if err != nil {
		t.Fatal(err)
	}
	if j := p.Workflows["a"].Judges; j == nil || *j {
		t.Errorf("a: judges %v, want false", j)
	}
	if j := p.Workflows["b"].Judges; j == nil || !*j {
		t.Errorf("b: judges %v, want true", j)
	}
	if j := p.Workflows["c"].Judges; j != nil {
		t.Errorf("c: judges %v, want nil", *j)
	}
	for _, body := range []string{"  a:\n    judges: \"no\"\n", "  a:\n    judges: 0\n", "  a:\n    judges: []\n", "  a:\n    judges:\n"} {
		if _, err := Load(write(t, withWorkflows(body))); err == nil {
			t.Errorf("%q: no error", body)
		}
	}
	if _, err := Load(write(t, withWorkflows("  a:\n    judge: false\n"))); err == nil || !strings.Contains(err.Error(), `Key "judge" is not one qory-stack.yaml reads.`) {
		t.Errorf("unknown key: %v", err)
	}
	_, err = Load(write(t, withWorkflows("  a:\n    judges: \"no\"\n")))
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal !!str into bool") {
		t.Errorf("a quoted no: %v", err)
	}
}

// TestLoadReadsReviewGroup reads review_group as written, and a workflow that leaves it
// out as none; a value that is no string or no group role name is refused, and a key
// that is not read is still refused.
func TestLoadReadsReviewGroup(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  a:\n    review_group: reviewers\n  b:\n    review_group: \"no\"\n  c: {}\n")))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a": "reviewers", "b": "no", "c": ""} {
		if got := p.Workflows[name].ReviewGroup; got != want {
			t.Errorf("%s: review_group %q, want %q", name, got, want)
		}
	}
	for _, value := range []string{"5", "true", "[reviewers]", "{a: b}", "", "\"\"", "a/b", "a@b", "\".hidden\"", "\"a b\""} {
		if _, err := Load(write(t, withWorkflows("  a:\n    review_group: "+value+"\n"))); err == nil {
			t.Errorf("%q: no error", value)
		}
	}
	_, err = Load(write(t, withWorkflows("  a:\n    review_group: 5\n")))
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal !!int into string") {
		t.Errorf("a number: %v", err)
	}
	_, err = Load(write(t, withWorkflows("  a:\n    review_group: a/b\n")))
	if err == nil || !strings.Contains(err.Error(), "Set workflows.a.review_group to a group role.") {
		t.Errorf("a name with a slash: %v", err)
	}
	if _, err := Load(write(t, withWorkflows("  a:\n    review_groups: reviewers\n"))); err == nil || !strings.Contains(err.Error(), `Key "review_groups" is not one qory-stack.yaml reads.`) {
		t.Errorf("unknown key: %v", err)
	}
}

// TestLoadReadsTheTier reads each workflow's tier as written, and a workflow with no
// tier, or none listed, as none.
func TestLoadReadsTheTier(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  deep:\n    tier: thinking\n"+
		"  quick:\n    tier: balanced\n  small:\n    tier: light\n"+
		"  plain:\n    access: write\n")))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"deep": "thinking", "quick": "balanced", "small": "light", "plain": "", "none": "",
	} {
		if got := p.Workflows[name].Tier; got != want {
			t.Errorf("%s: tier %q, want %q", name, got, want)
		}
	}
	if p.AccessOf("plain") != AccessWrite {
		t.Errorf("plain: %+v", p.Workflows["plain"])
	}
	p, err = Load(write(t, valid))
	if err != nil || p.Workflows != nil {
		t.Errorf("no workflows: %+v, %v", p, err)
	}
}

// TestLoadRefusesATierThatIsNoTier refuses a tier that is not light, balanced or thinking,
// with the fix first and the file last.
func TestLoadRefusesATierThatIsNoTier(t *testing.T) {
	isolate(t)
	for _, value := range []string{"fast", "fastest", "Thinking", "${qory:light}"} {
		path := write(t, withWorkflows("  hello:\n    tier: \""+value+"\"\n"))
		_, err := Load(path)
		want := "Set workflows.hello.tier to a valid tier.\n" +
			"  The tiers are light, balanced, and thinking.\n  The value is \"" + value + "\".\n" +
			"  workflows.hello is in " + path + "."
		if err == nil || err.Error() != want {
			t.Errorf("%q:\n%v\nwant:\n%s", value, err, want)
		}
	}
}

// TestLoadReadsThePlan reads the plan of a write workflow as written, and a workflow with
// no plan, or none listed, as none.
func TestLoadReadsThePlan(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  build:\n    access: write\n    plan: change-plan\n"+
		"  fix:\n    access: write\n    plan: a1\n    tier: thinking\n"+
		"  plain:\n    access: write\n  review:\n    access: read\n")))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"build": "change-plan", "fix": "a1", "plain": "", "review": "", "none": "",
	} {
		if got := p.Workflows[name].Plan; got != want {
			t.Errorf("%s: plan %q, want %q", name, got, want)
		}
	}
}

// TestLoadRefusesAPlanOnAWorkflowThatDoesNotWrite refuses a plan on a workflow whose
// access is read, write-no-push, or left out, with the fix first, the access, and the file
// last. The error is compared whole.
func TestLoadRefusesAPlanOnAWorkflowThatDoesNotWrite(t *testing.T) {
	isolate(t)
	for _, c := range []struct{ access, level string }{{"", "read"}, {"    access: read\n", "read"}, {"    access: write-no-push\n", "write-no-push"}} {
		path := write(t, withWorkflows("  hello:\n"+c.access+"    plan: change-plan\n"))
		_, err := Load(path)
		want := "Remove `plan` from hello, or set its access to write.\n" +
			"  Only a workflow with access write has a plan, and the access of hello is " + c.level + ".\n" +
			"  workflows.hello is in " + path + "."
		if err == nil || err.Error() != want {
			t.Errorf("access %q:\n%v\nwant:\n%s", c.access, err, want)
		}
	}
}

// TestLoadRefusesAPlanThatIsNoArtifactType refuses a plan that has not the form of an
// artifact type, with the fix first and the file last.
func TestLoadRefusesAPlanThatIsNoArtifactType(t *testing.T) {
	isolate(t)
	for _, value := range []string{"Change-Plan", "change_plan", "-plan", "plan-", "a--b", strings.Repeat("a", 65), "${qory:light}"} {
		path := write(t, withWorkflows("  hello:\n    access: write\n    plan: \""+value+"\"\n"))
		_, err := Load(path)
		want := "Set workflows.hello.plan to an artifact type.\n" +
			"  An artifact type is lowercase letters and digits in groups joined by single hyphens, starting with a letter, at most 64 bytes.\n" +
			"  The value is \"" + value + "\".\n  workflows.hello is in " + path + "."
		if err == nil || err.Error() != want {
			t.Errorf("%q:\n%v\nwant:\n%s", value, err, want)
		}
	}
}

// TestLoadRefusesABrokenWorkflow is Load ending with an error for a workflow name outside
// the contract's form, an access other than read, write-no-push or write, and a key a
// workflow does not have, time_limit among them: a dispatched run's time limit comes only
// from Qory Apiary.
func TestLoadRefusesABrokenWorkflow(t *testing.T) {
	isolate(t)
	for _, c := range []struct {
		body, want string
	}{
		{"  Hello: {}\n", "Rename the workflow \"Hello\".\n  A workflow name is a lower-case letter, then up to 54 lower-case letters, digits, and hyphens, 55 characters at most.\n  workflows is in {path}."},
		{"  hello_there: {}\n", "Rename the workflow \"hello_there\".\n  A workflow name is a lower-case letter, then up to 54 lower-case letters, digits, and hyphens, 55 characters at most.\n  workflows is in {path}."},
		{"  hello:\n    access: admin\n", "Set workflows.hello.access to a valid level.\n" +
			"  The levels are read, write-no-push, and write.\n  The value is \"admin\".\n" +
			"  workflows.hello is in {path}."},
		{"  hello:\n    access: Write\n", "Set workflows.hello.access to a valid level.\n" +
			"  The levels are read, write-no-push, and write.\n  The value is \"Write\".\n" +
			"  workflows.hello is in {path}."},
		{"  hello:\n    access: write\n    time_limit: 45m\n", `Key "time_limit" is not one qory-stack.yaml reads.`},
		{"  hello:\n    tier: fastest\n", "Set workflows.hello.tier to a valid tier.\n" +
			"  The tiers are light, balanced, and thinking.\n  The value is \"fastest\".\n" +
			"  workflows.hello is in {path}."},
		{"  hello:\n    prompt: x\n", `Key "prompt" is not one qory-stack.yaml reads.`},
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
		Workflows: map[string]Workflow{"hello": {Access: AccessWrite}}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p := &Stack{Extends: Source{Path: "../base"}, File: "/app/qory-stack.yaml"}
	out, err := Extend(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessOf("hello") != AccessWrite {
		t.Errorf("workflows %+v", out.Workflows)
	}
}

// TestTheSchemaTakesTheWorkflows validates stacks with workflows against
// stack.schema.json: a workflow name or an access that Load stops on fails, and so does
// a time_limit, which a workflow does not have.
func TestTheSchemaTakesTheWorkflows(t *testing.T) {
	s, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"  hello: {}\n", true},
		{"  Hello: {}\n", false},
		{"  hello:\n    time_limit: 7h\n", false},
		{"  hello:\n    access: read\n", true},
		{"  hello:\n    access: write\n    time_limit: 7h\n", false},
		{"  hello:\n    access: write-no-push\n", true},
		{"  hello:\n    access: admin\n", false},
		{"  hello:\n    access: Write\n", false},
		{"  hello:\n    tier: light\n", true},
		{"  hello:\n    tier: balanced\n", true},
		{"  hello:\n    tier: thinking\n    access: read\n", true},
		{"  hello:\n    tier: fastest\n", false},
		{"  hello:\n    tier: Thinking\n", false},
		{"  hello:\n    access: write\n    plan: change-plan\n", true},
		{"  hello:\n    access: write\n    plan: " + strings.Repeat("a", 64) + "\n", true},
		{"  hello:\n    access: write\n    plan: Change-Plan\n", false},
		{"  hello:\n    access: write\n    plan: a--b\n", false},
		{"  hello:\n    access: write\n    plan: " + strings.Repeat("a", 65) + "\n", false},
		{"  hello:\n    access: write\n    plan: \"\"\n", false},
		{"  hello:\n    access: write\n    plan: 3\n", false},
		{"  hello:\n    plan: change-plan\n", false},
		{"  hello:\n    access: read\n    plan: change-plan\n", false},
		{"  hello:\n    access: write-no-push\n    plan: change-plan\n", false},
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
	p, err := compose(t, defined(map[string]Workflow{"fix": {Access: AccessWrite}, "review": {}}))
	if err != nil {
		t.Fatal(err)
	}
	if p.AccessOf("fix") != AccessWrite || p.AccessOf("review") != AccessRead {
		t.Errorf("workflows %+v", p.Workflows)
	}
	for _, c := range []struct {
		w    Workflow
		want string
	}{
		{Workflow{Access: "admin"}, "to a valid level.\n" +
			"  The levels are read, write-no-push, and write.\n" +
			"  The value is \"admin\".\n  workflows.fix is in "},
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
	want := "Set workflows." + name + ".access to a valid level."
	if first != want || len(first) > 100 {
		t.Errorf("first line %q (%d characters), want %q within 100", first, len(first), want)
	}
	if len(WorkflowEntry(name)) != 64 {
		t.Errorf("the entry name has %d characters, want 64", len(WorkflowEntry(name)))
	}
}

// TestTheTierRefusalFirstLineFitsWithTheLongestName is a workflow named with 55
// characters and a tier that is no tier: the first line of the refusal has at most 100
// characters and starts with the fix.
func TestTheTierRefusalFirstLineFitsWithTheLongestName(t *testing.T) {
	isolate(t)
	name := "a" + strings.Repeat("b", 54)
	_, err := Load(write(t, withWorkflows("  "+name+":\n    tier: fastest\n")))
	if err == nil {
		t.Fatal("no refusal")
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	want := "Set workflows." + name + ".tier to a valid tier."
	if first != want || len(first) > 100 {
		t.Errorf("first line %q (%d characters), want %q within 100", first, len(first), want)
	}
}
