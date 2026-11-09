package pool

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/provisioner"
)

// TestReadSession reads the id and the leader's start stamp that run.session contains,
// and returns an error for an id that would signal more than one run or the pool
// itself: 0, 1, a negative id, the pool's process id, the pool's session. Text that is
// no number, a stamp that is no number, an empty file, and a file with the id alone are
// errors too, as is a folder without run.session.
func TestReadSession(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := readSession(dir); err == nil {
		t.Error("a folder without run.session was read")
	}
	for _, bad := range []string{"0 1.5", "1 1.5", "-5 1.5", strconv.Itoa(os.Getpid()) + " 1.5", strconv.Itoa(child.Session()) + " 1.5",
		"abc 1.5", "54321 abc", "54321 1.5 7", "54321", ""} {
		if err := os.WriteFile(filepath.Join(dir, runSessionFile), []byte(bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, stamp, err := readSession(dir); err == nil {
			t.Errorf("run.session %q was read as %d %q", bad, n, stamp)
		}
	}
	for _, good := range []string{"54321 8811", "54321 1762600000.000123"} {
		if err := os.WriteFile(filepath.Join(dir, runSessionFile), []byte(good+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, stamp, err := readSession(dir); err != nil || strconv.Itoa(n)+" "+stamp != good {
			t.Errorf("run.session %q was read as %d %q, %v", good, n, stamp, err)
		}
	}
}

// TestContinuedNeedsTheSameStamp shows a list as continued only when one of its
// processes has an id that an earlier list had, with the same start stamp.
func TestContinuedNeedsTheSameStamp(t *testing.T) {
	s := &leftSession{signalled: map[int]string{4242: "100.000001"}}
	if s.continued([]child.Member{{ID: 4242, Stamp: "100.000002"}, {ID: 4300, Stamp: "100.000001"}}) {
		t.Error("an id with another start continued the session")
	}
	if !s.continued([]child.Member{{ID: 4300, Stamp: "1.5"}, {ID: 4242, Stamp: "100.000001"}}) {
		t.Error("an id with the same start did not continue the session")
	}
}

// TestKillStopsAfterThreeRounds lists a session that has a new process at each list:
// kill sends SIGKILL in three rounds, then stops.
func TestKillStopsAfterThreeRounds(t *testing.T) {
	next := 5000
	var sent [][]int
	s := &leftSession{
		id:        4242,
		signalled: map[int]string{4243: "1.5"},
		list: func(int) ([]child.Member, error) {
			next++
			return []child.Member{{ID: 4243, Stamp: "1.5"}, {ID: next, Stamp: "2.5"}}, nil
		},
		signal: func(ids []int, session int, kill bool) {
			if session != 4242 || !kill {
				t.Errorf("the signal went to session %d with kill %v", session, kill)
			}
			sent = append(sent, ids)
		},
	}
	if err := s.kill(); err != nil {
		t.Fatal(err)
	}
	if len(sent) != killRounds || killRounds != 3 {
		t.Errorf("SIGKILL was sent in %d rounds, want 3: %v", len(sent), sent)
	}
}

// TestWriteSessionRemovesItsFileOnAFailure makes the rename to run.session fail, with
// a folder of that name: writeSession returns the error, and no file beside
// run.session is left.
func TestWriteSessionRemovesItsFileOnAFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, runSessionFile, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSession(dir, os.Getpid()); err == nil {
		t.Fatal("the rename over a folder did not fail")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, runSessionFile+".*")); len(left) != 0 {
		t.Errorf("the files %q are left", left)
	}
}

// TestEveryEndedReleaseHasALine checks that each status after which a release is not
// sent again has its line.
func TestEveryEndedReleaseHasALine(t *testing.T) {
	for status := 100; status < 600; status++ {
		if provisioner.EndsAttempts(&provisioner.Refusal{Status: status, Signed: true}) && releaseEndedLine[status] == "" {
			t.Errorf("status %d ends the release and has no line", status)
		}
	}
}
