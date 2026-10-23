//go:build unix

package devcontainer_test

import (
	"encoding/json"
	"path"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

// relocated is a configuration Relocate wrote, read back.
type relocated struct {
	Build struct {
		Dockerfile string            `json:"dockerfile"`
		Context    string            `json:"context"`
		Args       map[string]string `json:"args"`
	} `json:"build"`
	Features                    map[string]json.RawMessage `json:"features"`
	OverrideFeatureInstallOrder []any                      `json:"overrideFeatureInstallOrder"`
	Name                        string                     `json:"name"`
}

// TestRelocateDropsAMemberAndKeepsThePaths is a configuration with comments,
// initializeCommand and paths relative to its folder, written for another folder:
// without initializeCommand, and each path, resolved from the other folder as the
// reference implementation resolves it, names the place it named.
func TestRelocateDropsAMemberAndKeepsThePaths(t *testing.T) {
	const data = `{
	// The shop.
	"initializeCommand": "touch /tmp/host", /* runs on the host */
	"build": {"dockerfile": "Dockerfile", "context": "..", "args": {"A": "1"},},
	"features": {
		"./feature": {"version": "1"},
		"ghcr.io/devcontainers/features/go:1": {},
		"../.devcontainer/other/": {},
	},
	"overrideFeatureInstallOrder": ["./feature", "ghcr.io/devcontainers/features/go", 7],
	"name": "a<b>&c",
}`
	const root, dir = "/work/shop", "/tmp/qory-prebuild-x/devcontainer"
	c, err := devcontainer.Parse(".devcontainer/devcontainer.json", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Has("initializeCommand") || c.Has("postAttachCommand") {
		t.Error("Has does not tell which members the configuration has")
	}
	out, err := c.Relocate(root, dir, "initializeCommand")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "initializeCommand") || strings.Contains(string(out), "host") {
		t.Errorf("the copy keeps initializeCommand:\n%s", out)
	}
	var got relocated
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("the copy is not JSON: %v\n%s", err, out)
	}
	if p := path.Join(dir, got.Build.Dockerfile); p != root+"/.devcontainer/Dockerfile" {
		t.Errorf("build.dockerfile names %s", p)
	}
	if p := path.Join(dir, got.Build.Context); p != root {
		t.Errorf("build.context names %s", p)
	}
	if got.Build.Args["A"] != "1" || got.Name != "a<b>&c" {
		t.Errorf("the copy changed other members:\n%s", out)
	}
	want := map[string]string{
		root + "/.devcontainer/feature":       `{"version": "1"}`,
		"ghcr.io/devcontainers/features/go:1": `{}`,
		root + "/.devcontainer/other/":        `{}`,
	}
	for id, options := range got.Features {
		key := id
		if strings.HasPrefix(id, "../") {
			key = path.Join(dir, id)
			if strings.HasSuffix(id, "/") {
				key += "/"
			}
		}
		if w, ok := want[key]; !ok || w != string(options) {
			t.Errorf("features has %s, %s", id, options)
		}
	}
	if len(got.Features) != len(want) {
		t.Errorf("features are %v", got.Features)
	}
	if o := got.OverrideFeatureInstallOrder; len(o) != 3 || path.Join(dir, o[0].(string)) != root+"/.devcontainer/feature" || o[1] != "ghcr.io/devcontainers/features/go" || o[2] != 7.0 {
		t.Errorf("overrideFeatureInstallOrder is %v", o)
	}
	if !strings.HasSuffix(string(out), `,7]`+`,"name":"a<b>&c"}`) {
		t.Errorf("the copy does not keep a value that is not a string, or the order:\n%s", out)
	}
}

// TestRelocateAtTheRoot is a configuration at .devcontainer.json, whose folder is the
// repository's root, and initializeCommand null, which it has still.
func TestRelocateAtTheRoot(t *testing.T) {
	c, err := devcontainer.Parse(".devcontainer.json", []byte(`{"initializeCommand": null, "build": {"dockerfile": "docker/Dockerfile"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Has("initializeCommand") {
		t.Error("Has misses initializeCommand set to null")
	}
	out, err := c.Relocate("/work/shop", "/tmp/x", "initializeCommand")
	if err != nil {
		t.Fatal(err)
	}
	var got relocated
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if p := path.Join("/tmp/x", got.Build.Dockerfile); p != "/work/shop/docker/Dockerfile" || got.Build.Context != "" || strings.Contains(string(out), "initializeCommand") {
		t.Errorf("the copy is %s", out)
	}
}

// TestRelocateEdited is a copy with image and build.dockerfile edited: the IDs of
// Features that are not local stay as written, in features and
// overrideFeatureInstallOrder, a local Feature is moved, and build.context, left out,
// is written as the Dockerfile's folder, which it named; a context that is set is
// moved as before.
func TestRelocateEdited(t *testing.T) {
	const root, dir, copied = "/work/shop", "/tmp/x/devcontainer", "/tmp/x/dockerfile/Dockerfile"
	edit := devcontainer.Edit{
		Drop:       []string{"initializeCommand"},
		Image:      func(s string) string { return "mirror/" + s },
		Dockerfile: copied,
	}
	c, err := devcontainer.Parse(".devcontainer/devcontainer.json", []byte(`{
		"initializeCommand": "x",
		"image": "debian:12",
		"build": {"dockerfile": "docker/Dockerfile", "args": {"A": "1"}},
		"features": {"ghcr.io/devcontainers/features/go:1": {"v": "1"}, "./local": {}},
		"overrideFeatureInstallOrder": ["./local", "ghcr.io/devcontainers/features/go"],
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.RelocateEdited(root, dir, edit)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		relocated
		Image string `json:"image"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("the copy is not JSON: %v\n%s", err, out)
	}
	if got.Image != "mirror/debian:12" || got.Build.Dockerfile != copied || path.Join(dir, got.Build.Context) != root+"/.devcontainer/docker" ||
		got.Build.Args["A"] != "1" || strings.Contains(string(out), "initializeCommand") {
		t.Errorf("the copy is %s", out)
	}
	if string(got.Features["ghcr.io/devcontainers/features/go:1"]) != `{"v": "1"}` || got.Features["../../../work/shop/.devcontainer/local"] == nil || len(got.Features) != 2 {
		t.Errorf("features are %s", out)
	}
	if o := got.OverrideFeatureInstallOrder; len(o) != 2 || path.Join(dir, o[0].(string)) != root+"/.devcontainer/local" || o[1] != "ghcr.io/devcontainers/features/go" {
		t.Errorf("overrideFeatureInstallOrder is %v", o)
	}

	c, err = devcontainer.Parse(".devcontainer/devcontainer.json", []byte(`{"build": {"dockerfile": "Dockerfile", "context": ".."}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, err = c.RelocateEdited(root, dir, devcontainer.Edit{Dockerfile: copied}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Build.Dockerfile != copied || path.Join(dir, got.Build.Context) != root || strings.Count(string(out), "context") != 1 {
		t.Errorf("the copy is %s", out)
	}
}
