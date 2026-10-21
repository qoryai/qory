package cmd

import (
	"context"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/config"
)

// PoolStart is what qory run pool hands the pool's loop once its checks pass.
type PoolStart struct {
	File         string
	Pool         *config.ForagerPool
	Runs         *config.Forager
	State        string
	InstanceID   string
	InstanceName string
	NodeID       string
	RunURL       string
	Key          *accesskey.Key
	Pin          accesskey.Pin
}

// SetStartPool makes f what qory run pool calls in place of the pool's loop, and returns
// what restores the loop.
func SetStartPool(f func(PoolStart) error) func() {
	old := startPool
	startPool = func(_ context.Context, s *poolSettings, key *accesskey.Key, pin accesskey.Pin) error {
		return f(PoolStart{
			File: s.Forager.File, Pool: s.Pool, Runs: s.Runs, State: s.State,
			InstanceID: s.InstanceID, InstanceName: s.InstanceName,
			NodeID: s.Configuration.NodeID, RunURL: s.Configuration.Run.URL, Key: key, Pin: pin,
		})
	}
	return func() { startPool = old }
}

// SetNoCoreDumps makes qory run pool call f in place of turning core dumps off, until the
// returned function restores it.
func SetNoCoreDumps(f func() error) func() {
	old := noCoreDumps
	noCoreDumps = f
	return func() { noCoreDumps = old }
}
