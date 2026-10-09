package cmd

import (
	"fmt"
	"slices"
	"testing"

	"github.com/qoryai/forager/gateway"
)

// TestResendLinesLeaveOutForagersNotOpened pins what both resends, to the server and
// behind a gateway, pass on of Forager's report lines: when qory says itself that the run
// never opened, Forager's gateway.ResendNotOpened and gateway.ResendNoServer are left
// out, and every other line, the torn lines' and the server's stop among them, is
// passed on in order. When qory does not say so, nothing is left out.
func TestResendLinesLeaveOutForagersNotOpened(t *testing.T) {
	torn := fmt.Sprintf(gateway.ResendTorn, 2, "/run/events.jsonl")
	const stop = "the server said stop during the run; nothing is sent"
	for _, c := range []struct {
		name      string
		reported  []string
		notOpened bool
		want      []string
	}{
		{"never opened", []string{gateway.ResendNotOpened}, true, nil},
		{"no server", []string{gateway.ResendNoServer}, true, nil},
		{"torn, never opened", []string{torn, gateway.ResendNotOpened}, true, []string{torn}},
		{"torn, no server", []string{torn, gateway.ResendNoServer}, true, []string{torn}},
		{"stop kept", []string{stop, gateway.ResendNotOpened}, true, []string{stop}},
		{"opened: never opened kept", []string{gateway.ResendNotOpened}, false, []string{gateway.ResendNotOpened}},
		{"opened: no server kept", []string{gateway.ResendNoServer}, false, []string{gateway.ResendNoServer}},
		{"opened: all kept", []string{torn, stop}, false, []string{torn, stop}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			lines := &resendLines{report: func(l string) { got = append(got, l) }}
			for _, l := range c.reported {
				lines.line(l)
			}
			lines.done(c.notOpened)
			if !slices.Equal(got, c.want) {
				t.Errorf("passed on %q, want %q", got, c.want)
			}
		})
	}
}
