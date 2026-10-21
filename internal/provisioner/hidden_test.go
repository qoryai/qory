package provisioner_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/provisioner"
)

// testKey is a value of a run key's shape.
const testKey = "qrk_c2VjcmV0LXBheWxvYWQ.c2VjcmV0LXNpZ25hdHVyZQ"

// TestAHiddenNeverPrints prints, logs and marshals a Hidden alone, by its pointer, in
// an exported and an unexported field of a struct, in a slice and in a map, with every
// verb: [hidden] or an address each time, never the bytes.
func TestAHiddenNeverPrints(t *testing.T) {
	h := provisioner.Hide([]byte(testKey))
	type exported struct{ Key provisioner.Hidden }
	type unexported struct{ key provisioner.Hidden }
	values := []any{h, &h, exported{h}, &exported{h}, unexported{h}, &unexported{h}, []provisioner.Hidden{h}, map[string]provisioner.Hidden{"k": h}}
	var out bytes.Buffer
	for _, v := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T", "%p", "%10.3s"} {
			fmt.Fprintf(&out, verb+"\n", v)
		}
		fmt.Fprint(&out, v, "\n")
		fmt.Fprintln(&out, v)
		b, _ := json.Marshal(v)
		out.Write(b)
	}
	slog.New(slog.NewTextHandler(&out, nil)).Info("x", "key", h, "in", exported{h}, slog.Any("any", h))
	slog.New(slog.NewJSONHandler(&out, nil)).Info("x", "key", h, "in", exported{h}, slog.Any("any", h))
	text, _ := h.MarshalText()
	out.Write(text)
	for _, s := range []string{"qrk_", "c2VjcmV0", fmt.Sprintf("%x", testKey)[:16], fmt.Sprint([]byte(testKey))[:12]} {
		if strings.Contains(out.String(), s) {
			t.Errorf("%q is in the output:\n%s", s, out.String())
		}
	}
	if !strings.Contains(out.String(), "[hidden]") {
		t.Errorf("no [hidden] in the output:\n%s", out.String())
	}
}

// TestClearClearsEveryCopy writes a Hidden's bytes, then clears one copy: the bytes it
// was given are zeros, and every copy holds and writes nothing.
func TestClearClearsEveryCopy(t *testing.T) {
	b := []byte(testKey)
	h := provisioner.Hide(b)
	copied := h
	var w bytes.Buffer
	if n, err := copied.WriteTo(&w); err != nil || n != int64(len(testKey)) || w.String() != testKey || h.Len() != len(testKey) {
		t.Fatalf("written %d %v %q", n, err, w.String())
	}
	h.Clear()
	if !bytes.Equal(b, make([]byte, len(testKey))) {
		t.Errorf("the bytes are not zeros: %q", b)
	}
	w.Reset()
	if n, _ := copied.WriteTo(&w); n != 0 || copied.Len() != 0 || w.Len() != 0 {
		t.Errorf("a copy holds %d bytes after Clear", copied.Len())
	}
	var zero provisioner.Hidden
	zero.Clear()
	if n, err := zero.WriteTo(&w); n != 0 || err != nil || zero.Len() != 0 || zero.String() != "[hidden]" {
		t.Error("the zero Hidden")
	}
}
