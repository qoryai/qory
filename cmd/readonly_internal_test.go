package cmd

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
)

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
