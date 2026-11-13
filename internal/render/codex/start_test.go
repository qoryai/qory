package codex_test

import (
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestWalledArgs is each form of a walled launch of Codex: codex exec, as a run from a run
// code starts it, gets --sandbox danger-full-access right after exec; a start with no
// exec, and an exec that sets a sandbox mode or bypasses one, stay as given, and so does a
// flag after --.
func TestWalledArgs(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	w, ok := rt.(render.Walled)
	if !ok {
		t.Fatal("codex is no render.Walled")
	}
	for _, c := range []struct{ args, want []string }{
		{[]string{"exec", "--json", "$explore x"}, []string{"exec", "--sandbox", "danger-full-access", "--json", "$explore x"}},
		{[]string{"--model", "m", "exec", "hi"}, []string{"--model", "m", "exec", "--sandbox", "danger-full-access", "hi"}},
		{[]string{"exec", "--", "--sandbox"}, []string{"exec", "--sandbox", "danger-full-access", "--", "--sandbox"}},
		{[]string{"--model", "m"}, []string{"--model", "m"}},
		{[]string{"exec", "--sandbox", "read-only", "hi"}, []string{"exec", "--sandbox", "read-only", "hi"}},
		{[]string{"exec", "-s", "workspace-write", "hi"}, []string{"exec", "-s", "workspace-write", "hi"}},
		{[]string{"exec", "--sandbox=read-only", "hi"}, []string{"exec", "--sandbox=read-only", "hi"}},
		{[]string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hi"}, []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hi"}},
	} {
		if got := w.WalledArgs(c.args); !slices.Equal(got, c.want) {
			t.Errorf("WalledArgs(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}
