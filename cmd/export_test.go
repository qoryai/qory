package cmd

import (
	"context"

	"github.com/qoryai/forager/gateway"
)

// SetAfterStep makes every enrolment stop after the steps that follow the server's
// answer as f says, and returns what restores the default.
func SetAfterStep(f func(step string) error) func() {
	afterStep = f
	return func() { afterStep = func(string) error { return nil } }
}

// SetStartGateway makes qory gateway hand the configuration of its gateway to f in place
// of starting it, and end with what f returns; it returns what restores the default.
func SetStartGateway(f func(gateway.Config) error) func() {
	startGateway = func(_ context.Context, cfg gateway.Config) (*gateway.Gateway, error) { return nil, f(cfg) }
	return func() { startGateway = gateway.Start }
}
