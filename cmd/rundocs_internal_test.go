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
// Apiary or in the workflows of the harness's base, never --timeout, which such a run
// does not take. The harness is the workspace's for a run with a harness document, and
// the checkout's for one without.
func TestARunCodesTimeLimitLineNamesWhereTheLimitIsSet(t *testing.T) {
	for harness, want := range map[string]string{
		"{}\n": "set a longer time_limit_minutes in Qory Apiary, or a longer workflows.example.time_limit in the base of the workspace's harness: the run was cancelled at its time limit of 45m0s, before claude started",
		"":     "set a longer time_limit_minutes in Qory Apiary, or a longer workflows.example.time_limit in the base of the checkout's harness, when it has one: the run was cancelled at its time limit of 45m0s, before claude started",
	} {
		r := &codeRun{workflow: "example"}
		if harness != "" {
			r.harness = []byte(harness)
		}
		if got := r.timedOutLine(45*time.Minute, "claude"); got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	}
}

// TestAWalledRunFromARunCodeWithAMountOfQorysCacheFolderStops is a walled run from a
// run code, its harness hidden or shown, with a mount from wall.mounts or --mount: a
// mount that is qory's folder in the cache directory, contains it, lies inside it, or
// is a link to it or to a folder above it stops the run, exit 2, before the harness is
// fetched; qory writes the harness reports of runs from a run code there. Another mount
// passes.
func TestAWalledRunFromARunCodeWithAMountOfQorysCacheFolderStops(t *testing.T) {
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
	named, reference := codeRunHidden(t, true), codeRunHidden(t, false)
	if named.named == nil || reference.named != nil {
		t.Fatalf("the hidden harness is named %+v; the shown one %+v", named.named, reference.named)
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
			if got, want := reference.reportExposed(root, root, home, report, []givenMount{{path: c.path, from: from}}), named.reportExposed(root, root, home, report, []givenMount{{path: c.path, from: from}}); got == nil || got.Error() != want.Error() {
				t.Errorf("a shown harness with the mount %s: %v, want %v", c.path, got, want)
			}
		}
	}
	if err := named.reportExposed(root, root, home, report, []givenMount{{path: other, from: "--mount"}, {path: filepath.Join(base, "other"), from: "--mount"}}); err != nil {
		t.Errorf("mounts outside %s: %v, want none", cache, err)
	}
}

// codeRunHidden is the run of documents with the workflow example and harness_hidden
// set to hidden, as [codeRunOf] reads them.
func codeRunHidden(t *testing.T, hidden bool) *codeRun {
	t.Helper()
	r, err := codeRunOf(contracts.Documents{Workflow: "example", HarnessHidden: &hidden, RunSettings: &contracts.RunSettings{Runtime: "claude"}})
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
	named := codeRunHidden(t, true)
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

// TestAWalledRunFromARunCodeWithAMountOfItsReportStops is a walled run from a run code,
// its harness hidden or shown, whose home --home or harness.home sets outside qory's
// cache folder, with a mount from wall.mounts or --mount that is the harness report
// beside the home or contains it, directly, through a link or in another letter case:
// the run stops, exit 2, before the harness is fetched. The home itself and a sibling
// folder pass.
func TestAWalledRunFromARunCodeWithAMountOfItsReportStops(t *testing.T) {
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
	named, reference := codeRunHidden(t, true), codeRunHidden(t, false)
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
			if got, want := reference.reportExposed(root, root, home, report, []givenMount{{path: c.path, from: from}}), named.reportExposed(root, root, home, report, []givenMount{{path: c.path, from: from}}); got == nil || got.Error() != want.Error() {
				t.Errorf("a shown harness with the mount %s: %v, want %v", c.path, got, want)
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

// TestAWalledRunFromARunCodeInACheckoutThatExposesAReportStops is a walled run from a
// run code, its harness hidden or shown, in a checkout that contains qory's cache
// folder, such as ~ on Linux, lies inside it, or contains the harness report beside the
// home, directly, through a link or in another letter case: the checkout is mounted
// writable into the agent's container, so the run stops, exit 2, before the harness is
// fetched. A checkout beside the folder passes.
func TestAWalledRunFromARunCodeInACheckoutThatExposesAReportStops(t *testing.T) {
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
	named, reference := codeRunHidden(t, true), codeRunHidden(t, false)
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
		if got, want := check(reference, report), check(named, report); got == nil || got.Error() != want.Error() {
			t.Errorf("a shown harness in the checkout %s: %v, want %v", c.root, got, want)
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

// TestAWalledRunFromARunCodeFromAWorkingFolderThatExposesAReportStops is a walled run
// from a run code, its harness hidden or shown, from a working folder outside the
// checkout, as --home sets when it names a composed home whose report lists another
// checkout: a working folder that contains qory's cache folder, such as ~ on Linux,
// lies inside it, or contains this run's harness report, directly, through a link or in
// another letter case, stops the run, exit 2, before the harness is fetched: Forager
// mounts the working folder writable. A working folder beside the cache folder passes.
func TestAWalledRunFromARunCodeFromAWorkingFolderThatExposesAReportStops(t *testing.T) {
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
	named, reference := codeRunHidden(t, true), codeRunHidden(t, false)
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
		if got, want := reference.reportExposed(root, c.cwd, runHome, report, nil), named.reportExposed(root, c.cwd, runHome, report, nil); got == nil || got.Error() != want.Error() {
			t.Errorf("a shown harness from the working folder %s: %v, want %v", c.cwd, got, want)
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

// TestAWalledRunFromARunCodeWithAHomeThatContainsQorysCacheFolderStops is a walled run
// from a run code, its harness hidden or shown, with a home that is or contains qory's
// cache folder, directly, through a link or in another letter case: the home is mounted
// read-only, and the folder contains other runs' harness reports, so the run stops,
// exit 2, before the harness is fetched. The default home under <cache>/qory/homes
// passes.
func TestAWalledRunFromARunCodeWithAHomeThatContainsQorysCacheFolderStops(t *testing.T) {
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
	named, reference := codeRunHidden(t, true), codeRunHidden(t, false)
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
		if got, want := reference.reportExposed(root, root, c.home, report, nil), named.reportExposed(root, root, c.home, report, nil); got == nil || got.Error() != want.Error() {
			t.Errorf("a shown harness with the home %s: %v, want %v", c.home, got, want)
		}
	}
	if err := named.reportExposed(root, root, defaultHome, defaultHome+"-report.json", nil); err != nil {
		t.Errorf("the default home %s: %v, want none", defaultHome, err)
	}
}

// TestARunCodesLinesShowTheHarnessByItsPhrase is documents with the workflow example: the
// start and missing-workflow lines show the workspace's harness for a run with a harness
// document, and the checkout's harness for one without, hidden or shown.
func TestARunCodesLinesShowTheHarnessByItsPhrase(t *testing.T) {
	doc := &contracts.HarnessDocument{Harness: contracts.HarnessSection{Extends: contracts.HarnessBase{Git: "https://code.example.com/team/stacks", Ref: "release-7", Path: "stacks/core"}}}
	for _, hidden := range []bool{true, false} {
		for harness, phrase := range map[*contracts.HarnessDocument]string{doc: "the workspace's harness", nil: "the checkout's harness"} {
			r, err := codeRunOf(contracts.Documents{Harness: harness, Workflow: "example", HarnessHidden: &hidden, RunSettings: &contracts.RunSettings{Runtime: "claude"}})
			if err != nil {
				t.Fatal(err)
			}
			if (r.named != nil) != hidden || (r.harness != nil) != (harness != nil) {
				t.Errorf("hidden %v: named %+v, harness %q", hidden, r.named, r.harness)
			}
			_, missing := r.workflowArgs("claude", report.Report{}, "")
			for _, c := range []struct{ got, want string }{
				{r.startLine("run_1"), "qory run: the run run_1 starts the workflow example of " + phrase + " on claude"},
				{fmt.Sprint(missing), "add a skill or command example to " + phrase + ", or set another workflow in Qory Apiary: the composed harness has no skill or command example; the code is used"},
			} {
				if c.got != c.want {
					t.Errorf("hidden %v: got  %q\nwant %q", hidden, c.got, c.want)
				}
			}
		}
	}
}

// TestAHarnessDocumentWithASourceQoryDoesNotFetchStops is harnessChecked for a harness
// document with a source qory does not fetch: a folder, which the schema does not
// allow; a URL with a user part, or an @ before its path, in each form that hides it;
// one whose host, its percent-encodings decoded, has a character outside ASCII, such as
// a fullwidth : and @, which curl built with IDN maps to ASCII; one url.Parse cannot
// read; and one that is not https. Each is an input error that starts with its fix and
// shows no part of the URL while the harness is hidden. A host in the xn-- form passes.
func TestAHarnessDocumentWithASourceQoryDoesNotFetchStops(t *testing.T) {
	const settings = " of the harness in the workspace's settings in Qory Apiary"
	const user = "remove the user name and password from the URL of the module tools" + settings + ": the run's stack proxy sets the harness credentials of the run configuration, and qory sends no credential in a URL; the code is used"
	const ascii = "write the host in the URL of the module tools" + settings + " in ASCII, with the xn-- form for an international name: the host has a character outside ASCII, which git may map to another, such as a fullwidth @ to @; the code is used"
	withModule := func(git string) *contracts.HarnessDocument {
		return &contracts.HarnessDocument{Harness: contracts.HarnessSection{
			Extends: contracts.HarnessBase{Git: "https://code.example.com/team/stacks", Ref: "release-7", Path: "stacks/core"},
			Modules: []contracts.HarnessModule{{Name: "tools", Source: contracts.HarnessSource{Git: git, Ref: "main"}}},
		}}
	}
	for git, want := range map[string]string{
		"": "set the module tools" + settings + " to an https source: it is a folder, and a run from a run code reads no folder of this machine; the code is used",
		"https://deploy-user:s3cret-token@code.example.com/team/tools.git": user,
		"https://deploy-user@code.example.com/team/tools.git":              user,
		"https://code.example.com?@evil.example/team/tools.git":            user,
		"https://code.example.com#@evil.example/team/tools.git":            user,
		"https://code.example.com%3F@evil.example/team/tools.git":          user,
		`https://code.example.com\@evil.example/team/tools.git`:            user,
		"https://code.example.com:443@evil.example/team/tools.git":         user,
		"https://u%EF%BC%9Ap%EF%BC%A0code.example.com/team/tools.git":      ascii,
		"https://u：p＠code.example.com/team/tools.git":                      ascii,
		"https://u﹫code.example.com/team/tools.git":                        ascii,
		"https://code.example.com%EF%BC%8F.evil.example/team/tools.git":    ascii,
		"https://evil.example％40code.example.com/team/tools.git":           ascii,
		"https://code.example.com%40evil.example/team/tools.git":           "correct the URL of the module tools" + settings + ": qory could not read it as a URL; the code is used",
		"git@code.example.com:team/tools.git":                              "set the module tools" + settings + " to an https source: its URL is not an https URL with a host, and qory fetches a harness only over https; the code is used",
	} {
		err := harnessChecked(withModule(git), true)
		if err == nil || err.Error() != want || ExitCode(err) != 2 {
			t.Errorf("%s: %v (exit %d)\nwant %s", git, err, ExitCode(err), want)
		}
	}
	if err := harnessChecked(withModule("https://xn--bcher-kva.example/team/tools.git"), true); err != nil {
		t.Errorf("a host in the xn-- form: %v", err)
	}
	extends := withModule("https://code.example.com/team/tools.git")
	extends.Harness.Extends.Git = ""
	if err := harnessChecked(extends, true); err == nil || err.Error() != "set extends"+settings+" to an https source: it is a folder, and a run from a run code reads no folder of this machine; the code is used" {
		t.Errorf("a base in a folder: %v", err)
	}
	if err := harnessChecked(withModule("http://code.example.com/team/tools.git"), false); err == nil || err.Error() != "set the module tools"+settings+" to an https source: http://code.example.com/team/tools.git is not an https URL with a host, and qory fetches a harness only over https; the code is used" {
		t.Errorf("an http module of a shown harness: %v", err)
	}
}
