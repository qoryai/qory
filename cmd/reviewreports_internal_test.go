package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/wall"
)

func TestNewReviewReportsGivesEachRoundANewEmptyFolder(t *testing.T) {
	under := t.TempDir()
	one, m1, err := newReviewReports(under, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(one, "code-reviewer.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	two, m2, err := newReviewReports(under, 2)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatalf("rounds 1 and 2 share the folder %s", one)
	}
	if entries, err := os.ReadDir(two); err != nil || len(entries) != 0 {
		t.Errorf("round 2's folder has %v (err %v), want it empty", entries, err)
	}
	if info, err := os.Stat(two); err != nil {
		t.Error(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Errorf("round 2's folder has mode %v, want 0700", info.Mode().Perm())
	}
	if m1 != (wall.Mount{Path: one}) || m2 != (wall.Mount{Path: two}) {
		t.Errorf("mounts %+v and %+v, want each folder writable at its path", m1, m2)
	}
	long := filepath.Join(under, strings.Repeat("d", 120))
	for _, c := range []struct {
		name, under string
		round       int
		want        string
	}{
		{"round 1 again", under, 1, "Give the review round a reports folder that does not exist yet."},
		{"round 0", under, 0, "Number the review round from 1."},
		{"a missing folder", long, 1, "Check that qory can make the reports folder in the run's folder."},
	} {
		_, _, err := newReviewReports(c.under, c.round)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s returned %v, want %q", c.name, err, c.want)
			continue
		}
		if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
			t.Errorf("%s: the first line has %d characters", c.name, len(first))
		}
	}
}

func TestCheckReviewReports(t *testing.T) {
	base := t.TempDir()
	dir := func(parts ...string) string {
		p := filepath.Join(append([]string{base}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	checkout, artifacts, home := dir("w", "checkout"), dir("artifacts"), dir("h", "home")
	run := dir("run")
	reports := dir("run", "review-reports-1")
	itself := wall.Mount{Path: reports}

	if err := checkReviewReports(reports, checkout, artifacts, home, []wall.Mount{
		{Path: checkout, ReadOnly: true}, {Path: run, ReadOnly: true}, itself,
	}); err != nil {
		t.Errorf("a folder that keeps all three rules returned %v", err)
	}

	linkToCheckout := filepath.Join(base, "to-checkout")
	if err := os.Symlink(checkout, linkToCheckout); err != nil {
		t.Fatal(err)
	}
	dir("w", "checkout", "reports")

	outside := "Place the review reports folder outside the checkout, the artifact folder and the harness home."
	parent := "Place the review reports folder in a folder that the session cannot write."
	for _, c := range []struct {
		name               string
		reports, artifacts string
		home               string
		mounts             []wall.Mount
		want               string
	}{
		{"rule 1: in the checkout", dir("w", "checkout", "reports"), artifacts, home, nil, outside},
		{"rule 1: the checkout", checkout, artifacts, home, nil, outside},
		{"rule 1: over the checkout", dir("w"), artifacts, home, nil, outside},
		{"rule 1: in the checkout through a link", filepath.Join(linkToCheckout, "reports"), artifacts, home, nil, outside},
		{"rule 1: in the artifact folder", dir("artifacts", "reports"), artifacts, home, nil, outside},
		{"rule 1: the artifact folder", artifacts, artifacts, home, nil, outside},
		{"rule 1: over the artifact folder", run, dir("run", "artifacts"), home, nil, outside},
		{"rule 2: in the harness home", dir("h", "home", "reports"), artifacts, home, nil, outside},
		{"rule 2: the harness home", home, artifacts, home, nil, outside},
		{"rule 2: over the harness home", dir("h"), artifacts, home, nil, outside},
		{"rule 3: below a writable mount", reports, artifacts, home, []wall.Mount{{Path: run}, itself}, parent},
	} {
		err := checkReviewReports(c.reports, checkout, c.artifacts, c.home, c.mounts)
		if err == nil || !strings.HasPrefix(err.Error(), c.want+"\n") {
			t.Errorf("%s: returned %v, want %q", c.name, err, c.want)
		}
	}

	link := filepath.Join(base, "link")
	if err := os.Symlink(reports, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, filepath.Join(base, "missing")} {
		if err := checkReviewReports(p, checkout, artifacts, home, nil); err == nil {
			t.Errorf("%s returned no error", p)
		}
	}
}

func TestCheckReviewReportsRefusesPathsItCannotResolve(t *testing.T) {
	base := t.TempDir()
	reports := filepath.Join(base, "run", "review-reports-1")
	checkout, artifacts := filepath.Join(base, "checkout"), filepath.Join(base, "artifacts")
	locked := filepath.Join(base, "locked")
	home := filepath.Join(locked, "home")
	for _, p := range []string{reports, checkout, artifacts, home} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	want := "Give the review session an absolute path for "
	for _, c := range []struct{ name, artifacts, home string }{
		{"a home below a folder qory cannot search", artifacts, home},
		{"an empty home", artifacts, ""},
		{"a relative artifact folder", "artifacts", filepath.Join(base, "checkout")},
	} {
		err := checkReviewReports(reports, checkout, c.artifacts, c.home, nil)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: returned %v, want %q", c.name, err, want)
		}
	}
}

func TestCheckReviewReportsRefusesTheRunCopiesFolder(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, ".cache"))
	copies := runCopiesDir()
	if copies == "" {
		t.Skip("no cache folder")
	}
	reports := filepath.Join(copies, "r", "review-reports-1")
	checkout, artifacts, home := filepath.Join(base, "c"), filepath.Join(base, "a"), filepath.Join(base, "h")
	for _, p := range []string{reports, checkout, artifacts, home} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	err := checkReviewReports(reports, checkout, artifacts, home, []wall.Mount{{Path: reports}})
	if err == nil || !strings.HasPrefix(err.Error(), "Place the review reports folder in a folder that the session cannot write.\n") {
		t.Errorf("a folder in the run copies folder %s returned %v", copies, err)
	}
}

func TestCheckReviewReportsWantsANewFolderWithOneMount(t *testing.T) {
	base := t.TempDir()
	dir := func(parts ...string) string {
		p := filepath.Join(append([]string{base}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	checkout, artifacts, home := dir("checkout"), dir("artifacts"), dir("home")
	reports := dir("run", "review-reports-1")
	itself := wall.Mount{Path: reports}
	check := func(r string, mounts ...wall.Mount) error {
		return checkReviewReports(r, checkout, artifacts, home, mounts)
	}

	used := dir("run", "review-reports-2")
	if err := os.WriteFile(filepath.Join(used, "code-reviewer.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := dir("run", "review-reports-3")
	if err := os.Chmod(shared, 0o770); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(reports, link); err != nil {
		t.Fatal(err)
	}

	oneMount := "Give the reports folder one mount, writable at its path, and no other.\n"
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"a trailing slash on a link", check(link+"/", wall.Mount{Path: link + "/"}), "Give the review session an absolute, clean path for the reports folder.\n"},
		{"a folder with a report in it", check(used, wall.Mount{Path: used}), "Give the review session a new, empty reports folder.\n"},
		{"a group-writable folder", check(shared, wall.Mount{Path: shared}), "Give the review session a reports folder of qory's user, with no group or other write bit.\n"},
		{"no mount", check(reports), oneMount},
		{"two writable mounts", check(reports, itself, wall.Mount{Path: reports, Target: "/x"}), oneMount},
		{"a read-only mount beside", check(reports, itself, wall.Mount{Path: reports, ReadOnly: true}), oneMount},
		{"a mount inside", check(reports, itself, wall.Mount{Path: filepath.Join(reports, "sub")}), oneMount},
		{"a target inside", check(reports, itself, wall.Mount{Path: artifacts, ReadOnly: true, Target: filepath.Join(reports, "sub")}), oneMount},
	} {
		if c.err == nil || !strings.HasPrefix(c.err.Error(), c.want) {
			t.Errorf("%s: returned %v, want %q", c.name, c.err, c.want)
		}
	}
	if err := check(reports, itself, wall.Mount{Path: dir("run"), ReadOnly: true}); err != nil {
		t.Errorf("a read-only mount of the run folder returned %v", err)
	}
	if _, _, err := newReviewReports("run", 1); err == nil || !strings.HasPrefix(err.Error(), "Give the review round an absolute, clean folder for its reports.\n") {
		t.Errorf("a relative folder returned %v", err)
	}
}
