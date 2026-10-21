package config_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// TestForagerFileReadsTheImagesAllow reads wall.images_allow in the file's order, as
// written, a prefix written twice included, as Forager takes it, and lists it in one
// row; absent, null or empty, it allows nothing and has no row.
func TestForagerFileReadsTheImagesAllow(t *testing.T) {
	hermetic(t)
	want := []string{"registry.example/agents/", "registry.example/tools/go@", "ghcr.io/acme", "registry.example:5000", "ghcr.io/acme"}
	path := foragerFile(t, "wall:\n  adapter: docker\n  image: example.com/agent:1\n  images_allow:\n    - "+strings.Join(want, "\n    - ")+"\n")
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Forager.Wall.ImagesAllow; !slices.Equal(got, want) {
		t.Errorf("wall.images_allow read as %q, want %q", got, want)
	}
	var found bool
	for _, row := range c.Rows() {
		if row.Key == "wall.images_allow" {
			found = true
			if row.Value != strings.Join(want, ", ") || row.Origin != path {
				t.Errorf("the row is %+v", row)
			}
		}
	}
	if !found {
		t.Error("no row of wall.images_allow")
	}
	for _, body := range []string{"", "\n  images_allow:\n", "\n  images_allow: []\n"} {
		foragerFile(t, "wall:\n  adapter: docker\n  image: example.com/agent:1"+body+"\n")
		c, err := config.Load(t.TempDir(), true)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if len(c.Forager.Wall.ImagesAllow) != 0 {
			t.Errorf("%q: wall.images_allow read as %q", body, c.Forager.Wall.ImagesAllow)
		}
		for _, row := range c.Rows() {
			if row.Key == "wall.images_allow" {
				t.Errorf("%q: a row %+v", body, row)
			}
		}
	}
}

// TestForagerFileRefusesAnImagesAllowEntry is every refusal of wall.images_allow, each
// naming the file, the key and the entry's index and never the entry: what Forager
// refuses, an empty entry, *, and one that is not the start of a reference pinned by
// digest in lower case up to a whole part of it, and what is not a list of strings.
func TestForagerFileRefusesAnImagesAllowEntry(t *testing.T) {
	hermetic(t)
	const notPrefix = "is not the start of an image reference pinned by digest, in lower case, up to a whole part of it: a registry, a name or a path ending in a slash, or a name and its @"
	wall := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  images_allow: "
	for _, c := range []struct{ body, want string }{
		{wall + "[registry.example/, \"\"]\n", ": wall.images_allow[1] " + notPrefix},
		{wall + "[\"*\"]\n", ": wall.images_allow[0] " + notPrefix},
		{wall + "[Registry.Example/" + marker + "]\n", ": wall.images_allow[0] " + notPrefix},
		{wall + "[\"registry.example/" + strings.ToLower(marker) + ":1\"]\n", ": wall.images_allow[0] " + notPrefix},
		{wall + "[\"registry.example/" + strings.ToLower(marker) + "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"]\n", ": wall.images_allow[0] " + notPrefix},
		{wall + "[\"https://registry.example/" + strings.ToLower(marker) + "/\"]\n", ": wall.images_allow[0] " + notPrefix},
		{wall + "[\"registry.example/" + strings.ToLower(marker) + "-\"]\n", ": wall.images_allow[0] " + notPrefix},
		{wall + "[registry.example/, [" + marker + "]]\n", ": wall.images_allow[1] " + notPrefix},
		{wall + "registry.example/" + marker + "\n", ": line 4: wall.images_allow is not a list of strings"},
	} {
		path := foragerFile(t, c.body)
		_, err := config.Load(t.TempDir(), true)
		if err == nil || err.Error() != path+c.want {
			t.Errorf("%q: %v, want %q", c.body, err, path+c.want)
		}
		if err != nil && strings.Contains(strings.ToLower(err.Error()), strings.ToLower(marker)) {
			t.Errorf("%q: the refusal holds the value: %v", c.body, err)
		}
	}
}

// TestTheForagerSchemaTakesTheImagesAllow holds forager.schema.json to what the reader
// takes of wall.images_allow: a list of prefixes passes it, a prefix written twice
// among them, as Forager takes one, and what is not a list of strings, an empty entry,
// *, and one in upper case fail it.
func TestTheForagerSchemaTakesTheImagesAllow(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "forager.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	wall := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  images_allow: "
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{wall + "[registry.example/agents/, registry.example/tools/go@, ghcr.io/acme, \"registry.example:5000\"]\n", true},
		{wall + "[]\n", true},
		{wall + "registry.example/\n", false},
		{wall + "[\"\"]\n", false},
		{wall + "[\"*\"]\n", false},
		{wall + "[Registry.Example/]\n", false},
		{wall + "[[registry.example/]]\n", false},
		{wall + "[registry.example/, registry.example/]\n", true},
	} {
		var doc any
		if err := yaml.Unmarshal([]byte(c.body), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%q: %v, want valid %v", c.body, err, c.valid)
		}
	}
}
