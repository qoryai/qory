package compose_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/module"
)

// TestSchemas validates every fixture document against the contract's schemas: a
// qory.yaml that composes passes config.schema.json, one refused for its apiVersion, a
// kind, its qory key, its workflows, its runtimes, its bind or a key it does not read
// fails it,
// every module manifest passes module.schema.json unless the fixture expects it refused,
// every MCP server file passes
// mcp.schema.json unless the fixture expects it refused, the stack a fixture extends,
// stack/qory-stack.yaml, passes stack.schema.json unless the fixture expects it refused,
// and every process template, modules/<name>/process.yaml, passes process.schema.json
// unless the fixture expects it refused for what the schema checks.
func TestSchemas(t *testing.T) {
	c := jsonschema.NewCompiler()
	composeSchema, err := c.Compile("../../contracts/harness/v1/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	stackSchema, err := c.Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	moduleSchema, err := c.Compile("../../contracts/harness/v1/module.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	mcpSchema, err := c.Compile("../../contracts/harness/v1/mcp.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	processSchema, err := c.Compile("../../contracts/harness/v1/process.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := filepath.Glob(filepath.Join(fixtures, "*"))
	for _, dir := range dirs {
		errText, _ := os.ReadFile(filepath.Join(dir, "expected", "error.txt"))
		// fix is the fix line of the first refusal; a later line names the file to edit.
		fix, _, _ := strings.Cut(string(errText), "\n")
		// A workflow hook of a kind its point does not take passes the schema, which
		// knows the kinds and not the base stack's points.
		badKind := strings.Contains(string(errText), "kind ") && !strings.Contains(string(errText), "hooks[")
		wantInvalid := strings.Contains(string(errText), "apiVersion") || badKind || strings.HasPrefix(fix, "Set qory to ") ||
			strings.Contains(string(errText), " in qory.yaml.") && (strings.Contains(fix, " workflows.") || strings.HasPrefix(fix, "Remove workflows ")) ||
			strings.Contains(string(errText), "bind.") && strings.Contains(string(errText), " in qory.yaml") && !strings.Contains(string(errText), " as many, ")
		// A runtime's pin that is not an exact version, or of a runtime qory does not
		// install, fails the schema of the file that sets it.
		pinRefused := strings.HasPrefix(fix, "Set runtimes.") || strings.HasSuffix(fix, " from runtimes.")
		wantInvalid = wantInvalid || pinRefused && strings.Contains(string(errText), " is in qory.yaml.")
		// A plan on a workflow whose access is not write is refused by the stack check, and
		// the schemas state the same rule for the document that has the plan.
		planOnNoWrite := strings.HasPrefix(fix, "Remove `plan` from ")
		wantInvalid = wantInvalid || planOnNoWrite && fileHas(filepath.Join(dir, "qory.yaml"), "plan:")
		// A key the file does not read is refused by the decoder, and the schema of that file
		// allows no such key either.
		wantInvalid = wantInvalid || fix == "Remove the key, or rename it to one qory.yaml reads."
		err := composeSchema.Validate(document(t, filepath.Join(dir, "qory.yaml")))
		if wantInvalid && err == nil {
			t.Errorf("%s: the document passed the schema; want a failure", dir)
		}
		if !wantInvalid && err != nil {
			t.Errorf("%s: the document failed the schema: %v", dir, err)
		}
		if f := filepath.Join(dir, "stack", "qory-stack.yaml"); fileExists(f) {
			wantBadStack := (strings.HasPrefix(fix, "Set workflows.") || pinRefused) && strings.Contains(string(errText), " is in stack/qory-stack.yaml.") ||
				planOnNoWrite && fileHas(f, "plan:") ||
				fix == "Remove the key, or rename it to one qory-stack.yaml reads." && strings.Contains(string(errText), " of stack/qory-stack.yaml.")
			err := stackSchema.Validate(document(t, f))
			if wantBadStack && err == nil {
				t.Errorf("%s passed the schema; want a failure", f)
			}
			if !wantBadStack && err != nil {
				t.Errorf("%s: %v", f, err)
			}
		}
		manifests, _ := filepath.Glob(filepath.Join(dir, "modules", "*", "qory-module.yaml"))
		more, _ := filepath.Glob(filepath.Join(dir, "stack", "modules", "*", "qory-module.yaml"))
		manifests = append(manifests, more...)
		for _, m := range manifests {
			// A manifest the compose refuses for its roles or provides is in the error
			// by its path in the fixture: after the fix, or, for a role's map, at the end
			// of the cause line.
			rel, _ := filepath.Rel(dir, m)
			wantBad := strings.Contains(string(errText), " in "+rel+":") || strings.Contains(string(errText), " is in "+rel+".") ||
				strings.Contains(string(errText), "scope_checks") && strings.Contains(string(errText), "The manifest is "+rel+".")
			err := moduleSchema.Validate(document(t, m))
			if wantBad && err == nil {
				t.Errorf("%s passed the schema; want a failure", m)
			}
			if !wantBad && err != nil {
				t.Errorf("%s: %v", m, err)
			}
		}
		// A process template refused for a stack that exports.stacks does not list, or for
		// a workflow its stack does not declare, passes the schema: the schema checks one
		// file, and those refusals compare two. One refused for an artifact type name
		// listed twice, a json_schema that does not compile, or a submit.type that no
		// structured entry of artifact_types has, passes it too: a JSON Schema cannot
		// state those rules.
		processes, _ := filepath.Glob(filepath.Join(dir, "modules", "*", "process.yaml"))
		wantBadProcess := strings.Contains(string(errText), "process.yaml") &&
			!strings.Contains(string(errText), "exports.stacks lists") && !strings.Contains(string(errText), " does not declare it under workflows") &&
			!strings.Contains(string(errText), " has the name ") && !strings.Contains(string(errText), " so that it compiles as a JSON Schema") &&
			!strings.Contains(string(errText), "Set submit.type to a structured type of artifact_types.")
		for _, f := range processes {
			err := processSchema.Validate(document(t, f))
			if wantBadProcess && err == nil {
				t.Errorf("%s passed the schema; want a failure", f)
			}
			if !wantBadProcess && err != nil {
				t.Errorf("%s: %v", f, err)
			}
		}
		servers, _ := filepath.Glob(filepath.Join(dir, "modules", "*", "mcp", "*.json"))
		// A refused server names its file, mcp/<name>.json; a collision or an exclude over an
		// mcp entry names mcp/<name> without the extension, and its files are valid.
		wantBadServer := strings.Contains(string(errText), "mcp/") && strings.Contains(string(errText), ".json")
		for _, f := range servers {
			err := mcpSchema.Validate(document(t, f))
			if wantBadServer && err == nil {
				t.Errorf("%s passed the schema; want a failure", f)
			}
			if !wantBadServer && err != nil {
				t.Errorf("%s: %v", f, err)
			}
		}
	}
}

// fileHas reports whether the file at path contains text.
func fileHas(path, text string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), text)
}

// TestMCPSchemaMatchesTheReader keeps the schema and module.Read in step on the server
// shapes: each document is accepted or refused by both.
func TestMCPSchemaMatchesTheReader(t *testing.T) {
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("../../contracts/harness/v1/mcp.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{`{"command": "python3", "args": ["srv.py"], "env": {"A": "1"}}`, true},
		{`{"type": "http", "url": "https://mcp.example.com", "headers": {"X": "y"}}`, true},
		{`{"command": "x", "url": "https://mcp.example.com"}`, false},
		{`{"args": ["x"]}`, false},
		{`{"comand": "x"}`, false},
		{`{}`, false},
		{`{"command": "x", "type": ""}`, false},
		{`{"url": ""}`, false},
		{`{"command": "x", "env": {"A": ""}}`, true},
		{`{"command": "x", "description": "why"}`, true},
		{`{"command": "x", "description": 3}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %s valid=%v err=%v", c.body, c.valid, err)
		}
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "mcp"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mcp", "s.json"), []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := module.Read("core", dir, nil, ""); (err == nil) != c.valid {
			t.Errorf("reader: %s valid=%v err=%v", c.body, c.valid, err)
		}
	}
}

// TestStackSchemaKnowsBothSourceForms validates the source forms no fixture composes: a
// git source needs its ref, and a path source takes no ref.
func TestStackSchemaKnowsBothSourceForms(t *testing.T) {
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		source string
		valid  bool
	}{
		{`{"path": "modules/core"}`, true},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1"}`, true},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1", "path": "modules/core"}`, true},
		{`{"git": "https://git.example.com/acme/harness"}`, false},
		{`{"path": "modules/core", "ref": "v1"}`, false},
		{`{}`, false},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1", "module": "core"}`, true},
		{`{"path": "../harness", "module": "core"}`, true},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1", "path": "modules/core", "module": "core"}`, false},
		{`{"module": "core"}`, false},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1", "module": "a/b"}`, false},
		{`{"git": "https://git.example.com/acme/harness", "ref": "v1", "stack": "nextjs"}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(`{"apiVersion": "qory.dev/v1alpha1", "modules": [{"name": "core", "source": `+c.source+`}]}`), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("source %s: valid=%v, err=%v", c.source, c.valid, err)
		}
	}
}

// TestComposeSchemaKnowsExtends validates the shapes no fixture composes: a qory.yaml
// whose harness section extends a stack, one with a target beside extends, one with an
// extending block, one with modules and no extends, whose base compose -f names, one
// with extensions alone, one with its own target and modules, a worktree name
// without {branch}, worktree paths in both forms, a stack with
// extends, a stack with a target, a stack with an extending block, a module entry by
// name alone, a base and a module named as exports, and the exports section in each of
// its forms.
func TestComposeSchemaKnowsExtends(t *testing.T) {
	c := jsonschema.NewCompiler()
	composeSchema, err := c.Compile("../../contracts/harness/v1/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	stackSchema, err := c.Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		schema *jsonschema.Schema
		body   string
		valid  bool
	}{
		{composeSchema, `{"harness": {"extends": {"git": "https://git.example.com/acme/harness", "ref": "main", "path": "nextjs-15"}, "modules": [{"name": "app"}]}}`, true},
		{composeSchema, `{"harness": {"runtime": "codex", "extends": {"path": "../harness/nextjs-15"}, "modules": [{"name": "app"}]}, "worktree": {"base": "main", "link": [".env"], "run": {"add": ["pnpm install"]}}}`, true},
		{composeSchema, `{"harness": {"extends": {"path": "../harness/nextjs-15"}, "target": {"runtime": "claude"}, "modules": [{"name": "app"}]}}`, true},
		{composeSchema, `{"harness": {"extends": {"path": "../harness/nextjs-15"}, "target": {"runtime": "claude", "model": "opus"}}}`, true},
		{composeSchema, `{"harness": {"extends": {"path": "../harness/nextjs-15"}, "extending": {"kinds": ["skills"]}, "modules": [{"name": "app"}]}}`, false},
		{composeSchema, `{"harness": {"modules": [{"name": "app"}]}}`, true},
		{composeSchema, `{"harness": {"extensions": {"acme": {"team": "web"}}}}`, true},
		{composeSchema, `{"harness": {"extensions": {"sweep_floor": 40, "corpus_roots": ["scripts"], "resolve_ci": {"watched": {"workflows": ["Lint"]}}}}}`, true},
		{composeSchema, `{"harness": {"extensions": ["scripts"]}}`, false},
		{composeSchema, `{"harness": {"target": {"runtime": ["claude", "codex"], "model": "opus"}, "modules": [{"name": "app"}]}, "worktree": {"link": [".env"]}}`, true},
		{composeSchema, `{"worktree": {"name": "wt"}}`, false},
		{composeSchema, `{"worktree": {"link": [".env", {"from": "~/secrets/app.env", "to": ".env.local"}, {"from": "config/dev.json", "to": "config/local.json"}], "copy": [{"from": "config/dev.json"}]}}`, true},
		{composeSchema, `{"worktree": {"branch": "keep"}}`, true},
		{composeSchema, `{"worktree": {"branch": "drop"}}`, false},
		{composeSchema, `{"worktree": {"link": [{"from": "/etc/hosts"}]}}`, false},
		{composeSchema, `{"worktree": {"link": [{"from": "~/secrets/app.env"}]}}`, false},
		{composeSchema, `{"worktree": {"copy": [{"to": ".env"}]}}`, false},
		{composeSchema, `{"worktree": {"copy": [{"from": ".env", "into": "x"}]}}`, false},
		{stackSchema, `{"extends": {"path": "../harness/nextjs-15"}, "modules": [{"name": "app"}]}`, false},
		{stackSchema, `{"modules": [{"name": "app"}]}`, true},
		{stackSchema, `{"target": {"runtime": "claude"}, "modules": [{"name": "app"}]}`, false},
		{stackSchema, `{"modules": [{"name": "core"}], "extending": {"kinds": ["skills"], "instructions": true, "settings": ["permissions.allow"], "files": ["claude/rules/"]}}`, true},
		{stackSchema, `{"modules": [{"name": "core"}], "extending": {"kinds": ["hooks"]}}`, false},
		{stackSchema, `{"modules": [{"exclude": {"skills": ["x"]}}]}`, false},
		{composeSchema, `{"harness": {"extends": {"git": "https://git.example.com/acme/harness", "ref": "main", "stack": "nextjs"}, "modules": [{"name": "app"}, {"name": "extra", "source": {"git": "https://git.example.com/acme/harness", "ref": "main", "module": "extra"}}]}}`, true},
		{composeSchema, `{"harness": {"extends": {"path": "../harness", "stack": "nextjs"}, "modules": [{"name": "app"}]}}`, true},
		{composeSchema, `{"harness": {"extends": {"git": "https://git.example.com/acme/harness", "ref": "main", "path": "nextjs", "stack": "nextjs"}, "modules": [{"name": "app"}]}}`, false},
		{composeSchema, `{"harness": {"extends": {"git": "https://git.example.com/acme/harness", "ref": "main", "module": "core"}, "modules": [{"name": "app"}]}}`, false},
		{composeSchema, `{"exports": {"stacks": ["nextjs"], "modules": ["core", "nextjs"]}}`, true},
		{composeSchema, `{"exports": {"dir": "./harness", "stacks": ["nextjs"]}}`, true},
		{composeSchema, `{"exports": {"dir": {"stacks": "./stacks", "modules": "./lib/modules"}, "modules": ["core"]}}`, true},
		{composeSchema, `{"exports": {"dir": "./harness"}}`, true},
		{composeSchema, `{"exports": {}}`, false},
		{composeSchema, `{"exports": {"dir": {"stacks": "./stacks"}, "modules": ["core"]}}`, false},
		{composeSchema, `{"exports": {"modules": ["a/b"]}}`, false},
		{composeSchema, `{"exports": {"modules": ["core", "core"]}}`, false},
		{composeSchema, `{"exports": {"stack": ["nextjs"]}}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(`{"apiVersion": "qory.dev/v1alpha1", `+c.body[1:]), &doc); err != nil {
			t.Fatal(err)
		}
		if err := c.schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v, err=%v", c.body, c.valid, err)
		}
	}
}

// fileExists reports whether path is there.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func document(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	// Round-trip through JSON so the value holds JSON types.
	j, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(j, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestModuleSchemaTakesTheHostForagerTakes holds a declared host to the form Forager
// holds a policy's hosts to: a name, a *. suffix, or an IPv4 address in canonical form.
// A host whose last label is all digits, or 0x and hex digits, is no name.
func TestModuleSchemaTakesTheHostForagerTakes(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/module.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		host  string
		valid bool
	}{
		{"api.example.com", true},
		{"*.example.com", true},
		{"10.0.0.1", true},
		{"0x7f.example", true},
		{"*.example.123", true},
		{"api.example.123", false},
		{"127.1", false},
		{"443", false},
		{"0x1f", false},
		{"example.0x", false},
		{"10.0.0.01", false},
		{"256.1.1.1", false},
		{"api.example.com:443", false},
	} {
		doc := map[string]any{"apiVersion": "qory.dev/v1alpha1", "name": "core", "egress": []any{c.host}}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.host, err, c.valid)
		}
	}
}

// TestModuleSchemaMatchesTheReaderOnPushChecks keeps the schema and module.ReadManifest in
// step on push_checks: each item passes both or fails both. The one rule the schema
// cannot state, one type twice in one module, is the reader's alone.
func TestModuleSchemaMatchesTheReaderOnPushChecks(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/module.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		items string
		valid bool
	}{
		{`[]`, true},
		{`[{"type": "spec", "command": ["skills/run/checks.js", "push", "spec"]}]`, true},
		{`[{"type": "spec", "command": ["checks.js"]}, {"type": "review-comments", "command": ["./checks.js", ""]}]`, true},
		{`[{"type": "spec", "command": ["c.js", "--dir", "../x"]}]`, true},
		{`[{"type": "decision-record", "command": ["c.js"], "reads": ["acceptance-criteria", "questions"]}]`, true},
		{`[{"type": "spec", "command": ["c.js"], "reads": []}]`, true},
		{`[{"type": "spec", "command": ["c.js"], "reads": ["Questions"]}]`, false},
		{`[{"type": "spec", "command": ["c.js"], "reads": ["questions", "questions"]}]`, false},
		{`[{"type": "spec", "command": ["c.js"], "reads": [3]}]`, false},
		{`[{"type": "spec", "command": ["c.js"], "reads": "questions"}]`, false},
		{`[{"type": "Spec", "command": ["c.js"]}]`, false},
		{`[{"type": "spec-", "command": ["c.js"]}]`, false},
		{`[{"type": "", "command": ["c.js"]}]`, false},
		{`[{"type": "` + strings.Repeat("a", 65) + `", "command": ["c.js"]}]`, false},
		{`[{"command": ["c.js"]}]`, false},
		{`[{"type": "spec"}]`, false},
		{`[{"type": "spec", "command": []}]`, false},
		{`[{"type": "spec", "command": [""]}]`, false},
		{`[{"type": "spec", "command": ["/usr/bin/node"]}]`, false},
		{`[{"type": "spec", "command": ["../c.js"]}]`, false},
		{`[{"type": "spec", "command": ["a/../c.js"]}]`, false},
		{`[{"type": "spec", "command": ["a/.."]}]`, false},
		{`[{"type": "spec", "command": ["c.js", 3]}]`, false},
		{`[{"type": "spec", "command": "c.js"}]`, false},
		{`[{"type": "spec", "command": ["c.js"], "timeout": 5}]`, false},
		{`{"spec": ["c.js"]}`, false},
	} {
		var items any
		if err := json.Unmarshal([]byte(c.items), &items); err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{"apiVersion": "qory.dev/v1alpha1", "name": "core", "push_checks": items}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %s valid=%v err=%v", c.items, c.valid, err)
		}
		dir := t.TempDir()
		body, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, module.ManifestName), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := module.ReadManifest(dir); (err == nil) != c.valid {
			t.Errorf("reader: %s valid=%v err=%v", c.items, c.valid, err)
		}
	}
}

// TestModuleSchemaMatchesTheReaderOnSubmitProgram keeps the schema and module.ReadManifest
// in step on submit_program: each value passes both or fails both.
func TestModuleSchemaMatchesTheReaderOnSubmitProgram(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/module.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		value string
		valid bool
	}{
		{`{"command": ["bin/open-draft"]}`, true},
		{`{"command": ["./bin/open-draft", "--strict", ""]}`, true},
		{`{"command": ["c.js", "--dir", "../x"]}`, true},
		{`{"command": []}`, false},
		{`{"command": [""]}`, false},
		{`{"command": ["/usr/bin/node"]}`, false},
		{`{"command": ["../c.js"]}`, false},
		{`{"command": ["a/../c.js"]}`, false},
		{`{"command": ["c.js", 3]}`, false},
		{`{"command": "c.js"}`, false},
		{`{}`, false},
		{`{"command": ["c.js"], "timeout": 5}`, false},
		{`["c.js"]`, false},
	} {
		var value any
		if err := json.Unmarshal([]byte(c.value), &value); err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{"apiVersion": "qory.dev/v1alpha1", "name": "core", "submit_program": value}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %s valid=%v err=%v", c.value, c.valid, err)
		}
		dir := t.TempDir()
		body, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, module.ManifestName), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := module.ReadManifest(dir); (err == nil) != c.valid {
			t.Errorf("reader: %s valid=%v err=%v", c.value, c.valid, err)
		}
	}
}

// TestModuleSchemaMatchesTheReaderOnScopeChecks keeps the schema and module.ReadManifest in
// step on scope_checks: each value passes both or fails both. The one rule the schema
// cannot state, one type twice in one module, is the reader's alone, since a JSON object
// has each key once.
func TestModuleSchemaMatchesTheReaderOnScopeChecks(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/module.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		value string
		valid bool
	}{
		{`{}`, true},
		{`{"build-plan": ["checks.js", "scope", "--plan"]}`, true},
		{`{"build-plan": ["checks.js"], "change-plan": ["./bin/check", ""]}`, true},
		{`{"` + strings.Repeat("a", 64) + `": ["c.js"]}`, true},
		{`{"build-plan": ["c.js", "--dir", "../x"]}`, true},
		{`{"Build-Plan": ["c.js"]}`, false},
		{`{"build_plan": ["c.js"]}`, false},
		{`{"": ["c.js"]}`, false},
		{`{"` + strings.Repeat("a", 65) + `": ["c.js"]}`, false},
		{`{"build-plan": []}`, false},
		{`{"build-plan": [""]}`, false},
		{`{"build-plan": ["/usr/bin/node"]}`, false},
		{`{"build-plan": ["../c.js"]}`, false},
		{`{"build-plan": ["a/../c.js"]}`, false},
		{`{"build-plan": ["c.js", 3]}`, false},
		{`{"build-plan": [["c.js"]]}`, false},
		{`{"build-plan": [["c.js"], ["d.js"]]}`, false},
		{`{"build-plan": ["c.js"], "x": "y"}`, false},
		{`{"build-plan": "c.js scope"}`, false},
		{`{"build-plan": null}`, false},
		{`["c.js"]`, false},
		{`"build-plan"`, false},
	} {
		var value any
		if err := json.Unmarshal([]byte(c.value), &value); err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{"apiVersion": "qory.dev/v1alpha1", "name": "core", "scope_checks": value}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("schema: %s valid=%v err=%v", c.value, c.valid, err)
		}
		dir := t.TempDir()
		body, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, module.ManifestName), body, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := module.ReadManifest(dir); (err == nil) != c.valid {
			t.Errorf("reader: %s valid=%v err=%v", c.value, c.valid, err)
		}
	}
}

// TestConfigSchemaKnowsTiers takes harness.tiers in both entry forms, with or beside a
// document, and refuses a name that is not a tier, an empty model, an entry without a
// model and a key the entry does not have. A stack has no tiers.
func TestConfigSchemaKnowsTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	c := jsonschema.NewCompiler()
	composeSchema, err := c.Compile("../../contracts/harness/v1/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	stackSchema, err := c.Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		schema *jsonschema.Schema
		body   string
		valid  bool
	}{
		{composeSchema, `{"harness": {"tiers": {"light": "claude-haiku-5-5", "balanced": "claude-sonnet-5-5", "thinking": {"model": "claude-opus-5-5", "effort": "max"}}}}`, true},
		{composeSchema, `{"harness": {"extends": {"path": "../harness/nextjs-15"}, "tiers": {"thinking": {"model": "claude-opus-5-5"}}}}`, true},
		{composeSchema, `{"harness": {"tiers": {"heavy": "claude-haiku-5-5"}}}`, false},
		{composeSchema, `{"harness": {"tiers": {"light": ""}}}`, false},
		{composeSchema, `{"harness": {"tiers": {"light": {"effort": "max"}}}}`, false},
		{composeSchema, `{"harness": {"tiers": {"light": {"model": "a", "effort": ""}}}}`, false},
		{composeSchema, `{"harness": {"effort": "high"}}`, true},
		{composeSchema, `{"harness": {"effort": ""}}`, true},
		{composeSchema, `{"harness": {"effort": 3}}`, false},
		{composeSchema, `{"harness": {"tiers": {"light": {"model": "a", "extra": "b"}}}}`, false},
		{composeSchema, `{"harness": {"tiers": {"light": 5}}}`, false},
		{stackSchema, `{"tiers": {"light": "claude-haiku-5-5"}, "modules": [{"name": "app"}]}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(`{"apiVersion": "qory.dev/v1alpha1", `+c.body[1:]), &doc); err != nil {
			t.Fatal(err)
		}
		if err := c.schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v, err=%v", c.body, c.valid, err)
		}
	}
}

// TestSchemasKnowTheThreeWorkflowAccessLevels is the config schema and the stack schema
// with a workflow of each access level: read, write-no-push and write pass, and any other
// value, such as write_no_push or admin, fails.
func TestSchemasKnowTheThreeWorkflowAccessLevels(t *testing.T) {
	c := jsonschema.NewCompiler()
	composeSchema, err := c.Compile("../../contracts/harness/v1/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	stackSchema, err := c.Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		access string
		valid  bool
	}{
		{"read", true}, {"write-no-push", true}, {"write", true},
		{"write_no_push", false}, {"push", false}, {"admin", false}, {"", false},
	} {
		in := `"workflows": {"fix": {"access": "` + c.access + `"}}`
		for name, schema := range map[string]struct {
			schema *jsonschema.Schema
			body   string
		}{
			"config": {composeSchema, `{"apiVersion": "qory.dev/v1alpha1", ` +
				`"harness": {"target": {"runtime": "claude"}, ` +
				`"modules": [{"name": "app"}], ` + in + `}}`},
			"stack": {stackSchema, `{"apiVersion": "qory.dev/v1alpha1", ` +
				`"modules": [{"name": "app"}], ` + in + `}`},
		} {
			var doc any
			if err := json.Unmarshal([]byte(schema.body), &doc); err != nil {
				t.Fatal(err)
			}
			if err := schema.schema.Validate(doc); (err == nil) != c.valid {
				t.Errorf("%s schema, access %q: %v, want valid %t", name, c.access, err, c.valid)
			}
		}
	}
}

// TestSchemasKnowWorkflowHooks takes harness.hooks with a program or an agent, and hook
// points and extending.hooks in a stack, and refuses a hook with both or neither, a
// review with a program, an unknown kind or key, a point outside end, start and inside,
// a point with no kind, and limits below 1.
func TestSchemasKnowWorkflowHooks(t *testing.T) {
	c := jsonschema.NewCompiler()
	composeSchema, err := c.Compile("../../contracts/harness/v1/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	stackSchema, err := c.Compile("../../contracts/harness/v1/stack.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	const point = `"modules": [{"name": "core"}], "workflows": {"review-changes": {"hook_points": {"after-review-round": `
	for _, c := range []struct {
		schema *jsonschema.Schema
		body   string
		valid  bool
	}{
		{composeSchema, `{"harness": {"extends": {"path": "../base"}, "hooks": [{"at": "after-review-round", "kind": "check", "run": "scripts/check-licences.sh --all", "timeout": "5m"}, {"at": "after-review-round", "kind": "review", "agent": "licence-reviewer", "name": "licences", "required": false}]}}`, true},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "x.sh", "agent": "a"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "review", "run": "x.sh"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "lint", "run": "x.sh"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "x.sh", "when": "always"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "x.sh", "timeout": "0s"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "x.sh", "timeout": "500ms"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "", "agent": "a"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": null, "agent": "a"}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "x.sh", "required": null}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "x.sh", "name": ""}]}}`, false},
		{composeSchema, `{"harness": {"hooks": [{"at": "after-plan", "kind": "check", "run": "x.sh", "timeout": ""}]}}`, false},
		{stackSchema, `{` + point + `{"at": "end", "kinds": ["check", "review"]}}}}, "extending": {"hooks": {"per_point": 3, "per_workflow": 10, "timeout": "10m"}}}`, true},
		{stackSchema, `{` + point + `{"at": "end", "kinds": ["check"]}}}}, "extending": {"hooks": {}}}`, true},
		{stackSchema, `{` + point + `{"at": "middle", "kinds": ["check"]}}}}}`, false},
		{stackSchema, `{` + point + `{"at": "end", "kinds": []}}}}}`, false},
		{stackSchema, `{` + point + `{"at": "end", "kinds": ["lint"]}}}}}`, false},
		{stackSchema, `{"modules": [{"name": "core"}], "extending": {"hooks": {"per_point": 0}}}`, false},
		{stackSchema, `{"modules": [{"name": "core"}], "extending": {"hooks": {"timeout": "500ms"}}}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(`{"apiVersion": "qory.dev/v1alpha1", `+c.body[1:]), &doc); err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if err := c.schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v, err=%v", c.body, c.valid, err)
		}
	}
}

// questionsDocument is a document of questions.schema.json that passes: a question
// answered with an option, with an earlier answer of a note alone that a run reopened,
// an open question that follows from it, and a question that a run decided.
const questionsDocument = `{"questions": [
 {"id": "q1", "question": "Which database does the service use?",
  "context": "The service reads ` + "`DATABASE_URL`" + `.\n\n- one\n- two",
  "about": {"work_item": true},
  "options": [{"title": "PostgreSQL", "explanation": "The database of the other services."}],
  "state": "answered", "asked": {"run": 1, "at": "2026-11-04T10:00:00Z"},
  "answer": {"option": "PostgreSQL", "by": {"name": "Sam Example"}, "at": "2026-11-04T11:00:00Z"},
  "history": [{"option": null, "note": "Not\nsure", "by": {"name": "Sam Example"}, "at": "2026-11-04T10:30:00Z", "reopened": {"run": 2, "at": "2026-11-04T10:45:00Z"}}]},
 {"id": "q-2", "question": "Which port does it listen on?",
  "about": {"type": "acceptance-criteria", "name": "main", "item": "ac-3"}, "options": [],
  "state": "open", "asked": {"run": 2, "at": "2026-11-04T10:45:00Z"}, "follows": ["q1"]},
 {"id": "q3", "question": "Which cache does the service use?",
  "about": {"type": "proposal", "name": "main"},
  "options": [{"title": "Redis", "explanation": "The cache of the other services."}, {"title": "None", "explanation": "Every read goes to the database."}],
  "state": "decided", "asked": {"run": 2, "at": "2026-11-04T10:50:00Z"},
  "decision": {"option": "Redis", "reason": "The other services use Redis.", "run": 2, "at": "2026-11-04T10:51:00Z"}}
]}`

// TestQuestionsSchema is each document that questions.schema.json passes or fails.
func TestQuestionsSchema(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/questions.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string {
		if !strings.Contains(questionsDocument, from) {
			t.Fatalf("the document has no %s", from)
		}
		return strings.Replace(questionsDocument, from, to, 1)
	}
	option := `{"title": "PostgreSQL", "explanation": "The database of the other services."}`
	recommended := func(value string) string {
		return strings.Replace(option, `}`, `, "recommended": `+value+`}`, 1)
	}
	options := func(n int) string {
		return replace(option, strings.TrimSuffix(strings.Repeat(option+", ", n), ", "))
	}
	question := "Which database does the service use?"
	// proposalOn gives the first option of doc the member proposal, and withItem gives the
	// first question the member about, an entry of the output proposal/main.
	proposalOn := func(doc, value string) string {
		return strings.Replace(doc, option, strings.Replace(option, `}`, `, "proposal": `+value+`}`, 1), 1)
	}
	const workItem = `"about": {"work_item": true}`
	withItemOf := func(typ, item string) string {
		return replace(workItem, `"about": {"type": "`+typ+`", "name": "main", "item": `+item+`}`)
	}
	withItem := func(item string) string { return withItemOf("proposal", item) }
	withCriterion := func(item string) string { return withItemOf("acceptance-criteria", item) }
	// answerStart and historyStart start the answer and the history entry of the document.
	const (
		answerStart  = `"answer": {"option": "PostgreSQL"`
		historyStart = `"history": [{"option": null, "note": "Not\nsure"`
	)
	// decided is the state of the decided question, decisionStart starts its decision, and
	// redis is its first option.
	const (
		decided       = `"state": "decided"`
		decisionStart = `"decision": {"option": "Redis"`
		redis         = `{"title": "Redis", "explanation": "The cache of the other services."`
	)
	// reason and decisionRun are members of the decision, and answer is a member's answer
	// to the decided question.
	const (
		reason      = `"reason": "The other services use Redis.", `
		decisionRun = `"run": 2, "at": "2026-11-04T10:51:00Z"`
		answer      = `"answer": {"option": "None", "by": {"name": "Kim Example"}, "at": "2026-11-04T12:00:00Z"}, `
	)
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{questionsDocument, true},
		{`{"questions": []}`, true},
		{`{}`, false},
		{`[]`, false},
		{`{"questions": {}}`, false},
		// answer is present exactly when state is answered.
		{replace(`"state": "answered"`, `"state": "open"`), false},
		{replace(`"state": "answered"`, `"state": "withdrawn"`), false},
		{replace(`"state": "open"`, `"state": "answered"`), false},
		{replace(`"state": "open"`, `"state": "withdrawn"`), true},
		{replace(`"state": "open"`, `"state": "closed"`), false},
		{replace(`"state": "open", `, ``), false},
		// decision is present when state is decided, stays when the state changes after
		// that, and a decided question has at least one option and none with recommended.
		{replace(`"options": [{"title": "Redis", "explanation": "The cache of the other services."}, {"title": "None", "explanation": "Every read goes to the database."}]`, `"options": []`), false},
		{replace(`, {"title": "None", "explanation": "Every read goes to the database."}`, ``), true},
		{replace(decided, `"state": "withdrawn"`), true},
		{replace(decided+`, `, `"state": "answered", `+answer), true},
		{replace(decided, `"state": "open"`), true},
		{strings.Replace(replace(decided+`, `, `"state": "answered", `+answer), redis, redis+`, "recommended": true`, 1), true},
		{replace(redis, redis+`, "recommended": true`), false},
		{replace(redis, redis+`, "recommended": false`), false},
		{replace(",\n  "+decisionStart+`, `+reason+decisionRun+`}`, ``), false},
		{replace(`"state": "open"`, decided), false},
		{replace(decided+`, `, decided+`, `+answer), false},
		// decision is an object; each member is required and has the limits of its
		// definition. An option title the question does not have passes: the schema
		// cannot compare the two.
		{replace(`"decision": {"option": "Redis", "reason": "The other services use Redis.", "run": 2, "at": "2026-11-04T10:51:00Z"}`, `"decision": "Redis"`), false},
		{replace(`"decision": {"option": "Redis", "reason": "The other services use Redis.", "run": 2, "at": "2026-11-04T10:51:00Z"}`, `"decision": null`), false},
		{replace(decisionStart, `"decision": {"option": "MySQL"`), true},
		{replace(decisionStart+`, `, `"decision": {`), false},
		{replace(reason, ``), false},
		{replace(`, `+decisionRun, `, "at": "2026-11-04T10:51:00Z"`), false},
		{replace(decisionRun, `"run": 2`), false},
		{replace(decisionStart, `"decision": {"option": ""`), false},
		{replace(decisionStart, `"decision": {"option": null`), false},
		{replace(decisionStart, `"decision": {"option": "`+strings.Repeat("a", 200)+`"`), true},
		{replace(decisionStart, `"decision": {"option": "`+strings.Repeat("a", 201)+`"`), false},
		{replace(decisionStart, `"decision": {"option": "Re\ndis"`), false},
		{replace(reason, `"reason": "", `), false},
		{replace(reason, `"reason": "`+strings.Repeat("a", 2000)+`", `), true},
		{replace(reason, `"reason": "`+strings.Repeat("a", 2001)+`", `), false},
		{replace(reason, `"reason": "One.\nTwo.", `), true},
		{replace(reason, `"reason": "\u0007", `), false},
		{replace(decisionRun, `"run": 0, "at": "2026-11-04T10:51:00Z"`), false},
		{replace(decisionRun, `"run": 2.5, "at": "2026-11-04T10:51:00Z"`), false},
		{replace(decisionRun, `"run": 2, "at": "2026-11-04T10:51:00+01:00"`), false},
		// id
		{replace(`"id": "q-2"`, `"id": "`+strings.Repeat("a", 32)+`"`), true},
		{replace(`"id": "q-2"`, `"id": "`+strings.Repeat("a", 33)+`"`), false},
		{replace(`"id": "q-2"`, `"id": "Q2"`), false},
		{replace(`"id": "q-2"`, `"id": "q_2"`), false},
		{replace(`"id": "q-2"`, `"id": ""`), false},
		{replace(`"id": "q-2"`, `"id": 2`), false},
		{replace(`"id": "q-2", `, ``), false},
		// question
		{replace(question, strings.Repeat("a", 500)), true},
		{replace(question, strings.Repeat("a", 501)), false},
		{replace(question, strings.Repeat("ä", 500)), true},
		{replace(question, ""), false},
		{replace(question, `Which\ndatabase?`), false},
		{replace(question, `Which\tdatabase?`), false},
		{replace(question, `Which \u202edatabase?`), false},
		// context
		{replace(`"context": "The service`, `"context": "`+strings.Repeat("a", 20000-len("The service reads `DATABASE_URL`.\n\n- one\n- two"))+`The service`), true},
		{replace(`"context": "The service`, `"context": "`+strings.Repeat("a", 20001-len("The service reads `DATABASE_URL`.\n\n- one\n- two"))+`The service`), false},
		{replace("The service reads `DATABASE_URL`.", `\u0007`), false},
		{replace("The service reads `DATABASE_URL`.\\n\\n- one\\n- two", ""), false},
		// options
		{options(4), true},
		{options(5), false},
		{replace(`"options": [],`, ``), false},
		{replace(option, ``), true},
		// recommended is a boolean, and true on at most one option of a question.
		{replace(option, recommended(`true`)), true},
		{replace(option, recommended(`false`)), true},
		{replace(option, recommended(`true`)+`, `+recommended(`false`)), true},
		{replace(option, recommended(`false`)+`, `+recommended(`false`)), true},
		{replace(option, recommended(`true`)+`, `+option), true},
		{replace(option, recommended(`true`)+`, `+recommended(`true`)), false},
		{replace(option, recommended(`"true"`)), false},
		{replace(option, recommended(`null`)), false},
		{strings.Replace(replace(`"options": []`, `"options": [`+recommended(`true`)+`]`), option, recommended(`true`), 1), true},
		{replace(`"title": "PostgreSQL"`, `"title": "`+strings.Repeat("a", 200)+`"`), true},
		{replace(`"title": "PostgreSQL"`, `"title": "`+strings.Repeat("a", 201)+`"`), false},
		{replace(`"title": "PostgreSQL"`, `"title": ""`), false},
		{replace(`"title": "PostgreSQL"`, `"title": "Postgre SQL"`), true},
		{replace(`"title": "PostgreSQL"`, `"title": "Postgre\nSQL"`), false},
		{replace(`"title": "PostgreSQL"`, `"title": "Postgre\tSQL"`), false},
		{replace(answerStart, `"answer": {"option": "Postgre\nSQL"`), false},
		{replace(`"explanation": "The database of the other services."`, `"explanation": "`+strings.Repeat("a", 2000)+`"`), true},
		{replace(`"explanation": "The database of the other services."`, `"explanation": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(`, "explanation": "The database of the other services."`, ``), false},
		// asked
		{replace(`"asked": {"run": 1, `, `"asked": {"run": 0, `), false},
		{replace(`"asked": {"run": 1, `, `"asked": {"run": 1.5, `), false},
		{replace(`"asked": {"run": 1, `, `"asked": {`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-04T10:00:00+01:00"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-04T10:00:00.5Z"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-04T24:00:00Z"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-04t10:00:00z"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-12-31T10:00:00Z"`), true},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-01-01T10:00:00Z"`), true},
		{replace(`"2026-11-04T10:00:00Z"`, `"2028-02-29T10:00:00Z"`), true},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-13-04T10:00:00Z"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-00-04T10:00:00Z"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-32T10:00:00Z"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-00T10:00:00Z"`), false},
		{replace(`, "asked": {"run": 2, "at": "2026-11-04T10:45:00Z"}`, ``), false},
		// answer
		{replace(answerStart, `"answer": {"option": "PostgreSQL", "note": "Use the **shared** one."`), true},
		{replace(answerStart, `"answer": {"option": null, "note": "We use SQLite."`), true},
		{replace(answerStart, `"answer": {"option": null`), false},
		{replace(answerStart, `"answer": {"option": "PostgreSQL", "custom": false`), false},
		{replace(answerStart, `"answer": {"option": "PostgreSQL", "text": "PostgreSQL"`), false},
		{replace(answerStart, `"answer": {"option": "PostgreSQL", "note": "`+strings.Repeat("a", 2000)+`"`), true},
		{replace(answerStart, `"answer": {"option": "PostgreSQL", "note": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(answerStart, `"answer": {"option": "PostgreSQL", "note": ""`), false},
		{replace(answerStart, `"answer": {"note": "PostgreSQL"`), false},
		{replace(answerStart, `"answer": {"option": "`+strings.Repeat("a", 200)+`"`), true},
		{replace(answerStart, `"answer": {"option": "`+strings.Repeat("a", 201)+`"`), false},
		{replace(answerStart, `"answer": {"option": ""`), false},
		{replace(answerStart, `"answer": {"option": false`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {}`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "`+strings.Repeat("a", 160)+`"}`), true},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "`+strings.Repeat("a", 161)+`"}`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "Sam\nExample"}`), false},
		// follows
		{replace(`"follows": ["q1"]`, `"follows": []`), true},
		{replace(`"follows": ["q1"]`, `"follows": ["q1", "q1"]`), false},
		{replace(`"follows": ["q1"]`, `"follows": ["Q1"]`), false},
		{replace(`"follows": ["q1"]`, `"follows": "q1"`), false},
		// about
		{replace("\n  "+workItem+`,`, ``), false},
		{replace(workItem, `"about": null`), false},
		{replace(workItem, `"about": {}`), false},
		{replace(workItem, `"about": "work_item"`), false},
		{replace(workItem, `"about": {"work_item": false}`), false},
		{replace(workItem, `"about": {"work_item": "wi_7k2m9pq4xt3a8b1c"}`), false},
		{replace(workItem, `"about": {"work_item": true, "type": "proposal", "name": "main"}`), false},
		{replace(workItem, `"about": {"work_item": true, "id": "wi_7k2m9pq4xt3a8b1c"}`), false},
		{replace(workItem, `"about": {"type": "proposal", "name": "main", "work_item": true}`), false},
		{replace(workItem, `"criterion": "ac-3", "about": {"work_item": true}`), false},
		{replace(workItem, workItem+`, "criterion": "ac-3"`), false},
		{replace(workItem, `"about": {"type": "proposal", "name": "main"}`), true},
		{replace(workItem, `"about": {"type": "proposal"}`), false},
		{replace(workItem, `"about": {"name": "main"}`), false},
		{replace(workItem, `"about": {"type": "proposal", "name": "main", "version": 1}`), false},
		{replace(workItem, `"about": {"type": "Proposal", "name": "main"}`), false},
		{replace(workItem, `"about": {"type": "proposal--x", "name": "main"}`), false},
		{replace(workItem, `"about": {"type": "`+strings.Repeat("a", 64)+`", "name": "main"}`), true},
		{replace(workItem, `"about": {"type": "`+strings.Repeat("a", 65)+`", "name": "main"}`), false},
		{replace(workItem, `"about": {"type": "proposal", "name": ""}`), false},
		{replace(workItem, `"about": {"type": "proposal", "name": "`+strings.Repeat("a", 64)+`"}`), true},
		{replace(workItem, `"about": {"type": "proposal", "name": "`+strings.Repeat("a", 65)+`"}`), false},
		{replace(workItem, `"about": {"type": "proposal", "name": "a\nb"}`), false},
		{replace(workItem, `"about": {"type": 3, "name": "main"}`), false},
		// about.item of the type acceptance-criteria is a criterion id
		{withCriterion(`"ac-3"`), true},
		{withCriterion(`"ac-1"`), true},
		{withCriterion(`"ac-9999"`), true},
		{withCriterion(`"screen-2"`), false},
		{withCriterion(`"ac-0"`), false},
		{withCriterion(`"ac-01"`), false},
		{withCriterion(`"ac-03"`), false},
		{withCriterion(`"ac-10000"`), false},
		{withCriterion(`"ac-"`), false},
		{withCriterion(`"ac-x"`), false},
		{withCriterion(`"ac-3a"`), false},
		{withCriterion(`"ac-3\n"`), false},
		{withCriterion(`"AC-3"`), false},
		{withCriterion(`3`), false},
		{withCriterion(`null`), false},
		{withCriterion(`""`), false},
		{replace(workItem, `"about": {"type": "acceptance-criteria", "name": "main"}`), true},
		// about.item of another type is any line of 1 to 32 characters
		{withItemOf("mockup", `"screen-2"`), true},
		{withItemOf("mockup", `"ac-3"`), true},
		{withItemOf("mockup", `"ac-0"`), true},
		{withItemOf("mockup", `"AC-3"`), true},
		{withItem(`"screen-2"`), true},
		{withItem(`"ac-3"`), true},
		{withItem(`"login"`), true},
		{withItem(`"Login screen"`), true},
		{withItem(`"` + strings.Repeat("a", 32) + `"`), true},
		{withItem(`"` + strings.Repeat("a", 33) + `"`), false},
		{withItem(`3`), false},
		{withItem(`null`), false},
		{withItem(`""`), false},
		{withItem(`"a\nb"`), false},
		{withItem(`"a\u200bb"`), false},
		// an option's proposal, only on a question that is about an entry
		{proposalOn(withItem(`"ac-3"`), `"Use PostgreSQL 16."`), true},
		{proposalOn(withItem(`"login"`), `"Use PostgreSQL 16."`), true},
		{proposalOn(withItem(`"ac-3"`), `"`+strings.Repeat("a", 2000)+`"`), true},
		{proposalOn(withItem(`"ac-3"`), `"Use PostgreSQL\n16."`), true},
		{proposalOn(questionsDocument, `"Use PostgreSQL 16."`), false},
		{proposalOn(replace(workItem, `"about": {"type": "proposal", "name": "main"}`), `"Use PostgreSQL 16."`), false},
		{replace(`{"title": "None", "explanation": "Every read goes to the database."}`, `{"title": "None", "explanation": "Every read goes to the database.", "proposal": "Use no cache."}`), false},
		{strings.Replace(replace(redis, redis+`, "proposal": "Use Redis."`), `"about": {"type": "proposal", "name": "main"}`, `"about": {"type": "proposal", "name": "main", "item": "ac-3"}`, 1), true},
		{proposalOn(withItem(`"ac-3"`), `""`), false},
		{proposalOn(withItem(`"ac-3"`), `"`+strings.Repeat("a", 2001)+`"`), false},
		{proposalOn(withItem(`"ac-3"`), `null`), false},
		{proposalOn(withItem(`"ac-3"`), `7`), false},
		{proposalOn(withItem(`"ac-3"`), `"Use\u200bPostgreSQL."`), false},
		{replace(workItem, `"about": {"type": "proposal", "name": "main", "item": "ac-3"}`), true},
		{strings.Replace(withItem(`"ac-3"`), option, recommended(`true`), 1), true},
		// history
		{replace(`, "reopened": {"run": 2, "at": "2026-11-04T10:45:00Z"}`, ``), false},
		{replace(`"reopened": {"run": 2, `, `"reopened": {"run": 0, `), false},
		{replace(historyStart, `"history": [{"option": "PostgreSQL"`), true},
		{replace(historyStart, `"history": [{"option": "PostgreSQL", "note": "Not\nsure"`), true},
		{replace(historyStart, `"history": [{"option": null`), false},
		{replace(historyStart, historyStart+`, "custom": true`), false},
		{replace(historyStart, historyStart+`, "text": "Not sure"`), false},
		{replace(historyStart, `"history": [{"option": null, "note": "`+strings.Repeat("a", 2000)+`"`), true},
		{replace(historyStart, `"history": [{"option": null, "note": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(historyStart, `"history": [{"option": null, "note": ""`), false},
		{replace(historyStart, `"history": [{"note": "Not\nsure"`), false},
		// a member this schema does not define, at each level
		{replace(`{"questions": [`, `{"version": 1, "questions": [`), false},
		{replace(`"id": "q-2"`, `"id": "q-2", "priority": 1`), false},
		{replace(`"title": "PostgreSQL"`, `"title": "PostgreSQL", "default": true`), false},
		{replace(answerStart, answerStart+`, "rank": 1`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "Sam Example", "email": "sam@example.com"}`), false},
		{replace(`"asked": {"run": 1, `, `"asked": {"step": "explore", "run": 1, `), false},
		{replace(`"reopened": {"run": 2, `, `"reopened": {"note": "a", "run": 2, `), false},
		{replace(historyStart, historyStart+`, "rank": 1`), false},
		{replace(decisionStart, decisionStart+`, "rank": 1`), false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}

// verdictDocument is a document of verdict.schema.json that passes: an escalation
// with a point to answer and a point to run again.
const verdictDocument = `{"verdict": "escalate", "points": [
 {"text": "The ticket does not say which customers see the new price.", "action": "answer", "question": "q4"},
 {"text": "The checks script failed on the spec's links.", "action": "run_again"}
]}`

// TestVerdictSchema is each document that verdict.schema.json passes or fails.
func TestVerdictSchema(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/verdict.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string {
		if !strings.Contains(verdictDocument, from) {
			t.Fatalf("the document has no %s", from)
		}
		return strings.Replace(verdictDocument, from, to, 1)
	}
	point := `{"text": "The checks script failed.", "action": "run_again"}`
	pointsOf := func(verdict string, n int) string {
		return `{"verdict": "` + verdict + `", "points": [` + strings.TrimSuffix(strings.Repeat(point+", ", n), ", ") + `]}`
	}
	points := func(n int) string { return pointsOf("escalate", n) }
	answer := `{"text": "The spec does not say who sees the price.", "action": "answer", "question": "q4"}`
	text := func(s string) string {
		return replace(`"text": "The checks script failed on the spec's links."`, `"text": "`+s+`"`)
	}
	fix := func(s string) string {
		return replace(`"action": "run_again"`, `"action": "run_again", "fix": "`+s+`"`)
	}
	rlo := string(rune(0x202e)) // a right-to-left override, invisible
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{verdictDocument, true},
		{`{"verdict": "passed"}`, true},
		{`{"verdict": "passed", "track": {"id": "small-change", "reason": "One file."}}`, true},
		{`{"verdict": "passed", "track": {"id": "small-change", "kind": "A text that the product shows", "reason": "One file."}}`, false},
		{`{"verdict": "changes-requested"}`, true},
		{`{}`, false},
		{`[]`, false},
		{`{"verdict": "failed"}`, false},
		// points is required with escalate, optional with changes-requested, and absent
		// with passed.
		{`{"verdict": "escalate"}`, false},
		{replace(`"verdict": "escalate"`, `"verdict": "passed"`), false},
		{pointsOf("passed", 1), false},
		{`{"verdict": "passed", "points": []}`, false},
		// with changes-requested, 1 to 20 points, each with the action run_again
		{pointsOf("changes-requested", 1), true},
		{pointsOf("changes-requested", 20), true},
		{pointsOf("changes-requested", 0), false},
		{pointsOf("changes-requested", 21), false},
		{replace(`"verdict": "escalate"`, `"verdict": "changes-requested"`), false},
		{`{"verdict": "changes-requested", "points": [` + answer + `]}`, false},
		{`{"verdict": "changes-requested", "points": [` + point + `, ` + answer + `]}`, false},
		{`{"verdict": "changes-requested", "points": [{"text": "The checks script failed.", "action": "ask"}]}`, false},
		{`{"verdict": "changes-requested", "points": [{"text": "The checks script failed."}]}`, false},
		{`{"verdict": "changes-requested", "points": [{"text": "The checks script failed.", "action": "run_again", "question": "q4"}]}`, false},
		{`{"verdict": "changes-requested", "points": {}}`, false},
		// with escalate, 1 to 20 points
		{points(1), true},
		{points(20), true},
		{points(0), false},
		{points(21), false},
		{`{"verdict": "escalate", "points": [` + answer + `]}`, true},
		{`{"verdict": "escalate", "points": {}}`, false},
		{`{"verdict": "escalate", "points": ["The spec's links are broken."]}`, false},
		// text and action are required.
		{replace(`, "action": "run_again"`, ``), false},
		{replace(`"text": "The checks script failed on the spec's links.", `, ``), false},
		{replace(`"action": "run_again"`, `"action": "ask"`), false},
		// question is required with answer and absent with run_again.
		{replace(`, "question": "q4"`, ``), false},
		{replace(`"action": "run_again"`, `"action": "run_again", "question": "q4"`), false},
		{replace(`"question": "q4"`, `"question": "`+strings.Repeat("a", 32)+`"`), true},
		{replace(`"question": "q4"`, `"question": "`+strings.Repeat("a", 33)+`"`), false},
		{replace(`"question": "q4"`, `"question": ""`), false},
		{replace(`"question": "q4"`, `"question": "Q4"`), false},
		{replace(`"question": "q4"`, `"question": 4`), false},
		// text: 1 to 500 characters on one line, with the character rules of $defs line
		{text(strings.Repeat("a", 500)), true},
		{text(strings.Repeat("é", 500)), true},
		{text(strings.Repeat("a", 501)), false},
		{text(""), false},
		{text(`One sentence, with ' and \" and \\.`), true},
		{text(`One\tsentence.`), false},
		{text(`One\nsentence.`), false},
		{text(`One\rsentence.`), false},
		{text(`One\u0007sentence.`), false},
		{text(`One ` + rlo + `sentence.`), false},
		{replace(`"text": "The checks script failed on the spec's links."`, `"text": 1`), false},
		// fix: optional with run_again and absent with answer, with the rules of text
		{fix(`Fix the link to /docs/start in the spec.`), true},
		{`{"verdict": "changes-requested", "points": [{"text": "The checks script failed.", "action": "run_again", "fix": "Fix the link."}]}`, true},
		{replace(`"question": "q4"`, `"question": "q4", "fix": "Answer q4."`), false},
		{fix(strings.Repeat("a", 500)), true},
		{fix(strings.Repeat("é", 500)), true},
		{fix(strings.Repeat("a", 501)), false},
		{fix(""), false},
		{fix(`One\nsentence.`), false},
		{fix(`One\tsentence.`), false},
		{fix(`One ` + rlo + `sentence.`), false},
		{replace(`"action": "run_again"`, `"action": "run_again", "fix": 1`), false},
		// a member this schema does not define, at each level, and the old text gaps
		{replace(`"verdict": "escalate", `, `"verdict": "escalate", "gaps": "Add the empty cart.", `), false},
		{`{"verdict": "passed", "gaps": "Add the empty cart."}`, false},
		{replace(`"action": "run_again"`, `"action": "run_again", "gaps": "Add the empty cart."`), false},
		{replace(`"action": "run_again"`, `"action": "run_again", "priority": 1`), false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}

// reviewCommentsDocument is a document of review-comments.schema.json that passes: a
// closed review comment with a member's reply and a run's reply, and an open review
// comment with no reply.
const reviewCommentsDocument = `{"review_comments": [
 {"id": "c1", "on": {"artifact": "proposal", "version": 2},
  "text": "Keep the old ` + "`/v1`" + ` route.\n\n- one\n- two",
  "by": {"name": "Sam Example"}, "at": "2026-11-04T10:00:00Z", "state": "closed",
  "replies": [
   {"text": "Agreed.", "by": {"name": "Kim Example"}, "at": "2026-11-04T10:05:00Z"},
   {"text": "The proposal keeps ` + "`/v1`" + `.", "by": {"run": 3}, "at": "2026-11-04T10:30:00Z", "outcome": "changed"}]},
 {"id": "c-2", "on": {"artifact": "reply-draft", "version": 1},
  "text": "Why two steps?", "by": {"name": "Sam Example"}, "at": "2026-11-04T11:00:00Z", "state": "open"}
]}`

// TestReviewCommentsSchema is each document that review-comments.schema.json passes or
// fails.
func TestReviewCommentsSchema(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/review-comments.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string {
		if !strings.Contains(reviewCommentsDocument, from) {
			t.Fatalf("the document has no %s", from)
		}
		return strings.Replace(reviewCommentsDocument, from, to, 1)
	}
	memberReply := `"by": {"name": "Kim Example"}, "at": "2026-11-04T10:05:00Z"`
	runReply := `"by": {"run": 3}, "at": "2026-11-04T10:30:00Z", "outcome": "changed"`
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{reviewCommentsDocument, true},
		{`{"review_comments": []}`, true},
		{`{}`, false},
		{`{"comments": []}`, false},
		{`[]`, false},
		{replace(`, "state": "open"`, `, "state": "open", "replies": []`), true},
		// outcome is present exactly when a run wrote the reply.
		{replace(runReply, `"by": {"run": 3}, "at": "2026-11-04T10:30:00Z"`), false},
		{replace(memberReply, memberReply+`, "outcome": "answered"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "answered"`), true},
		{replace(`"outcome": "changed"`, `"outcome": "declined"`), true},
		{replace(`"outcome": "changed"`, `"outcome": "done"`), false},
		// question is present exactly when outcome is asked.
		{replace(`"outcome": "changed"`, `"outcome": "asked", "question": "q-3"`), true},
		{replace(`"outcome": "changed"`, `"outcome": "asked"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "changed", "question": "q-3"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "answered", "question": "q-3"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "declined", "question": "q-3"`), false},
		{replace(memberReply, memberReply+`, "question": "q-3"`), false},
		{replace(memberReply, memberReply+`, "outcome": "asked", "question": "q-3"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "asked", "question": "Q_3"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "asked", "question": "`+strings.Repeat("a", 33)+`"`), false},
		{replace(`"by": {"run": 3}`, `"by": {"run": 0}`), false},
		{replace(`"by": {"run": 3}`, `"by": {"run": 3, "name": "Sam Example"}`), false},
		{replace(`"by": {"run": 3}`, `"by": {}`), false},
		// state; a closed review comment has at least one reply by a run.
		{replace(`"state": "open"`, `"state": "closed"`), false},
		{replace(`, "state": "open"`, `, "state": "closed", "replies": []`), false},
		{replace(`, "state": "open"`, `, "state": "closed", "replies": [{"text": "Two.", `+runReply+`}]`), true},
		{replace(`, "state": "open"`, `, "state": "closed", "replies": [{"text": "Two.", `+memberReply+`}]`), false},
		{replace(`, "state": "open"`, `, "state": "open", "replies": [{"text": "Two.", `+memberReply+`}]`), true},
		{replace(runReply, `"by": {"name": "Kim Example"}, "at": "2026-11-04T10:30:00Z"`), false},
		{replace(`"state": "open"`, `"state": "resolved"`), false},
		{replace(`, "state": "open"`, ``), false},
		// id
		{replace(`"id": "c-2"`, `"id": "`+strings.Repeat("a", 32)+`"`), true},
		{replace(`"id": "c-2"`, `"id": "`+strings.Repeat("a", 33)+`"`), false},
		{replace(`"id": "c-2"`, `"id": "C2"`), false},
		{replace(`"id": "c-2", `, ``), false},
		// on
		{replace(`, "version": 1}`, `}`), false},
		{replace(`"artifact": "reply-draft", `, ``), false},
		{replace(`"version": 1}`, `"version": 0}`), false},
		{replace(`"artifact": "reply-draft"`, `"artifact": "Reply-draft"`), false},
		{replace(`"artifact": "reply-draft"`, `"artifact": "reply--draft"`), false},
		{replace(`"artifact": "reply-draft"`, `"artifact": "`+strings.Repeat("a", 64)+`"`), true},
		{replace(`"artifact": "reply-draft"`, `"artifact": "`+strings.Repeat("a", 65)+`"`), false},
		// on.screen is optional: on a closed review comment with a run's reply, and on an
		// open one. reviewCommentsDocument itself has no screen.
		{replace(`"version": 2}`, `"version": 2, "screen": "cart"}`), true},
		{replace(`"version": 1}`, `"version": 1, "screen": "cart"}`), true},
		{replace(`"version": 2}`, `"version": 2, "screen": "`+strings.Repeat("a", 32)+`"}`), true},
		{replace(`"version": 2}`, `"version": 2, "screen": "`+strings.Repeat("a", 33)+`"}`), false},
		{replace(`"version": 2}`, `"version": 2, "screen": ""}`), false},
		{replace(`"version": 2}`, `"version": 2, "screen": 1}`), false},
		{replace(`"version": 2}`, `"version": 2, "screen": null}`), false},
		{replace(`"version": 2}`, `"version": 2, "screen": ["cart"]}`), false},
		{replace(`"version": 2}`, `"version": 2, "screen": "a\nb"}`), false},
		{replace(`"version": 2}`, `"version": 2, "screen": "a\u202eb"}`), false},
		// on a round: on has only run, the run's number from 1, and no screen.
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"run": 3}`), true},
		{replace(`{"artifact": "proposal", "version": 2}`, `{"run": 1}`), true},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"run": 0}`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"run": 1, "artifact": "proposal"}`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"run": 1, "version": 1}`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"run": 3, "screen": "cart"}`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"run": "3"}`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"run": 1.5}`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{}`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `{"artifact": "reply-draft", "version": 1, "run": 1}`), false},
		// on and a reply's by are objects.
		{replace(`{"artifact": "reply-draft", "version": 1}`, `3`), false},
		{replace(`{"artifact": "reply-draft", "version": 1}`, `null`), false},
		{replace(`"by": {"run": 3}`, `"by": 3`), false},
		// text
		{replace(`"Why two steps?"`, `"`+strings.Repeat("a", 2000)+`"`), true},
		{replace(`"Why two steps?"`, `"`+strings.Repeat("a", 2001)+`"`), false},
		{replace(`"Why two steps?"`, `""`), false},
		{replace(`"Why two steps?"`, `"Why \u202etwo steps?"`), false},
		{replace(`"text": "Agreed."`, `"text": "`+strings.Repeat("a", 2001)+`"`), false},
		// by and at
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"run": 3}`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "Sam\nExample"}`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-04T10:00:00+01:00"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-12-31T10:00:00Z"`), true},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-13-04T10:00:00Z"`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-32T10:00:00Z"`), false},
		{replace(`, "at": "2026-11-04T11:00:00Z"`, ``), false},
		// a member this schema does not define, at each level
		{replace(`{"review_comments": [`, `{"version": 1, "review_comments": [`), false},
		{replace(`"id": "c-2"`, `"id": "c-2", "anchor": "line 4"`), false},
		{replace(`"version": 2}`, `"version": 2, "file": "a.md"}`), false},
		{replace(`"text": "Agreed."`, `"text": "Agreed.", "note": "a"`), false},
		{replace(`"by": {"name": "Kim Example"}`, `"by": {"name": "Kim Example", "email": "kim@example.com"}`), false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}

// proposalSourcesDocument is a document of proposal-sources.schema.json that passes: a
// file in a further repository, a web page, and a record in another system.
const proposalSourcesDocument = `{"sources": [
 {"kind": "file", "ref": "src/cart.ts", "system": "git.example.com", "container": "web", "locator": "lines 40-48", "why": "The cart total is computed here."},
 {"kind": "page", "ref": "https://docs.example.com/api?v=2#auth", "why": "The limits of the API."},
 {"kind": "record", "ref": "ACC-1042", "system": "crm.example.com", "container": "Accounts", "locator": "renewal date", "why": "The renewal date of the account."}
]}`

// TestProposalSourcesSchema is each document that proposal-sources.schema.json passes
// or fails.
func TestProposalSourcesSchema(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/proposal-sources.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string {
		if !strings.Contains(proposalSourcesDocument, from) {
			t.Fatalf("the document has no %s", from)
		}
		return strings.Replace(proposalSourcesDocument, from, to, 1)
	}
	rlo := string(rune(0x202e)) // a right-to-left override, invisible
	page := `{"kind": "page", "ref": "https://docs.example.com/api?v=2#auth", "why": "The limits of the API."}`
	why := `"why": "The limits of the API."`
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{proposalSourcesDocument, true},
		{`{"sources": []}`, true},
		{`{"sources": [` + page + `]}`, true},
		{`{"sources": [{"kind": "document", "ref": "Q3 plan", "why": "The budget."}]}`, true},
		{`{}`, false},
		{`[]`, false},
		{`{"sources": {}}`, false},
		// kind
		{replace(`"kind": "page"`, `"kind": "a`+strings.Repeat("b", 31)+`"`), true},
		{replace(`"kind": "page"`, `"kind": "a`+strings.Repeat("b", 32)+`"`), false},
		{replace(`"kind": "page"`, `"kind": "Page"`), false},
		{replace(`"kind": "page"`, `"kind": "1page"`), false},
		{replace(`"kind": "page"`, `"kind": "web page"`), false},
		{replace(`"kind": "page"`, `"kind": ""`), false},
		{replace(`"kind": "page", `, ``), false},
		// ref
		{replace(`"ref": "ACC-1042"`, `"ref": "`+strings.Repeat("a", 2048)+`"`), true},
		{replace(`"ref": "ACC-1042"`, `"ref": "`+strings.Repeat("a", 2049)+`"`), false},
		{replace(`"ref": "ACC-1042"`, `"ref": ""`), false},
		{replace(`"ref": "ACC-1042"`, `"ref": "ACC\n1042"`), false},
		{replace(`"ref": "ACC-1042"`, `"ref": "ACC\t1042"`), false},
		{replace(`"ref": "ACC-1042"`, `"ref": "ACC-`+rlo+`1042"`), false},
		{replace(`"ref": "ACC-1042", `, ``), false},
		// system
		{replace(`"system": "crm.example.com"`, `"system": "CRM-1.Example.com"`), true},
		{replace(`"system": "crm.example.com"`, `"system": "localhost"`), true},
		{replace(`"system": "crm.example.com"`, `"system": "`+strings.Repeat("a", 63)+`"`), true},
		{replace(`"system": "crm.example.com"`, `"system": "`+strings.Repeat("a", 64)+`"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "`+strings.TrimSuffix(strings.Repeat("a.", 127), ".")+`"`), true},
		{replace(`"system": "crm.example.com"`, `"system": "`+strings.TrimSuffix(strings.Repeat("a.", 127), ".")+`a"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "https://crm.example.com"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "crm.example.com:8443"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "crm..example.com"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "crm.example.com."`), false},
		{replace(`"system": "crm.example.com"`, `"system": "crm_1.example.com"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "c-r-m.example.com"`), true},
		{replace(`"system": "crm.example.com"`, `"system": "a.b"`), true},
		{replace(`"system": "crm.example.com"`, `"system": "a`+strings.Repeat("-", 61)+`a"`), true},
		{replace(`"system": "crm.example.com"`, `"system": "-crm.example.com"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "crm-.example.com"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "crm.example.com-"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "crm.-example.com"`), false},
		{replace(`"system": "crm.example.com"`, `"system": "-"`), false},
		{replace(`"system": "crm.example.com"`, `"system": ""`), false},
		// container
		{replace(`"container": "Accounts"`, `"container": "`+strings.Repeat("a", 200)+`"`), true},
		{replace(`"container": "Accounts"`, `"container": "`+strings.Repeat("a", 201)+`"`), false},
		{replace(`"container": "Accounts"`, `"container": ""`), false},
		{replace(`"container": "Accounts"`, `"container": "Acc\nounts"`), false},
		// locator
		{replace(`"locator": "renewal date"`, `"locator": "`+strings.Repeat("a", 500)+`"`), true},
		{replace(`"locator": "renewal date"`, `"locator": "`+strings.Repeat("a", 501)+`"`), false},
		{replace(`"locator": "renewal date"`, `"locator": ""`), false},
		{replace(`"locator": "renewal date"`, `"locator": "renewal\ndate"`), false},
		// why
		{replace(why, `"why": "`+strings.Repeat("a", 500)+`"`), true},
		{replace(why, `"why": "`+strings.Repeat("a", 501)+`"`), false},
		{replace(why, `"why": ""`), false},
		{replace(why, `"why": "The limits\nof the API."`), false},
		{replace(`, `+why, ``), false},
		// a member this schema does not define, at each level
		{replace(`{"sources": [`, `{"version": 1, "sources": [`), false},
		{replace(why, why+`, "title": "API"`), false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}

// acceptanceCriteriaDocument is a document of acceptance-criteria.schema.json that
// passes: a summary, a criterion and a criterion that is removed.
const acceptanceCriteriaDocument = `{"summary": "A customer sees the new price before they pay.",
 "criteria": [
  {"id": "ac-1", "text": "The price page shows the new price.", "check": "Open the price page as a customer.\nThe price is 12.50."},
  {"id": "ac-2", "text": "The old price is gone.", "check": "Search the price page for 10.00.",
   "removed": {"reason": "The old price stays for existing customers."}}
]}`

// TestAcceptanceCriteriaSchema is each document that acceptance-criteria.schema.json
// passes or fails.
func TestAcceptanceCriteriaSchema(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/acceptance-criteria.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string {
		if !strings.Contains(acceptanceCriteriaDocument, from) {
			t.Fatalf("the document has no %s", from)
		}
		return strings.Replace(acceptanceCriteriaDocument, from, to, 1)
	}
	const (
		summary = `"summary": "A customer sees the new price before they pay."`
		text    = `"text": "The price page shows the new price."`
		check   = `"check": "Search the price page for 10.00."`
		removed = `"removed": {"reason": "The old price stays for existing customers."}`
	)
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{acceptanceCriteriaDocument, true},
		{`{}`, false},
		{`[]`, false},
		{`{"summary": "One.", "criteria": []}`, false},
		{`{"criteria": [{"id": "ac-1", "text": "One.", "check": "Look."}]}`, false},
		{`{"summary": "One."}`, false},
		{`{"summary": "One.", "criteria": {}}`, false},
		{replace(`{"summary"`, `{"title": "Price", "summary"`), false},
		// summary
		{replace(summary, `"summary": ""`), false},
		{replace(summary, `"summary": "`+strings.Repeat("a", 2000)+`"`), true},
		{replace(summary, `"summary": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(summary, `"summary": "One.\n\nTwo."`), true},
		{replace(summary, `"summary": "One.​Two."`), false},
		{replace(summary, `"summary": null`), false},
		// the criterion's id: ac- and a number from 1 to 9999 with no leading zero
		{replace(`"ac-1"`, `"ac-9999"`), true},
		{replace(`"ac-1"`, `"ac-0"`), false},
		{replace(`"ac-1"`, `"ac-01"`), false},
		{replace(`"ac-1"`, `"ac-10000"`), false},
		{replace(`"ac-1"`, `"AC-1"`), false},
		{replace(`"ac-1"`, `"ac-1 "`), false},
		{replace(`"ac-1"`, `"ac-"`), false},
		{replace(`"ac-1"`, `"c1"`), false},
		{replace(`"ac-1"`, `1`), false},
		{replace(`"id": "ac-1", `, ``), false},
		// text and check
		{replace(text, `"text": ""`), false},
		{replace(text, `"text": "`+strings.Repeat("a", 2000)+`"`), true},
		{replace(text, `"text": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(text, `"text": 1`), false},
		{replace(text+`, `, ``), false},
		{replace(check, `"check": ""`), false},
		{replace(check, `"check": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(check, `"check": "Search‮the page."`), false},
		{replace(`, "check": "Open the price page as a customer.\nThe price is 12.50."`, ``), false},
		// removed, with its reason
		{replace(removed, `"removed": {"reason": "Not needed."}`), true},
		{replace(removed, `"removed": {}`), false},
		{replace(removed, `"removed": {"reason": ""}`), false},
		{replace(removed, `"removed": {"reason": "`+strings.Repeat("a", 2001)+`"}`), false},
		{replace(removed, `"removed": true`), false},
		{replace(removed, `"removed": "Not needed."`), false},
		{replace(removed, `"removed": {"reason": "Not needed.", "by": "Sam"}`), false},
		{replace(`"check": "Open the price page as a customer.\nThe price is 12.50."`, `"check": "Open the price page as a customer.", `+removed), true},
		// a member this schema does not define
		{replace(`"id": "ac-1"`, `"id": "ac-1", "state": "open"`), false},
		{replace(`"id": "ac-1"`, `"id": "ac-1", "criterion": "ac-1"`), false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}

// verificationDocument is a document of verification.schema.json that passes: a row that
// is met, a row that is not met, a criterion that was not verified and two model
// families.
const verificationDocument = `{"target_version": "5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e",
 "matrix": [
  {"criterion": "ac-1", "state": "met", "evidence": "The price page shows 12.50."},
  {"criterion": "ac-2", "state": "not_met", "evidence": "The page still shows 10.00 in the footer."}
 ],
 "not_verified": [{"criterion": "ac-3", "reason": "The payment sandbox was down."}],
 "model_families": ["anthropic", "openai"]}`

// TestVerificationSchema is each document that verification.schema.json passes or fails.
func TestVerificationSchema(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../../contracts/harness/v1/verification.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string {
		if !strings.Contains(verificationDocument, from) {
			t.Fatalf("the document has no %s", from)
		}
		return strings.Replace(verificationDocument, from, to, 1)
	}
	const (
		version = `"5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e"`
		matrix  = `"matrix": [
  {"criterion": "ac-1", "state": "met", "evidence": "The price page shows 12.50."},
  {"criterion": "ac-2", "state": "not_met", "evidence": "The page still shows 10.00 in the footer."}
 ]`
		row      = `{"criterion": "ac-1", "state": "met", "evidence": "The price page shows 12.50."}`
		notVer   = `"not_verified": [{"criterion": "ac-3", "reason": "The payment sandbox was down."}]`
		families = `"model_families": ["anthropic", "openai"]`
	)
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{verificationDocument, true},
		{`{}`, false},
		{`[]`, false},
		{`{"target_version": ` + version + `, "matrix": [], "not_verified": [], "model_families": ["anthropic"]}`, true},
		{replace(`"target_version": `+version+`,`, ``), false},
		{replace(matrix+`,`, ``), false},
		{replace(notVer+`,`, ``), false},
		{replace(",\n "+families, ``), false},
		{replace(`{"target_version"`, `{"title": "Check", "target_version"`), false},
		{replace(`"target_version": `+version+`,`, `"commit": `+version+`,`), false},
		// target_version: 1 to 255 letters, digits and the characters ._/-
		{replace(version, `"`+strings.Repeat("a", 40)+`"`), true},
		{replace(version, `"`+strings.Repeat("a", 64)+`"`), true},
		{replace(version, `"2026-11-18.3"`), true},
		{replace(version, `"v1.2.3"`), true},
		{replace(version, `"release/2026_11"`), true},
		{replace(version, `"A"`), true},
		{replace(version, `"`+strings.Repeat("a", 255)+`"`), true},
		{replace(version, `"`+strings.Repeat("a", 256)+`"`), false},
		{replace(version, `""`), false},
		{replace(version, `"2026 11"`), false},
		{replace(version, `"v1:2"`), false},
		{replace(version, `"v1\n"`), false},
		{replace(version, `"v1@x"`), false},
		{replace(version, `12`), false},
		{replace(version, `null`), false},
		// the matrix rows
		{replace(row, `{"criterion": "ac-9999", "state": "met", "evidence": "Seen."}`), true},
		{replace(`"state": "met"`, `"state": "not_met"`), true},
		{replace(`"state": "met"`, `"state": "verified"`), false},
		{replace(`"state": "met"`, `"state": "failed"`), false},
		{replace(`"state": "met"`, `"state": "not_verified"`), false},
		{replace(`"state": "met"`, `"state": "passed"`), false},
		{replace(`"state": "met"`, `"state": "Met"`), false},
		{replace(`"state": "met"`, `"state": true`), false},
		{replace(`"state": "met", `, ``), false},
		{replace(`"criterion": "ac-1", "state"`, `"state"`), false},
		{replace(`, "evidence": "The price page shows 12.50."`, ``), false},
		{replace(`"evidence": "The price page shows 12.50."`, `"evidence": ""`), false},
		{replace(`"evidence": "The price page shows 12.50."`, `"evidence": "`+strings.Repeat("a", 2000)+`"`), true},
		{replace(`"evidence": "The price page shows 12.50."`, `"evidence": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(`"evidence": "The price page shows 12.50."`, `"evidence": "Seen.\nTwice."`), true},
		{replace(`"evidence": "The price page shows 12.50."`, `"evidence": "Seen.​"`), false},
		{replace(`"state": "met"`, `"state": "met", "by": "model"`), false},
		{replace(matrix, `"matrix": {}`), false},
		// a criterion id in a row, in not_verified: ac- and a number from 1 to 9999
		{replace(`"ac-1"`, `"ac-0"`), false},
		{replace(`"ac-1"`, `"ac-01"`), false},
		{replace(`"ac-1"`, `"ac-10000"`), false},
		{replace(`"ac-1"`, `"AC-1"`), false},
		{replace(`"ac-1"`, `1`), false},
		{replace(`"ac-3"`, `"ac-9999"`), true},
		{replace(`"ac-3"`, `"ac-0"`), false},
		{replace(`"ac-3"`, `"ac-01"`), false},
		{replace(`"ac-3"`, `"ac-10000"`), false},
		{replace(`"ac-3"`, `"q3"`), false},
		// the criteria that were not verified, with their reason
		{replace(`"reason": "The payment sandbox was down."`, `"reason": ""`), false},
		{replace(`, "reason": "The payment sandbox was down."`, ``), false},
		{replace(`"reason": "The payment sandbox was down."`, `"reason": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(`"reason": "The payment sandbox was down."`, `"reason": "Down.", "state": "not_verified"`), false},
		{replace(`"criterion": "ac-3", `, ``), false},
		{replace(`"not_verified": [{"criterion": "ac-3", "reason": "The payment sandbox was down."}]`, `"not_verified": ["ac-3"]`), false},
		// the model families
		{replace(families, `"model_families": []`), false},
		{replace(families, `"model_families": ["anthropic"]`), true},
		{replace(families, `"model_families": "anthropic"`), false},
		{replace(families, `"model_families": ["anthropic", "anthropic"]`), false},
		{replace(families, `"model_families": [""]`), false},
		{replace(families, `"model_families": ["`+strings.Repeat("a", 64)+`"]`), true},
		{replace(families, `"model_families": ["`+strings.Repeat("a", 65)+`"]`), false},
		{replace(families, `"model_families": ["a\nb"]`), false},
		{replace(families, `"model_families": [1]`), false},
		{replace(families, `"model_families": [{"name": "anthropic"}]`), false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}
