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
	"time"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// The refs of the repository of the harness's base and of the repository of its module
// tools, which, as the URLs, no text of a run whose harness is hidden shows.
const (
	namedRef = "stacks-r7"
	toolsRef = "tools-r3"
)

// The harness of a run from a run code as a text shows it, for a harness document, and
// the bases of a harness document and of the checkout's file.
const (
	workspaceHarness = "the workspace's harness"
	workspaceBase    = "the base of the workspace's harness"
	checkoutBase     = "the base of the checkout's harness"
)

// extendable is the block that a stack file ends with so that a harness may extend it,
// as the harness document and the checkout's qory.yaml of these tests do, with modules
// that ship an AGENTS.md.
const extendable = "extending:\n  instructions: true\n"

// deadProxy is a proxy URL that leads nowhere, for the proxy a run must not use.
const deadProxy = "http://qory:nowhere@127.0.0.1:9"

// codeHarness is a run from a run code whose documents set harness_hidden to true and
// contain a harness document that extends the folder examples/hello at [namedRef] of a
// repository that the run's stack proxy serves and its host proxy does not. With
// checkout set, the documents contain no harness document, and the checkout's qory.yaml
// extends that folder, which the run's host proxy serves and its stack proxy does not.
type codeHarness struct {
	// stack changes the base's stack file, nil for none; files are further files of the
	// base's repository, and tools, when not nil, the files of the repository of the
	// module tools, at [toolsRef].
	stack func(string) string
	files map[string]string
	tools map[string]string
	// links and toolsLinks are the links of the base's repository and of the repository
	// of the module tools, each path to its target; a link replaces the file at its path.
	links, toolsLinks map[string]string
	// modules are the modules of the harness document, or of the checkout's qory.yaml,
	// each as the documents have it.
	modules []map[string]any
	// change changes the documents; nowhere changes what the run opened nowhere has.
	change  func(map[string]any)
	nowhere func(*cmd.Nowhere)
	// checkout, when set, is the run whose checkout's qory.yaml extends the base, with
	// the documents' harness document left out; document, when not nil, changes the
	// text of that qory.yaml.
	checkout bool
	document func(string) string
	// shown sets harness_hidden to false.
	shown bool
	// folder is the base's folder in its repository, examples/hello when it is "".
	folder string
	// before, when not nil, runs once the run's environment is set, and returns further
	// arguments of qory run.
	before func() []string
}

// harnessRun is what a [codeHarness] run came to: the output with its error, the run's
// spec at the open and the session's at the join, the events of its host steps, the
// calls the run got, the commits of the base and of the module tools, the server of the
// run's proxies, the run directory, the URL of the base's repository, and addresses,
// the texts of the addresses that no text of a hidden harness may show.
type harnessRun struct {
	out       string
	err       error
	open      session.OpenSpec
	opened    bool
	spec      session.SessionSpec
	joined    bool
	events    []cmd.StepEvent
	calls     []string
	commit    string
	tools     string
	server    *proxygittest.Server
	runDir    string
	url       string
	addresses []string
	folder    string
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

// userPart is the line of a module of the base, more, whose source has a user part.
const userPart = "remove the user name and password from the source of the module more in " + workspaceBase + ": the run's stack proxy sets the harness credentials of the run configuration, so a source's URL needs none"

// nonASCII is the line of a module of the base, more, whose source has a host with a
// character outside ASCII.
const nonASCII = "write the host in the source of the module more in " + workspaceBase + " in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @"

// thenMore is a stack file that lists the module tools, at [toolsURL], then the module
// more at url.
func thenMore(url string) func(string) string {
	return func(s string) string {
		return withTools(toolsURL, toolsRef)(s) + "  - name: more\n    source: {git: \"" + url + "\", ref: main, path: modules/more}\n"
	}
}

// run runs qory run from the run code of c and opens the run nowhere.
func (c codeHarness) run(t *testing.T) harnessRun {
	t.Helper()
	folder := c.folder
	if folder == "" {
		folder = "examples/hello"
	}
	files := map[string]string{}
	for k, v := range helloFiles(t) {
		files[folder+strings.TrimPrefix(k, "examples/hello")] = v
	}
	maps.Copy(files, c.files)
	if c.stack != nil {
		files[folder+"/qory-stack.yaml"] = c.stack(files[folder+"/qory-stack.yaml"])
	}
	files[folder+"/qory-stack.yaml"] += extendable
	for name := range c.links {
		delete(files, name)
	}
	h := newHostFetch(t, "", "main")
	r := harnessRun{server: h.server, runDir: h.runDir, folder: folder}
	r.commit = h.server.RepositoryWithLinks(t, "example-org/hello", namedRef, files, c.links)
	if c.tools != nil {
		r.tools = h.server.RepositoryWithLinks(t, "example-org/tools", toolsRef, c.tools, c.toolsLinks)
	}
	r.url = h.server.URL("example-org/hello")
	r.addresses = []string{r.url, strings.TrimSuffix(r.url, ".git"), "example-org/hello", "example-org/tools", "other.test", namedRef, toolsRef, string(filepath.Separator) + "sources" + string(filepath.Separator)}
	section := map[string]any{"extends": map[string]any{"git": r.url, "ref": namedRef, "path": folder}}
	if c.modules != nil {
		section["modules"] = c.modules
	}
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		delete(d, "harness")
		if !c.checkout {
			d["harness"] = map[string]any{"harness": section}
		}
		d["harness_hidden"] = !c.shown
		if c.change != nil {
			c.change(d)
		}
	})
	t.Cleanup(cmd.SetJoinSession(func(s session.SessionSpec) { r.spec, r.joined = s, true }))
	t.Setenv("QORY_RUN_KEY", "")
	nowhere := cmd.Nowhere{
		System: proxygittest.Host, Proxy: deadProxy,
		StackProxy: h.server.Proxy.URL, StackCABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		OnOpen: func(s session.OpenSpec) { r.open, r.opened = s, true },
		OnStep: func(e cmd.StepEvent) { r.events = append(r.events, e) },
	}
	if c.checkout {
		doc, err := json.Marshal(map[string]any{"harness": section})
		if err != nil {
			t.Fatal(err)
		}
		text := string(doc) + "\n"
		if c.document != nil {
			text = c.document(text)
		}
		writeFile(t, "qory.yaml", text)
	}
	if c.nowhere != nil {
		c.nowhere(&nowhere)
	}
	args := []string{"run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL}
	if c.before != nil {
		args = append(args, c.before()...)
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
// base's folder, which the clone of the base contains, counts for all but the paths.
func (r harnessRun) noAddress(t *testing.T) {
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

// fetchedNothing fails the test when r opened a proxy or fetched a harness source.
func (r harnessRun) fetchedNothing(t *testing.T) {
	t.Helper()
	if slices.Contains(r.calls, "stack proxy") || slices.Contains(r.calls, "host proxy") || r.server.Tunnels() != 0 || strings.Contains(r.out, "fetched") {
		t.Errorf("the run fetched before it stopped: the calls %q, %d tunnels\n%s", r.calls, r.server.Tunnels(), r.out)
	}
}

// logged is the text of every log event of r's host steps.
func (r harnessRun) logged() string {
	var b strings.Builder
	for _, e := range r.events {
		if e.Type == "log" {
			b.WriteString(e.Line)
		}
	}
	return b.String()
}

// noHarnessFile fails the test when the run wrote harness.yaml into its run directory.
func (r harnessRun) noHarnessFile(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(r.runDir, "harness.yaml")); err == nil {
		t.Error("the run wrote harness.yaml")
	}
}

// TestARunCodeComposesTheWorkspacesHarnessThroughTheStackProxy is a run from a run code
// whose documents contain a harness document whose base lists the module tools from
// another repository, and whose harness is hidden, in a checkout whose qory.yaml lists a
// folder that does not exist: qory writes the document to harness.yaml in the run
// directory, mode 0600, and composes it in place of the checkout's qory.yaml. It
// fetches the base and the module through the run's stack proxy, which it closes before
// the join, and opens no host proxy. Every line, step event and spec shows the names,
// never an address, a ref or a path in the run's sources, and the harness list for the
// session's start line has the names with their commits. The base's
// workflows.hello.time_limit is the session's time limit.
func TestARunCodeComposesTheWorkspacesHarnessThroughTheStackProxy(t *testing.T) {
	r := codeHarness{
		stack: func(s string) string {
			return withTools(toolsURL, toolsRef)(s) + "workflows:\n  hello:\n    time_limit: 5h\n"
		},
		tools: toolsModule(),
		before: func() []string {
			writeFile(t, "qory.yaml", "harness:\n  modules:\n    - name: other\n      source: {path: ./nowhere}\n")
			return nil
		},
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out,
		"qory run: the run "+keyRunID+" starts the workflow hello of "+workspaceHarness+" on claude\n",
		"qory run: fetched "+workspaceBase+" at "+r.commit[:12]+" in ",
		"qory run: fetched the module tools at "+r.tools[:12]+" in ",
		"qory run: composed ")
	wants(t, r.logged(), "qory run: fetched "+workspaceBase+" at ", "qory run: fetched the module tools at ")
	stack := slices.Index(r.calls, "stack proxy")
	if stack < 0 || slices.Index(r.calls, "stack proxy closed") < stack || slices.Index(r.calls, "join") < slices.Index(r.calls, "stack proxy closed") {
		t.Errorf("the calls %q do not open and close the stack proxy before the join", r.calls)
	}
	if slices.Contains(r.calls, "host proxy") {
		t.Errorf("the calls %q open the host proxy", r.calls)
	}
	if r.server.Tunnels() == 0 {
		t.Error("no fetch went through the stack proxy")
	}
	want := []session.HarnessSource{{Source: "base of the workspace's harness", Commit: r.commit}, {Source: "module tools", Commit: r.tools}}
	if !slices.Equal(r.spec.Harness, want) {
		t.Errorf("the harness list is %+v, want %+v", r.spec.Harness, want)
	}
	file := filepath.Join(r.runDir, "harness.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Harness struct {
			Extends struct{ Git, Ref, Path string } `json:"extends"`
		} `json:"harness"`
	}
	st, statErr := os.Stat(file)
	if json.Unmarshal(data, &doc) != nil || statErr != nil || st.Mode().Perm() != 0o600 || doc.Harness.Extends.Git != r.url || doc.Harness.Extends.Ref != namedRef || doc.Harness.Extends.Path != r.folder {
		t.Errorf("harness.yaml (%v) has:\n%s", statErr, data)
	}
	if r.spec.Timeout <= 5*time.Hour-time.Minute || r.spec.Timeout > 5*time.Hour {
		t.Errorf("the time limit left is %v, want 5h less the time before the join", r.spec.Timeout)
	}
	r.noAddress(t)
}

// TestARunCodesShownHarnessDocumentShowsAddresses is a run from a run code whose
// documents contain a harness document with a module of another repository and set
// harness_hidden to false: qory fetches both through the run's stack proxy, and its
// lines show the URL and the ref, as for any harness.
func TestARunCodesShownHarnessDocumentShowsAddresses(t *testing.T) {
	r := codeHarness{shown: true, modules: []map[string]any{{"name": "tools", "source": map[string]any{"git": toolsURL, "ref": toolsRef, "path": "modules/tools"}}}, tools: toolsModule()}.run(t)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "qory run: the run "+keyRunID+" starts the workflow hello of "+workspaceHarness+" on claude\n",
		"qory run: fetched "+r.url+"#"+namedRef+" at "+r.commit[:12]+" in ", "qory run: fetched "+toolsURL+"#"+toolsRef+" at ")
	if slices.Contains(r.calls, "host proxy") || r.server.Tunnels() == 0 {
		t.Errorf("the calls %q, %d tunnels", r.calls, r.server.Tunnels())
	}
}

// TestARunCodesShownHarnessDocumentWithAGoneRefShowsIt is a run from a run code whose
// documents contain a harness document whose base's ref its repository does not have,
// with harness_hidden set to false: the run stops, exit 2, with a line that shows the
// URL and the ref and the stack proxy's fix.
func TestARunCodesShownHarnessDocumentWithAGoneRefShowsIt(t *testing.T) {
	r := codeHarness{shown: true, change: func(d map[string]any) {
		d["harness"].(map[string]any)["harness"].(map[string]any)["extends"].(map[string]any)["ref"] = "stacks-gone"
	}}.run(t)
	if r.joined || cmd.ExitCode(r.err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d), want exit 2 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, "qory run: check that "+r.url+" has the branch, tag or commit stacks-gone and that gateway.stack_hosts in the gateway's forager.yaml or the run configuration's stack_hosts lists "+proxygittest.Host+", and, for a private repository, that the run configuration has a harness credential whose hosts and paths include the repository. The fetch through the run's stack proxy failed: "+r.url+" has no branch or tag stacks-gone\n")
}

// TestARunCodesHarnessDocumentWithASourceQoryDoesNotFetchStops is a run from a run code
// whose documents contain a harness document with a source that is not https, has a
// user part, an @ before its path, a host with a character outside ASCII, or a URL that
// does not parse. Each stops the run after the redemption, exit 2, before the run opens
// and before anything is fetched, with a line that starts with its fix in the
// workspace's settings, shows the URL only when the harness is shown and has no user
// part, and ends "; the code is used".
func TestARunCodesHarnessDocumentWithASourceQoryDoesNotFetchStops(t *testing.T) {
	const settings = " of the harness in the workspace's settings in Qory Apiary"
	setExtends := func(git string) func(map[string]any) {
		return func(d map[string]any) {
			d["harness"].(map[string]any)["harness"].(map[string]any)["extends"].(map[string]any)["git"] = git
		}
	}
	module := func(git string) []map[string]any {
		return []map[string]any{{"name": "more", "source": map[string]any{"git": git, "ref": "main"}}}
	}
	userLine := "remove the user name and password from the URL of the module more" + settings + ": the run's stack proxy sets the harness credentials of the run configuration, so a source's URL needs none; the code is used"
	host := proxygittest.Host
	for _, c := range []struct {
		name string
		c    codeHarness
		want string
	}{
		{"an http base", codeHarness{change: setExtends("http://" + host + "/example-org/hello.git")},
			"set extends" + settings + " to an https source: its URL is not an https URL with a host, and qory fetches a harness only over https; the code is used"},
		{"an http base, shown", codeHarness{shown: true, change: setExtends("http://" + host + "/example-org/hello.git")},
			"set extends" + settings + " to an https source: http://" + host + "/example-org/hello.git is not an https URL with a host, and qory fetches a harness only over https; the code is used"},
		{"an ssh module", codeHarness{modules: module("git@" + host + ":example-org/more.git")},
			"set the module more" + settings + " to an https source: its URL is not an https URL with a host, and qory fetches a harness only over https; the code is used"},
		{"a module with a user part", codeHarness{modules: module("https://reader:s3cret@" + host + "/example-org/more.git")}, userLine},
		{"a module with a user part, shown", codeHarness{shown: true, modules: module("https://reader:s3cret@" + host + "/example-org/more.git")}, userLine},
		{"a module with an @ after a ?", codeHarness{modules: module("https://" + host + "?@evil.example/example-org/more.git")}, userLine},
		{"a module with an @ after a port", codeHarness{modules: module("https://" + host + ":443@evil.example/example-org/more.git")}, userLine},
		{"a module with a fullwidth @ in its host", codeHarness{modules: module("https://u＠" + host + "/example-org/more.git")},
			"write the host in the URL of the module more" + settings + " in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @; the code is used"},
		{"a module whose URL does not parse", codeHarness{modules: module("https://" + host + "%40evil.example/example-org/more.git")},
			"correct the URL of the module more" + settings + ": qory could not read it as a URL; the code is used"},
		{"a module listed without a name", codeHarness{modules: []map[string]any{{"source": map[string]any{"git": "ssh://" + host + "/example-org/more.git", "ref": "main"}}}},
			"set modules[0]" + settings + " to an https source: its URL is not an https URL with a host, and qory fetches a harness only over https; the code is used"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.c.run(t)
			if r.err == nil || r.err.Error() != c.want || cmd.ExitCode(r.err) != cmd.ExitInput {
				t.Fatalf("%v (exit %d), want %q, exit 2\n%s", r.err, cmd.ExitCode(r.err), c.want, r.out)
			}
			if r.opened || r.joined {
				t.Errorf("the run opened: %v, joined: %v", r.opened, r.joined)
			}
			r.fetchedNothing(t)
			r.noHarnessFile(t)
			r.addresses = append(r.addresses, "reader", "s3cret", "evil.example", "＠")
			if strings.HasSuffix(c.name, ", shown") {
				r.addresses, r.folder = []string{"reader", "s3cret"}, "\x00"
			}
			r.noAddress(t)
		})
	}
}

// TestARunCodeOfAHiddenHarnessThatFailsShowsNoAddress is a run from a run code of a
// hidden harness document that stops before the join: the base's ref is gone, its
// folder is not in the repository or has no stack file, the stack proxy returns 407 for
// the base or 403 for the host of a module, a module's ref is gone, a module's manifest
// does not parse, a module of the base has a source with a user part, an @ before its
// path, a host outside ASCII, a URL that does not parse or a URL that is not https, two
// modules ship one skill, the workflow is not in the harness, and the stack proxy does
// not open. Each line starts with its fix and shows the names, and no line, step event
// or error shows an address, a ref or a path in the run's sources.
func TestARunCodeOfAHiddenHarnessThatFailsShowsNoAddress(t *testing.T) {
	tools := toolsURL
	extends := func(d map[string]any) map[string]any {
		return d["harness"].(map[string]any)["harness"].(map[string]any)["extends"].(map[string]any)
	}
	for _, c := range []struct {
		name string
		c    codeHarness
		exit int
		want []string
	}{
		{"the base's ref is gone", codeHarness{change: func(d map[string]any) { extends(d)["ref"] = "stacks-gone" }}, cmd.ExitInput,
			[]string{"qory run: set the ref of " + workspaceBase + " to a branch, tag or commit its repository has: the repository of " + workspaceBase + " has no branch or tag by the ref it is set to\n"}},
		{"the folder is not there", codeHarness{change: func(d map[string]any) { extends(d)["path"] = "examples/none" }}, cmd.ExitInput,
			[]string{"check " + workspaceBase + ": the repository of " + workspaceBase + " has no folder at the path it is set to"}},
		{"the folder has no stack file", codeHarness{change: func(d map[string]any) { extends(d)["path"] = "examples" }}, cmd.ExitInput,
			[]string{"check the folder of " + workspaceBase + ": its repository has no qory-stack.yaml there"}},
		{"the stack proxy returns 407 for the base", codeHarness{nowhere: func(n *cmd.Nowhere) {
			n.StackProxy = strings.Replace(n.StackProxy, proxygittest.Password, "another-password", 1)
		}}, 1, []string{"qory run: take the work item in again in Qory Apiary: the run's stack proxy returned 407 Proxy Authentication Required to the password Forager set for the run, so the fetch of " + workspaceBase + " failed; the code is used\n"}},
		{"the stack proxy returns 403 for a module's host", codeHarness{stack: withTools("https://other.test/example-org/tools.git", toolsRef)}, 1,
			[]string{"qory run: add the host of the module tools to gateway.stack_hosts in the gateway's forager.yaml or to the stack_hosts of the run configuration that Qory Apiary sends, and, for a private repository, a harness credential for the module tools to that run configuration, then take the work item in again: the fetch of the module tools through the run's stack proxy failed: the stack proxy returned 403, as it does for a host that neither list of stack hosts contains, and for a host of the run configuration's stack_hosts that the gateway's policy denies or has path rules for; the code is used\n"}},
		{"a module's ref is gone", codeHarness{stack: withTools(tools, "tools-gone"), tools: toolsModule()}, cmd.ExitInput,
			[]string{"qory run: set the ref of the module tools to a branch, tag or commit its repository has: the repository of the module tools has no branch or tag by the ref it is set to\n"}},
		{"a module's manifest does not parse", codeHarness{files: map[string]string{"examples/hello/modules/world/qory-module.yaml": "apiVersion: [\n"}}, cmd.ExitInput,
			[]string{"module world: qory-module.yaml in the module world: yaml: "}},
		{"a module's source has a user part", codeHarness{stack: thenMore("https://reader:s3cret@" + proxygittest.Host + "/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source has a user part that does not parse", codeHarness{stack: thenMore("https://reader:p%zzw@" + proxygittest.Host + "/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source has an @ after a #", codeHarness{stack: thenMore("https://" + proxygittest.Host + "#@evil.example/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source has an @ after %3F", codeHarness{stack: thenMore("https://" + proxygittest.Host + "%3F@evil.example/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source has an @ after a backslash", codeHarness{stack: thenMore("https://" + proxygittest.Host + `\\@evil.example/example-org/more.git`), tools: toolsModule()}, cmd.ExitInput,
			[]string{userPart}},
		{"a module's source has a fullwidth : and @ in its host", codeHarness{stack: thenMore("https://u：p＠" + proxygittest.Host + "/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{nonASCII}},
		{"a module's source has an encoded fullwidth / in its host", codeHarness{stack: thenMore("https://" + proxygittest.Host + "%EF%BC%8F.evil.example/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{nonASCII}},
		{"a module's source does not parse", codeHarness{stack: thenMore("https://" + proxygittest.Host + "%40evil.example/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{"correct the URL in the source of the module more in " + workspaceBase + ": qory could not read it as a URL"}},
		{"a module's source is not https", codeHarness{stack: thenMore("http://" + proxygittest.Host + "/example-org/more.git"), tools: toolsModule()}, cmd.ExitInput,
			[]string{"qory run: use an https URL for the module more: a run from a control plane fetches git sources only over https\n"}},
		{"two modules ship one skill", codeHarness{stack: withTools(tools, toolsRef), tools: map[string]string{
			"modules/tools/qory-module.yaml":      "apiVersion: qory.dev/v1alpha1\nname: tools\n",
			"modules/tools/skills/greet/SKILL.md": "---\nname: greet\ndescription: Greets.\n---\nHello.\n",
		}}, 3, []string{"skills/greet is provided by 2 modules", "tools  the module tools  ", "world  " + workspaceBase + "  "}},
		{"the workflow is not in the harness", codeHarness{change: func(d map[string]any) { d["workflow"] = "deploy" }}, 1,
			[]string{"add a skill or command deploy to " + workspaceHarness + ", or set another workflow in Qory Apiary: the composed harness has no skill or command deploy; the code is used"}},
		{"the stack proxy does not open", codeHarness{nowhere: func(n *cmd.Nowhere) { n.StackProxyErr = errors.New("the run is closed") }}, 1,
			[]string{"qory run: take the work item in again in Qory Apiary: the run's stack proxy, through which qory fetches " + workspaceHarness + ", did not open: the run is closed; the code is used\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.c.run(t)
			r.addresses = append(r.addresses, "reader", "s3cret", "p%zzw", "evil.example", "example-org/more", "＠", "stacks-gone", "tools-gone", "examples/none")
			if r.joined || cmd.ExitCode(r.err) != c.exit {
				t.Fatalf("%v (exit %d), want exit %d before the join\n%s", r.err, cmd.ExitCode(r.err), c.exit, r.out)
			}
			if strings.Contains(r.out, "fetched the module more") {
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

// TestARunCodeWithoutAHarnessDocumentComposesTheCheckoutsFile is a run from a run code
// whose documents contain no harness document, in a checkout whose qory.yaml extends a
// stack of another repository, which sets the workflow's time limit: qory composes the
// checkout's file, fetches the base through the run's stack proxy, which it closes
// before the join, opens no host proxy and writes no harness.yaml. With the harness
// hidden, every line, event and spec shows the base by its name; with it shown, the
// fetch line shows the URL and the ref.
func TestARunCodeWithoutAHarnessDocumentComposesTheCheckoutsFile(t *testing.T) {
	for _, shown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shown %v", shown), func(t *testing.T) {
			r := codeHarness{checkout: true, shown: shown, stack: func(s string) string { return s + "workflows:\n  hello:\n    time_limit: 5h\n" }}.run(t)
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			fetched := "qory run: fetched " + checkoutBase + " at " + r.commit[:12] + " in "
			if shown {
				fetched = "qory run: fetched " + r.url + "#" + namedRef + " at " + r.commit[:12] + " in "
			}
			wants(t, r.out, "qory run: the run "+keyRunID+" starts the workflow hello of the checkout's harness on claude\n", fetched, "qory run: composed ")
			stack := slices.Index(r.calls, "stack proxy")
			if stack < 0 || slices.Index(r.calls, "stack proxy closed") < stack || slices.Index(r.calls, "join") < slices.Index(r.calls, "stack proxy closed") || slices.Contains(r.calls, "host proxy") || r.server.Tunnels() == 0 {
				t.Errorf("the calls %q, %d tunnels, want the stack proxy opened and closed before the join, and no host proxy", r.calls, r.server.Tunnels())
			}
			r.noHarnessFile(t)
			if r.spec.Timeout <= 5*time.Hour-time.Minute || r.spec.Timeout > 5*time.Hour {
				t.Errorf("the time limit left is %v, want 5h less the time before the join", r.spec.Timeout)
			}
			if shown {
				return
			}
			want := []session.HarnessSource{{Source: "base of the checkout's harness", Commit: r.commit}}
			if !slices.Equal(r.spec.Harness, want) {
				t.Errorf("the harness list is %+v, want %+v", r.spec.Harness, want)
			}
			r.noAddress(t)
		})
	}
}

// TestARunCodeOfAHiddenCheckoutHarnessThatFailsShowsNoAddress is a run from a run code
// whose documents contain no harness document and hide the harness, in a checkout whose
// qory.yaml extends a stack whose ref is gone or whose folder is not there, whose fetch
// the run's stack proxy denies, or that lists a module whose ref is gone or whose URL is
// not https: each stops with a line that shows the names, and no line, step event or
// error shows an address or a fix of the host proxy.
func TestARunCodeOfAHiddenCheckoutHarnessThatFailsShowsNoAddress(t *testing.T) {
	for _, c := range []struct {
		name string
		c    codeHarness
		exit int
		want string
	}{
		{"the base's ref is gone", codeHarness{document: func(s string) string { return strings.Replace(s, namedRef, "stacks-gone", 1) }}, cmd.ExitInput,
			"qory run: set the ref of " + checkoutBase + " to a branch, tag or commit its repository has: the repository of " + checkoutBase + " has no branch or tag by the ref it is set to\n"},
		{"the base's folder is not there", codeHarness{document: func(s string) string { return strings.Replace(s, "examples/hello", "examples/none", 1) }}, cmd.ExitInput,
			"check " + checkoutBase + ": the repository of " + checkoutBase + " has no folder at the path it is set to"},
		{"the stack proxy returns 407", codeHarness{nowhere: func(n *cmd.Nowhere) {
			n.StackProxy = strings.Replace(n.StackProxy, proxygittest.Password, "another-password", 1)
		}}, 1, "qory run: take the work item in again in Qory Apiary: the run's stack proxy returned 407 Proxy Authentication Required to the password Forager set for the run, so the fetch of " + checkoutBase + " failed; the code is used\n"},
		{"a module's ref is gone", codeHarness{modules: []map[string]any{{"name": "tools", "source": map[string]any{"git": toolsURL, "ref": "tools-gone", "path": "modules/tools"}}}, tools: toolsModule()}, cmd.ExitInput,
			"qory run: set the ref of the module tools to a branch, tag or commit its repository has: the repository of the module tools has no branch or tag by the ref it is set to\n"},
		{"a module's source is not https", codeHarness{modules: []map[string]any{{"name": "tools", "source": map[string]any{"git": "git@" + proxygittest.Host + ":example-org/tools.git", "ref": toolsRef}}}}, cmd.ExitInput,
			"qory run: use an https URL for the module tools: a run from a control plane fetches git sources only over https\n"},
		{"the stack proxy returns 403 for a module's host", codeHarness{modules: []map[string]any{{"name": "tools", "source": map[string]any{"git": "https://other.test/example-org/tools.git", "ref": toolsRef, "path": "modules/tools"}}}}, 1,
			"qory run: add the host of the module tools to gateway.stack_hosts in the gateway's forager.yaml or to the stack_hosts of the run configuration that Qory Apiary sends, and, for a private repository, a harness credential for the module tools to that run configuration, then take the work item in again: the fetch of the module tools through the run's stack proxy failed: the stack proxy returned 403, as it does for a host that neither list of stack hosts contains, and for a host of the run configuration's stack_hosts that the gateway's policy denies or has path rules for; the code is used\n"},
		{"the base's URL has a user part", codeHarness{document: func(s string) string { return strings.Replace(s, "https://", "https://reader:s3cret@", 1) }}, cmd.ExitInput,
			"remove the user name and password from the URL of " + checkoutBase + ": the run's stack proxy sets the harness credentials of the run configuration, so a source's URL needs none"},
		{"the base's host has a character outside ASCII", codeHarness{document: func(s string) string { return strings.Replace(s, "https://", "https://u＠", 1) }}, cmd.ExitInput,
			"write the host in the URL of " + checkoutBase + " in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.c.checkout = true
			r := c.c.run(t)
			r.addresses = append(r.addresses, "stacks-gone", "tools-gone", "examples/none", "reader", "s3cret", "＠")
			if r.joined || cmd.ExitCode(r.err) != c.exit {
				t.Fatalf("%v (exit %d), want exit %d before the join\n%s", r.err, cmd.ExitCode(r.err), c.exit, r.out)
			}
			wants(t, r.out+r.err.Error(), c.want)
			if strings.Contains(r.out, "host proxy") || strings.Contains(r.out, "run's policy") {
				t.Errorf("the line has a fix of the host proxy:\n%s", r.out)
			}
			r.noAddress(t)
		})
	}
}

// TestARunCodeWithNoHarnessAnywhereStops is a run from a run code whose documents contain
// no harness document, in a checkout with no qory.yaml or harness.yaml: the run stops,
// exit 1, with the line of the guard, before it opens a proxy or fetches anything.
func TestARunCodeWithNoHarnessAnywhereStops(t *testing.T) {
	r := codeHarness{checkout: true, before: func() []string {
		if err := os.Remove("qory.yaml"); err != nil {
			t.Fatal(err)
		}
		return nil
	}}.run(t)
	want := "qory run: take the work item in again in Qory Apiary: the run's documents contain no harness and the checkout has no qory.yaml or harness.yaml; the code is used\n"
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.out, want)
	wants(t, r.logged(), want)
	r.fetchedNothing(t)
	r.noHarnessFile(t)
}

// TestAHarnessDocumentWithNoNameInItsBaseIsTheBaseByAPhrase is a run from a run code of
// a hidden harness document whose base, in the folder examples/greeter, sets no name:
// the compose report records the base as base, not by its folder's name.
func TestAHarnessDocumentWithNoNameInItsBaseIsTheBaseByAPhrase(t *testing.T) {
	r := codeHarness{
		folder: "examples/greeter",
		stack:  func(s string) string { return strings.Replace(s, "\nname: hello\n", "\n", 1) },
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
	if err := json.Unmarshal(data, &rep); err != nil || rep.Base.Name != "base" {
		t.Errorf("the base is %q (%v):\n%s", rep.Base.Name, err, data)
	}
	r.noAddress(t)
}

// TestAWalledRunOfAHiddenHarnessThatMountsTheFolderOfItsHomeStops is a walled run from a
// run code whose harness is hidden, with --home set to a folder outside qory's cache
// folder and that folder mounted by --mount: the folder contains the harness report
// beside the home, so the run stops, exit 2, with the line of the guard of the report,
// before it opens the run's stack proxy, fetches anything or runs a command of the
// wall.
func TestAWalledRunOfAHiddenHarnessThatMountsTheFolderOfItsHomeStops(t *testing.T) {
	docker, log := fakeDocker(t)
	helper := staticELF(t)
	homes := filepath.Join(t.TempDir(), "homes")
	r := codeHarness{before: func() []string {
		if err := os.MkdirAll(homes, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: example.com/agent:1\n")
		return []string{"--home", homes, "--mount", homes}
	}}.run(t)
	// The home is under homes with its links resolved, so the report is too.
	real, err := filepath.EvalSymlinks(homes)
	if err != nil {
		t.Fatal(err)
	}
	prefix, suffix := "remove "+homes+" from --mount: it contains "+real+string(filepath.Separator), "-report.json, the harness report of this run, and the agent's container has every mount; the code is used"
	if r.err == nil || !strings.HasPrefix(r.err.Error(), prefix) || !strings.HasSuffix(r.err.Error(), suffix) || cmd.ExitCode(r.err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d), want %q…%q, exit %d\n%s", r.err, cmd.ExitCode(r.err), prefix, suffix, cmd.ExitInput, r.out)
	}
	r.fetchedNothing(t)
	if data, err := os.ReadFile(log); err == nil {
		t.Errorf("the wall ran a command:\n%s", data)
	}
	if entries, _ := os.ReadDir(homes); len(entries) != 0 {
		t.Errorf("the run wrote into %s: %v", homes, entries)
	}
	r.noAddress(t)
}

// TestAWalledRunOfAHiddenHarnessFromTheCacheDirectoryStops is a walled run from a run
// code whose harness is hidden, started in the cache directory with --home set to a
// composed home whose report lists a checkout elsewhere: the checkout is the report's,
// and the working folder, which the agent's container gets writable, contains qory's
// cache folder, so the run stops, exit 2, with the line of the working folder, before
// it opens the run's stack proxy, fetches anything or runs a command of the wall.
func TestAWalledRunOfAHiddenHarnessFromTheCacheDirectoryStops(t *testing.T) {
	docker, log := fakeDocker(t)
	helper := staticELF(t)
	var cache, cwd string
	r := codeHarness{before: func() []string {
		root, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		base, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		cache = filepath.Join(base, "qory")
		if err := os.MkdirAll(cache, 0o700); err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(t.TempDir(), checkout.Key(root))
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, home+"-report.json", `{"checkout": "`+root+`"}`+"\n")
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+helper+"\n  user: \"1000:1000\"\n  image: example.com/agent:1\n")
		t.Chdir(base)
		if cwd, err = os.Getwd(); err != nil {
			t.Fatal(err)
		}
		return []string{"--home", home}
	}}.run(t)
	want := "run qory from a folder that does not contain " + cache + ": the working folder " + cwd + " is mounted into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under " + cache + "; the code is used"
	if r.err == nil || r.err.Error() != want || cmd.ExitCode(r.err) != cmd.ExitInput {
		t.Fatalf("%v (exit %d), want %q, exit %d\n%s", r.err, cmd.ExitCode(r.err), want, cmd.ExitInput, r.out)
	}
	r.fetchedNothing(t)
	if data, err := os.ReadFile(log); err == nil {
		t.Errorf("the wall ran a command:\n%s", data)
	}
	r.noAddress(t)
}

// TestAWalledRunFromARunCodeThatMountsQorysCacheFolderStops is a walled run from a run
// code, its harness hidden or shown, with qory's folder in the cache directory mounted
// by --mount or by wall.mounts in forager.yaml: the folder contains the harness reports
// of the runs on this machine whose harness is hidden, so the run stops, exit 2, with
// the line of the guard of that folder, before it opens the run's stack proxy, fetches
// anything or runs a command of the wall.
func TestAWalledRunFromARunCodeThatMountsQorysCacheFolderStops(t *testing.T) {
	for _, c := range []struct {
		from  string
		shown bool
	}{{"--mount", false}, {"wall.mounts in forager.yaml", false}, {"--mount", true}} {
		from := c.from
		t.Run(fmt.Sprintf("%s, shown %v", from, c.shown), func(t *testing.T) {
			docker, log := fakeDocker(t)
			helper := staticELF(t)
			var cache string
			r := codeHarness{shown: c.shown, before: func() []string {
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
			want := "remove " + cache + " from " + from + ": it is " + cache + ", where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and the agent's container has every mount; the code is used"
			if r.err == nil || r.err.Error() != want || cmd.ExitCode(r.err) != cmd.ExitInput {
				t.Fatalf("%v (exit %d), want %q, exit %d\n%s", r.err, cmd.ExitCode(r.err), want, cmd.ExitInput, r.out)
			}
			r.fetchedNothing(t)
			if data, err := os.ReadFile(log); err == nil {
				t.Errorf("the wall ran a command:\n%s", data)
			}
			r.noAddress(t)
		})
	}
}

// outsideFile is a file outside every repository of a test, with content, for a link
// that a repository contains to lead to.
func outsideFile(t *testing.T, name, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "secret-folder-1", name)
	writeFile(t, file, content)
	return file
}

// TestAHarnessWhoseFilesLinkOutOfTheirCloneStops is a run from a run code whose base's
// stack file, or its repository's qory.yaml, or the qory.yaml that lists the exports of
// the module tools, is a link that leads outside the clone, to a file of this machine:
// the run stops before the join, exit 2, and reads no file outside the clone. For the
// documents' harness and for the checkout's file, with the harness hidden, the line
// shows no folder; for the checkout's file with the harness shown, it shows the base's
// address, as for a run without a run code.
func TestAHarnessWhoseFilesLinkOutOfTheirCloneStops(t *testing.T) {
	stackFile := func(t *testing.T) map[string]string {
		return map[string]string{"examples/hello/qory-stack.yaml": outsideFile(t, "qory-stack.yaml", "apiVersion: qory.dev/v1alpha1\nname: elsewhere\nmodules:\n  - name: x\n    source: {path: x}\nextending: {}\n")}
	}
	config := func(t *testing.T) map[string]string {
		return map[string]string{"qory.yaml": outsideFile(t, "qory.yaml", "apiVersion: qory.dev/v1alpha1\n")}
	}
	for _, c := range []struct {
		name  string
		c     func(t *testing.T) codeHarness
		shown bool
		want  string
	}{
		{"the stack file of the documents' base", func(t *testing.T) codeHarness { return codeHarness{links: stackFile(t)} }, false,
			"check the folder of " + workspaceBase + ": its qory-stack.yaml links outside its repository"},
		{"the stack file of the checkout's base", func(t *testing.T) codeHarness { return codeHarness{checkout: true, links: stackFile(t)} }, false,
			"check the folder of " + checkoutBase + ": its qory-stack.yaml links outside its repository"},
		{"the stack file of the checkout's base, shown", func(t *testing.T) codeHarness { return codeHarness{checkout: true, shown: true, links: stackFile(t)} }, true,
			": qory-stack.yaml links outside the repository"},
		{"the qory.yaml of the documents' base", func(t *testing.T) codeHarness { return codeHarness{links: config(t)} }, false,
			"check " + workspaceBase + ": the qory.yaml of its repository links outside its repository"},
		{"the qory.yaml of the checkout's base", func(t *testing.T) codeHarness { return codeHarness{checkout: true, links: config(t)} }, false,
			"check " + checkoutBase + ": the qory.yaml of its repository links outside its repository"},
		{"the qory.yaml of an export", func(t *testing.T) codeHarness {
			return codeHarness{
				modules:    []map[string]any{{"name": "tools", "source": map[string]any{"git": toolsURL, "ref": toolsRef, "module": "tools"}}},
				tools:      toolsModule(),
				toolsLinks: map[string]string{"qory.yaml": outsideFile(t, "qory.yaml", "apiVersion: qory.dev/v1alpha1\nexports:\n  dir: secret-folder-1\n  modules: [tools]\n")},
			}
		}, false, "the qory.yaml of the repository of the module tools links outside its repository"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.c(t).run(t)
			if r.joined || cmd.ExitCode(r.err) != cmd.ExitInput {
				t.Fatalf("%v (exit %d), want exit 2 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			wants(t, r.out+r.err.Error(), c.want)
			if strings.Contains(r.out+r.err.Error(), "elsewhere") {
				t.Errorf("the run read the file outside the clone:\n%s", r.out)
			}
			if c.shown {
				wants(t, r.err.Error(), "extends "+r.url+"#"+namedRef)
				return
			}
			r.noAddress(t)
		})
	}
}

// TestAHiddenHarnessShowsNoFolderOfASourceOrAnExport is a run from a run code of a
// hidden harness, the documents' or the checkout's, with a folder qory does not take:
// the module tools exports itself at a folder that its repository does not have, a
// module of the base or of the checkout's qory.yaml sets a source.path that leads out
// of its repository, and the base extends another stack, which the read of the base's
// stack file stops before [stack.Extend] runs. A base or a module selected as an export,
// from the folder that exports.dir sets, has a file qory cannot read or no manifest, or
// an exports.dir that leads out of its repository. Each stops the run before the join,
// exit 2, with a line and events that show no folder and no address.
func TestAHiddenHarnessShowsNoFolderOfASourceOrAnExport(t *testing.T) {
	// web is a base that its repository exports as the stack web from the folder
	// secret-folder-9, with a key that qory-stack.yaml does not read.
	const webFolder = "secret-folder-9/stacks/web"
	web := func(c codeHarness) codeHarness {
		c.folder = webFolder
		c.files = map[string]string{"qory.yaml": "apiVersion: qory.dev/v1alpha1\nexports:\n  dir: secret-folder-9\n  stacks: [web]\n"}
		c.stack = func(s string) string { return s + "bogus: 1\n" }
		c.change = func(d map[string]any) {
			if h, ok := d["harness"].(map[string]any); ok {
				h["harness"].(map[string]any)["extends"] = map[string]any{"git": d["harness"].(map[string]any)["harness"].(map[string]any)["extends"].(map[string]any)["git"], "ref": namedRef, "stack": "web"}
			}
		}
		c.document = func(text string) string {
			return strings.Replace(text, `,"path":"`+webFolder+`"`, `,"stack":"web"`, 1)
		}
		return c
	}
	// tools is the module tools, which its repository exports from the folder
	// exports.dir sets, with the files of that folder.
	tools := func(dir string, files map[string]string) codeHarness {
		repo := map[string]string{"qory.yaml": "apiVersion: qory.dev/v1alpha1\nexports:\n  dir: " + dir + "\n  modules: [tools]\n", "README.md": "# Tools\n"}
		for name, body := range files {
			repo[dir+"/modules/tools/"+name] = body
		}
		return codeHarness{
			modules: []map[string]any{{"name": "tools", "source": map[string]any{"git": toolsURL, "ref": toolsRef, "module": "tools"}}},
			tools:   repo,
		}
	}
	for _, c := range []struct {
		name string
		c    codeHarness
		want string
	}{
		{"an export at a folder that is not there", codeHarness{
			modules: []map[string]any{{"name": "tools", "source": map[string]any{"git": toolsURL, "ref": toolsRef, "module": "tools"}}},
			tools:   map[string]string{"qory.yaml": "apiVersion: qory.dev/v1alpha1\nexports:\n  dir: secret-folder-5\n  modules: [tools]\n", "README.md": "# Tools\n"},
		}, "the module tools exports module tools at a folder that is not there"},
		{"a base module whose path leads out", codeHarness{stack: func(s string) string {
			return s + "  - name: more\n    source: {git: \"" + toolsURL + "\", ref: " + toolsRef + ", path: ../secret-folder-2}\n"
		}}, "module more: source.path is not a directory inside the repository"},
		{"a checkout's module whose path leads out", codeHarness{checkout: true, modules: []map[string]any{
			{"name": "more", "source": map[string]any{"git": toolsURL, "ref": toolsRef, "path": "../secret-folder-4"}},
		}}, "module more: source.path is not a directory inside the repository"},
		{"a base that extends another stack", codeHarness{stack: func(s string) string {
			return s + "extends: {git: \"" + toolsURL + "\", ref: " + toolsRef + ", path: secret-folder-3}\n"
		}}, "qory-stack.yaml in " + workspaceBase + ": extends is not a stack's; the harness section of a checkout's qory.yaml extends a stack"},
		{"the documents' base by its export", web(codeHarness{}), "qory-stack.yaml in " + workspaceBase + ": line "},
		{"the checkout's base by its export", web(codeHarness{checkout: true}), "qory-stack.yaml in " + checkoutBase + ": line "},
		{"an exported module with a manifest qory cannot read", tools("secret-folder-8", map[string]string{"qory-module.yaml": "name: [\n", "AGENTS.md": "# Tools\n"}),
			"module tools: qory-module.yaml in the module tools: "},
		{"an exported module without a manifest", tools("secret-folder-8", map[string]string{"AGENTS.md": "# Tools\n"}),
			"the module tools has no qory-module.yaml"},
		{"an exports.dir that leads out", tools("../secret-folder-6", nil),
			"module tools: qory.yaml in the module tools: exports.dir stacks is not a directory inside the repository"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.c.run(t)
			if r.joined || cmd.ExitCode(r.err) != cmd.ExitInput {
				t.Fatalf("%v (exit %d), want exit 2 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			wants(t, r.out+r.err.Error(), c.want)
			r.noAddress(t)
			// A clone in <run>/sources contains the repository's folders; no text does.
			texts := []string{r.out, r.err.Error()}
			for _, e := range r.events {
				texts = append(texts, fmt.Sprintf("%+v", e))
			}
			for _, text := range texts {
				if strings.Contains(text, "secret-folder") {
					t.Errorf("a text shows a folder of the repository:\n%s", text)
				}
			}
		})
	}
}

// TestAHarnessDocumentWhosePathLeadsOutStops is a run from a run code whose documents'
// harness sets the path of extends or of a module to a path that leads out of its
// repository, is absolute, or is the repository's root: the run stops after the
// redemption, exit 2, before it opens and before anything is fetched, with a line that
// starts with its fix in the workspace's settings and shows no folder.
func TestAHarnessDocumentWhosePathLeadsOutStops(t *testing.T) {
	const settings = " of the harness in the workspace's settings in Qory Apiary"
	const cause = " to a folder inside its repository: the path is absolute, the repository's root, or leads out of the repository; the code is used"
	extendsPath := func(folder string) func(map[string]any) {
		return func(d map[string]any) {
			d["harness"].(map[string]any)["harness"].(map[string]any)["extends"].(map[string]any)["path"] = folder
		}
	}
	for _, c := range []struct {
		name string
		c    codeHarness
		want string
	}{
		{"extends out", codeHarness{change: extendsPath("../secret-folder-2")}, "set the path of extends" + settings + cause},
		{"extends absolute", codeHarness{change: extendsPath("/secret-folder-2")}, "set the path of extends" + settings + cause},
		{"extends at the root", codeHarness{change: extendsPath(".")}, "set the path of extends" + settings + cause},
		{"a module out", codeHarness{modules: []map[string]any{{"name": "more", "source": map[string]any{"git": toolsURL, "ref": toolsRef, "path": "modules/../../secret-folder-2"}}}},
			"set the path of the module more" + settings + cause},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.c.run(t)
			r.addresses = append(r.addresses, "secret-folder")
			if r.err == nil || r.err.Error() != c.want || cmd.ExitCode(r.err) != cmd.ExitInput {
				t.Fatalf("%v (exit %d), want %q, exit 2\n%s", r.err, cmd.ExitCode(r.err), c.want, r.out)
			}
			if r.opened || r.joined {
				t.Errorf("the run opened: %v, joined: %v", r.opened, r.joined)
			}
			r.fetchedNothing(t)
			r.noHarnessFile(t)
			r.noAddress(t)
		})
	}
}
