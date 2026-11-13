package codex_test

import (
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestWalledArgs is each form of a walled launch of Codex: codex exec, as a run from a run
// code starts it, gets --sandbox danger-full-access right after exec, also with a sandbox
// flag after --; a start with no exec, and arguments that set a sandbox mode, bypass one
// or ask for --approve-for-me, before exec or after it, stay as given. The prompt
// $<name> stays as given.
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
		{[]string{"exec", "--sandbox", "read-only", "$explore x"}, []string{"exec", "--sandbox", "read-only", "$explore x"}},
		{[]string{"$explore", "exec"}, []string{"$explore", "exec", "--sandbox", "danger-full-access"}},
		{[]string{"--model", "m", "exec", "hi"}, []string{"--model", "m", "exec", "--sandbox", "danger-full-access", "hi"}},
		{[]string{"exec", "--", "--sandbox"}, []string{"exec", "--sandbox", "danger-full-access", "--", "--sandbox"}},
		{[]string{"--model", "m"}, []string{"--model", "m"}},
		{[]string{"exec", "--sandbox", "read-only", "hi"}, []string{"exec", "--sandbox", "read-only", "hi"}},
		{[]string{"exec", "-s", "workspace-write", "hi"}, []string{"exec", "-s", "workspace-write", "hi"}},
		{[]string{"exec", "--sandbox=read-only", "hi"}, []string{"exec", "--sandbox=read-only", "hi"}},
		{[]string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hi"}, []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hi"}},
		{[]string{"exec", "--yolo", "hi"}, []string{"exec", "--yolo", "hi"}},
		{[]string{"exec", "-sread-only", "hi"}, []string{"exec", "-sread-only", "hi"}},
		{[]string{"exec", "-s=read-only", "hi"}, []string{"exec", "-s=read-only", "hi"}},
		{[]string{"--sandbox", "read-only", "exec", "hi"}, []string{"--sandbox", "read-only", "exec", "hi"}},
		{[]string{"-s", "read-only", "exec", "hi"}, []string{"-s", "read-only", "exec", "hi"}},
		{[]string{"--yolo", "exec", "hi"}, []string{"--yolo", "exec", "hi"}},
		{[]string{"exec", "--approve-for-me", "hi"}, []string{"exec", "--approve-for-me", "hi"}},
		{[]string{"exec", "--not-so-yolo", "hi"}, []string{"exec", "--not-so-yolo", "hi"}},
		{[]string{"--approve-for-me", "exec", "hi"}, []string{"--approve-for-me", "exec", "hi"}},
		{[]string{"exec", "--search", "hi"}, []string{"exec", "--sandbox", "danger-full-access", "--search", "hi"}},
	} {
		if got := w.WalledArgs(c.args); !slices.Equal(got, c.want) {
			t.Errorf("WalledArgs(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}
