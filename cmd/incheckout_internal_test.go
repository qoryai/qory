package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/stack"
)

// TestModulesInCheckoutFollowsTheFileThatListsThem checks a module by the file that
// lists it: a base module of a stack -f named inside the checkout is checked, one of a
// stack outside is not, a module listed by its name alone resolves under the stack's root,
// a git source is checked for https and not for its path, and a base module of a git stack
// that the checkout extends is checked for https.
func TestModulesInCheckoutFollowsTheFileThatListsThem(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "modules", "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := &stack.Stack{File: filepath.Join(root, "qory.yaml"), Root: root}
	inBase := &stack.Stack{File: filepath.Join(root, "stacks", "qory-stack.yaml")}
	outBase := &stack.Stack{File: filepath.Join(outside, "qory-stack.yaml")}
	merged := func(mods ...stack.Module) *stack.Stack {
		return &stack.Stack{File: doc.File, Root: root, Modules: mods}
	}
	far := stack.Module{Name: "far", Source: stack.Source{Path: outside}, Base: true}
	for _, c := range []struct {
		name    string
		p       *stack.Stack
		base    *stack.Stack
		docIn   bool
		refused bool
	}{
		{"a base module of a stack inside", merged(far), inBase, true, true},
		{"a base module of a stack outside", merged(far), outBase, true, false},
		{"a module by its name alone", merged(stack.Module{Name: "core"}), nil, true, false},
		{"a module by its name alone, the root outside", &stack.Stack{File: doc.File, Root: outside, Modules: []stack.Module{{Name: "core"}}}, nil, true, true},
		{"a git source over https", merged(stack.Module{Name: "g", Source: stack.Source{Git: "https://example.com/h", Ref: "main", Path: outside}}), nil, true, false},
		{"a git source over ssh", merged(stack.Module{Name: "g", Source: stack.Source{Git: "ssh://example.com/h", Ref: "main"}}), nil, true, true},
		{"a git source over ssh of a base outside", merged(stack.Module{Name: "g", Source: stack.Source{Git: "ssh://example.com/h", Ref: "main"}, Base: true}), outBase, true, false},
		{"a document outside the checkout", merged(stack.Module{Name: "far", Source: stack.Source{Path: outside}}), nil, false, false},
	} {
		err := modulesInCheckout(c.p, doc, c.base, root, c.docIn)
		if (err != nil) != c.refused {
			t.Errorf("%s: %v, refused %v", c.name, err, c.refused)
		}
	}
	gitDoc := &stack.Stack{File: doc.File, Root: root, Extends: stack.Source{Git: "https://example.com/base", Ref: "main"}}
	ssh := stack.Module{Name: "g", Source: stack.Source{Git: "ssh://example.com/h", Ref: "main"}, Base: true}
	if err := modulesInCheckout(merged(ssh), gitDoc, nil, root, true); err == nil {
		t.Error("a base module over ssh of a git stack the checkout extends is not refused")
	}
}
