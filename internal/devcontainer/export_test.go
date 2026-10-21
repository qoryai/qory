package devcontainer

import (
	"testing"
	"time"
)

// ShortenGrace sets [grace] to d for the test.
func ShortenGrace(t *testing.T, d time.Duration) {
	old := grace
	grace = d
	t.Cleanup(func() { grace = old })
}

// Members is c without the text Parse read, so a test compares the members it read.
func Members(c *Config) *Config {
	read := *c
	read.clean = nil
	return &read
}
