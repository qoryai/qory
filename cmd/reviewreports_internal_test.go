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
	if _, _, err := newReviewReports(under, 1); err == nil {
		t.Error("round 1 got its folder again, with round 1's reports in it")
	}
	if _, _, err := newReviewReports(under, 0); err == nil || !strings.HasPrefix(err.Error(), "Number the review round from 1.") {
		t.Errorf("round 0 returned %v", err)
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
	checkout, artifacts, home := dir("checkout"), dir("artifacts"), dir("home")
	run := dir("run")
	reports := dir("run", "review-reports-1")
	own := wall.Mount{Path: reports}

	if err := checkReviewReports(reports, checkout, artifacts, home, []wall.Mount{
		{Path: checkout, ReadOnly: true}, {Path: run, ReadOnly: true}, own,
	}); err != nil {
		t.Errorf("a folder that keeps all three rules returned %v", err)
	}

	outside := "Place the review reports folder outside the checkout, the artifact folder and the harness home."
	parent := "Place the review reports folder below no folder that the session can write."
	for _, c := range []struct {
		name               string
		reports, artifacts string
		home               string
		mounts             []wall.Mount
		want               string
	}{
		{"rule 1: in the checkout", dir("checkout", "reports"), artifacts, home, nil, outside},
		{"rule 1: in the artifact folder", dir("artifacts", "reports"), artifacts, home, nil, outside},
		{"rule 1: over the artifact folder", run, dir("run", "artifacts"), home, nil, outside},
		{"rule 2: in the harness home", dir("home", "reports"), artifacts, home, nil, outside},
		{"rule 3: below a writable mount", reports, artifacts, home, []wall.Mount{{Path: run}, own}, parent},
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
