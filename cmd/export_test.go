package cmd

import (
	"context"
	"io"
	"time"

	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/foragerdir"
	walled "github.com/qoryai/qory/internal/places"
)

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

// SetStartGateway makes qory gateway, and a qory run with no separate gateway, hand the
// configuration of the gateway to f in place of starting it, and end with what f returns;
// it returns what restores the default.
func SetStartGateway(f func(gateway.Config) error) func() {
	startGateway = func(_ context.Context, cfg gateway.Config) (*gateway.Gateway, error) { return nil, f(cfg) }
	return func() { startGateway = gateway.Start }
}

// RecordCheckout records the checkout at root as the place of a walled run, the way qory
// run does before its session: its git settings, its qory files and its harness.
func RecordCheckout(root string) error {
	at, err := placesAt(root)
	if err != nil {
		return err
	}
	files, err := qoryFiles(root)
	if err != nil {
		return err
	}
	return recordPlace(context.Background(), root, walled.KindCheckout, "run-1", files, at, nil)
}

// RecordPlaces records the places a walled run in the checkout at root with mounts may
// write, the way qory run does before its session, and returns the hold on them.
func RecordPlaces(root string, mounts []wall.Mount) (*walled.Hold, error) {
	at, err := placesAt(root)
	if err != nil {
		return nil, err
	}
	spec := &session.Spec{RunID: "run-2", Mounts: mounts}
	return recordPlaces(context.Background(), spec, root, at)
}

// SetTerminal makes qory take stdin for a terminal when terminal is set, and returns
// what restores the default.
func SetTerminal(terminal bool) func() {
	askTerminal = func(any) bool { return terminal }
	return func() { askTerminal = isTerminal }
}

// ShortenSocketGrace has every run's socket keep accepting for d after the runtime
// exits, in place of Forager's 200 ms, and returns what restores the default.
func ShortenSocketGrace(d time.Duration) func() {
	before := socketGrace
	socketGrace = d
	return func() { socketGrace = before }
}

// SetPrepareSeed makes qory run prepare look for the image's copy of the run's
// repository at dir, and returns what restores the default.
func SetPrepareSeed(dir string) func() {
	before := prepareSeed
	prepareSeed = dir
	return func() { prepareSeed = before }
}

// SetStartSession makes qory run hand the Spec of each session to f before it runs it,
// and returns what restores the default.
func SetStartSession(f func(session.Spec)) func() {
	startSession = func(ctx context.Context, spec session.Spec) (*session.Result, error) {
		f(spec)
		return session.Run(ctx, spec)
	}
	return func() { startSession = session.Run }
}

// CopiedModules reads and composes the checkout the process stands in the way qory
// harness compose --home home does, "" for the configuration's home, writes nothing, and
// returns each module's name with whether the home contains copies of its files.
func CopiedModules(home string) (map[string]bool, error) {
	pr, err := prepare(io.Discard, io.Discard, composeOptions{homeOptions: homeOptions{home: home}})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range pr.res.Modules {
		out[m.Name] = m.Copy
	}
	return out, nil
}
