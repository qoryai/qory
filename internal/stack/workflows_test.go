package stack

import (
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

// TestLoadRefusesABrokenWorkflow refuses a workflow name outside the contract's form, a
// time_limit that is no duration or not above zero, and a key a workflow does not have.
func TestLoadRefusesABrokenWorkflow(t *testing.T) {
	isolate(t)
	for _, c := range []struct {
		body, want string
	}{
		{"  Hello: {}\n", `name the workflow "Hello" with a lower-case letter, then up to 63 lower-case letters, digits and hyphens: workflows lists it`},
		{"  hello_there: {}\n", `name the workflow "hello_there" with a lower-case letter, then up to 63 lower-case letters, digits and hyphens: workflows lists it`},
		{"  hello:\n    time_limit: 7 hours\n", `set workflows.hello.time_limit to a duration above zero, such as 7h or 45m: "7 hours" is not one`},
		{"  hello:\n    time_limit: 0s\n", `set workflows.hello.time_limit to a duration above zero, such as 7h or 45m: "0s" is not one`},
		{"  hello:\n    time_limit: -1h\n", `set workflows.hello.time_limit to a duration above zero, such as 7h or 45m: "-1h" is not one`},
		{"  hello:\n    prompt: x\n", `key "prompt" is not one qory-stack.yaml reads`},
	} {
		path := write(t, withWorkflows(c.body))
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.body, err, c.want)
		}
	}
}

// TestExtendTakesTheBaseWorkflows is a checkout's qory.yaml extending a stack: the
// stack that composes has the base's workflows.
func TestExtendTakesTheBaseWorkflows(t *testing.T) {
	base := &Stack{Modules: []Module{{Name: "core", Source: Source{Path: "modules/core"}}}, Extending: &Extending{},
		Workflows: map[string]Workflow{"hello": {TimeLimitText: "7h", TimeLimit: 7 * time.Hour}}, Root: "/repo", File: "/repo/qory-stack.yaml"}
	p := &Stack{Extends: Source{Path: "../base"}, File: "/app/qory-stack.yaml"}
	out, err := Extend(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if out.TimeLimitOf("hello") != 7*time.Hour {
		t.Errorf("workflows %+v", out.Workflows)
	}
}

// TestTheSchemaTakesTheWorkflows validates stacks with workflows against
// stack.schema.json: a time_limit in Go's duration form above zero passes, and a
// workflow name or a time_limit that Load refuses, a zero one among them, fails.
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
