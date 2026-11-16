package cmd_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// otherRunID is the run id of a second run from a run code, beside [keyRunID].
const otherRunID = "0192f0c1-7d4e-7a2b-8c3d-4e5f6a7b8c9e"

// homeInputs is what the home at a join has of a run from a run code: the bytes of
// $QORY_HARNESS_HOME/inputs.json and the file's mode, and the bytes of the inputs.json
// in the folder in/ that the session's spec mounts read-only.
type homeInputs struct {
	copied, written []byte
	mode            fs.FileMode
}

// inputsAtJoin reads the inputs of the run whose session's spec is s, at its join.
func inputsAtJoin(t *testing.T, s session.SessionSpec) homeInputs {
	t.Helper()
	var in homeInputs
	path := filepath.Join(s.HarnessHome, "inputs.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the copy of the inputs in the home: %v", err)
	}
	in.mode = info.Mode().Perm()
	if in.copied, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Mounts {
		if filepath.Base(m.Path) == "in" && m.ReadOnly {
			if in.written, err = os.ReadFile(filepath.Join(m.Path, "inputs.json")); err != nil {
				t.Fatal(err)
			}
		}
	}
	return in
}

// mountsHome reports whether s mounts its home read-only.
func mountsHome(s session.SessionSpec) bool {
	for _, m := range s.Mounts {
		if m.Path == s.HarnessHome && m.ReadOnly {
			return true
		}
	}
	return false
}

// sameFolder fails the test when a and b, with their links resolved, are not one folder.
func sameFolder(t *testing.T, what, a, b string) {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil || ra != rb {
		t.Errorf("%s is %s, want %s (%v, %v)", what, a, b, errA, errB)
	}
}

// otherInputs is documents with the member second added to its inputs, so a second run's
// inputs.json differs from the first run's.
func otherInputs(t *testing.T, documents json.RawMessage) json.RawMessage {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(documents, &doc); err != nil {
		t.Fatal(err)
	}
	inputs, ok := doc["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("the documents' inputs are %v, want an object", doc["inputs"])
	}
	inputs["second"] = true
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestARunFromARunCodeReadsItsInputsInItsHome is a walled run from a run code with no --home
// and no harness.home: it composes into runs/<name>-<hash>/<run id> under qory/homes, which the
// agent's container mounts read-only, the home contains inputs.json, mode 0644, with the bytes
// of the inputs.json that the workflow gets, and the home is gone after the run.
func TestARunFromARunCodeReadsItsInputsInItsHome(t *testing.T) {
	var root string
	var s session.SessionSpec
	var in homeInputs
	r := codeHarness{
		before: func() []string {
			walledByDocker(t)
			var err error
			if root, err = os.Getwd(); err != nil {
				t.Fatal(err)
			}
			return nil
		},
		joined: func(spec session.SessionSpec) {
			s = spec
			in = inputsAtJoin(t, spec)
			sameFolder(t, "the home's folder", filepath.Dir(spec.HarnessHome), runHomesFolder(t, root))
		},
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if filepath.Base(s.HarnessHome) != keyRunID {
		t.Errorf("the home is %s, want the folder %s", s.HarnessHome, keyRunID)
	}
	if !mountsHome(s) {
		t.Errorf("the session's mounts %+v do not have the home %s read-only", s.Mounts, s.HarnessHome)
	}
	if in.mode != 0o644 {
		t.Errorf("the copy's mode is %v, want 0644", in.mode)
	}
	if in.written == nil || string(in.copied) != string(in.written) {
		t.Errorf("the copy in the home is %q, want the bytes of in/inputs.json, %q", in.copied, in.written)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(in.copied, &doc); err != nil || doc["qory"] == nil {
		t.Errorf("the copy %q has no member qory: %v", in.copied, err)
	}
	if _, err := os.Lstat(s.HarnessHome); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the home stays after the run: %v", err)
	}
}

// TestTwoRunsFromRunCodesKeepTheirHomes is a second walled run from a run code in the same
// checkout, which starts while the first run's agent runs: each run composes into a home for
// its run id, so the second run's compose and end leave the first run's home and its copy of
// the inputs as they are, though the second run's inputs differ.
func TestTwoRunsFromRunCodesKeepTheirHomes(t *testing.T) {
	var nowhere cmd.Nowhere
	var documents json.RawMessage
	var first, second string
	var whole map[string]string
	joins := 0
	r := codeHarness{
		before: func() []string {
			walledByDocker(t)
			return nil
		},
		nowhere: func(n *cmd.Nowhere) { nowhere = *n },
		relay:   func(g *codeRelay) { documents = g.documents },
		joined: func(s session.SessionSpec) {
			joins++
			if joins == 2 {
				second = s.HarnessHome
				sameFolder(t, "the second run's folder of homes", filepath.Dir(second), filepath.Dir(first))
				if !maps.Equal(snapshot(t, first), whole) {
					t.Error("the second run's compose changed the first run's home")
				}
				inputsAtJoin(t, s)
				return
			}
			first = s.HarnessHome
			inputsAtJoin(t, s)
			whole = snapshot(t, first)
			g := newCodeRelay(t)
			g.documents, g.runID = otherInputs(t, documents), otherRunID
			again := nowhere
			again.Dir, again.OnOpen, again.OnStep = tempDir(t), nil, nil
			_, _, restore := cmd.OpenNowhereWith(again)
			out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL)
			restore()
			if !errors.Is(err, cmd.ErrJoinedNowhere) {
				t.Errorf("the second run: %v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
			}
			if !maps.Equal(snapshot(t, first), whole) {
				t.Error("the second run's end changed the first run's home")
			}
			if second != "" {
				if _, err := os.Lstat(second); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("the second run's home stays after its run: %v", err)
				}
			}
		},
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || joins != 2 {
		t.Fatalf("%v (exit %d), %d joins, want 2\n%s", r.err, cmd.ExitCode(r.err), joins, r.out)
	}
	if filepath.Base(first) != keyRunID || filepath.Base(second) != otherRunID {
		t.Errorf("the homes are %s and %s, want the folders %s and %s", first, second, keyRunID, otherRunID)
	}
	if _, err := os.Lstat(first); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the first run's home stays after its run: %v", err)
	}
}

// TestARunFromARunCodeWithARelativeCacheFolderHasAnAbsoluteHome is a run from a run code,
// started in a folder of the checkout, with a relative HOME and no XDG_CACHE_HOME, so the
// cache directory is relative: the session's QORY_HARNESS_HOME, its spec's HarnessHome, is an
// absolute path under HOME as it counts from the working folder.
func TestARunFromARunCodeWithARelativeCacheFolderHasAnAbsoluteHome(t *testing.T) {
	var home string
	r := codeHarness{
		before: func() []string {
			root, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			sub := filepath.Join(root, "sub")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(sub)
			// From sub, ../../relative-home is beside the checkout.
			home = filepath.Join(filepath.Dir(root), "relative-home")
			t.Setenv("XDG_CACHE_HOME", "")
			t.Setenv("HOME", filepath.Join("..", "..", "relative-home"))
			return nil
		},
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if got := r.spec.HarnessHome; !filepath.IsAbs(got) || !strings.HasPrefix(got, home+string(filepath.Separator)) {
		t.Errorf("the home is %s, want an absolute path under %s", got, home)
	}
	if !slices.Contains(strings.Split(r.spec.HarnessHome, string(filepath.Separator)), keyRunID) {
		t.Errorf("the home %s is not the folder of the run's id", r.spec.HarnessHome)
	}
}

// TestARunFromARunCodeWithItsRunIDTakesOneHome is a run from a run code given --run-id, the id
// of the run key it is redeemed for: the run composes into runs/<name>-<hash>/<run id>, not a
// folder under it, writes the report per checkout, beside runs/<name>-<hash>, and its home is
// gone after the run.
func TestARunFromARunCodeWithItsRunIDTakesOneHome(t *testing.T) {
	var root, home string
	r := codeHarness{
		before: func() []string {
			var err error
			if root, err = os.Getwd(); err != nil {
				t.Fatal(err)
			}
			return []string{"--run-id", keyRunID}
		},
		joined: func(s session.SessionSpec) {
			home = s.HarnessHome
			sameFolder(t, "the home", home, filepath.Join(runHomesFolder(t, root), keyRunID))
		},
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the home stays after the run: %v", err)
	}
	if left := runHomes(t, runHomesFolder(t, root)); len(left) != 0 {
		t.Errorf("the folders after the run: %v, want none", left)
	}
	runKeyReport(t, root)
}

// TestARunFromARunCodeWithAGivenHomeRemovesTheCopyOfItsInputs is a run from a run code with
// --home: the home has the copy of the inputs while the agent runs, and once the run is over
// the home stays and the copy is gone.
func TestARunFromARunCodeWithAGivenHomeRemovesTheCopyOfItsInputs(t *testing.T) {
	given := filepath.Join(tempDir(t), "homes")
	var home string
	r := codeHarness{
		before: func() []string { return []string{"--home", given} },
		joined: func(s session.SessionSpec) {
			home = s.HarnessHome
			inputsAtJoin(t, s)
		},
	}.run(t)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if !strings.HasPrefix(home, given+string(filepath.Separator)) {
		t.Fatalf("the home is %s, want a folder of %s", home, given)
	}
	if _, err := os.Stat(filepath.Join(home, "claude")); err != nil {
		t.Errorf("the given home is gone after the run: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "inputs.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy of the inputs stays in the given home: %v", err)
	}
}

// TestARunFromARunCodeWithAGivenHomeRemovesTheCopyAfterAFailure is a run from a run code with
// --home whose runtime, codex, runs no command by its name from a headless start, and whose
// harness has the workflow only as a command: the run stops after qory composed the home and
// wrote the copy of the inputs, exit 1 before the join, and the given home stays with no copy,
// so the next run in it does not stop at inputs.json.
func TestARunFromARunCodeWithAGivenHomeRemovesTheCopyAfterAFailure(t *testing.T) {
	given := filepath.Join(tempDir(t), "homes")
	r := codeHarness{
		change: func(d map[string]any) { d["run_settings"] = map[string]any{"runtime": "codex"} },
		before: func() []string { return []string{"--home", given} },
	}.run(t)
	if r.joined || r.err == nil || errors.Is(r.err, cmd.ErrJoinedNowhere) {
		t.Fatalf("%v (exit %d), want a failure before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.err.Error(), "codex runs no command by its name from a headless start")
	var homes []string
	entries, err := os.ReadDir(given)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			homes = append(homes, filepath.Join(given, e.Name()))
		}
	}
	if len(homes) != 1 {
		t.Fatalf("the folders of %s are %v, want the one home of the run", given, homes)
	}
	if _, err := os.Stat(filepath.Join(homes[0], "AGENTS.md")); err != nil {
		t.Errorf("the run did not compose the home before it failed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(homes[0], "inputs.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy of the inputs stays in the given home after the failure: %v", err)
	}
}
