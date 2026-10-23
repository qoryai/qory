package provisioner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"
)

// claimRefused is the error of a claim's answer that breaks the schema.
const claimRefused = "it breaks claim-answer.schema.json"

// testCode is the run code of the contract's claim-answer fixtures.
const testCode = "qecr_01JB8Z3K5Q7R9T2V4W6X8Y0A1C.uoES-kuj1vk0sq0qoGlmAg"

// claimFixture is the contract's claim-answer fixture name, decoded.
func claimFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := fs.ReadFile(contracts.ProvisionerFS, "fixtures/claim-answer/"+name)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// written is what h writes.
func written(t *testing.T, h Hidden) string {
	t.Helper()
	var b bytes.Buffer
	if _, err := h.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestTheRunCodeIsReadFromTheBytes reads a claim's answer whose run code is written
// with an escape: the run code is the top-level member's, unescaped. Clearing the
// answer after leaves the run code whole, so it shares no bytes with the answer.
func TestTheRunCodeIsReadFromTheBytes(t *testing.T) {
	d := claimFixture(t, "machine.json")
	body := bytes.Replace(marshal(t, d), []byte(`"`+testCode+`"`), []byte(`"\u0071ecr_`+testCode[5:]+`"`), 1)
	if bytes.Contains(body, []byte(testCode)) || !bytes.Contains(body, []byte(`\u0071ecr_`)) {
		t.Fatal("the answer does not write the run code with an escape")
	}
	cl, err := readClaim(body)
	if err != nil {
		t.Fatal(err)
	}
	clear(body)
	if got := written(t, cl.RunCode); got != testCode {
		t.Errorf("the run code %q", got)
	}
	if cl.DispatchID != "dp_8w3k0q9t2v7m4r6x" || cl.PoolID != "np_f1xt0re000000000" || cl.Machine != "small" ||
		cl.GatewayURL != "https://gateway.example:8443" || cl.MaxLifetimeMinutes != 1470 || contracts.FormatTime(cl.LeaseUntil) != "2026-10-15T12:05:00Z" {
		t.Errorf("claimed %+v", cl)
	}
	cl, err = readClaim(marshal(t, claimFixture(t, "two-server-keys.json")))
	if err != nil || !strings.HasPrefix(written(t, cl.RunCode), testCode+".") || cl.Machine != "large" {
		t.Errorf("two server keys: %+v %v", cl, err)
	}
	cl, err = readClaim(marshal(t, claimFixture(t, "default-machine.json")))
	if err != nil || cl.Machine != "" {
		t.Errorf("the default machine: %+v %v", cl, err)
	}
}

// TestARunCodeOutsideItsShapeIsRefused reads claims whose run code breaks its shape
// once unescaped, is too long, is not a string or is missing: each is refused with an
// error that quotes none of it, and no Hidden is returned.
func TestARunCodeOutsideItsShapeIsRefused(t *testing.T) {
	for _, code := range []any{
		"qecr_01JB8Z3K5Q7R9T2V4W6X8Y0A1C", testCode + "\n", strings.ToLower(testCode), "qecn_" + testCode[5:],
		testCode + ".uoES-kuj1vk0sq0qoGlmAg.uoES-kuj1vk0sq0qoGlmAg", 7, map[string]any{"qecr_a": "b"}, nil,
	} {
		d := claimFixture(t, "machine.json")
		d["run_code"] = code
		cl, err := readClaim(marshal(t, d))
		if err == nil || err.Error() != claimRefused || cl.RunCode.Len() != 0 {
			t.Errorf("%v: %v", code, err)
		}
	}
	d := claimFixture(t, "machine.json")
	delete(d, "run_code")
	if _, err := readClaim(marshal(t, d)); err == nil || err.Error() != claimRefused {
		t.Errorf("no run code: %v", err)
	}
	if _, _, ok := memberSpan([]byte(`{"x":{"run_code":"qecr_a"}}`), "run_code"); ok {
		t.Error("a nested run_code is taken for the answer's")
	}
}

// TestAClaimOfAnotherRevisionIsRefused reads the contract's claim, then changes it in
// one place each: another revision, a member of revision 3, a member twice, a member
// the schema does not define, and a lifetime outside its bounds. Each is refused, and
// the error contains no value.
func TestAClaimOfAnotherRevisionIsRefused(t *testing.T) {
	if _, err := readClaim(marshal(t, claimFixture(t, "machine.json"))); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(d map[string]any){
		"revision 0":  func(d map[string]any) { d["revision"] = 0 },
		"revision 2":  func(d map[string]any) { d["revision"] = 2 },
		"revision 3":  func(d map[string]any) { d["revision"] = 3 },
		"no revision": func(d map[string]any) { delete(d, "revision") },
		"a run id":    func(d map[string]any) { d["run_id"] = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9d" },
		"a step":      func(d map[string]any) { d["step"] = "explore" },
		"a target": func(d map[string]any) {
			d["target"] = map[string]any{"system": "code.example.com", "path": "acme/shop"}
		},
		"a run key":              func(d map[string]any) { d["run_key"] = "qrk_abc.def" },
		"a lifetime of 14":       func(d map[string]any) { d["max_lifetime_minutes"] = 14 },
		"a lifetime of 1471":     func(d map[string]any) { d["max_lifetime_minutes"] = 1471 },
		"a gateway over http":    func(d map[string]any) { d["gateway_url"] = "http://gateway.example" },
		"a machine in capitals":  func(d map[string]any) { d["machine"] = "Small" },
		"no gateway":             func(d map[string]any) { delete(d, "gateway_url") },
		"a lease that is a date": func(d map[string]any) { d["lease_until"] = "2026-10-15" },
	} {
		d := claimFixture(t, "machine.json")
		edit(d)
		cl, err := readClaim(marshal(t, d))
		if err == nil || err.Error() != claimRefused || cl.RunCode.Len() != 0 {
			t.Errorf("%s: got %v, want %s", name, err, claimRefused)
			continue
		}
		if strings.Contains(err.Error(), "qecr_") || strings.Contains(err.Error(), "qrk_") {
			t.Errorf("%s: the error %q contains a secret", name, err)
		}
	}
	twice := bytes.Replace(marshal(t, claimFixture(t, "machine.json")), []byte(`"revision":1,`), []byte(`"revision":1,"revision":1,`), 1)
	if _, err := readClaim(twice); err != errNotOneDocument {
		t.Errorf("a member twice: %v", err)
	}
}

// TestAClaimOfAnotherRevisionStartsWithItsFix joins readClaim's errors to the claim: the
// error of another revision, which the schema stops first today, starts with update
// qory, and a schema's error has no fix.
func TestAClaimOfAnotherRevisionStartsWithItsFix(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{otherRevision(2), fmt.Sprintf("the claim: update qory: Qory Apiary's response breaks the contract, so nothing in it is used: it has the contract's revision 2, not %d", contracts.ProvisionerRevision)},
		{errors.New(claimRefused), "the claim: Qory Apiary's response breaks the contract, so nothing in it is used: " + claimRefused},
	} {
		err := claimUnusable("the claim", c.err)
		if !errors.Is(err, ErrAnswerUnusable) || err.Error() != c.want {
			t.Errorf("got %v, want %q", err, c.want)
		}
	}
}

// TestARunCodeIsASecret checks that what the client checks for secrets finds a run code
// in any case, beside an access key secret and a run key.
func TestARunCodeIsASecret(t *testing.T) {
	for _, s := range []string{testCode, "the code QECR_x", "qak_x", "qrk_x"} {
		if !containsSecret(s) || shown(s) != hiddenText {
			t.Errorf("%q is not taken for a secret", s)
		}
	}
	if containsSecret("the claim") {
		t.Error("plain text is taken for a secret")
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
