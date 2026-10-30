package prebuild

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/qoryai/forager/mask"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/secretmask"
)

// TestALongLineIsMaskedBeforeItsCut is a line of a command's output, longer than a
// LineWriter keeps, with a value of the mask list across the place where the LineWriter
// cuts it: the value is *** in the parts, and no part contains a piece of it.
func TestALongLineIsMaskedBeforeItsCut(t *testing.T) {
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	const value = "prebuild-secret-value"
	secretmask.Default().Add(value)
	var parts []string
	w, flush := maskedLines(child.NewLineWriter("", func(line string) { parts = append(parts, line) }))
	io.WriteString(w, strings.Repeat("a", 16<<10-5)+value+" end\n")
	flush()
	got := strings.Join(parts, "")
	if got != strings.Repeat("a", 16<<10-5)+"*** end" {
		t.Errorf("the parts end %q", got[max(0, len(got)-20):])
	}
	for _, p := range parts {
		if strings.Contains(p, value[:5]) || strings.Contains(p, value[len(value)-5:]) {
			t.Errorf("a part contains a piece of the value: %.30q", p[max(0, len(p)-30):])
		}
	}
}

// joinOnly is an opened run whose Join calls join with the session's spec.
type joinOnly struct {
	Opened
	join func(spec session.SessionSpec)
}

func (j joinOnly) Join(_ context.Context, spec session.SessionSpec) (*session.Result, error) {
	j.join(spec)
	return &session.Result{}, nil
}

// TestTheStepsOutputIsMaskedOnce is the step's output as Forager's session copies it,
// through a writer of Forager's mask package on Settings.Masker, to the session's
// stdout: a value of the mask list prints as *** once, and a forwarder's password prints
// as qory-proxy-password, intact, although the list also contains proxy-password. A
// second pass would make it qory-***.
func TestTheStepsOutputIsMaskedOnce(t *testing.T) {
	list := secretmask.New()
	old := secretmask.SetDefault(list)
	t.Cleanup(func() { secretmask.SetDefault(old) })
	const value = "split-secret-value"
	list.Add(value)
	list.Add("proxy-password")
	masker := list.Masker()
	masker.AddProxy("qory", "forwarder-password-1")
	var lines []string
	s := Settings{Masker: masker, Log: func(line string) { lines = append(lines, line) }}
	run := joinOnly{join: func(spec session.SessionSpec) {
		copied := mask.NewWriter(spec.Stdout, masker)
		io.WriteString(copied, "step: "+value[:8])
		io.WriteString(copied, value[8:]+" via http://qory:forwarder-password-1@127.0.0.1:3128\n")
		copied.Flush()
	}}
	s.step(context.Background(), run, Run{RunID: "run-1"}, t.TempDir())
	want := []string{"run-1: step: *** via http://qory:qory-proxy-password@127.0.0.1:3128"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("the log got %q, want %q", lines, want)
	}
}
