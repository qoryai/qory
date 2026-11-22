package cmd

import (
	"slices"
	"strings"
	"testing"
)

// TestForagerRuntimesAreReadByTheRuleOfTheContract reads runtime lists: a runtime object
// with a member qory does not read loads, and a must_understand of members qory reads
// is met. A must_understand that lists another member is an error with the runtime's
// place. A fixed member in another case, before or after the defined one, never fills
// it.
func TestForagerRuntimesAreReadByTheRuleOfTheContract(t *testing.T) {
	for _, c := range []struct {
		name, runtime, want string
	}{
		{"an unknown member", `{"name":"claude","fixed":{"A":"1"},"zz":{"x":1}}`, ""},
		{"must understand what qory reads", `{"name":"claude","fixed":{"A":"1"},"must_understand":["/name","/fixed/A"]}`, ""},
		{"a variant before", `{"FIXED":{"A":"2"},"name":"claude","fixed":{"A":"1"}}`, ""},
		{"a variant after", `{"name":"claude","fixed":{"A":"1"},"Fixed":{"A":"2"}}`, ""},
		{"must understand an unknown member", `{"name":"claude","fixed":{"A":"1"},"zz":1,"must_understand":["/zz"]}`, `runtimes.json: runtime 2: use a reader that defines "/zz"`},
		{"must understand a scheme", `{"name":"claude","fixed":{"A":"1"},"model":{"keys":[{"auth":{"scheme":"zz"}}]},"must_understand":["/model/keys/0/auth/scheme"]}`, `runtime 2: use a reader that defines "/model/keys/0/auth/scheme"`},
		{"must_understand of another shape", `{"name":"claude","fixed":{"A":"1"},"must_understand":"/name"}`, "runtime 2: set must_understand to a list of JSON Pointers"},
	} {
		list, err := readForagerList([]byte(`{"version":1,"runtimes":[{"name":"codex"},` + c.runtime + `]}`))
		switch {
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want == "" && (len(list) != 2 || list[1].Name != "claude" || !slices.Equal(fixedOf(list[1]), []string{"A=1"})):
			t.Errorf("%s: %+v", c.name, list)
		}
	}
}

// fixedOf is the fixed variables of rt, NAME=value sorted by name.
func fixedOf(rt foragerRuntime) []string {
	var out []string
	for n, v := range rt.Fixed {
		out = append(out, n+"="+v)
	}
	slices.Sort(out)
	return out
}
