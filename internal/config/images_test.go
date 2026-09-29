package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/config"
)

// imagesFile is a wall section that defines two images, one with a Docker of its own,
// and names the first as the default.
const imagesFile = `apiVersion: qory.dev/v1alpha1
wall:
  adapter: docker
  image: go
  images:
    go:
      ref: ghcr.io/qoryai/agent-go@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    go-docker:
      ref: ghcr.io/qoryai/agent-go-docker:1
      runtime: sysbox-runc
      docker: true
    plain.v2:
      ref: example.com/agent:2
      runtime: runc
      docker: false
`

// TestRunnerFileReadsTheImages reads wall.images in the file's order, keeps wall.image as
// written, and lists each image with its reference, its runtime and its daemon.
func TestRunnerFileReadsTheImages(t *testing.T) {
	hermetic(t)
	path := runnerFile(t, imagesFile)
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	w := c.Runner.Wall
	want := []config.RunnerImage{
		{Name: "go", Ref: "ghcr.io/qoryai/agent-go@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{Name: "go-docker", Ref: "ghcr.io/qoryai/agent-go-docker:1", Runtime: "sysbox-runc", Docker: true},
		{Name: "plain.v2", Ref: "example.com/agent:2", Runtime: "runc"},
	}
	if w == nil || w.Image != "go" || len(w.Images) != len(want) {
		t.Fatalf("wall read as %+v", w)
	}
	for i := range want {
		if w.Images[i] != want[i] {
			t.Errorf("image %d read as %+v, want %+v", i, w.Images[i], want[i])
		}
	}
	if !w.Defines("go-docker") || w.Defines("example.com/agent:2") || (*config.RunnerWall)(nil).Defines("go") {
		t.Error("Defines does not answer by name alone")
	}
	var keys []string
	rows := map[string]config.Row{}
	for _, row := range c.Rows() {
		rows[row.Key] = row
		if strings.HasPrefix(row.Key, "runner.wall.image") {
			keys = append(keys, row.Key)
		}
	}
	if strings.Join(keys, " ") != "runner.wall.image runner.wall.images.go runner.wall.images.go-docker runner.wall.images.plain.v2" {
		t.Errorf("the image rows are %v", keys)
	}
	for key, want := range map[string]string{
		"runner.wall.image":            "go",
		"runner.wall.images.go":        want[0].Ref,
		"runner.wall.images.go-docker": "ghcr.io/qoryai/agent-go-docker:1, runtime sysbox-runc, docker (experimental)",
		"runner.wall.images.plain.v2":  "example.com/agent:2, runtime runc",
	} {
		if rows[key].Value != want || rows[key].Origin != path {
			t.Errorf("%s: %+v, want %q from %s", key, rows[key], want, path)
		}
	}
}

// TestRunnerFileRefusesAnImageItCannotRun is every refusal of wall.images, each naming
// the file and the key: what the runner would refuse before a run is refused when the
// file is read, so qory config says it as well.
func TestRunnerFileRefusesAnImageItCannotRun(t *testing.T) {
	hermetic(t)
	wall := "wall:\n  adapter: docker\n  images:"
	for _, c := range []struct{ body, want string }{
		{wall + " [go]\n", "wall.images is a mapping from a name to an image"},
		{wall + "\n", "wall.images is a mapping from a name to an image"},
		{wall + " {Go: {ref: a}}\n", `wall.images: "Go" is not 1 to 64 of a-z, 0-9, underscore, dot and dash`},
		{wall + " {-go: {ref: a}}\n", `wall.images: "-go" is not 1 to 64`},
		{wall + " {" + strings.Repeat("a", 65) + ": {ref: a}}\n", "is not 1 to 64"},
		{wall + " {go: {ref: a}, go: {ref: b}}\n", "wall.images.go is defined twice"},
		{wall + " {go: example.com/agent:1}\n", "wall.images.go is a mapping: ref, and runtime and docker when it needs them"},
		{wall + " {go: }\n", "wall.images.go is a mapping"},
		{wall + " {go: {}}\n", "wall.images.go.ref is required"},
		{wall + " {go: {runtime: sysbox-runc}}\n", "wall.images.go.ref is required"},
		{wall + " {go: {ref: \"--privileged\"}}\n", `wall.images.go.ref "--privileged" is not an image reference`},
		{wall + " {go: {ref: [a]}}\n", `wall.images.go.ref "" is not an image reference`},
		{wall + " {go: {ref: a, runtime: \"--privileged\"}}\n", `wall.images.go.runtime "--privileged" is not a container runtime's name`},
		{wall + " {go: {ref: a, docker: yes}}\n", "wall.images.go.docker is true or false"},
		{wall + " {go: {ref: a, docker: true}}\n", "wall.images.go: docker needs a runtime that runs a daemon without privileges, such as runtime: sysbox-runc"},
		{wall + " {go: {ref: a, privileged: true}}\n", `wall.images.go: key "privileged" is not one; an image has ref, runtime, docker`},
		{wall + " {go: {ref: a, mounts: [/var/run/docker.sock]}}\n", `wall.images.go: key "mounts" is not one`},
		{wall + " {go: {ref: a, ref: b}}\n", "wall.images.go.ref appears twice"},
	} {
		path := runnerFile(t, c.body)
		_, err := config.Load(t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), path) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}

// TestTheRunnerSchemaTakesTheImages holds runner.schema.json to what the reader takes
// of wall.images: the definitions of the docs pass it, and each shape the reader refuses
// fails it. A name defined twice is YAML's to refuse, not the schema's.
func TestTheRunnerSchemaTakesTheImages(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(filepath.Join("..", "..", "contracts", "harness", "v1", "runner.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	wall := "wall:\n  adapter: docker\n  image: go\n  images:"
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{strings.TrimPrefix(imagesFile, "apiVersion: qory.dev/v1alpha1\n"), true},
		{wall + " {}\n", true},
		{wall + " {go: {ref: a, docker: false}}\n", true},
		{"wall: {adapter: docker, image: example.com/agent:1}\n", true},
		{wall + " [go]\n", false},
		{wall + " {Go: {ref: a}}\n", false},
		{wall + " {go: example.com/agent:1}\n", false},
		{wall + " {go: {}}\n", false},
		{wall + " {go: {ref: \"--privileged\"}}\n", false},
		{wall + " {go: {ref: a, runtime: \"--privileged\"}}\n", false},
		{wall + " {go: {ref: a, docker: \"yes\"}}\n", false},
		{wall + " {go: {ref: a, docker: true}}\n", false},
		{wall + " {go: {ref: a, privileged: true}}\n", false},
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
