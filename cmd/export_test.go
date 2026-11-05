package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/pool/prebuild"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/runcopy"
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
		if !isGitSession(spec) {
			f(spec)
		}
		return before(r, ctx, spec)
	}
	return func() { joinSession = before }
}

// SetJoinResult makes the join of each run return what f returns, with no session, as a
// session that ended that way does, and returns what restores the default.
func SetJoinResult(f func(session.SessionSpec) (*session.Result, error)) func() {
	before := joinSession
	joinSession = func(r openedRun, ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if isGitSession(spec) {
			return before(r, ctx, spec)
		}
		return f(spec)
	}
	return func() { joinSession = before }
}

// isGitSession is whether spec is a run's git session, see [runcopy.Dir.GitSession].
func isGitSession(spec session.SessionSpec) bool {
	return len(spec.Args) > 1 && filepath.Base(spec.Args[0]) == runcopy.SessionScriptName
}

// hostGitFolder is the folder of this machine's git, for the PATH of a git session in a
// test: the run's image has its git in other folders. The session's first argument after
// the script file is that PATH.
func hostGitFolder(spec session.SessionSpec) (session.SessionSpec, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return spec, err
	}
	folders := filepath.Dir(path) + ":/usr/bin:/bin"
	spec.Args = slices.Clone(spec.Args)
	spec.Args[1] = folders
	spec.Env = slices.Clone(spec.Env)
	for i, e := range spec.Env {
		if strings.HasPrefix(e, "PATH=") {
			spec.Env[i] = "PATH=" + folders
		}
	}
	return spec, nil
}

// RunGitSessionsOnHost makes every git session of a test run as a process on this machine,
// since a test has no image with git and runs no docker. A test that wants the session to
// reach its join uses [SetGitSession].
func RunGitSessionsOnHost() {
	before := joinSession
	joinSession = func(r openedRun, ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if !isGitSession(spec) {
			return before(r, ctx, spec)
		}
		return GitSessionOnHost(ctx, spec)
	}
}

// GitSessionOnHost runs the git session spec as a process on this machine.
func GitSessionOnHost(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
	spec, err := hostGitFolder(spec)
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, spec.Command, spec.Args...)
	c.Dir, c.Env, c.Stdout, c.Stderr = spec.Dir, spec.Env, spec.Stdout, spec.Stderr
	res := &session.Result{}
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			res.TimedOut = true
			return res, nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, err
		}
		res.ExitCode = exit.ExitCode()
	}
	return res, nil
}

// SetReturnAfterSignal sets how long the return of a local run has after a signal, and returns
// what restores the default.
func SetReturnAfterSignal(d time.Duration) func() {
	before := returnAfterSignal
	returnAfterSignal = d
	return func() { returnAfterSignal = before }
}

// SetGitSession makes the join of a git session return what f returns, and returns what
// restores the default.
func SetGitSession(f func(context.Context, session.SessionSpec) (*session.Result, error)) func() {
	before := joinSession
	joinSession = func(r openedRun, ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if isGitSession(spec) {
			return f(ctx, spec)
		}
		return before(r, ctx, spec)
	}
	return func() { joinSession = before }
}

// GitSessionThroughJoin makes a git session join its run as any session does, with this
// machine's git on its PATH, for a test with a wall that runs its containers on this
// machine. It returns what restores the default.
func GitSessionThroughJoin() func() {
	before := joinSession
	joinSession = func(r openedRun, ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if !isGitSession(spec) {
			return before(r, ctx, spec)
		}
		spec, err := hostGitFolder(spec)
		if err != nil {
			return nil, err
		}
		return r.Join(ctx, spec)
	}
	return func() { joinSession = before }
}

// SetCopyStepLimit sets how long a check of a run's bundles or a copy back may take, and returns
// what restores the default.
func SetCopyStepLimit(limit time.Duration) func() {
	before := copyStepLimit
	copyStepLimit = limit
	return func() { copyStepLimit = before }
}

// SetPushTimes sets the time the push step has past a run's limit and after a signal, and
// returns what restores the defaults.
func SetPushTimes(limit, signal time.Duration) func() {
	before := pushTimes
	pushTimes.limit, pushTimes.signal = limit, signal
	return func() { pushTimes = before }
}

// SetBranchWait makes the push step pass each wait to f in place of waiting, and returns
// what restores the default.
func SetBranchWait(f func(time.Duration)) func() {
	before := branchWait
	branchWait = func(_ context.Context, d time.Duration) error {
		f(d)
		return nil
	}
	return func() { branchWait = before }
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
// system and repositories, its host proxy's URL and CA bundle, its stack proxy's URL
// and CA bundle, a port that takes no connection for StackProxy "", its run directory,
// "" for a new temporary folder, the error of HostProxy and of StackProxy, nil for
// none, OnStep, which,
// when not nil, gets each event of the run's host steps, as Forager writes them,
// Setup, which, when not nil, gets the spec of the run's Setup and returns its error,
// OnOpen, which, when not nil, gets the spec of each open, FetchImage, which, when not
// nil, is the run's FetchImage, RemoveSetup, which, when not nil, is the run's
// RemoveSetup, and AgentEnv, which, when
// not nil, gets the spec of the run's AgentEnv and returns its result. With AgentEnv
// nil, AgentEnv returns System and Repositories, each when set, as QORY_RUN_SYSTEM and
// QORY_RUN_REPOSITORIES, sorted by name. With AtGateway set, each open also opens the
// run at a gateway of Forager's that starts in this process for it, over the
// gateway's local link, and Close closes the run there with qory's outcome and then
// ends that gateway: the gateway writes its record, events.jsonl, and the session
// writes session.jsonl, in the folder of the run's id under the open's RunsDir.
// PolicyImage is the image the run's policy selects, "" for none: the gateway of
// AtGateway runs under a policy that selects it, and a run with no gateway resolves it
// against the open's images, as that gateway does. Image returns what the gateway
// answered, and OnImage, when not nil, gets each value Image returns. With Real set,
// which needs AtGateway, the run's Setup, RemoveSetup and Join are those of the run at
// that gateway, so they use a real wall, and every other call is the fake's.
type Nowhere struct {
	System, Repositories string
	Proxy, CABundle      string
	StackProxy           string
	StackCABundle        string
	Dir                  string
	ProxyErr             error
	StackProxyErr        error
	OnStep               func(StepEvent)
	Setup                func(session.SetupSpec) error
	AgentEnv             func(session.SessionSpec) ([]string, error)
	OnOpen               func(session.OpenSpec)
	FetchImage           func(context.Context) error
	RemoveSetup          func(context.Context) error
	// HostProxyErr, when not nil, is called by each HostProxy, which returns its error, as
	// Forager's returns one once a wall could not be removed.
	HostProxyErr func() error
	AtGateway    bool
	Real         bool
	PolicyImage  string
	OnImage      func(session.Image)
	// CloseErr, when not nil, is the error of the run's Close, which then returns no result.
	CloseErr error
	// WallKept is [session.Closed.WallKept] of the run's Close.
	WallKept bool
	// RunAPI is the base URL of the run's run API, "" for a server that returns 404 with
	// no error code to every request, as Qory Apiary does with its feature workflow_model
	// off.
	RunAPI string
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
// proxy closed" for each close of one, "stack proxy" and "stack proxy closed" for each
// StackProxy and close of one, "fetch image", "setup", "remove setup" and "join".
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
	openRun = func(ctx context.Context, spec session.OpenSpec) (openedRun, error) {
		if n.OnOpen != nil {
			n.OnOpen(spec)
		}
		var at *gatewayRun
		if n.AtGateway {
			var err error
			if at, err = openAtGateway(ctx, spec, n.PolicyImage); err != nil {
				return nil, err
			}
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
		return &nowhere{id: spec.RunID, dir: dir, values: n, image: answeredImage(spec, n.PolicyImage), note: note, at: at, closed: func(o session.Outcome) {
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
	image  session.Image
	note   func(string)
	closed func(session.Outcome)
	at     *gatewayRun
	steps  int
	open   *nowhereStep
	ended  bool
}

// errNowhereClosed is what [Run.HostProxy] and [Run.RunAPI] of Forager return after Close.
var errNowhereClosed = errors.New("the run is closed")

// gatewayRun is a run [Nowhere.AtGateway] opened at a gateway of Forager's in this
// process, and that gateway.
type gatewayRun struct {
	run *session.Run
	g   *gateway.Gateway
}

// answeredImage is the image a gateway's run answer sets for the run of spec under a
// policy that selects selected, "" for none: the entry of spec.Images of that name, or
// of the default's, or the name as a reference; the zero Image with no wall.
func answeredImage(spec session.OpenSpec, selected string) session.Image {
	if spec.Wall == nil {
		return session.Image{}
	}
	if selected == "" {
		selected = spec.Image
	}
	if i := slices.IndexFunc(spec.Images, func(d session.Image) bool { return d.Name == selected }); i >= 0 {
		return spec.Images[i]
	}
	return session.Image{Ref: selected}
}

// openAtGateway starts a gateway in this process with no server, which keeps the
// record of each run in the folder of its id under spec.RunsDir and runs each under a
// policy that selects image, "" for no policy, and opens the run of spec there, over
// the gateway's local link.
func openAtGateway(ctx context.Context, spec session.OpenSpec, image string) (*gatewayRun, error) {
	var pol *gateway.Policy
	if image != "" {
		pol = &gateway.Policy{Version: 1, Image: image, Egress: gateway.PolicyEgress{Mode: "observe"}}
	}
	g, err := gateway.Start(ctx, gateway.Config{
		RunDir:       func(id string) string { return filepath.Join(spec.RunsDir, id) },
		NoLinkSocket: true,
		Report:       func(string) {},
		Policy:       pol,
	})
	if err != nil {
		return nil, err
	}
	spec.Gateway = session.LocalGateway(g.LocalLink())
	r, err := session.Open(ctx, spec)
	if err != nil {
		g.Close(ctx)
		return nil, err
	}
	return &gatewayRun{run: r, g: g}, nil
}

// RunAPI is the client and base URL of [Nowhere.RunAPI].
func (n *nowhere) RunAPI() (*http.Client, string, error) {
	if n.ended {
		return nil, "", errNowhereClosed
	}
	if n.values.RunAPI != "" {
		return http.DefaultClient, n.values.RunAPI, nil
	}
	return &http.Client{Transport: noRunAPI{}}, "http://run-api.qory.invalid/v1/runs/" + n.id, nil
}

// noRunAPI is a transport that returns 404 with an error page to every request.
type noRunAPI struct{}

// RoundTrip returns the response.
func (noRunAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusNotFound, Header: http.Header{}, Request: r,
		Body: io.NopCloser(strings.NewReader("<html>Not found</html>")),
	}, nil
}

func (n *nowhere) Join(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
	if n.open != nil {
		return nil, fmt.Errorf("exit step %d before the join", n.open.n)
	}
	n.note("join")
	if n.values.Real {
		return n.at.run.Join(ctx, spec)
	}
	return nil, ErrJoinedNowhere
}

func (n *nowhere) Setup(ctx context.Context, spec session.SetupSpec) error {
	if n.open != nil {
		return fmt.Errorf("exit step %d before the setup", n.open.n)
	}
	n.note("setup")
	if n.values.Real {
		return n.at.run.Setup(ctx, spec)
	}
	if n.values.Setup != nil {
		return n.values.Setup(spec)
	}
	return nil
}

func (n *nowhere) RemoveSetup(ctx context.Context) error {
	if n.open != nil {
		return fmt.Errorf("exit step %d before the setup container's removal", n.open.n)
	}
	n.note("remove setup")
	if n.values.Real {
		return n.at.run.RemoveSetup(ctx)
	}
	if n.values.RemoveSetup != nil {
		return n.values.RemoveSetup(ctx)
	}
	return nil
}

func (n *nowhere) FetchImage(ctx context.Context) error {
	if n.open != nil {
		return fmt.Errorf("exit step %d before the image's fetch", n.open.n)
	}
	n.note("fetch image")
	if n.values.FetchImage != nil {
		return n.values.FetchImage(ctx)
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

func (n *nowhere) Close(ctx context.Context, out session.Outcome) (*session.Closed, error) {
	if n.open != nil {
		n.open.Exit("cancelled", -1)
	}
	n.ended = true
	n.closed(out)
	if n.values.CloseErr != nil {
		return nil, n.values.CloseErr
	}
	if n.at != nil {
		closed, err := n.at.run.Close(ctx, out)
		_, endErr := n.at.g.Close(ctx)
		return closed, errors.Join(err, endErr)
	}
	return &session.Closed{RunID: n.id, State: out.State, Reason: out.Reason, ExitCode: out.ExitCode, WallKept: n.values.WallKept}, nil
}

func (n *nowhere) HostProxy() (hostProxy, error) {
	n.note("host proxy")
	if n.ended {
		return hostProxy{}, errNowhereClosed
	}
	if n.values.ProxyErr != nil {
		return hostProxy{}, n.values.ProxyErr
	}
	if n.values.HostProxyErr != nil {
		if err := n.values.HostProxyErr(); err != nil {
			return hostProxy{}, err
		}
	}
	var once sync.Once
	return hostProxy{proxy: proxygit.Proxy{URL: n.values.Proxy, CABundle: n.values.CABundle}, close: func() error {
		once.Do(func() { n.note("host proxy closed") })
		return nil
	}}, nil
}

func (n *nowhere) StackProxy() (hostProxy, error) {
	n.note("stack proxy")
	if n.values.StackProxyErr != nil {
		return hostProxy{}, n.values.StackProxyErr
	}
	url := n.values.StackProxy
	if url == "" {
		url = "http://qory:nowhere@127.0.0.1:9"
	}
	var once sync.Once
	return hostProxy{proxy: proxygit.Proxy{URL: url, CABundle: n.values.StackCABundle}, close: func() error {
		once.Do(func() { n.note("stack proxy closed") })
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

// Image is the image of the gateway's run answer: with AtGateway, the answer of the
// gateway in this process; otherwise [answeredImage].
func (n *nowhere) Image() session.Image {
	img := n.image
	if n.at != nil {
		img = n.at.run.Image()
	}
	if n.values.OnImage != nil {
		n.values.OnImage(img)
	}
	return img
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

// ReservedMounts is the check of a walled run's read-write mounts, other than its
// checkout at root, against the home folder and the state directory, which qory run
// makes before the run starts anything.
func ReservedMounts(root string, mounts []wall.Mount) error {
	return reservedMounts(root, mounts)
}

// SetTempStoreDir sets the store of a run whose checkout is read-only to dir, and returns
// a function that restores it.
func SetTempStoreDir(dir string) func() {
	before := tempStoreDir
	tempStoreDir = dir
	return func() { tempStoreDir = before }
}

// SetRunStartFolder sets the folder that a run in a wall started in, and returns a
// function that restores the check.
func SetRunStartFolder(dir string) func() {
	before := runStartFolder
	runStartFolder = func() string { return dir }
	return func() { runStartFolder = before }
}

// SetCheckoutUnwritable makes a run read every checkout as one it cannot write, as a
// read-only mount does for root too, and returns a function that restores the check.
func SetCheckoutUnwritable() func() {
	checkoutWritable = func(string) bool { return false }
	return func() { checkoutWritable = canWrite }
}
