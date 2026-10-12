package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestForagerFileReadsThePaths reads gateway.egress.paths by host, an empty list
// included, and lists them in one row by host in order; without them the row is
// "(none)", a default.
func TestForagerFileReadsThePaths(t *testing.T) {
	hermetic(t)
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if row := pathsRow(c.Rows()); row.Value != "(none)" || row.Origin != config.Default {
		t.Errorf("no file: %+v", row)
	}
	path := foragerFile(t, "gateway:\n  egress:\n    mode: enforce\n    allow: [api.example.com]\n")
	if c, err = config.Load(t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	if c.Forager.Egress.Paths != nil {
		t.Errorf("no paths read as %v", c.Forager.Egress.Paths)
	}
	if row := pathsRow(c.Rows()); row.Value != "(none)" || row.Origin != path {
		t.Errorf("no paths: %+v", row)
	}
	path = foragerFile(t, "gateway:\n  egress:\n    mode: observe\n    paths:\n      \"*.files.example.com\": []\n      api.example.com: [\"/v1/*\", /v2/status]\n")
	if c, err = config.Load(t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	paths := c.Forager.Egress.Paths
	if len(paths) != 2 || strings.Join(paths["api.example.com"], " ") != "/v1/* /v2/status" || paths["*.files.example.com"] == nil || len(paths["*.files.example.com"]) != 0 {
		t.Errorf("gateway.egress.paths read as %#v", paths)
	}
	if row, want := pathsRow(c.Rows()), "*.files.example.com (no path); api.example.com /v1/*, /v2/status"; row.Value != want || row.Origin != path {
		t.Errorf("the row: %+v, want %q from %s", row, want, path)
	}
}

// pathsRow is the gateway.egress.paths row of rows.
func pathsRow(rows []config.Row) config.Row {
	for _, row := range rows {
		if row.Key == "gateway.egress.paths" {
			return row
		}
	}
	return config.Row{}
}

// TestForagerFileRefusesPathsItCannotTake refuses gateway.egress.paths that is not a
// mapping from a host to a list of paths, a key Forager does not read as a host, a path
// it does not read as one, and a path listed twice: each word for word after the file,
// naming the host where it is a host, and never the key that is not one or the path.
func TestForagerFileRefusesPathsItCannotTake(t *testing.T) {
	hermetic(t)
	const host = ": gateway.egress.paths: a key is not a lower-case host name or a *. suffix, or it ends in a number and is not an IPv4 address in canonical form; no port, path or scheme"
	for _, c := range []struct{ body, want string }{
		{"paths: [\"/v1/*\"]", ": gateway.egress.paths is a mapping from a host to a list of paths"},
		{"paths:", ": gateway.egress.paths is a mapping from a host to a list of paths"},
		{"paths: {api.example.com: /v1/*}", ": gateway.egress.paths: api.example.com is a list of paths"},
		{"paths: {api.example.com: [[/v1/*]]}", ": gateway.egress.paths.api.example.com[0] is not a path from /, matched whole or up to a final *"},
		{"paths: {api.example.com: [/v1, ~]}", ": gateway.egress.paths.api.example.com[1] is not a path from /, matched whole or up to a final *"},
		{"paths: {api.example.com: [~, v1]}", ": gateway.egress.paths.api.example.com[0] is not a path from /, matched whole or up to a final *"},
		{"paths: {api.example.com: [/v1, null, v1]}", ": gateway.egress.paths.api.example.com[1] is not a path from /, matched whole or up to a final *"},
		{"paths: {api.example.com: [/v1, !!int 7]}", ": gateway.egress.paths.api.example.com[1] is not a path from /, matched whole or up to a final *"},
		{"paths: {443: [/v1]}", host},
		{"paths: {api.example.com: }", ": gateway.egress.paths: api.example.com is a list of paths"},
		{"paths: {\"API.example.com\": [/v1/*]}", host},
		{"paths: {\"api.example.com:443\": [/v1/*]}", host},
		{"paths: {api.example.123: [/v1/*]}", host},
		{"paths: {\"127.1\": [/v1/*]}", host},
		{"paths: {\"api.example.com:443\": /v1/*}", host},
		{"paths: {? [a.example] : [/v1/*]}", host},
		{"paths: {api.example.com: [/v1/*, v1]}", ": gateway.egress.paths.api.example.com[1] is not a path from /, matched whole or up to a final *"},
		{"paths: {api.example.com: [\"/v1/*/x\"]}", ": gateway.egress.paths.api.example.com[0] is not a path from /, matched whole or up to a final *"},
		{"paths: {api.example.com: [/v1, /v2, /v1]}", ": gateway.egress.paths.api.example.com lists a path twice"},
	} {
		path := foragerFile(t, "gateway:\n  egress:\n    mode: observe\n    "+c.body+"\n")
		_, err := config.LoadForager()
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q after the file", c.body, err, c.want)
		}
	}
}

// TestForagerFileReadsThePathsAsTheirValues reads an alias key as the host its anchor
// holds, not the anchor's name, and an alias entry as the path it stands for; an alias
// key that writes a host again is refused as a host written twice.
func TestForagerFileReadsThePathsAsTheirValues(t *testing.T) {
	hermetic(t)
	foragerFile(t, "gateway:\n  egress:\n    mode: observe\n    allow: [&h api.example.com]\n    paths:\n      *h: [&p /v1/*, /v2]\n      files.example.com: [*p]\n")
	r, err := config.LoadForager()
	if err != nil {
		t.Fatal(err)
	}
	if paths := r.Egress.Paths; len(paths) != 2 || strings.Join(paths["api.example.com"], " ") != "/v1/* /v2" || strings.Join(paths["files.example.com"], " ") != "/v1/*" {
		t.Errorf("aliases read as %#v", paths)
	}
	path := foragerFile(t, "gateway:\n  egress:\n    mode: observe\n    allow: [&h api.example.com]\n    paths:\n      api.example.com: [/v1/*]\n      *h: [/v2]\n")
	want := path + ": line 7: gateway.egress.paths.api.example.com is written twice; it was first written at line 6"
	if _, err := config.LoadForager(); err == nil || err.Error() != want {
		t.Errorf("an alias key that writes a host again: %v, want %q", err, want)
	}
	path = foragerFile(t, "gateway:\n  egress:\n    mode: observe\n    paths:\n      a.example: &h [/v1]\n      *h: [/v2]\n")
	want = path + ": gateway.egress.paths: a key is not a lower-case host name or a *. suffix, or it ends in a number and is not an IPv4 address in canonical form; no port, path or scheme"
	if _, err := config.LoadForager(); err == nil || err.Error() != want {
		t.Errorf("an alias key of a list: %v, want %q", err, want)
	}
}

// TestTheForagerSchemaTakesThePaths holds forager.schema.json to what the reader takes
// of gateway.egress.paths: a mapping from a host in allow's grammar to a list of paths
// in a policy's grammar, an empty list included.
func TestTheForagerSchemaTakesThePaths(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		paths string
		valid bool
	}{
		{"{api.example.com: [/v1/*, /v2/status]}", true},
		{"{\"*.files.example.com\": []}", true},
		{"{}", true},
		{"[/v1/*]", false},
		{"{api.example.com: /v1/*}", false},
		{"{\"API.example.com\": [/v1/*]}", false},
		{"{\"api.example.com:443\": [/v1/*]}", false},
		{"{api.example.com: [v1]}", false},
		{"{api.example.com: [\"/v1/*/x\"]}", false},
		{"{api.example.com: [/v1, /v1]}", false},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte("apiVersion: qory.dev/v1alpha1\ngateway: {egress: {mode: observe, paths: "+c.paths+"}}\n"), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.paths, err, c.valid)
		}
	}
}
