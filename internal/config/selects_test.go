package config_test

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/gateway"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestTheGitHubRepositoryPatternNamesOneRepository is the pattern qory supplies for
// github.com, matched whole as Forager matches it: one owner/name, never a name of . or
// .., which GitHub forbids, and nothing longer than GitHub takes.
func TestTheGitHubRepositoryPatternNamesOneRepository(t *testing.T) {
	whole := regexp.MustCompile(`^(?:` + config.GitHubRepository + `)$`)
	for _, c := range []struct {
		value string
		match bool
	}{
		{"o/.", false},
		{"o/..", false},
		{"o/", false},
		{"o/a,b", false},
		{"o/a/b", false},
		{"o/" + strings.Repeat("a", 101), false},
		{"-o/a", false},
		{strings.Repeat("o", 40) + "/a", false},
		{"o/a", true},
		{"o/.a", true},
		{"o/..a", true},
		{"o/...", true},
		{"o/a.b", true},
		{"o/_", true},
		{"o/" + strings.Repeat("a", 100), true},
		{strings.Repeat("o", 39) + "/a", true},
		{"acme/shop", true},
	} {
		if got := whole.MatchString(c.value); got != c.match {
			t.Errorf("%q: matched %v, want %v", c.value, got, c.match)
		}
	}
}

// selectsCredentials defines model, which takes no argument, and github, which takes
// one, each with a value no refusal may print.
const selectsCredentials = `  credentials:
    model:
      env: MODEL_TOKEN_VALUE
      hosts: [api.model.example]
      auth: {scheme: bearer}
    github:
      adapter: [/opt/adapters/github-adapter, "${argument}"]
      argument: "[a-z]+/[a-z]+"
      hosts: [api.github.com]
`

// TestGatewaySelectsReads is gateway.selects.credentials read in the file's order, each
// entry as written, and gateway.selects among the gateway's keys. An entry without a
// name, a key an entry does not take and a value that is not a list are refused by
// their place, and no refusal holds a value.
func TestGatewaySelectsReads(t *testing.T) {
	hermetic(t)
	foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+selectsCredentials+"  selects:\n    credentials:\n      - {name: github, argument_label: repository, forge: github.com}\n      - {name: model}\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	want := []config.ForagerSelect{{Name: "github", ArgumentLabel: "repository", Forge: "github.com"}, {Name: "model"}}
	if !slices.Equal(r.Selects, want) {
		t.Errorf("read %+v, want %+v", r.Selects, want)
	}
	if !slices.Contains(r.GatewayKeys, "selects") {
		t.Errorf("the gateway's keys %v lack selects", r.GatewayKeys)
	}
	for _, c := range []struct{ body, want string }{
		{"  selects:\n    credentials:\n      - {name: model}\n      - {argument_label: secret-label-value}\n", "gateway.selects.credentials[1].name is required"},
		{"  selects:\n    credentials:\n      - {name: \"\"}\n", "gateway.selects.credentials[0].name is required"},
		{"  selects:\n    credentials:\n      - {name: github, pattern: secret-pattern-value}\n", `line 5: key "pattern" is not one forager.yaml reads`},
		{"  selects:\n    credentials: secret-list-value\n", "line 4: gateway.selects.credentials is not a list"},
		{"  selects:\n    labels: [secret-key-value]\n", `line 4: key "labels" is not one forager.yaml reads`},
	} {
		path := foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+c.body)
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+": "+c.want {
			t.Errorf("%q: error %v, want %q after the file", c.body, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "secret-") {
			t.Errorf("%q: the refusal holds a value: %v", c.body, err)
		}
	}
}

// TestSelectionsAreWhatForagerTakes is gateway.selects as Forager's gateway takes it:
// an entry for a credential that takes an argument with its label, its forge and the
// pattern qory supplies for the forge, and one for a credential that takes none by its
// name alone.
func TestSelectionsAreWhatForagerTakes(t *testing.T) {
	hermetic(t)
	foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+selectsCredentials+"  selects:\n    credentials:\n      - {name: github, argument_label: repository, forge: github.com}\n      - {name: model}\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Selections()
	if err != nil {
		t.Fatal(err)
	}
	want := []gateway.Selection{{Name: "github", Label: "repository", Forge: "github.com", Pattern: config.GitHubRepository}, {Name: "model"}}
	if !slices.Equal(got, want) {
		t.Errorf("selections %+v, want %+v", got, want)
	}
	foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+selectsCredentials)
	if r, err = config.LoadForager(); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Selections(); err != nil || got != nil {
		t.Errorf("without gateway.selects: %+v, %v", got, err)
	}
}

// TestSelectionsRefuseWhatTheyCannotSelect is each refusal of an entry of
// gateway.selects: a name no credential has, a credential that takes an argument
// without its label or its forge, one that takes none with either, and a forge whose
// repository form qory does not know. Each names the entry's name, and the forge alone
// of its values; none holds a label or a credential's value.
func TestSelectionsRefuseWhatTheyCannotSelect(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ entry, want string }{
		{"{name: tracker}", "forager.yaml: gateway.selects.credentials: tracker is no credential that gateway.credentials or gateway.integrations defines"},
		{"{name: github}", "forager.yaml: gateway.selects.credentials: github needs argument_label and forge"},
		{"{name: github, argument_label: secret-label-value}", "forager.yaml: gateway.selects.credentials: github needs argument_label and forge"},
		{"{name: github, forge: github.com}", "forager.yaml: gateway.selects.credentials: github needs argument_label and forge"},
		{"{name: model, argument_label: secret-label-value}", "forager.yaml: gateway.selects.credentials: model takes no argument, so argument_label and forge do not apply"},
		{"{name: model, forge: github.com}", "forager.yaml: gateway.selects.credentials: model takes no argument, so argument_label and forge do not apply"},
		{"{name: github, argument_label: secret-label-value, forge: gitlab.example}", "forager.yaml: gateway.selects.credentials: github: forge gitlab.example is not one qory knows the repository form of; use github.com"},
	} {
		foragerFile(t, "apiVersion: qory.dev/v1alpha1\ngateway:\n"+selectsCredentials+"  selects:\n    credentials:\n      - {name: model}\n      - "+c.entry+"\n")
		r, err := config.LoadForager()
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Selections()
		if err == nil || err.Error() != c.want || got != nil {
			t.Errorf("%s: %+v, %v, want %q", c.entry, got, err, c.want)
		}
		if err != nil {
			for _, value := range []string{"secret-label-value", "MODEL_TOKEN_VALUE", "api.model.example", "api.github.com", "github-adapter", "[a-z]+"} {
				if strings.Contains(err.Error(), value) {
					t.Errorf("%s: the refusal holds %q: %v", c.entry, value, err)
				}
			}
		}
	}
}

// TestTheForagerSchemaTakesTheSelects holds forager.schema.json to what the reader takes
// of gateway.selects: entries of a name, with argument_label and forge github.com, and
// no other key.
func TestTheForagerSchemaTakesTheSelects(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{"gateway: {selects: {credentials: [{name: github, argument_label: repository, forge: github.com}, {name: model}]}}\n", true},
		{"gateway: {selects: {credentials: []}}\n", true},
		{"gateway: {selects: {}}\n", true},
		{"gateway: {selects: {credentials: [{argument_label: repository}]}}\n", false},
		{"gateway: {selects: {credentials: [{name: \"\"}]}}\n", false},
		{"gateway: {selects: {credentials: [{name: github, pattern: \"[a-z]+\"}]}}\n", false},
		{"gateway: {selects: {credentials: [{name: github, argument_label: repository, forge: gitlab.example}]}}\n", false},
		{"gateway: {selects: {labels: [repository]}}\n", false},
		{"gateway: {selects: {credentials: github}}\n", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\n"+c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.body, err, c.valid)
		}
	}
}
