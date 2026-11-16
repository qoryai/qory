package module

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/stack"
)

// TestReadManifestReadsARoleContract is a role and a group role written as maps with a
// contract: each reads as its fill, one or many, as the short form does, and the
// contract's path is in Contracts, keyed by role. A map without a contract adds none.
func TestReadManifestReadsARoleContract(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  agents:\n    developer: {fill: one, contract: ./roles/developer.yaml}\n    reviewer: {fill: one}\n    reviewers: {fill: many, contract: roles/reviewer.yaml}\n  skills:\n    lint: one\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := (stack.Roles{"agents": {"developer": "one", "reviewer": "one", "reviewers": "many"}, "skills": {"lint": "one"}}); !reflect.DeepEqual(m.Roles, want) {
		t.Errorf("roles = %v, want %v", m.Roles, want)
	}
	if want := map[string]string{"agents/developer": filepath.Join("roles", "developer.yaml"), "agents/reviewers": filepath.Join("roles", "reviewer.yaml")}; !reflect.DeepEqual(m.Contracts, want) {
		t.Errorf("contracts = %v, want %v", m.Contracts, want)
	}
	m, err = ReadManifest(tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n  agents:\n    reviewer: one\n"}))
	if err != nil || m.Contracts != nil {
		t.Errorf("a manifest without a contract: %v, %v", m.Contracts, err)
	}
}

// TestReadManifestRefusesABadRoleMap is each map form of a role qory does not read: another
// key, no fill, a fill other than one, a map for a skills or commands role, and a contract
// that is not a path inside the module. Each refusal starts with its fix and contains the
// manifest's path.
func TestReadManifestRefusesABadRoleMap(t *testing.T) {
	const head = "apiVersion: qory.dev/v1alpha1\nname: core\nroles:\n"
	for _, c := range []struct{ body, want string }{
		{"  agents:\n    developer: {fill: one, paths: [a]}\n", "Remove key paths from roles.agents.developer.\n  A role's map has fill and contract. The map is in {path}."},
		{"  agents:\n    developer: {contract: roles/developer.yaml}\n", "Add fill: one or fill: many to roles.agents.developer.\n  A role's map sets fill. The map is in {path}."},
		{"  agents:\n    developer: {fill: two}\n", "Set roles.agents.developer to one or many in {path}."},
		{"  skills:\n    lint: {fill: one, contract: roles/lint.yaml}\n", "Set roles.skills.lint to one.\n  Only an agents role is a map, to give it a contract. The role is in {path}."},
		{"  commands:\n    ship: {fill: one}\n", "Set roles.commands.ship to one.\n  Only an agents role is a map, to give it a contract. The role is in {path}."},
		{"  agents:\n    developer: {fill: one, contract: ../developer.yaml}\n", "Set roles.agents.developer.contract of the module to a path inside the module.\n  The value is \"../developer.yaml\". The map is in {path}."},
		{"  agents:\n    developer: {fill: one, contract: roles/../developer.yaml}\n", "Set roles.agents.developer.contract of the module to a path inside the module.\n  The value is \"roles/../developer.yaml\". The map is in {path}."},
		{"  agents:\n    developer: {fill: one, contract: /etc/developer.yaml}\n", "Set roles.agents.developer.contract of the module to a path inside the module.\n  The value is \"/etc/developer.yaml\". The map is in {path}."},
		{"  agents:\n    developer: {fill: one, contract: \"\"}\n", "Set roles.agents.developer.contract of the module to a path inside the module.\n  The value is empty or not a path. The map is in {path}."},
		{"  agents:\n    developer: {fill: one, contract: [a]}\n", "Set roles.agents.developer.contract of the module to a path inside the module.\n  The value is empty or not a path. The map is in {path}."},
		{"  agents:\n    qak_x: {fill: one, paths: [a]}\n", "Remove key paths from roles.agents.(a name that contains an access key secret).\n  A role's map has fill and contract. The map is in {path}."},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.body})
		_, err := ReadManifest(dir)
		want := strings.ReplaceAll(c.want, "{path}", filepath.Join(dir, ManifestName))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v\nwant %q", c.body, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "qak_") {
			t.Errorf("%q: the refusal contains the secret: %v", c.body, err)
		}
	}
}

// TestParseContractReadsEveryKey is a contract with every key, one with none, and {}.
func TestParseContractReadsEveryKey(t *testing.T) {
	c, err := ParseContract([]byte("sections: [Guardrails, The work]\nfront_matter: [name, model]\ntools: [Read, Bash]\ntier: true\nreport: {kind: list}\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Contract{Sections: []string{"Guardrails", "The work"}, FrontMatter: []string{"name", "model"}, Tools: []string{"Read", "Bash"}, Tier: true, Report: map[string]any{"kind": "list"}}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("contract = %+v, want %+v", c, want)
	}
	c, err = ParseContract([]byte("{}\n"))
	if err != nil || !reflect.DeepEqual(c, Contract{}) {
		t.Errorf("{}: %+v, %v", c, err)
	}
	c, err = ParseContract([]byte("report: {}\ntier: false\n"))
	if err != nil || c.Report == nil || c.Tier {
		t.Errorf("an empty report: %+v, %v", c, err)
	}
}

// TestParseContractRefusesWhatItDoesNotRead is each contract that does not read: the
// error's text, with the contract's place, is its fix and then its cause.
func TestParseContractRefusesWhatItDoesNotRead(t *testing.T) {
	const where = "roles/developer.yaml of module core"
	for _, c := range []struct{ body, want string }{
		{"", "Write roles/developer.yaml of module core as a role contract, a map.\n  The file is empty."},
		{"- a\n", "Write roles/developer.yaml of module core as a role contract, a map.\n  The file is a list."},
		{"sections: [a\n", "Write roles/developer.yaml of module core as a role contract, a map.\n  yaml: line 1: did not find expected ',' or ']'"},
		{"paths: [a]\n", "Remove key paths from roles/developer.yaml of module core.\n  A role contract has sections, front_matter, tools, tier and report, and paths is none of them."},
		{"tier: true\ntier: false\n", "Remove one of the two keys tier from roles/developer.yaml of module core.\n  A key is written once."},
		{"sections: Guardrails\n", "Write sections in roles/developer.yaml of module core as a list of names.\n  sections is a string."},
		{"tools:\n", "Write tools in roles/developer.yaml of module core as a list of names.\n  tools is empty."},
		{"sections: [Guardrails, 3]\n", "Write sections in roles/developer.yaml of module core as a list of names.\n  Item 2 of sections is a number."},
		{"front_matter: [\"\"]\n", "Write front_matter in roles/developer.yaml of module core as a list of names.\n  Item 1 of front_matter is empty."},
		{"tools: [\"Read\\nBash\"]\n", "Write tools in roles/developer.yaml of module core as a list of names.\n  Item 1 of tools has a line break."},
		{"sections: [\"## Guardrails\"]\n", "Write sections in roles/developer.yaml of module core as a list of names.\n  Item 1 of sections is \"## Guardrails\", and a section is the heading's text without ##."},
		{"tools: [Read, Bash, Read]\n", "Remove item 3 from tools in roles/developer.yaml of module core.\n  Item 3 repeats item 1, \"Read\"."},
		{"tier: \"yes\"\n", "Set tier in roles/developer.yaml of module core to true or false.\n  tier is \"yes\"."},
		{"tier: [true]\n", "Set tier in roles/developer.yaml of module core to true or false.\n  tier is a list."},
		{"report: []\n", "Write report in roles/developer.yaml of module core as a map.\n  report is a list."},
	} {
		_, err := ParseContract([]byte(c.body))
		var ce *ContractError
		if !errors.As(err, &ce) {
			t.Errorf("%q: %v, want a ContractError", c.body, err)
			continue
		}
		if got := ce.In(where); got != c.want {
			t.Errorf("%q:\n%s\nwant:\n%s", c.body, got, c.want)
		}
	}
}

// TestHeadings is the level-2 headings of a body: a closing sequence is removed, a
// heading of another level, one indented four spaces and one inside a fenced code block
// do not count, and a fence closes only with a run of the character that opened it.
func TestHeadings(t *testing.T) {
	body := strings.Join([]string{
		"## Guardrails",
		"### The work",
		"##The work",
		"   ## Stop conditions ##",
		"    ## Indented",
		"```markdown",
		"## In a fence",
		"~~~",
		"## Still in the fence",
		"```",
		"~~~~",
		"## In a tilde fence",
		"~~~~",
		"## Expected output #  ",
		"## C#",
		"##",
	}, "\n")
	want := []string{"Guardrails", "Stop conditions", "Expected output", "C#", ""}
	if got := Headings(body); !reflect.DeepEqual(got, want) {
		t.Errorf("headings = %q, want %q", got, want)
	}
}

// TestToolsOf is the tools key as a comma-separated string and as a list, each trimmed
// with the empty ones left out, and nil for no key or a key of another type.
func TestToolsOf(t *testing.T) {
	for _, c := range []struct {
		front map[string]any
		want  []string
	}{
		{map[string]any{"tools": "Read, Edit,Bash(git:*), "}, []string{"Read", "Edit", "Bash(git:*)"}},
		{map[string]any{"tools": []any{"Read", " Bash ", 3, ""}}, []string{"Read", "Bash"}},
		{map[string]any{}, nil},
		{map[string]any{"tools": 3}, nil},
	} {
		if got := ToolsOf(c.front); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: %q, want %q", c.front, got, c.want)
		}
	}
}
