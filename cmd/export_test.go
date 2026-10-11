package cmd

import "github.com/qoryai/qory/internal/foragerdir"

// SetWriteLabels makes qory run keep a checkout's labels with f, and returns what
// restores the default.
func SetWriteLabels(f func(d foragerdir.Dir, root string, l foragerdir.Labels) error) func() {
	writeLabels = f
	return func() { writeLabels = foragerdir.Dir.WriteLabels }
}

// SetAfterStep makes every enrolment stop after the steps that follow the server's
// answer as f says, and returns what restores the default.
func SetAfterStep(f func(step string) error) func() {
	afterStep = f
	return func() { afterStep = func(string) error { return nil } }
}
