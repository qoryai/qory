package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/report"
)

// TestARunCodesTimeLimitLineNamesWhereTheLimitIsSet is the line of a run from a run code
// whose time limit came before the runtime started: its fix is a longer limit in Qory
// Apiary or in the workflow, never --timeout, which such a run does not take.
func TestARunCodesTimeLimitLineNamesWhereTheLimitIsSet(t *testing.T) {
	r := &codeRun{workflow: "example", stack: "https://code.example.com/acme/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example"}
	want := "set a longer time_limit_minutes in Qory Apiary, or a longer workflows.example.time_limit in the stack https://code.example.com/acme/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example: the run was cancelled at its time limit of 45m0s, before claude started"
	if got := r.timedOutLine(45*time.Minute, "claude"); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// TestAWalledRunOfANamedStackWithAMountOfQorysCacheFolderStops is a walled run from a run
// code whose stack is a name, with a mount from wall.mounts or --mount: a mount that is
// qory's folder in the cache directory, contains it, lies inside it, or is a link to it
// or to a folder above it stops the run, exit 2, before the stack is fetched; qory
// writes the harness reports of runs from a run code there. Another mount, and a run of
// a stack reference, pass.
func TestAWalledRunOfANamedStackWithAMountOfQorysCacheFolderStops(t *testing.T) {
	isolate(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "qory")
	inside := filepath.Join(cache, "homes", "app-1a2b3c4d")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	link, above := filepath.Join(other, "cache"), filepath.Join(other, "above")
	if err := os.Symlink(cache, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(base), above); err != nil {
		t.Fatal(err)
	}
	// The home is the default one, which lies inside the cache folder and passes.
	root, home, report := t.TempDir(), inside, inside+"-report.json"
	named, reference := codeRunOfStack(t, "example"), codeRunOfStack(t, exampleStackReference)
	if named.named == nil || named.stack != exampleStackReference || reference.named != nil {
		t.Fatalf("the name runs %q, named %+v; the reference is named %+v", named.stack, named.named, reference.named)
	}
	for _, c := range []struct{ path, how string }{
		{cache, "is"}, {base, "contains"}, {filepath.Dir(base), "contains"}, {inside, "lies inside"}, {link, "is"}, {above, "contains"},
	} {
		for _, from := range []string{"wall.mounts in forager.yaml", "--mount"} {
			err := named.reportExposed(root, root, home, report, []givenMount{{path: other, from: "--mount"}, {path: c.path, from: from}})
			want := "remove " + c.path + " from " + from + ": it " + c.how + " " + cache + ", where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and the agent's container has every mount; the code is used"
			if err == nil || err.Error() != want {
				t.Errorf("the mount %s from %s: got %v\nwant %s", c.path, from, err, want)
			} else if code := ExitCode(err); code != 2 {
				t.Errorf("the mount %s from %s: exit %d, want 2", c.path, from, code)
			}
			if err := reference.reportExposed(root, root, home, report, []givenMount{{path: c.path, from: from}}); err != nil {
				t.Errorf("a stack reference with the mount %s: %v, want none", c.path, err)
			}
		}
	}
	if err := named.reportExposed(root, root, home, report, []givenMount{{path: other, from: "--mount"}, {path: filepath.Join(base, "other"), from: "--mount"}}); err != nil {
		t.Errorf("mounts outside %s: %v, want none", cache, err)
	}
}

// exampleStackReference is the address of the named stack example in the tests of the
// guard of qory's cache folder.
const exampleStackReference = "https://code.example.com/team/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example"

// codeRunOfStack is the run of documents whose stack is stack, as [codeRunOf] reads
// them: for a stack name, stack_reference is set to [exampleStackReference].
func codeRunOfStack(t *testing.T, stack string) *codeRun {
	t.Helper()
	doc := contracts.Documents{Stack: stack, Workflow: "example", RunSettings: &contracts.RunSettings{Runtime: "claude"}}
	if !strings.HasPrefix(stack, stackReferencePrefix) {
		doc.StackReference = exampleStackReference
	}
	r, err := codeRunOf(doc)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestAMountInAnotherLetterCaseOfQorysCacheFolderStops is a mount of a folder above
// qory's cache folder, and of the folder itself, written in another letter case, on a
// disk that ignores case, as APFS does by default on macOS: each is the same folder, so
// it stops the run. On a disk that keeps case apart, the test is skipped.
func TestAMountInAnotherLetterCaseOfQorysCacheFolderStops(t *testing.T) {
	isolate(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "qory")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(filepath.Dir(base), strings.ToUpper(filepath.Base(base)))
	if upper == base {
		upper = filepath.Join(filepath.Dir(base), strings.ToLower(filepath.Base(base)))
	}
	if _, err := os.Stat(upper); err != nil {
		t.Skipf("the disk of %s keeps letter case apart: %v", base, err)
	}
	named := codeRunOfStack(t, "example")
	for _, c := range []struct{ path, how string }{
		{upper, "contains"}, {filepath.Join(upper, "QORY"), "is"}, {filepath.Join(upper, "Qory", "Homes"), "lies inside"},
	} {
		err := named.reportExposed(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "app-1a2b3c4d"), filepath.Join(t.TempDir(), "app-1a2b3c4d-report.json"), []givenMount{{path: c.path, from: "--mount"}})
		want := "remove " + c.path + " from --mount: it " + c.how + " " + cache + ", where qory writes the harness report of each run with a run key or a run code that has no --home or harness.home, and the agent's container has every mount; the code is used"
		if err == nil || err.Error() != want {
			t.Errorf("the mount %s: got %v\nwant %s", c.path, err, want)
		}
	}
}

// upperBase is path with its last name in upper case, and in lower case when it is in
// upper case already; "" on a disk that keeps letter case apart, where the two are not
// one file.
func upperBase(path string) string {
	other := filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
	if other == path {
		other = filepath.Join(filepath.Dir(path), strings.ToLower(filepath.Base(path)))
	}
	if a, errA := os.Stat(path); errA == nil {
		if b, errB := os.Stat(other); errB == nil && os.SameFile(a, b) {
			return other
		}
	}
	return ""
}

// TestAWalledRunOfANamedStackWithAMountOfItsReportStops is a walled run from a run code
// whose stack is a name and whose home --home or harness.home sets outside qory's cache
// folder, with a mount from wall.mounts or --mount that is the harness report beside
// the home or contains it, directly, through a link or in another letter case: the run
// stops, exit 2, before the stack is fetched. The home itself, a sibling folder and a
// run of a stack reference pass.
func TestAWalledRunOfANamedStackWithAMountOfItsReportStops(t *testing.T) {
	isolate(t)
	homes := filepath.Join(t.TempDir(), "homes")
	home := filepath.Join(homes, "app-1a2b3c4d")
	report := home + "-report.json"
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	link := filepath.Join(other, "homes")
	if err := os.Symlink(homes, link); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	named, reference := codeRunOfStack(t, "example"), codeRunOfStack(t, exampleStackReference)
	cases := []struct{ path, how string }{
		{homes, "contains"}, {filepath.Dir(homes), "contains"}, {report, "is"}, {link, "contains"},
	}
	if upper := upperBase(homes); upper != "" {
		cases = append(cases, struct{ path, how string }{upper, "contains"})
	}
	for _, c := range cases {
		for _, from := range []string{"wall.mounts in forager.yaml", "--mount"} {
			err := named.reportExposed(root, root, home, report, []givenMount{{path: other + "-none", from: "--mount"}, {path: c.path, from: from}})
			want := "remove " + c.path + " from " + from + ": it " + c.how + " " + report + ", the harness report of this run, and the agent's container has every mount; the code is used"
			if err == nil || err.Error() != want {
				t.Errorf("the mount %s from %s: got %v\nwant %s", c.path, from, err, want)
			} else if code := ExitCode(err); code != 2 {
				t.Errorf("the mount %s from %s: exit %d, want 2", c.path, from, code)
			}
			if err := reference.reportExposed(root, root, home, report, []givenMount{{path: c.path, from: from}}); err != nil {
				t.Errorf("a stack reference with the mount %s: %v, want none", c.path, err)
			}
		}
	}
	sibling := filepath.Join(homes, "other")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := named.reportExposed(root, root, home, report, []givenMount{{path: home, from: "--mount"}, {path: sibling, from: "--mount"}}); err != nil {
		t.Errorf("the home and a folder beside the report: %v, want none", err)
	}
}

// TestAWalledRunOfANamedStackInACheckoutThatExposesAReportStops is a walled run from a
// run code whose stack is a name, in a checkout that contains qory's cache folder, such
// as ~ on Linux, lies inside it, or contains the harness report beside the home,
// directly, through a link or in another letter case: the checkout is mounted writable
// into the agent's container, so the run stops, exit 2, before the stack is fetched. A
// run of a stack reference passes, and so does a checkout beside the folder.
func TestAWalledRunOfANamedStackInACheckoutThatExposesAReportStops(t *testing.T) {
	isolate(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "qory")
	inside := filepath.Join(cache, "work", "app")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	links := t.TempDir()
	linkHome := filepath.Join(links, "home")
	if err := os.Symlink(home, linkHome); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "homes", "app-1a2b3c4d-report.json")
	runHome := strings.TrimSuffix(report, "-report.json")
	named, reference := codeRunOfStack(t, "example"), codeRunOfStack(t, exampleStackReference)
	type checkoutCase struct{ root, want string }
	inCache := func(root, where string) checkoutCase {
		return checkoutCase{root, "start the run in a checkout " + where + " " + cache + ": the checkout " + root + " is mounted into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under " + cache + "; the code is used"}
	}
	cases := []checkoutCase{
		inCache(home, "that does not contain"), inCache(linkHome, "that does not contain"),
		inCache(cache, "outside"), inCache(inside, "outside"),
	}
	if upper := upperBase(home); upper != "" {
		cases = append(cases, inCache(upper, "that does not contain"))
	}
	work := t.TempDir()
	withReport := filepath.Join(work, "homes", "app-1a2b3c4d-report.json")
	if err := os.MkdirAll(filepath.Dir(withReport), 0o700); err != nil {
		t.Fatal(err)
	}
	linkWork := filepath.Join(links, "work")
	if err := os.Symlink(work, linkWork); err != nil {
		t.Fatal(err)
	}
	ofReport := func(root string) checkoutCase {
		return checkoutCase{root, "start the run in a checkout that does not contain " + withReport + ": the checkout " + root + " is mounted into the agent's container, and " + withReport + " is the harness report of this run; the code is used"}
	}
	reported := []checkoutCase{ofReport(work), ofReport(linkWork)}
	if upper := upperBase(work); upper != "" {
		reported = append(reported, ofReport(upper))
	}
	for _, c := range cases {
		check := func(r *codeRun, rep string) error { return r.reportExposed(c.root, c.root, runHome, rep, nil) }
		if err := check(named, report); err == nil || err.Error() != c.want || ExitCode(err) != 2 {
			t.Errorf("the checkout %s: got %v (exit %d)\nwant %s", c.root, err, ExitCode(err), c.want)
		}
		if err := check(reference, report); err != nil {
			t.Errorf("a stack reference in the checkout %s: %v, want none", c.root, err)
		}
	}
	for _, c := range reported {
		if err := named.reportExposed(c.root, c.root, runHome, withReport, nil); err == nil || err.Error() != c.want || ExitCode(err) != 2 {
			t.Errorf("the checkout %s: got %v (exit %d)\nwant %s", c.root, err, ExitCode(err), c.want)
		}
	}
	if err := named.reportExposed(filepath.Join(base, "other"), filepath.Join(base, "other"), runHome, report, nil); err != nil {
		t.Errorf("a checkout beside %s: %v, want none", cache, err)
	}
}

// TestEncloseListsTheMountsOfWallMountsAndMount is the mounts of wall.mounts and --mount
// that a walled run's guard of the harness reports checks, each with where it is set:
// not the checkout, which the guard checks by its root, and not the home, mounted
// read-only, which the guard lets pass.
func TestEncloseListsTheMountsOfWallMountsAndMount(t *testing.T) {
	fromFile, fromFlag := t.TempDir(), t.TempDir()
	r := &config.Forager{Wall: &config.ForagerWall{Adapter: config.WallDocker, Image: "example.com/agent:1", Helper: "/opt/qory/qory-linux", Mounts: []config.ForagerMount{{Path: fromFile, ReadOnly: true}}}}
	var spec runSpec
	if err := enclose(&spec, r, wallOptions{mounts: []string{fromFlag}}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	want := []givenMount{{path: fromFile, from: "wall.mounts in forager.yaml"}, {path: fromFlag, from: "--mount"}}
	if !reflect.DeepEqual(spec.given, want) {
		t.Errorf("given %v, want %v", spec.given, want)
	}
	var unwalled runSpec
	if err := enclose(&unwalled, &config.Forager{}, wallOptions{}, "", false, "/usr/local/bin/qory", t.TempDir(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if unwalled.given != nil {
		t.Errorf("without a wall, given %v, want none", unwalled.given)
	}
}

// TestANamedStackRunsFromItsAddress is documents that set the stack core by its name,
// with its address in stack_reference: the run composes the address, and its start,
// time limit and missing-workflow lines show the stack's name and no part of the
// address.
func TestANamedStackRunsFromItsAddress(t *testing.T) {
	const address = "https://code.example.com/team/stacks#release-7:stacks/core"
	r, err := codeRunOf(contracts.Documents{Stack: "core", StackReference: address, Workflow: "example", RunSettings: &contracts.RunSettings{Runtime: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.stack != address || r.named == nil || r.named.Stack != "core" {
		t.Fatalf("the stack is %q, named %+v", r.stack, r.named)
	}
	if ref := r.reference(); ref.Git != "https://code.example.com/team/stacks" || ref.Ref != "release-7" || ref.Path != "stacks/core" {
		t.Errorf("the reference is %+v", ref)
	}
	_, missing := r.workflowArgs("claude", report.Report{}, t.TempDir())
	for _, c := range []struct{ got, want string }{
		{r.startLine("run_1"), "qory run: the run run_1 starts the workflow example of the stack core on claude"},
		{r.timedOutLine(45*time.Minute, "claude"), "set a longer time_limit_minutes in Qory Apiary, or a longer workflows.example.time_limit in the stack core: the run was cancelled at its time limit of 45m0s, before claude started"},
		{fmt.Sprint(missing), "add a skill or command example to the stack core, or set another workflow in Qory Apiary: the composed harness has no skill or command example; the code is used"},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}
}

// TestANamedStackWhoseAddressHasAUserPartStops is documents that set the stack core by
// its name with an address qory does not fetch: none, one that is not a stack
// reference, one whose URL does not parse, and one whose URL has a user name, with a
// password or without. Each stops
// the run before the stack is fetched, with a line that starts with its fix and shows
// neither the address nor the user part.
func TestANamedStackWhoseAddressHasAUserPartStops(t *testing.T) {
	const unreadable = "set the address of the stack core in Qory Apiary's list of stacks as <https URL>#<ref>:<folder>: qory could not read the address as a stack reference; the code is used"
	const user = "set the address of the stack core in Qory Apiary's list of stacks without a user name or password: the address has a user part, and qory fetches a named stack with no credential; the code is used"
	for _, c := range []struct{ address, want string }{
		{"", "ask the instance admin of Qory Apiary to set the address of the stack core in Qory Apiary's list of stacks: the response to the run code sets the stack core by its name and has no address for it; the code is used"},
		{"https://code.example.com/team/stacks#release-7", unreadable},
		{"https://code.example.com/team/st%zzacks#release-7:core", unreadable},
		{"https://deploy-user:s3cret-token@code.example.com/team/stacks#release-7:core", user},
		{"https://deploy-user@code.example.com/team/stacks#release-7:core", user},
	} {
		_, err := codeRunOf(contracts.Documents{Stack: "core", StackReference: c.address, Workflow: "example", RunSettings: &contracts.RunSettings{Runtime: "claude"}})
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v\nwant %s", c.address, err, c.want)
		}
		if err != nil && (strings.Contains(err.Error(), "code.example.com") || strings.Contains(err.Error(), "deploy-user") || strings.Contains(err.Error(), "s3cret")) {
			t.Errorf("the line shows the address: %v", err)
		}
	}
}

// TestANamedStacksAddressWithAnAtBeforeItsPathStops is namedStackOf for an address with
// an @ between :// and the first / after it, whatever comes before the @: each stops
// with the line of a user part, without the documents' schema, which allows no @ there.
func TestANamedStacksAddressWithAnAtBeforeItsPathStops(t *testing.T) {
	const user = "set the address of the stack core in Qory Apiary's list of stacks without a user name or password: the address has a user part, and qory fetches a named stack with no credential; the code is used"
	for _, address := range []string{
		"https://code.example.com?@evil.example/team/stacks#release-7:core",
		"https://code.example.com#@evil.example/team/stacks#release-7:core",
		"https://code.example.com%3F@evil.example/team/stacks#release-7:core",
		`https://code.example.com\@evil.example/team/stacks#release-7:core`,
		"https://code.example.com:443@evil.example/team/stacks#release-7:core",
	} {
		_, err := namedStackOf(contracts.Documents{Stack: "core", StackReference: address})
		if err == nil || err.Error() != user {
			t.Errorf("%s: %v\nwant %s", address, err, user)
		}
	}
	if _, err := namedStackOf(contracts.Documents{Stack: "core", StackReference: "https://code.example.com/team/stacks#release-7:core"}); err != nil {
		t.Errorf("an address without an @: %v", err)
	}
}

// TestAWalledRunOfANamedStackFromAWorkingFolderThatExposesAReportStops is a walled run
// from a run code whose stack is a name, from a working folder outside the checkout, as
// --home sets when it names a composed home whose report lists another checkout: a
// working folder that contains qory's cache folder, such as ~ on Linux, lies inside
// it, or contains this run's harness report, directly, through a link or in another
// letter case, stops the run, exit 2, before the stack is fetched: Forager mounts the
// working folder writable. A run of a stack reference passes, and so does a working
// folder beside the cache folder.
func TestAWalledRunOfANamedStackFromAWorkingFolderThatExposesAReportStops(t *testing.T) {
	isolate(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "qory")
	inside := filepath.Join(cache, "work", "app")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	userHome := os.Getenv("HOME")
	links := t.TempDir()
	linkHome := filepath.Join(links, "home")
	if err := os.Symlink(userHome, linkHome); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	report := filepath.Join(t.TempDir(), "homes", "app-1a2b3c4d-report.json")
	runHome := strings.TrimSuffix(report, "-report.json")
	named, reference := codeRunOfStack(t, "example"), codeRunOfStack(t, exampleStackReference)
	type cwdCase struct{ cwd, want string }
	inCache := func(cwd, where string) cwdCase {
		return cwdCase{cwd, "run qory from a folder " + where + " " + cache + ": the working folder " + cwd + " is mounted into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under " + cache + "; the code is used"}
	}
	cases := []cwdCase{
		inCache(userHome, "that does not contain"), inCache(linkHome, "that does not contain"),
		inCache(cache, "outside"), inCache(inside, "outside"),
	}
	if upper := upperBase(userHome); upper != "" {
		cases = append(cases, inCache(upper, "that does not contain"))
	}
	for _, c := range cases {
		if err := named.reportExposed(root, c.cwd, runHome, report, nil); err == nil || err.Error() != c.want || ExitCode(err) != 2 {
			t.Errorf("the working folder %s: got %v (exit %d)\nwant %s", c.cwd, err, ExitCode(err), c.want)
		}
		if err := reference.reportExposed(root, c.cwd, runHome, report, nil); err != nil {
			t.Errorf("a stack reference from the working folder %s: %v, want none", c.cwd, err)
		}
	}
	work := t.TempDir()
	withReport := filepath.Join(work, "homes", "app-1a2b3c4d-report.json")
	if err := os.MkdirAll(filepath.Dir(withReport), 0o700); err != nil {
		t.Fatal(err)
	}
	linkWork := filepath.Join(links, "work")
	if err := os.Symlink(work, linkWork); err != nil {
		t.Fatal(err)
	}
	reported := []string{work, linkWork}
	if upper := upperBase(work); upper != "" {
		reported = append(reported, upper)
	}
	for _, cwd := range reported {
		want := "run qory from a folder that does not contain " + withReport + ": the working folder " + cwd + " is mounted into the agent's container, and " + withReport + " is the harness report of this run; the code is used"
		if err := named.reportExposed(root, cwd, runHome, withReport, nil); err == nil || err.Error() != want || ExitCode(err) != 2 {
			t.Errorf("the working folder %s: got %v (exit %d)\nwant %s", cwd, err, ExitCode(err), want)
		}
	}
	if err := named.reportExposed(root, filepath.Join(base, "other"), runHome, report, nil); err != nil {
		t.Errorf("a working folder beside %s: %v, want none", cache, err)
	}
}

// TestANamedStacksAddressWithAHostOutsideASCIIStops is namedStackOf for an address whose
// host, its percent-encodings decoded, has a character outside ASCII, such as a
// fullwidth : and @, which curl built with IDN maps to a user part: it stops with a line
// that starts with its fix and shows no part of the address. A host in the xn-- form
// passes.
func TestANamedStacksAddressWithAHostOutsideASCIIStops(t *testing.T) {
	const want = "write the host in the address of the stack core in Qory Apiary's list of stacks in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @; the code is used"
	for _, address := range []string{
		"https://u%EF%BC%9Ap%EF%BC%A0code.example.com/team/stacks#main:core",
		"https://u：p＠code.example.com/team/stacks#main:core",
		"https://u﹫code.example.com/team/stacks#main:core",
		"https://code.example.com%EF%BC%8F.evil.example/team/stacks#main:core",
		"https://evil.example％40code.example.com/team/stacks#main:core",
	} {
		_, err := namedStackOf(contracts.Documents{Stack: "core", StackReference: address})
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", address, err, want)
		}
	}
	if _, err := namedStackOf(contracts.Documents{Stack: "core", StackReference: "https://xn--bcher-kva.example/team/stacks#main:core"}); err != nil {
		t.Errorf("a host in the xn-- form: %v", err)
	}
}

// TestAWalledRunOfANamedStackWithAHomeThatContainsQorysCacheFolderStops is a walled run
// from a run code whose stack is a name, with a home that is or contains qory's cache
// folder, directly, through a link or in another letter case: the home is mounted
// read-only, and the folder contains other runs' harness reports, so the run stops,
// exit 2, before the stack is fetched. The default home under <cache>/qory/homes and a
// run of a stack reference pass.
func TestAWalledRunOfANamedStackWithAHomeThatContainsQorysCacheFolderStops(t *testing.T) {
	isolate(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "qory")
	defaultHome := filepath.Join(cache, "homes", "app-1a2b3c4d")
	if err := os.MkdirAll(defaultHome, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	named, reference := codeRunOfStack(t, "example"), codeRunOfStack(t, exampleStackReference)
	cases := []struct{ home, where string }{
		{cache, "other than"}, {base, "that does not contain"}, {filepath.Dir(base), "that does not contain"}, {link, "that does not contain"},
	}
	if upper := upperBase(base); upper != "" {
		cases = append(cases, struct{ home, where string }{upper, "that does not contain"})
	}
	for _, c := range cases {
		report := c.home + "-report.json"
		err := named.reportExposed(root, root, c.home, report, nil)
		want := "set --home or harness.home to a folder " + c.where + " " + cache + ": the home " + c.home + " is mounted read-only into the agent's container, and qory writes the harness report of each run with a run key or a run code that has no --home or harness.home under " + cache + "; the code is used"
		if err == nil || err.Error() != want || ExitCode(err) != 2 {
			t.Errorf("the home %s: got %v (exit %d)\nwant %s", c.home, err, ExitCode(err), want)
		}
		if err := reference.reportExposed(root, root, c.home, report, nil); err != nil {
			t.Errorf("a stack reference with the home %s: %v, want none", c.home, err)
		}
	}
	if err := named.reportExposed(root, root, defaultHome, defaultHome+"-report.json", nil); err != nil {
		t.Errorf("the default home %s: %v, want none", defaultHome, err)
	}
}
