package cmd

import "github.com/qoryai/forager/link"

// SetAfterStep makes every enrolment stop after the steps that follow the server's
// answer as f says, and returns what restores the default.
func SetAfterStep(f func(step string) error) func() {
	afterStep = f
	return func() { afterStep = func(string) error { return nil } }
}

// SetLinkHanded makes every run tell f the gateway's local link it hands its session,
// and returns what restores the default.
func SetLinkHanded(f func(link.Local)) func() {
	linkHanded = f
	return func() { linkHanded = func(link.Local) {} }
}
