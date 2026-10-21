package provisioner

import (
	"bytes"
	"strings"
	"testing"
)

const testRun = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d"

// answerWith is a handover's 200 at revision 3 whose run_key member is key, as written
// in JSON, with inputs.
func answerWith(key, inputs string) []byte {
	return []byte(`{"version":1,"revision":3,"run_id":"` + testRun + `","run_key":` + key +
		`,"step":"explore","domain":"software","access":"read","target":{"system":"code.example.com","path":"acme/shop"},` +
		`"work_item":{"id":"wi_8Kx2mQ4vT9","url":"https://control.example.com/work-items/wi_8Kx2mQ4vT9","title":"A title"},"inputs":` + inputs + `}`)
}

// TestTheRunKeyIsReadFromTheBytes reads a handover whose run key is written with an
// escape, and whose inputs hold another run_key member: the run key is the
// top-level member's, unescaped, and the inputs are the answer's bytes. Clearing the
// answer after leaves the run key and the inputs whole, so neither shares its bytes.
func TestTheRunKeyIsReadFromTheBytes(t *testing.T) {
	inputs := `{"version":1,"run_key":"qrk_not.this"}`
	body := answerWith(`"\u0071rk_abc.def"`, inputs)
	h, err := readHandover(body, testRun)
	if err != nil {
		t.Fatal(err)
	}
	clear(body)
	var w bytes.Buffer
	_, _ = h.RunKey.WriteTo(&w)
	if w.String() != "qrk_abc.def" || string(h.Inputs) != inputs {
		t.Errorf("the run key %q and the inputs %s", w.String(), h.Inputs)
	}
}

// TestARunKeyOutsideItsShapeIsRefused reads handovers whose run key breaks its shape
// once unescaped, is too long, is not a string or is missing: each is refused with an
// error that quotes none of it.
func TestARunKeyOutsideItsShapeIsRefused(t *testing.T) {
	for _, key := range []string{
		`"\u0071rk_abc"`, `"qrk_abc.def\n"`, `"QRK_abc.def"`, `"qrk_` + strings.Repeat("a", maxRunKey) + `.b"`, `7`, `{"qrk_a":"b"}`,
	} {
		_, err := readHandover(answerWith(key, `{"version":1}`), testRun)
		if err == nil || strings.Contains(strings.ToLower(err.Error()), "qrk_a") {
			t.Errorf("%s: %v", key, err)
		}
	}
	_, err := readHandover([]byte(`{"version":1,"revision":3}`), testRun)
	if err == nil {
		t.Error("no run key")
	}
	if _, _, ok := runKeySpan([]byte(`{"inputs":{"run_key":"qrk_a.b"}}`)); ok {
		t.Error("a nested run_key is taken for the answer's")
	}
}

// TestALargeAnswerIsReadIntoOneBuffer reads a body by its length and with none: within
// the bound it is whole, over it it is refused.
func TestALargeAnswerIsReadIntoOneBuffer(t *testing.T) {
	for _, length := range []int64{-1, 10} {
		b, large, err := readLimited(strings.NewReader("0123456789"), length, 10)
		if err != nil || large || string(b) != "0123456789" || cap(b) != 10 {
			t.Errorf("length %d: %q %v %v cap %d", length, b, large, err, cap(b))
		}
		if _, large, _ := readLimited(strings.NewReader("0123456789x"), length, 10); !large && length < 0 {
			t.Errorf("length %d: over the bound is read", length)
		}
	}
}
