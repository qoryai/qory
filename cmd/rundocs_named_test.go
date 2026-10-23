package cmd_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// The refs of the named stack's repository and of the repository of its module tools,
// which, as the URLs, no text of the run shows.
const (
	namedRef = "stacks-r7"
	toolsRef = "tools-r3"
)

// namedStack is a run from a run code whose documents set the stack hello by its name,
// with its address in stack_reference: the folder examples/hello at [namedRef] of a
// repository that the run's stack proxy serves and its host proxy does not.
type namedStack struct {
	// stack changes the stack file, nil for none; files are further files of the
	// stack's repository, and tools, when not nil, the files of the repository of the
	// module tools, at [toolsRef].
	stack func(string) string
	files map[string]string
	tools map[string]string
	// change changes the documents; nowhere changes what the run opened nowhere has.
	change  func(map[string]any)
	nowhere func(*cmd.Nowhere)
	// document, when set, is the checkout's qory.yaml, and extra, when not nil, the
	// files of the repository acme/extra on another server, which the run's host proxy
	// serves.
	document string
	extra    map[string]string
	// folder is the stack's folder in its repository, examples/hello when it is "".
	folder string
	// before, when not nil, runs once the run's environment is set, and returns further
	// arguments of qory run.
	before func() []string
}

// namedRun is what a [namedStack] run came to: the output with its error, the run's
// spec at the open and the session's at the join, the events of its host steps, the
// calls the run got, the commits of the stack and of the module tools, the server of
// the stack proxy and the one of the host proxy, the run directory, and addresses, the
// texts of the addresses that nothing may show.
type namedRun struct {
	out        string
	err        error
	open       session.OpenSpec
	spec       session.SessionSpec
	joined     bool
	events     []cmd.StepEvent
	calls      []string
	commit     string
	tools      string
	server     *proxygittest.Server
	hostServer *proxygittest.Server
	runDir     string
	addresses  []string
	folder     string
}

// toolsURL is the URL of the repository of the module tools on the server.
const toolsURL = "https://" + proxygittest.Host + "/example-org/tools.git"

// toolsModule are the files of the repository of the module tools.
func toolsModule() map[string]string {
	return map[string]string{
		"modules/tools/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: tools\n",
		"modules/tools/AGENTS.md":        "# Tools\n",
	}
}

// withTools is a stack file that lists the module tools from the repository at url, at
// ref.
func withTools(url, ref string) func(string) string {
	return func(s string) string {
		return s + "  - name: tools\n    source: {git: \"" + url + "\", ref: " + ref + ", path: modules/tools}\n"
	}
}

// userPart is the line of a module of the stack hello, more, whose source has a user
// part.
const userPart = "remove the user name and password from the source of the module more in the stack hello: a named stack's sources are fetched with no credential"

// thenMore is a stack file that lists the module tools, at [toolsURL], then the module
// more at url.
func thenMore(url string) func(string) string {
	return func(s string) string {
		return withTools(toolsURL, toolsRef)(s) + "  - name: more\n    source: {git: \"" + url + "\", ref: main, path: modules/more}\n"
	}
}

// run runs qory run from the run code of n and opens the run nowhere.
func (n namedStack) run(t *testing.T) namedRun {
	t.Helper()
	folder := n.folder
	if folder == "" {
		folder = "examples/hello"
	}
	files := map[string]string{}
	for k, v := range helloFiles(t) {
		files[folder+strings.TrimPrefix(k, "examples/hello")] = v
	}
	maps.Copy(files, n.files)
	if n.stack != nil {
		files[folder+"/qory-stack.yaml"] = n.stack(files[folder+"/qory-stack.yaml"])
	}
	h := newHostFetch(t, "", "main")
	r := namedRun{server: h.server, runDir: h.runDir, folder: folder}
	r.commit = h.server.Repository(t, "example-org/hello", namedRef, files)
	if n.tools != nil {
		r.tools = h.server.Repository(t, "example-org/tools", toolsRef, n.tools)
	}
	url := h.server.URL("example-org/hello")
	r.addresses = []string{url, strings.TrimSuffix(url, ".git"), "example-org/hello", "example-org/tools", "other.test", namedRef, toolsRef, string(filepath.Separator) + "sources" + string(filepath.Separator)}
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		d["stack"] = "hello"
		d["stack_reference"] = url + "#" + namedRef + ":" + folder
		if n.change != nil {
			n.change(d)
		}
	})
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) { r.spec, r.joined = s, true }))
	t.Setenv("QORY_RUN_KEY", "")
	nowhere := cmd.Nowhere{
		System: proxygittest.Host, Proxy: "http://qory:nowhere@127.0.0.1:9",
		StackProxy: h.server.Proxy.URL, StackCABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		OnOpen: func(s session.OpenSpec) { r.open = s },
		OnStep: func(e cmd.StepEvent) { r.events = append(r.events, e) },
	}
	if n.extra != nil {
		r.hostServer = proxygittest.Start(t)
		r.hostServer.Repository(t, "acme/extra", "main", n.extra)
		nowhere.Proxy, nowhere.CABundle = r.hostServer.Proxy.URL, r.hostServer.Proxy.CABundle
	}
	if n.document != "" {
		writeFile(t, "qory.yaml", n.document)
	}
	if n.nowhere != nil {
		n.nowhere(&nowhere)
	}
	args := []string{"run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL}
	if n.before != nil {
		args = append(args, n.before()...)
	}
	_, calls, restore := cmd.OpenNowhereWith(nowhere)
	defer restore()
	r.out, r.err = run(t, args...)
	r.calls = calls()
	return r
}

// noAddress fails the test for each text of r that shows an address: a line of the
// output, the error, an event of the run's host steps, the run's spec and the
// session's, and the path of a file in the run's sources outside a clone's .git. The
// stack's folder, which the clone of the stack contains, counts for all but the paths.
func (r namedRun) noAddress(t *testing.T) {
	t.Helper()
	texts := map[string]string{"the output": r.out, "the open's spec": fmt.Sprintf("%+v", r.open), "the session's spec": fmt.Sprintf("%+v", r.spec)}
	if r.err != nil {
		texts["the error"] = r.err.Error()
	}
	for i, e := range r.events {
		texts[fmt.Sprintf("event %d", i+1)] = fmt.Sprintf("%+v", e)
	}
	sources := filepath.Join(r.runDir, "sources")
	filepath.WalkDir(sources, func(path string, d os.DirEntry, err error) error {
		if err == nil && !strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			texts["the path "+path] = strings.TrimPrefix(path, sources)
		}
		return nil
	})
	for where, text := range texts {
		for _, a := range r.addresses {
			if strings.Contains(text, a) {
				t.Errorf("%s shows %q:\n%s", where, a, text)
			}
		}
		if !strings.HasPrefix(where, "the path ") && strings.Contains(text, r.folder) {
			t.Errorf("%s shows the folder %q:\n%s", where, r.folder, text)
		}
	}
}

// logged is the text of every log event of r's host steps.
func (r namedRun) logged() string {
	var b strings.Builder
	for _, e := range r.events {
		if e.Type == "log" {
			b.WriteString(e.Line)
		}
	}
	return b.String()
}

// TestARunCodeFetchesANamedStackThroughTheStackProxy is a run from a run code whose
// documents set the stack hello by its name, which lists the module tools from another
// repository: qory fetches both through the run's stack proxy, which it closes before
// the join, and not through its host proxy, which leads nowhere. Every line, step event
// and spec shows the names, never an address, a ref or a path in the run's sources, and
// the harness list for the session's start line has the names with their commits.
func TestARunCodeFetchesANamedStackThroughTheStackProxy(t *testing.T) {
	r := namedStack{stack: withTools(toolsURL, toolsRef), tools: toolsModule()}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out,
		"qory run: the run "+keyRunID+" starts the workflow hello of the stack hello on claude\n",
		"qory run: fetched the stack hello at "+r.commit[:12]+" in ",
		"qory run: fetched the module tools at "+r.tools[:12]+" in ",
		"qory run: composed ")
	wants(t, r.logged(), "qory run: fetched the stack hello at ", "qory run: fetched the module tools at ")
	stack := slices.Index(r.calls, "stack proxy")
	if stack < 0 || slices.Index(r.calls, "stack proxy closed") < stack || slices.Index(r.calls, "join") < slices.Index(r.calls, "stack proxy closed") {
		t.Errorf("the calls %q do not open and close the stack proxy before the join", r.calls)
	}
	if r.server.Tunnels() == 0 {
		t.Error("no fetch went through the stack proxy")
	}
	want := []session.HarnessSource{{Source: "stack hello", Commit: r.commit}, {Source: "module tools", Commit: r.tools}}
	if !slices.Equal(r.spec.Harness, want) {
		t.Errorf("the harness list is %+v, want %+v", r.spec.Harness, want)
	}
	r.noAddress(t)
}

// TestACheckoutsModulesStayOnTheHostProxy is a run from a run code of a named stack in
// a checkout whose qory.yaml composes on it with a module of another repository: qory
// fetches the stack through the run's stack proxy, and the checkout's module through
// the run's host proxy, by its URL, as a run of a stack by reference does.
func TestACheckoutsModulesStayOnTheHostProxy(t *testing.T) {
	extra := "https://" + proxygittest.Host + "/acme/extra.git"
	r := namedStack{
		stack:    composesOn,
		document: "harness:\n  modules:\n    - name: extra\n      source: {git: \"" + extra + "\", ref: main, path: modules/extra}\n",
		extra: map[string]string{
			"modules/extra/qory-module.yaml":      "apiVersion: qory.dev/v1alpha1\nname: extra\n",
			"modules/extra/skills/extra/SKILL.md": "---\nname: extra\ndescription: Extra.\n---\nExtra.\n",
		},
	}.run(t)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "qory run: fetched the stack hello at "+r.commit[:12]+" in ", "qory run: fetched "+extra+"#main at ")
	if r.server.Tunnels() == 0 || r.hostServer.Tunnels() == 0 {
		t.Errorf("tunnels: the stack proxy %d, the host proxy %d", r.server.Tunnels(), r.hostServer.Tunnels())
	}
	if len(r.spec.Harness) != 2 || r.spec.Harness[0].Source != "stack hello" || r.spec.Harness[1].Source != extra+"#main:modules/extra" {
		t.Errorf("the harness list is %+v", r.spec.Harness)
	}
	r.noAddress(t)
}

// TestARunCodeOfAStackByReferenceOpensNoStackProxy is a run from a run code whose
// documents set the stack by reference: qory fetches it through the run's host proxy,
// as before, and opens no stack proxy.
func TestARunCodeOfAStackByReferenceOpensNoStackProxy(t *testing.T) {
	r := runFromCode(t, nil, nil)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "qory run: fetched <url>#"+r.commit+" at ")
	if slices.Contains(r.calls, "stack proxy") || !slices.Contains(r.calls, "host proxy") {
		t.Errorf("the calls are %q", r.calls)
	}
}

// TestARunCodeOfANamedStackThatFailsShowsNoAddress is a run from a run code of a named
// stack that stops before the join: the stack's ref is gone, its folder has no stack,
// the stack proxy returns 407 for the stack or 403 for the host of a module, a module's
// ref is gone, a module's manifest does not parse, two modules ship one skill, the
// workflow is not in the harness, and the stack proxy does not open. Each line starts
// with its fix and shows the names, and no line, step event or error shows an address,
// a ref or a path in the run's sources.
func TestARunCodeOfANamedStackThatFailsShowsNoAddress(t *testing.T) {
	tools := toolsURL
	for _, c := range []struct {
		name string
		n    namedStack
		exit int
		want []string
	}{
		{"the stack's ref is gone", namedStack{change: func(d map[string]any) {
			d["stack_reference"] = strings.Replace(d["stack_reference"].(string), namedRef, "stacks-gone", 1)
		}}, cmd.ExitInput, []string{"qory run: set the address of the stack hello in Qory Apiary's list of stacks to a branch, tag or commit its repository has: the repository of the stack hello has no branch or tag by the ref it is set to\n"}},
		{"the folder has no stack", namedStack{change: func(d map[string]any) {
			d["stack_reference"] = strings.Replace(d["stack_reference"].(string), ":examples/hello", ":examples/none", 1)
		}}, cmd.ExitInput, []string{"check the folder in the address of the stack hello in Qory Apiary's list of stacks: the repository of the stack hello has no folder at the path it is set to"}},
		{"the stack proxy returns 407 for the stack", namedStack{nowhere: func(n *cmd.Nowhere) {
			n.StackProxy = strings.Replace(n.StackProxy, proxygittest.Password, "another-password", 1)
		}}, 1, []string{"qory run: take the work item in again in Qory Apiary: the run's stack proxy returned 407 Proxy Authentication Required to the password Forager set for the run, so the fetch of the stack hello failed; the code is used\n"}},
		{"the stack proxy returns 403 for a module's host", namedStack{stack: withTools("https://other.test/example-org/tools.git", toolsRef)}, 1,
			[]string{"qory run: add the host of the module tools to gateway.stack_hosts in forager.yaml, or check its source in the stack's files: the fetch of the module tools through the run's stack proxy failed: the stack proxy returned 403, as it does for a host that gateway.stack_hosts does not list\n"}},
		{"a module's ref is gone", namedStack{stack: withTools(tools, "tools-gone"), tools: toolsModule()}, cmd.ExitInput,
			[]string{"qory run: set the ref of the module tools to a branch, tag or commit its repository has: the repository of the module tools has no branch or tag by the ref it is set to\n"}},
		{"a module's manifest does not parse", namedStack{files: map[string]string{"examples/hello/modules/world/qory-module.yaml": "apiVersion: [\n"}}, cmd.ExitInput,
			[]string{"modules/world/qory-module.yaml in the stack hello"}},
		{"a module's source has a user part", namedStack{stack: thenMore("https://reader:s3cret@" + proxygittest.Host + "/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source has a user part that does not parse", namedStack{stack: thenMore("https://reader:p%zzw@" + proxygittest.Host + "/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source has a user part with a space", namedStack{stack: thenMore("https://reader:p w@" + proxygittest.Host + "/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source does not parse", namedStack{stack: thenMore("https://" + proxygittest.Host + "%40evil.example/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{"correct the URL in the source of the module more in the stack hello: qory could not read it as a URL"}},
		{"two modules ship one skill", namedStack{stack: withTools(tools, toolsRef), tools: map[string]string{
			"modules/tools/qory-module.yaml":      "apiVersion: qory.dev/v1alpha1\nname: tools\n",
			"modules/tools/skills/greet/SKILL.md": "---\nname: greet\ndescription: Greets.\n---\nHello.\n",
		}}, 3, []string{"skills/greet is provided by 2 modules", "tools  the module tools  ", "world  the stack hello  "}},
		{"the workflow is not in the harness", namedStack{change: func(d map[string]any) { d["workflow"] = "deploy" }}, 1,
			[]string{"add a skill or command deploy to the stack hello, or set another workflow in Qory Apiary: the composed harness has no skill or command deploy; the code is used"}},
		{"the stack proxy does not open", namedStack{nowhere: func(n *cmd.Nowhere) { n.StackProxyErr = errors.New("the run is closed") }}, 1,
			[]string{"qory run: take the work item in again in Qory Apiary: the run's stack proxy, through which qory fetches the stack hello, did not open: the run is closed; the code is used\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.n.run(t)
			r.addresses = append(r.addresses, "reader", "s3cret", "p%zzw", "p w", "evil.example", "example-org/more")
			if r.joined || cmd.ExitCode(r.err) != c.exit {
				t.Fatalf("%v (exit %d), want exit %d before the join\n%s", r.err, cmd.ExitCode(r.err), c.exit, r.out)
			}
			if strings.Contains(r.out, "fetched the module") && strings.HasPrefix(c.name, "a module's source") {
				t.Errorf("the module was fetched:\n%s", r.out)
			}
			wants(t, r.out+r.err.Error(), c.want...)
			if c.name != "the workflow is not in the harness" {
				wants(t, r.logged(), c.want[0])
			}
			r.noAddress(t)
		})
	}
}

// composesOn is the stack file of hello as a stack that a checkout composes on.
func composesOn(s string) string { return s + "extending:\n  kinds: [skills]\n" }

// extraModule are the files of the repository acme/extra, with the module extra.
func extraModule() map[string]string {
	return map[string]string{
		"modules/extra/qory-module.yaml":      "apiVersion: qory.dev/v1alpha1\nname: extra\n",
		"modules/extra/skills/extra/SKILL.md": "---\nname: extra\ndescription: Extra.\n---\nExtra.\n",
	}
}

// TestACheckoutModuleThatFailsOnANamedStackHasTheHostProxysLine is a run from a run
// code of a named stack in a checkout whose qory.yaml composes on it with a module whose
// ref is gone, or whose fetch the run's host proxy denies: each stops with the line of a
// source fetched through the host proxy, which shows the module's URL and ref, and no
// fix of the stack proxy.
func TestACheckoutModuleThatFailsOnANamedStackHasTheHostProxysLine(t *testing.T) {
	extra := "https://" + proxygittest.Host + "/acme/extra.git"
	for _, c := range []struct {
		name, ref string
		nowhere   func(*cmd.Nowhere)
		exit      int
		want      string
	}{
		{"the ref is gone", "gone", nil, cmd.ExitInput,
			"qory run: check that " + extra + " has the branch, tag or commit gone, that the run's policy allows " + proxygittest.Host + " and, for a private repository, that the run configuration has a credential that reads it. The fetch failed: " + extra + " has no branch or tag gone\n"},
		{"the fetch fails", "main", func(n *cmd.Nowhere) { n.Proxy = strings.Replace(n.Proxy, proxygittest.Password, "another-password", 1) }, 1,
			"qory run: check that " + extra + " has the branch, tag or commit main, that the run's policy allows " + proxygittest.Host + " and, for a private repository, that the run configuration has a credential that reads it. The fetch failed: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := namedStack{
				stack:    composesOn,
				document: "harness:\n  modules:\n    - name: extra\n      source: {git: \"" + extra + "\", ref: " + c.ref + ", path: modules/extra}\n",
				extra:    extraModule(),
				nowhere:  c.nowhere,
			}.run(t)
			if r.joined || cmd.ExitCode(r.err) != c.exit {
				t.Fatalf("%v (exit %d), want exit %d before the join\n%s", r.err, cmd.ExitCode(r.err), c.exit, r.out)
			}
			wants(t, r.out, "qory run: fetched the stack hello at ", c.want)
			if strings.Contains(r.out, "stack_hosts") || strings.Contains(r.out, "the stack proxy") {
				t.Errorf("the line has a fix of the stack proxy:\n%s", r.out)
			}
			r.noAddress(t)
		})
	}
}

// TestANamedStackWithNoNameInItsFileIsTheBaseByItsName is a run from a run code of a
// named stack in the folder examples/greeter, whose file sets no name, in a checkout
// whose qory.yaml composes on it: the compose report records the base by the stack's
// name, hello, not by the folder's.
func TestANamedStackWithNoNameInItsFileIsTheBaseByItsName(t *testing.T) {
	r := namedStack{
		folder:   "examples/greeter",
		stack:    func(s string) string { return composesOn(strings.Replace(s, "\nname: hello\n", "\n", 1)) },
		document: "harness:\n  modules:\n    - name: extra\n      source: {git: \"https://" + proxygittest.Host + "/acme/extra.git\", ref: main, path: modules/extra}\n",
		extra:    extraModule(),
	}.run(t)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	var reports []string
	cache, _ := os.UserCacheDir()
	for _, dir := range []string{r.runDir, ".", cache} {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(path, "-report.json") {
				reports = append(reports, path)
			}
			return nil
		})
	}
	if len(reports) != 1 {
		t.Fatalf("the run directory has the reports %q", reports)
	}
	data, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Base struct{ Name string } `json:"base"`
	}
	if err := json.Unmarshal(data, &rep); err != nil || rep.Base.Name != "hello" {
		t.Errorf("the base is %q (%v):\n%s", rep.Base.Name, err, data)
	}
	r.noAddress(t)
}

// TestAWalledRunOfANamedStackThatMountsQorysCacheFolderStops is a walled run from a run
// code whose documents set the stack hello by its name, with qory's folder in the cache
// directory mounted by --mount or by wall.mounts in forager.yaml: the run stops, exit 2,
// with the line of the guard of that folder, before it opens the run's stack proxy,
// fetches anything or runs a command of the wall.
func TestAWalledRunOfANamedStackThatMountsQorysCacheFolderStops(t *testing.T) {
	for _, from := range []string{"--mount", "wall.mounts in forager.yaml"} {
		t.Run(from, func(t *testing.T) {
			docker, log := fakeDocker(t)
			helper := staticELF(t)
			var cache string
			r := namedStack{before: func() []string {
				base, err := os.UserCacheDir()
				if err != nil {
					t.Fatal(err)
				}
				cache = filepath.Join(base, "qory")
				if err := os.MkdirAll(cache, 0o700); err != nil {
					t.Fatal(err)
				}
				wall := "wall:\n  adapter: docker\n  command: " + docker + "\n  helper: " + helper + "\n  user: \"1000:1000\"\n  image: example.com/agent:1\n"
				file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
				if from == "--mount" {
					writeFile(t, file, wall)
					return []string{"--mount", cache}
				}
				writeFile(t, file, wall+"  mounts: [\""+cache+"\"]\n")
				return nil
			}}.run(t)
			want := "remove " + cache + " from " + from + ": it is " + cache + ", where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and a walled run of a named stack mounts no part of " + cache + "; the code is used"
			if r.err == nil || r.err.Error() != want || cmd.ExitCode(r.err) != cmd.ExitInput {
				t.Fatalf("%v (exit %d), want %q, exit %d\n%s", r.err, cmd.ExitCode(r.err), want, cmd.ExitInput, r.out)
			}
			if slices.Contains(r.calls, "stack proxy") || r.server.Tunnels() != 0 || strings.Contains(r.out, "fetched") {
				t.Errorf("the run fetched before it stopped: the calls %q, %d tunnels\n%s", r.calls, r.server.Tunnels(), r.out)
			}
			if data, err := os.ReadFile(log); err == nil {
				t.Errorf("the wall ran a command:\n%s", data)
			}
			r.noAddress(t)
		})
	}
}
