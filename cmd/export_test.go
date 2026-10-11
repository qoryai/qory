package cmd

import "time"

// SetAfterStep makes every enrolment stop after the steps that follow the server's
// answer as f says, and returns what restores the default.
func SetAfterStep(f func(step string) error) func() {
	afterStep = f
	return func() { afterStep = func(string) error { return nil } }
}

// ShortenSocketGrace has every run's socket keep accepting for d after the runtime
// exits, in place of Forager's 200 ms, and returns what restores the default.
func ShortenSocketGrace(d time.Duration) func() {
	before := socketGrace
	socketGrace = d
	return func() { socketGrace = before }
}
