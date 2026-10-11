package cmd_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/receiver"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/foragerdir"
)

// keptLabels is the labels qory keeps for the checkout at root, nil for none.
func keptLabels(t *testing.T, root string) foragerdir.Labels {
	t.Helper()
	l, err := configDir().ReadLabels(root)
	if errors.Is(err, foragerdir.ErrNoLabels) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// wantsLabels fails the test unless the checkout at root keeps want.
func wantsLabels(t *testing.T, root string, want foragerdir.Labels) {
	t.Helper()
	if got := keptLabels(t, root); !maps.Equal(got, want) {
		t.Errorf("the checkout keeps %v, want %v", got, want)
	}
}

// labelsChangedText is the labels_changed refusal of the checkout at root, kept as
// first and now from the origin remote as now.
func labelsChangedText(root, first, now string) string {
	return "the checkout " + root + " first ran against the server as " + first + ", and its origin remote now gives " + now + "; the server chooses a run's policy by its labels, so the run does not start: if you changed the remote, run the same command with --relabel (labels_changed)"
}

// TestACheckoutKeepsItsLabels is a checkout's runs against the server. The first run the
// server accepts keeps the labels the origin remote gives, mode 0600 in labels, mode
// 0700, beside forager.yaml. Once the remote changes, a run is refused before anything
// registers, labels_changed, exit 2: the runtime does not run, the server gets no
// request, and nothing is recorded. --relabel runs, keeps the new labels and says so; a
// later run with them runs, and says nothing of them. A label given with --label is used
// as given, neither checked nor kept.
func TestACheckoutKeepsItsLabels(t *testing.T) {
	root, srv := serverRun(t, "", "")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("the first run: %v\n%s", err, out)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/app"})
	path, err := configDir().LabelsPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(t, path); m != 0o600 {
		t.Errorf("%s is mode %v", path, m)
	}
	if m := mode(t, filepath.Dir(path)); m != 0o700 {
		t.Errorf("%s is mode %v", filepath.Dir(path), m)
	}

	runGit(t, root, "remote", "set-url", "origin", "https://git.example.com/acme/shop.git")
	clearRuns(t, root)
	before := len(srvTargets(srv))
	out, err := run(t, "run")
	want := labelsChangedText(root, "forge git.example.com, repository acme/app", "forge git.example.com, repository acme/shop")
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a changed origin: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	lacks(t, out, "hello from", "labels are now")
	if n := len(srvTargets(srv)); n != before {
		t.Errorf("a refused run sent the server %d requests", n-before)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a refused run left records %v", ids)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/app"})

	out, err = run(t, "run", "--relabel")
	if err != nil {
		t.Fatalf("--relabel: %v\n%s", err, out)
	}
	wants(t, out, "hello from", "qory run: the checkout's labels are now forge git.example.com, repository acme/shop\n")
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/shop"})
	if out, err = run(t, "run"); err != nil {
		t.Fatalf("a run with the new labels: %v\n%s", err, out)
	}
	lacks(t, out, "labels are now", "labels were not kept")

	runGit(t, root, "remote", "set-url", "origin", "https://git.example.com/acme/other.git")
	if out, err := run(t, "run", "--label", "repository=acme/given"); err != nil {
		t.Fatalf("a repository given with --label: %v\n%s", err, out)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/shop"})
	srv.mu.Lock()
	last := srv.registered[len(srv.registered)-1]
	srv.mu.Unlock()
	if last != "forge=git.example.com&repository=acme%2Fgiven" {
		t.Errorf("the run registered %q", last)
	}
}

// TestACheckoutWithoutAnOriginKeepsNone is a checkout without an origin remote: its first
// run keeps no forge and no repository, and once it has a remote, a run is refused,
// naming neither.
func TestACheckoutWithoutAnOriginKeepsNone(t *testing.T) {
	root, _ := serverRun(t, "", "")
	runGit(t, root, "remote", "remove", "origin")
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("the first run: %v\n%s", err, out)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "", "repository": ""})
	runGit(t, root, "remote", "add", "origin", "https://git.example.com/acme/app.git")
	_, err := run(t, "run")
	if want := labelsChangedText(root, "no forge, no repository", "forge git.example.com, repository acme/app"); err == nil || err.Error() != want {
		t.Errorf("a remote added: %v\nwant %q", err, want)
	}
}

// TestLabelsAreKeptOnlyAgainstTheServer is what neither checks nor keeps a checkout's
// labels: --local, a run without a server, and a run the server refuses; --relabel with
// --local is refused as input.
func TestLabelsAreKeptOnlyAgainstTheServer(t *testing.T) {
	root, srv := serverRun(t, "", "")
	if out, err := run(t, "run", "--local"); err != nil {
		t.Fatalf("--local: %v\n%s", err, out)
	}
	wantsLabels(t, root, nil)

	srv.refuse = &receiver.Refusal{Status: 409, Code: "repository_not_allowed"}
	clearRuns(t, root)
	if _, err := run(t, "run"); cmd.ExitCode(err) != 1 {
		t.Errorf("a refused registration: %v (exit %d)", err, cmd.ExitCode(err))
	}
	wantsLabels(t, root, nil)
	srv.refuse = nil

	clearRuns(t, root)
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("an accepted run: %v\n%s", err, out)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/app"})
	runGit(t, root, "remote", "set-url", "origin", "https://git.example.com/acme/shop.git")
	clearRuns(t, root)
	if out, err := run(t, "run", "--local"); err != nil {
		t.Fatalf("--local with another origin: %v\n%s", err, out)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/app"})

	want := "--relabel keeps a checkout's new labels for the server, and --local contacts none: leave out --relabel"
	if out, err := run(t, "run", "--local", "--relabel"); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("--relabel --local: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}

	// Without a server, nothing is checked or kept.
	if err := os.Remove(filepath.Join(string(configDir()), "forager.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(configDir().Path(foragerdir.SecretFile)); err != nil {
		t.Fatal(err)
	}
	clearRuns(t, root)
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("without a server: %v\n%s", err, out)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/app"})

	other := runKeyCheckout(t)
	if out, err := run(t, "run"); err != nil {
		t.Fatalf("a machine without forager.yaml: %v\n%s", err, out)
	}
	if _, err := os.Lstat(configDir().Path(foragerdir.LabelsDir)); !os.IsNotExist(err) {
		t.Errorf("a run without a server made %s: %v", configDir().Path(foragerdir.LabelsDir), err)
	}
	wantsLabels(t, other, nil)
}

// TestLabelsThatCannotBeKeptLetTheRunGoOn is a labels beside forager.yaml that is a
// symbolic link, where qory keeps nothing: the accepted run says in one line that the
// labels were not kept, and runs.
func TestLabelsThatCannotBeKeptLetTheRunGoOn(t *testing.T) {
	serverRun(t, "", "")
	if err := os.Symlink(t.TempDir(), configDir().Path(foragerdir.LabelsDir)); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "hello from", "qory run: the checkout's labels were not kept: ", "is a symbolic link; it must be the directory itself; the next run checks none\n")
	if strings.Count(out, "labels were not kept") != 1 {
		t.Errorf("said more than once:\n%s", out)
	}
}

// TestAMachineBehindViaKeepsItsCheckoutsLabels is a run of a machine behind
// gateway.server.via: once the server accepts the registration qory sends through via,
// the checkout's labels are kept, though the run then does not reach its gateway. Once
// the remote changes, a run is refused before it registers through via.
func TestAMachineBehindViaKeepsItsCheckoutsLabels(t *testing.T) {
	root := runKeyCheckout(t)
	const id = "0192f0c1-7d4e-7a2b-8c3d-555555555555"
	kept := filepath.Join(string(configDir()), foragerdir.RunKeysDir, id)
	g := newKeptKeyGateway(t, func() string { return kept })
	srv := newFakeServer(t, "")
	srv.choose(receiver.Default, g.url(), newKey(t))
	via := newViaGateway(t, srv.URL)
	walledServerFile(t, srv, via.via())
	if out, err := run(t, "run", "--run-id", id); err == nil {
		t.Fatalf("the run went on without its gateway:\n%s", out)
	}
	srv.mu.Lock()
	registered := len(srv.registered)
	srv.mu.Unlock()
	if registered != 1 {
		t.Fatalf("the server registered %d runs", registered)
	}
	wantsLabels(t, root, foragerdir.Labels{"forge": "git.example.com", "repository": "acme/app"})

	runGit(t, root, "remote", "set-url", "origin", "https://git.example.com/acme/shop.git")
	before := len(via.relayed())
	_, err := run(t, "run")
	if want := labelsChangedText(root, "forge git.example.com, repository acme/app", "forge git.example.com, repository acme/shop"); err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a changed origin: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	if n := len(via.relayed()); n != before {
		t.Errorf("a refused run sent via %d requests", n-before)
	}
}
