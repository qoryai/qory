package prebuild

import (
	"testing"
	"time"
)

// ShortenGrace sets [grace] and [cleanWait] to d for the test.
func ShortenGrace(t *testing.T, d time.Duration) {
	oldGrace, oldClean := grace, cleanWait
	grace, cleanWait = d, 10*d
	t.Cleanup(func() { grace, cleanWait = oldGrace, oldClean })
}
