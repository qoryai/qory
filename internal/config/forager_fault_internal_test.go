package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// walkFault is findFault of the file body as forager.yaml, with the time it took.
func walkFault(t *testing.T, body string, known bool) (*fault, time.Duration) {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f := findFault(&doc, reflect.TypeFor[foragerFile](), "", known)
	return f, time.Since(start)
}

// TestTheWalkEnds is the walk of a file the decoder refuses for its anchors, which
// foragerDecodeError never walks, walked all the same: a mapping that merges itself is a
// fault, and merges that expand without end stop at the walk's limit, at once.
func TestTheWalkEnds(t *testing.T) {
	f, took := walkFault(t, "session: &s {<<: *s}\n", true)
	if f == nil || f.line != 1 || f.text != "session merges a mapping that holds the merge" || took > 2*time.Second {
		t.Errorf("a mapping that merges itself: %+v in %v", f, took)
	}
	bomb := "gateway:\n  credentials:\n    c0: &m0 {k: v}\n"
	for i := 1; i < 9; i++ {
		bomb += fmt.Sprintf("    c%d: &m%d {<<: [%s*m%d]}\n", i, i, strings.Repeat(fmt.Sprintf("*m%d, ", i-1), 9), i-1)
	}
	f, took = walkFault(t, bomb+"wall:\n  <<: *m8\n", false)
	if f == nil || f.line != 0 || f.text != tooManyAliases || took > 2*time.Second {
		t.Errorf("merges that expand without end: %+v in %v", f, took)
	}
}

// TestADecoderPanicIsARefusal is the YAML decoder's panic on a key that is not a name
// beside a merge, decoded without the check before it: it is the fixed refusal, which
// holds no value. The enrolment writer, which reads the file as nodes alone, does not
// reach it.
func TestADecoderPanicIsARefusal(t *testing.T) {
	const body = "wall:\n  ? [VALUEMARKER7Q]\n  : y\n  <<: {adapter: docker}\n"
	var f foragerFile
	err := safeDecode(func() error { return yaml.Unmarshal([]byte(body), &f) })
	if !errors.Is(err, errDecoderFailed) {
		t.Fatalf("%v, want %v", err, errDecoderFailed)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if got := foragerDecodeError("forager.yaml", &doc, reflect.TypeFor[foragerFile](), "", true, err); got.Error() != "forager.yaml: the YAML decoder failed reading the file" {
		t.Errorf("%v", got)
	}
	if _, err := editEnrolment([]byte(body), Enrolment{URL: "https://apiary.example", AccessKeyID: "ak_0123456789abcdef"}); err != nil {
		t.Errorf("the enrolment writer: %v", err)
	}
}
