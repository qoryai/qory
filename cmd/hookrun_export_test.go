package cmd

import (
	"context"

	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/compose"
)

// SetReadHooks makes each session read its workflow hooks with f in place of its home's
// hooks.json, and returns what restores the default.
func SetReadHooks(f func(home string) ([]compose.Hook, error)) func() {
	before := readHooks
	readHooks = f
	return func() { readHooks = before }
}

// SetHookContainer makes each program hook run f with the checkout's root in place of its
// container, with f's result as the program's exit status, and returns what restores the
// default.
func SetHookContainer(f func(dir string) int) func() {
	before := runHookContainer
	runHookContainer = func(_ context.Context, c gitContainer) (int, error) { return f(c.Session.Dir), nil }
	return func() { runHookContainer = before }
}

// SetHookMounts makes each program hook run f with its container's binds in place of its
// container, with f's result as the program's exit status, and returns what restores the
// default.
func SetHookMounts(f func(mounts []wall.Mount) int) func() {
	before := runHookContainer
	runHookContainer = func(_ context.Context, c gitContainer) (int, error) { return f(c.Mounts), nil }
	return func() { runHookContainer = before }
}
