package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/suite"
)

// TestAStageWithReviewersGetsTheSuitesBodyInTheFile: forReview starts the suite on the commit,
// waits, and writes what GET returns for the suite to the file, and posts no verdict.
func TestAStageWithReviewersGetsTheSuitesBodyInTheFile(t *testing.T) {
	for _, c := range []struct {
		name, result, state string
		counts              map[string]any
	}{
		{"red", redResult, "failed", map[string]any{"tests": 2.0, "failed": 1.0, "skipped": 0.0}},
		{"green", greenResult, "passed", map[string]any{"tests": 2.0, "failed": 0.0, "skipped": 0.0}},
		{"no result file", "", "passed", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t, suiteWrites(c.result, 0))
			file := filepath.Join(t.TempDir(), "suite-result.json")
			snap, err := h.forReview(context.Background(), suiteCommit, file)
			if err != nil || string(snap.State) != c.state {
				t.Fatalf("forReview = %+v, %v", snap, err)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("the file %q: %v", data, err)
			}
			want := map[string]any{"suite": snap.ID, "commit": suiteCommit, "state": c.state, "counts": nil, "minutes": 1.0, "output": "ran make test\n"}
			if c.counts != nil {
				want["counts"] = c.counts
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("the file has %v, want %v", got, want)
			}
			if !strings.HasSuffix(string(data), "}\n") {
				t.Errorf("the file ends %q", data[max(0, len(data)-5):])
			}
			if info, _ := os.Stat(file); info.Mode().Perm() != 0o644 {
				t.Errorf("mode %v, want 0644", info.Mode().Perm())
			}
			if posted := h.api.verdicts(); len(posted) != 0 {
				t.Errorf("posted %q", posted)
			}
		})
	}
}

// TestTheFileIsTheBodyOfTheSuiteARequestStarted: a suite that a session's request started on the
// commit is the one the file has, and a suite that a stop ends is in the file as stopped.
func TestTheFileIsTheBodyOfTheSuiteARequestStarted(t *testing.T) {
	h := newHelper(t, suiteWrites(greenResult, 0))
	h.start(t, suiteCommit)
	first, _ := h.reg.Newest()
	file := filepath.Join(t.TempDir(), "r.json")
	snap, err := h.forReview(context.Background(), suiteCommit, file)
	if err != nil || snap.ID != first.ID {
		t.Errorf("forReview = %+v, %v, want suite %s", snap, err, first.ID)
	}
	stopped := newHelper(t, func(ctx context.Context, _ suite.Job, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(40*time.Millisecond, cancel)
	if snap, err := stopped.forReview(ctx, suiteCommit, file); err != nil || snap.State != suite.Stopped {
		t.Errorf("a stop: %+v, %v, want a stopped suite and no error", snap, err)
	}
	if data, _ := os.ReadFile(file); !strings.Contains(string(data), `"state":"stopped"`) {
		t.Errorf("the file has %q", data)
	}
}

// TestAStageWithReviewersRefusesWhatItCannotRunOrWrite: no suite, a commit that is no id, and a
// file that cannot be written are errors with the fix first and the cause on the next line.
func TestAStageWithReviewersRefusesWhatItCannotRunOrWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "r.json")
	h := newHelper(t, suiteWrites(greenResult, 0))
	noPlan := &suiteRun{none: errNoBuildPlan}
	var unwalled *suiteRun
	for _, c := range []struct {
		name   string
		r      *suiteRun
		commit string
		file   string
		want   string
	}{
		{"no wall", unwalled, suiteCommit, file, "the run has no wall"},
		{"no plan", noPlan, suiteCommit, file, "the run started with no build plan"},
		{"a ref", h.suiteRun, "HEAD", file, "the commit is not a full commit id"},
		{"a folder that is gone", h.suiteRun, suiteCommit, filepath.Join(t.TempDir(), "gone", "r.json"), "no such file or directory"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.r.forReview(context.Background(), c.commit, c.file)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want one with %q", err, c.want)
			}
			first, _, _ := strings.Cut(err.Error(), "\n")
			if len(first) > 100 {
				t.Errorf("the first line has %d characters: %q", len(first), first)
			}
		})
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("a refusal wrote the file")
	}
}

// TestTheSuiteBodyOfTheFileIsTheBodyOfGet: the file and GET /v1/suites/<id> have one body.
func TestTheSuiteBodyOfTheFileIsTheBodyOfGet(t *testing.T) {
	q, reg, _ := suiteRoute(t, context.Background(), suiteWrites(redResult, 0))
	r := &suiteRun{reg: reg}
	file := filepath.Join(t.TempDir(), "r.json")
	snap, err := r.forReview(context.Background(), suiteCommit, file)
	if err != nil {
		t.Fatal(err)
	}
	status, viaGet, _ := suiteAsk(t, q, http.MethodGet, "/v1/suites/"+snap.ID, "")
	data, _ := os.ReadFile(file)
	var viaFile map[string]any
	if err := json.Unmarshal(data, &viaFile); err != nil || status != http.StatusOK {
		t.Fatalf("file %q: %v; GET %d", data, err, status)
	}
	// The minutes are read at the moment of each read, so only the end state compares equal.
	delete(viaGet, "minutes")
	delete(viaFile, "minutes")
	if !reflect.DeepEqual(viaGet, viaFile) {
		t.Errorf("GET has %v, the file %v", viaGet, viaFile)
	}
}
