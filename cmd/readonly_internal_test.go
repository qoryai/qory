package cmd

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/stack"
)

// TestARunReadsOnlyWhenEitherLevelIsRead is every combination of the workflow's access
// in the stack, read, write or undeclared, and access in the run's documents, read,
// write or empty as with no target. The run reads only unless the workflow is write and
// the documents do not lower it. A write in the documents does not raise a workflow
// that the stack declares read or does not declare. A run not from a run code does not
// read only.
func TestARunReadsOnlyWhenEitherLevelIsRead(t *testing.T) {
	declared := map[string]map[string]stack.Workflow{
		"declared read":  {"hello": {Access: stack.AccessRead}},
		"declared write": {"hello": {Access: stack.AccessWrite}},
		"undeclared":     {},
	}
	for _, c := range []struct {
		declared, access string
		want             bool
	}{
		{"declared read", stack.AccessRead, true},
		{"declared read", stack.AccessWrite, true},
		{"declared read", "", true},
		{"declared write", stack.AccessRead, true},
		{"declared write", stack.AccessWrite, false},
		{"declared write", "", false},
		{"undeclared", stack.AccessRead, true},
		{"undeclared", stack.AccessWrite, true},
		{"undeclared", "", true},
	} {
		t.Run(c.declared+" with access "+c.access, func(t *testing.T) {
			r := &codeRun{workflow: "hello", access: c.access}
			if got := readsOnly(r, declared[c.declared]); got != c.want {
				t.Errorf("readsOnly is %v, want %v", got, c.want)
			}
		})
	}
	if readsOnly(nil, declared["declared read"]) {
		t.Error("a run not from a run code reads only")
	}
}

// TestTheNestedDockerLineShowsTheImage is the line of a read run on an image with nested
// Docker: it shows the image's name, its reference when the image has no name, and a
// phrase in place of a value that contains a secret.
func TestTheNestedDockerLineShowsTheImage(t *testing.T) {
	for _, c := range []struct {
		name string
		img  session.Image
		want string
	}{
		{"a name", session.Image{Name: "nested", Ref: "example.com/nested:1", Docker: true}, "image nested has"},
		{"a reference", session.Image{Ref: "example.com/nested:1", Docker: true}, "image example.com/nested:1 has"},
		{"a secret in the name", session.Image{Name: "qak_" + strings.Repeat("a", 8), Docker: true}, "image (a name that contains an access key secret) has"},
		{"a secret in the reference", session.Image{Ref: "example.com/qak_" + strings.Repeat("a", 8), Docker: true}, "image (a name that contains an access key secret) has"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := &runSpec{}
			spec.Wall = &wall.Docker{}
			err := readOnlyCheckout(spec, "/checkout", "hello", c.img)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("the line is %v, want %q in it", err, c.want)
			}
			if spec.WorkspaceReadOnly {
				t.Error("the refused run binds the workspace read-only")
			}
		})
	}
}
