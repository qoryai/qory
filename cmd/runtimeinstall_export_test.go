package cmd

import (
	"context"

	"github.com/qoryai/qory/internal/runtimeinstall"
)

// SetFetchRuntime makes fetch the source of the runtime's releases of qory run, until
// the returned restore runs.
func SetFetchRuntime(fetch func(ctx context.Context, runtime string) (runtimeinstall.Release, error)) (restore func()) {
	before := fetchRuntime
	fetchRuntime = func(ctx context.Context, _ openedRun, runtime string) (runtimeinstall.Release, error) {
		return fetch(ctx, runtime)
	}
	return func() { fetchRuntime = before }
}
