package provisioner

import (
	"strings"
	"testing"
)

// TestFitLogCountsTheEscapedBytes cuts logs whose escaped size is just within and just
// over the request's bound: within is whole, over is cut at a line.
func TestFitLogCountsTheEscapedBytes(t *testing.T) {
	budget := 1<<20 - logOverhead
	// Each "<" is 6 bytes escaped and each line feed 2.
	line := strings.Repeat("<", 9) + "\n"
	lines := budget / 56
	within := strings.Repeat(line, lines)
	if part, cut := fitLog(within); cut || part != within {
		t.Errorf("within: cut %v, %d of %d bytes", cut, len(part), len(within))
	}
	over := line + within
	if part, cut := fitLog(over); !cut || part != within {
		t.Errorf("over: cut %v, %d of %d bytes", cut, len(part), len(within))
	}
}
