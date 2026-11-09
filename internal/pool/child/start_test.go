package child

import "testing"

// TestStatField reads fields of /proc/<pid>/stat after the command's name, which may
// contain spaces and parentheses, and returns an error for a field before the name's
// end, a field past the last, and contents with no name.
func TestStatField(t *testing.T) {
	// The fields 3 to 22 of a stat line; 22, starttime, is 8811.
	rest := " S 1 4242 4242 0 -1 4194560 120 0 0 0 1 2 0 0 20 0 1 0 8811 2445312 145"
	for _, tc := range []struct {
		name, stat string
		n          int
		want       string
	}{
		{name: "a plain name", stat: "4242 (sleep)" + rest, n: 22, want: "8811"},
		{name: "a name with spaces and parentheses", stat: "4242 (a) b (c)" + rest, n: 22, want: "8811"},
		{name: "a name that ends in a parenthesis", stat: "4242 (x))" + rest, n: 22, want: "8811"},
		{name: "the state", stat: "4242 (a) b (c)" + rest, n: 3, want: "S"},
		{name: "the process group", stat: "4242 (sleep)" + rest, n: 5, want: "4242"},
		{name: "a trailing line end", stat: "4242 (sleep)" + rest + "\n", n: 22, want: "8811"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := statField([]byte(tc.stat), tc.n)
			if err != nil || got != tc.want {
				t.Errorf("field %d is %q, %v, want %q", tc.n, got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, stat string
		n          int
	}{
		{name: "the name", stat: "4242 (sleep)" + rest, n: 2},
		{name: "a field past the last", stat: "4242 (sleep) S 1", n: 22},
		{name: "no name", stat: "4242 sleep S 1", n: 3},
		{name: "empty", stat: "", n: 22},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := statField([]byte(tc.stat), tc.n); err == nil {
				t.Errorf("field %d of %q was read as %q", tc.n, tc.stat, got)
			}
		})
	}
}
