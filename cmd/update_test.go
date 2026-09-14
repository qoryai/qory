package cmd_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/update"
)

// releases points the update package at a fake GitHub whose latest release is tag, for one
// test, and restores the real site after it.
func releases(t *testing.T, tag string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+update.Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	was := update.GitHub
	t.Cleanup(func() { update.GitHub = was })
	update.GitHub = update.Site{API: s.URL, Downloads: s.URL, Client: s.Client()}
}

// TestUpdateCheckSaysWhatIsNewest runs update --check for a build behind the newest
// release, one at it, and one ahead of it, and checks what a person reads: behind is
// told the release, at it is up to date, and ahead is ahead, never up to date.
func TestUpdateCheckSaysWhatIsNewest(t *testing.T) {
	emptyDir(t)
	releases(t, "v0.5.0")

	release(t, "0.4.0")
	out, err := run(t, "update", "--check")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "qory update · 0.4.0", "newest     0.5.0", "changelog  https://github.com/qoryai/qory/releases/tag/v0.5.0", "✓ 0.5.0 is available; run qory update to install it")

	release(t, "0.5.0")
	out, err = run(t, "update", "--check")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "✓ up to date: 0.5.0 is the newest release")
	lacks(t, out, "changelog")

	release(t, "0.6.0")
	out, err = run(t, "update", "--check")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "✓ ahead of 0.5.0, the newest release")
	lacks(t, out, "up to date", "available")
}

// TestUpdateSaysTheNewestReleaseForABuildWithoutAVersion runs update as the test binary,
// which carries no release version, and checks that it is told the newest release and
// not that it is behind or up to date, since nothing says which.
func TestUpdateSaysTheNewestReleaseForABuildWithoutAVersion(t *testing.T) {
	emptyDir(t)
	releases(t, "v99.0.0")
	release(t, "dev")

	out, err := run(t, "update")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "carries no version", "✓ the newest release is 99.0.0")
	lacks(t, out, "up to date", "changelog")
}

// TestUpdateCheckComparesAPseudoVersion runs update --check for a build from main, one
// stamped with a pseudo-version after v0.4.0, against a newest release of 0.4.0 and of
// 0.4.1, and checks that it reads as ahead of the first and behind the second.
func TestUpdateCheckComparesAPseudoVersion(t *testing.T) {
	emptyDir(t)
	release(t, "v0.4.1-0.20260912000000-abcdef123456")

	releases(t, "v0.4.0")
	out, err := run(t, "update", "--check")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "ahead of the newest release", "✓ ahead of 0.4.0, the newest release")
	lacks(t, out, "available")

	releases(t, "v0.4.1")
	out, err = run(t, "update", "--check")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "✓ 0.4.1 is available; run qory update to install it")
	lacks(t, out, "ahead")
}

// TestUpdateOffersTheReleaseToABuildAhead runs update for a build ahead of the newest
// release: it asks on a terminal and keeps the build on no, goes on to install on yes,
// which the fake site, holding no archive, stops, and in a script it needs --release.
func TestUpdateOffersTheReleaseToABuildAhead(t *testing.T) {
	emptyDir(t)
	releases(t, "v0.4.0")
	release(t, "v0.4.1-0.20260912000000-abcdef123456")

	out, _, err := runSplit(t, "n\n", "update")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "Install the release 0.4.0 over it? [y/N]", "✓ kept 0.4.1-0.20260912000000-abcdef123456")

	out, _, err = runSplit(t, "y\n", "update")
	if err == nil || !strings.Contains(err.Error(), "has no archive") {
		t.Fatalf("err = %v, want the install to have been tried:\n%s", err, out)
	}

	// No terminal answers, so the flag has to.
	_, err = run(t, "update")
	if err == nil || !strings.Contains(err.Error(), "--release") {
		t.Fatalf("err = %v, want one naming --release", err)
	}
	if cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("exit = %d, want %d", cmd.ExitCode(err), cmd.ExitInput)
	}
	_, err = run(t, "update", "--release")
	if err == nil || !strings.Contains(err.Error(), "has no archive") {
		t.Fatalf("err = %v, want the install to have been tried after --release", err)
	}
}
