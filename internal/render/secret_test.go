package render_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// TestRefusalsPrintNoSecret is a runtime, a files entry, a module and a checkout path
// whose names contain an access key secret: each refusal prints them as Forager's
// phrases.
func TestRefusalsPrintNoSecret(t *testing.T) {
	name := "(a name that contains an access key secret)"
	value := "(a value that contains an access key secret)"
	_, err := render.Lookup("qak_rt")
	if err == nil || !strings.HasPrefix(err.Error(), "runtime "+name+" is not one this qory renders; runtimes: ") {
		t.Errorf("lookup: %v", err)
	}
	err = render.CheckFiles(&compose.Result{Entries: []compose.Entry{{Kind: render.FilesKind, Name: "qak_rt/x.md", Module: "qak_m"}}})
	if err == nil || !strings.HasPrefix(err.Error(), "module "+name+" ships files/"+name+", and "+name+" is not a runtime qory renders; runtimes: ") {
		t.Errorf("files: %v", err)
	}
	for _, c := range []struct {
		err  render.ForeignPathError
		want string
	}{
		{render.ForeignPathError{Path: "/work/app/qak_link"}, "/work/app/" + name + " is not a link qory wrote; qory does not replace it"},
		{render.ForeignPathError{Path: "/work/app/qak_link", Target: "/elsewhere/qak_t"}, "/work/app/" + name + " links to " + value + ", which qory did not write; qory does not replace it"},
		{render.ForeignPathError{Path: "/work/app/qak_link", Reason: "has uncommitted changes"}, "/work/app/" + name + " has uncommitted changes; qory does not replace it"},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
