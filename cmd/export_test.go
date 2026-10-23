package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/foragerdir"
	walled "github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/pool/prebuild"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
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
	spec := &runSpec{OpenSpec: session.OpenSpec{RunID: "run-2"}, SessionSpec: session.SessionSpec{Mounts: mounts}}
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

// SetJoinSession makes qory run hand the spec of each session to f before the session
// joins its run, and returns what restores the default.
func SetJoinSession(f func(session.SessionSpec)) func() {
	before := joinSession
	joinSession = func(r openedRun, ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		f(spec)
		return before(r, ctx, spec)
	}
	return func() { joinSession = before }
}

// ErrJoinedNowhere is the error of the join of a run [OpenNowhere] opened.
var ErrJoinedNowhere = errors.New("the run was opened nowhere, so no session joins it")

// OpenNowhere makes qory run open each run without reaching a gateway: the open makes
// no run directory and no connection, the session's join fails with
// [ErrJoinedNowhere], and the close keeps the outcome qory closes the run with. The run
// lists no repositories, and its host proxy leads to a port of this machine that
// takes no connection. It returns the outcomes the closes got, in order, and what
// restores the default.
func OpenNowhere() (outcomes func() []session.Outcome, restore func()) {
	outcomes, _, restore = OpenNowhereWith(Nowhere{Proxy: "http://qory:nowhere@127.0.0.1:9"})
	return outcomes, restore
}

// Nowhere is what a run [OpenNowhereWith] opens has: the server's values of the run's
// system and repositories, its host proxy's URL and CA bundle, its run directory, ""
// for a new temporary folder, the error of HostProxy, nil for none, OnStep, which,
// when not nil, gets each event of the run's host steps, as Forager writes them,
// Setup, which, when not nil, gets the spec of the run's Setup and returns its error,
// OnOpen, which, when not nil, gets the spec of each open, and AgentEnv, which, when
// not nil, gets the spec of the run's AgentEnv and returns its result. With AgentEnv
// nil, AgentEnv returns System and Repositories, each when set, as QORY_RUN_SYSTEM and
// QORY_RUN_REPOSITORIES, sorted by name.
type Nowhere struct {
	System, Repositories string
	Proxy, CABundle      string
	Dir                  string
	ProxyErr             error
	OnStep               func(StepEvent)
	Setup                func(session.SetupSpec) error
	AgentEnv             func(session.SessionSpec) ([]string, error)
	OnOpen               func(session.OpenSpec)
}

// StepEvent is one event of a host step of a run [OpenNowhereWith] opened: Type is
// "step_started", with Name and Kind, "log", with Line, the text of one Log, or
// "step_exited", with State and ExitCode, as Forager's dev.qory.run.step_started,
// dev.qory.run.log and dev.qory.run.step_exited have them. Step is the step's number.
type StepEvent struct {
	Type, Name, Kind, Line, State string
	Step, ExitCode                int
}

// OpenNowhereWith is [OpenNowhere] for runs that have what n says. Each run's directory
// is n.Dir, or a new temporary folder, which restore removes. Besides the outcomes, it
// returns the calls the runs got, in order: "host proxy" for each HostProxy, "host
// proxy closed" for each close of one, "setup" and "join".
func OpenNowhereWith(n Nowhere) (outcomes func() []session.Outcome, calls func() []string, restore func()) {
	before := openRun
	var mu sync.Mutex
	var got []session.Outcome
	var made []string
	var did []string
	note := func(call string) {
		mu.Lock()
		defer mu.Unlock()
		did = append(did, call)
	}
	openRun = func(_ context.Context, spec session.OpenSpec) (openedRun, error) {
		if n.OnOpen != nil {
			n.OnOpen(spec)
		}
		dir := n.Dir
		if dir == "" {
			var err error
			if dir, err = os.MkdirTemp("", "qory-nowhere-"); err != nil {
				return nil, err
			}
			mu.Lock()
			made = append(made, dir)
			mu.Unlock()
		}
		return &nowhere{id: spec.RunID, dir: dir, values: n, note: note, closed: func(o session.Outcome) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, o)
		}}, nil
	}
	outcomes = func() []session.Outcome {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
	calls = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(did)
	}
	return outcomes, calls, func() {
		openRun = before
		mu.Lock()
		defer mu.Unlock()
		for _, d := range made {
			os.RemoveAll(d)
		}
	}
}

// nowhere is a run [OpenNowhere] opened. Its steps are numbered as Forager numbers
// them, one open at a time, and Close ends an open one cancelled with -1.
type nowhere struct {
	id     string
	dir    string
	values Nowhere
	note   func(string)
	closed func(session.Outcome)
	steps  int
	open   *nowhereStep
}

func (n *nowhere) Join(context.Context, session.SessionSpec) (*session.Result, error) {
	if n.open != nil {
		return nil, fmt.Errorf("exit step %d before the join", n.open.n)
	}
	n.note("join")
	return nil, ErrJoinedNowhere
}

func (n *nowhere) Setup(_ context.Context, spec session.SetupSpec) error {
	if n.open != nil {
		return fmt.Errorf("exit step %d before the setup", n.open.n)
	}
	n.note("setup")
	if n.values.Setup != nil {
		return n.values.Setup(spec)
	}
	return nil
}

func (n *nowhere) AgentEnv(spec session.SessionSpec) ([]string, error) {
	if n.open != nil {
		return nil, fmt.Errorf("exit step %d before the agent's environment", n.open.n)
	}
	n.note("agent env")
	if n.values.AgentEnv != nil {
		return n.values.AgentEnv(spec)
	}
	var env []string
	if n.values.Repositories != "" {
		env = append(env, preparation.EnvRepositories+"="+n.values.Repositories)
	}
	if n.values.System != "" {
		env = append(env, preparation.EnvSystem+"="+n.values.System)
	}
	return env, nil
}

func (n *nowhere) Close(_ context.Context, out session.Outcome) (*session.Closed, error) {
	if n.open != nil {
		n.open.Exit("cancelled", -1)
	}
	n.closed(out)
	return &session.Closed{RunID: n.id, State: out.State, Reason: out.Reason, ExitCode: out.ExitCode}, nil
}

func (n *nowhere) HostProxy() (hostProxy, error) {
	n.note("host proxy")
	if n.values.ProxyErr != nil {
		return hostProxy{}, n.values.ProxyErr
	}
	var once sync.Once
	return hostProxy{proxy: proxygit.Proxy{URL: n.values.Proxy, CABundle: n.values.CABundle}, close: func() error {
		once.Do(func() { n.note("host proxy closed") })
		return nil
	}}, nil
}

func (n *nowhere) StartStep(kind, name string) (hostStep, error) {
	if n.open != nil {
		return nil, fmt.Errorf("exit step %d before the next host step starts", n.open.n)
	}
	n.steps++
	n.open = &nowhereStep{r: n, n: n.steps}
	n.event(StepEvent{Type: "step_started", Step: n.steps, Name: name, Kind: kind})
	return n.open, nil
}

// event passes e to OnStep, when the run has one.
func (n *nowhere) event(e StepEvent) {
	if n.values.OnStep != nil {
		n.values.OnStep(e)
	}
}

// nowhereStep is a host step of a run [OpenNowhere] opened.
type nowhereStep struct {
	r      *nowhere
	n      int
	exited bool
}

func (s *nowhereStep) Log(b []byte) error {
	if s.exited {
		return fmt.Errorf("step %d exited", s.n)
	}
	s.r.event(StepEvent{Type: "log", Step: s.n, Line: string(b)})
	return nil
}

func (s *nowhereStep) Exit(state string, exitCode int) error {
	if s.exited {
		return fmt.Errorf("step %d exited already", s.n)
	}
	s.exited = true
	s.r.open = nil
	s.r.event(StepEvent{Type: "step_exited", Step: s.n, State: state, ExitCode: exitCode})
	return nil
}

func (n *nowhere) System() string       { return n.values.System }
func (n *nowhere) Repositories() string { return n.values.Repositories }
func (n *nowhere) Dir() string          { return n.dir }

// SetBeforeJoin makes qory run call f once the harness is composed, before the run's
// session joins, and returns what restores the default.
func SetBeforeJoin(f func()) func() {
	beforeJoin = f
	return func() { beforeJoin = nil }
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

// SetRunCodeCAFile makes qory run redeem a run code over TLS checked against the
// authorities in caFile, in place of the system's roots, and returns what restores the
// default.
func SetRunCodeCAFile(caFile string) func() {
	before := runCodeVia
	runCodeVia = func(gatewayURL string) server.Via { return server.Via{URL: gatewayURL, CAFile: caFile} }
	return func() { runCodeVia = before }
}

// RedeemRunCode redeems the run code in file through the gateway at gatewayURL with the
// key pair k, its proof made at now, as qory run redeems one, and returns the run's id
// and its run key.
func RedeemRunCode(ctx context.Context, k *accesskey.Key, file, gatewayURL string, now time.Time) (runID, key string, err error) {
	code, _, _, err := readRunCode(file, nil)
	if err != nil {
		return "", "", err
	}
	got, _, err := redeemRunCode(ctx, k, &runCode{code: code, gateway: gatewayURL}, "run-01", now)
	if err != nil {
		return "", "", err
	}
	return got.key.RunID, got.value, nil
}

// GiveRunCode reads the run code of --run-code-file file as qory run does, with the
// process's stdin and the gateway https://gateway.example:8443, and returns whether
// the code came from stdin, and the error.
func GiveRunCode(file string) (bool, error) {
	c, err := givenRunCode(true, true, false, file, "https://gateway.example:8443", os.Stdin)
	return c != nil && c.stdin, err
}

// SetRunCodePause makes qory run pause for d, in place of a second, before it sends a
// redemption whose answer was lost again, and returns what restores the default.
func SetRunCodePause(d time.Duration) func() {
	before := runCodePause
	runCodePause = d
	return func() { runCodePause = before }
}

// SetLaunchPrebuild makes qory run run the prebuild of a run code with f, in place of
// prebuild.Launch, and returns what restores the default.
func SetLaunchPrebuild(f func(ctx context.Context, s prebuild.Settings, r prebuild.Run) (prebuild.Result, error)) func() {
	before := launchPrebuild
	launchPrebuild = f
	return func() { launchPrebuild = before }
}
