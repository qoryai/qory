package integration

import (
	"testing"
	"time"
)

// ShortenDescribeWait has [Describe] wait d for the rest of the test.
func ShortenDescribeWait(t *testing.T, d time.Duration) {
	before := describeWait
	describeWait = d
	t.Cleanup(func() { describeWait = before })
}

// MaxOutput is the most of describe's output that is kept.
const MaxOutput = maxOutput
