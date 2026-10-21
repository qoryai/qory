package devcontainer_test

import (
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/devcontainer"
)

// TestLoadMembersNamesWhatIsSet is the configuration, read, and the members asked for
// that it sets: at the top and under build, in the order asked, and neither a member
// set to an empty value nor one left out.
func TestLoadMembersNamesWhatIsSet(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".devcontainer/devcontainer.json", `{
		// legacy
		"dockerFile": "Dockerfile",
		"context": "",
		"build": {"dockerfile": "Dockerfile", "options": ["--network=host"],},
		"dockerComposeFile": null,
	}`)
	c, set, err := devcontainer.LoadMembers(root, []string{"build.options", "dockerComposeFile", "context", "dockerFile", "build.target", "image"})
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.Path != ".devcontainer/devcontainer.json" || c.Build == nil || c.Build.Dockerfile != "Dockerfile" {
		t.Errorf("the configuration is %+v", c)
	}
	var names, values []string
	for _, m := range set {
		names, values = append(names, m.Name), append(values, string(m.Value))
	}
	if want := []string{"build.options", "dockerFile"}; !reflect.DeepEqual(names, want) {
		t.Errorf("set %v, want %v", names, want)
	}
	if want := []string{`["--network=host"]`, `"Dockerfile"`}; !reflect.DeepEqual(values, want) {
		t.Errorf("values %v, want %v", values, want)
	}
}

// TestLoadMembersWithoutConfiguration is a repository that keeps none: nothing, and no
// error.
func TestLoadMembersWithoutConfiguration(t *testing.T) {
	c, set, err := devcontainer.LoadMembers(t.TempDir(), []string{"dockerFile"})
	if c != nil || set != nil || err != nil {
		t.Errorf("LoadMembers: %v, %v, %v", c, set, err)
	}
}

// TestLoadMembersRefusesWhatLoadRefuses is a configuration that does not read.
func TestLoadMembersRefusesWhatLoadRefuses(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".devcontainer.json", `{"image": "a", "image": "b"}`)
	if _, _, err := devcontainer.LoadMembers(root, nil); err == nil {
		t.Error("a repeated member is read")
	}
}
