package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/ui"
)

// TestPrintCollisionWritesTheEntryOfABaseModule is a collision of the checkout's module
// app with two base modules: core-x, which the base lists by the export core and
// qory.yaml lists with an exclude already, and tools, which the base lists by a path
// alone. The fix replaces the entry of core with the whole exclude, under the name
// qory.yaml lists it by; the collision with tools says to rename the entry.
func TestPrintCollisionWritesTheEntryOfABaseModule(t *testing.T) {
	e := &compose.CollisionError{
		Collisions: []compose.Collision{
			{Kind: "skills", Name: "explore-a-ticket", Modules: []string{"core-x", "app"}, Base: "web@abc"},
			{Kind: "skills", Name: "triage", Modules: []string{"tools", "app"}, Base: "web@abc", Rename: true},
		},
		Pins:     map[string]string{"core-x": "abc", "tools": "abc", "app": "working-tree"},
		Sources:  map[string]string{"core-x": "base", "tools": "base", "app": "modules/app"},
		Order:    []string{"core-x", "tools", "app"},
		InBase:   map[string]bool{"core-x": true},
		Listed:   map[string]string{"core-x": "core"},
		Excluded: map[string]map[string][]string{"core-x": {"skills": {"review"}}},
	}
	var b bytes.Buffer
	printCollision(ui.New(&b), e, nil)
	out := b.String()
	for _, want := range []string{
		"Module core-x belongs to the base stack web@abc; list it in qory.yaml as core.",
		"It belongs to the base stack web@abc; rename yours.",
		"Keep one and exclude it from the others. For example, in qory.yaml, under harness.modules:",
		"Replace the entry of module core with:",
		"- name: core\n",
		"    skills: [review, explore-a-ticket]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "name: core-x") || strings.Contains(out, "name: tools") || strings.Contains(out, "  ...") {
		t.Errorf("an entry qory.yaml cannot list:\n%s", out)
	}
}
