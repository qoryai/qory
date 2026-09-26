package config

import "testing"

// LowerMaxLinks has a program followed through at most n links for the rest of the test.
func LowerMaxLinks(t *testing.T, n int) {
	before := maxLinks
	maxLinks = n
	t.Cleanup(func() { maxLinks = before })
}
