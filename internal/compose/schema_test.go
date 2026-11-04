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
// kind, its qory key, its workflows or its bind fails it,
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
		wantInvalid := strings.Contains(string(errText), "apiVersion") || strings.Contains(string(errText), "kind ") || strings.Contains(string(errText), "qory \"") ||
			strings.HasPrefix(string(errText), "qory.yaml: ") && strings.Contains(string(errText), "workflows") ||
			strings.Contains(string(errText), "bind.") && strings.Contains(string(errText), " in qory.yaml")
		err := composeSchema.Validate(document(t, filepath.Join(dir, "qory.yaml")))
		if wantInvalid && err == nil {
			t.Errorf("%s: the document passed the schema; want a failure", dir)
		}
		if !wantInvalid && err != nil {
			t.Errorf("%s: the document failed the schema: %v", dir, err)
		}
		if f := filepath.Join(dir, "stack", "qory-stack.yaml"); fileExists(f) {
			wantBadStack := strings.HasPrefix(string(errText), "stack/qory-stack.yaml: ")
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
			// by its path in the fixture.
			rel, _ := filepath.Rel(dir, m)
			wantBad := strings.Contains(string(errText), " in "+rel+":")
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
		// listed twice, or a json_schema that does not compile, passes it too: a JSON
		// Schema cannot state either rule.
		processes, _ := filepath.Glob(filepath.Join(dir, "modules", "*", "process.yaml"))
		wantBadProcess := strings.Contains(string(errText), "process.yaml: ") &&
			!strings.Contains(string(errText), "exports.stacks lists") && !strings.Contains(string(errText), " under workflows in ") &&
			!strings.Contains(string(errText), " of artifact_types: entry ") && !strings.Contains(string(errText), " compiles as a JSON Schema: ")
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
		{composeSchema, `{"harness": {"tiers": {"light": "claude-haiku-5-5", "fast": "claude-sonnet-5-5", "thinking": {"model": "claude-opus-5-5", "effort": "max"}}}}`, true},
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

// questionsDocument is a document of questions.schema.json that passes: an answered
// question with an earlier answer that a run reopened, and an open question that follows
// from it.
const questionsDocument = `{"questions": [
 {"id": "q1", "question": "Which database does the service use?",
  "context": "The service reads ` + "`DATABASE_URL`" + `.\n\n- one\n- two",
  "options": [{"title": "PostgreSQL", "explanation": "The database of the other services."}],
  "state": "answered", "asked": {"run": 1, "at": "2026-11-04T10:00:00Z"},
  "answer": {"text": "PostgreSQL", "custom": false, "by": {"name": "Sam Example"}, "at": "2026-11-04T11:00:00Z"},
  "history": [{"text": "Not\nsure", "custom": true, "by": {"name": "Sam Example"}, "at": "2026-11-04T10:30:00Z", "reopened": {"run": 2, "at": "2026-11-04T10:45:00Z"}}]},
 {"id": "q-2", "question": "Which port does it listen on?", "options": [],
  "state": "open", "asked": {"run": 2, "at": "2026-11-04T10:45:00Z"}, "follows": ["q1"]}
]}`

// TestQuestionsSchema is each document that questions.schema.json takes or does not take.
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
	options := func(n int) string {
		return replace(option, strings.TrimSuffix(strings.Repeat(option+", ", n), ", "))
	}
	question := "Which database does the service use?"
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
		{replace(question, `Which ‮database?`), false},
		// context
		{replace(`"context": "The service`, `"context": "`+strings.Repeat("a", 20000-len("The service reads `DATABASE_URL`.\n\n- one\n- two"))+`The service`), true},
		{replace(`"context": "The service`, `"context": "`+strings.Repeat("a", 20001-len("The service reads `DATABASE_URL`.\n\n- one\n- two"))+`The service`), false},
		{replace("The service reads `DATABASE_URL`.", `\u0007`), false},
		{replace("The service reads `DATABASE_URL`.\\n\\n- one\\n- two", ""), false},
		// options
		{options(10), true},
		{options(11), false},
		{replace(`"options": [],`, ``), false},
		{replace(`"title": "PostgreSQL"`, `"title": "`+strings.Repeat("a", 200)+`"`), true},
		{replace(`"title": "PostgreSQL"`, `"title": "`+strings.Repeat("a", 201)+`"`), false},
		{replace(`"title": "PostgreSQL"`, `"title": ""`), false},
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
		{replace(`, "asked": {"run": 2, "at": "2026-11-04T10:45:00Z"}`, ``), false},
		// answer
		{replace(`"custom": false, `, ``), false},
		{replace(`"custom": false`, `"custom": "no"`), false},
		{replace(`"text": "PostgreSQL"`, `"text": ""`), false},
		{replace(`"text": "PostgreSQL"`, `"text": "`+strings.Repeat("a", 2001)+`"`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {}`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "`+strings.Repeat("a", 160)+`"}`), true},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "`+strings.Repeat("a", 161)+`"}`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "Sam\nExample"}`), false},
		// follows
		{replace(`"follows": ["q1"]`, `"follows": []`), true},
		{replace(`"follows": ["q1"]`, `"follows": ["q1", "q1"]`), false},
		{replace(`"follows": ["q1"]`, `"follows": ["Q1"]`), false},
		{replace(`"follows": ["q1"]`, `"follows": "q1"`), false},
		// history
		{replace(`, "reopened": {"run": 2, "at": "2026-11-04T10:45:00Z"}`, ``), false},
		{replace(`"reopened": {"run": 2, `, `"reopened": {"run": 0, `), false},
		{replace(`"history": [{"text": "Not\nsure"`, `"history": [{"text": ""`), false},
		// a member this schema does not define, at each level
		{replace(`{"questions": [`, `{"version": 1, "questions": [`), false},
		{replace(`"id": "q-2"`, `"id": "q-2", "priority": 1`), false},
		{replace(`"title": "PostgreSQL"`, `"title": "PostgreSQL", "default": true`), false},
		{replace(`"custom": false`, `"custom": false, "note": "a"`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "Sam Example", "email": "sam@example.com"}`), false},
		{replace(`"asked": {"run": 1, `, `"asked": {"step": "explore", "run": 1, `), false},
		{replace(`"reopened": {"run": 2, `, `"reopened": {"note": "a", "run": 2, `), false},
		{replace(`"history": [{"text": "Not\nsure"`, `"history": [{"note": "a", "text": "Not\nsure"`), false},
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

// TestReviewCommentsSchema is each document that review-comments.schema.json takes or
// does not take.
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
		{replace(memberReply, memberReply+`, "question": "q-3"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "asked", "question": "Q_3"`), false},
		{replace(`"outcome": "changed"`, `"outcome": "asked", "question": "`+strings.Repeat("a", 33)+`"`), false},
		{replace(`"by": {"run": 3}`, `"by": {"run": 0}`), false},
		{replace(`"by": {"run": 3}`, `"by": {"run": 3, "name": "Sam Example"}`), false},
		{replace(`"by": {"run": 3}`, `"by": {}`), false},
		// state
		{replace(`"state": "open"`, `"state": "closed"`), true},
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
		// text
		{replace(`"Why two steps?"`, `"`+strings.Repeat("a", 2000)+`"`), true},
		{replace(`"Why two steps?"`, `"`+strings.Repeat("a", 2001)+`"`), false},
		{replace(`"Why two steps?"`, `""`), false},
		{replace(`"Why two steps?"`, `"Why ‮two steps?"`), false},
		{replace(`"text": "Agreed."`, `"text": "`+strings.Repeat("a", 2001)+`"`), false},
		// by and at
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"run": 3}`), false},
		{replace(`"by": {"name": "Sam Example"}`, `"by": {"name": "Sam\nExample"}`), false},
		{replace(`"2026-11-04T10:00:00Z"`, `"2026-11-04T10:00:00+01:00"`), false},
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
