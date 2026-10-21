package pool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// TestASecondSignalExitsAtOnce ends the context at the first SIGTERM or SIGINT, and
// exits at the second with 128 and its number, not before.
func TestASecondSignalExitsAtOnce(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			codes := make(chan int, 2)
			ctx, stop := pool.OnSignals(context.Background(), func(code int) { codes <- code })
			defer stop()
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("the first signal did not end the context")
			}
			select {
			case code := <-codes:
				t.Fatalf("the first signal exited %d", code)
			case <-time.After(100 * time.Millisecond):
			}
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-codes:
				if code != 128+int(sig) {
					t.Errorf("exited %d, want %d", code, 128+int(sig))
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the second signal did not exit")
			}
		})
	}
}

// TestNoCoreDumps sets RLIMIT_CORE to 0, soft and hard.
func TestNoCoreDumps(t *testing.T) {
	var was syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &was); err != nil {
		t.Fatal(err)
	}
	if err := pool.NoCoreDumps(); err != nil {
		t.Fatal(err)
	}
	var now syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &now); err != nil {
		t.Fatal(err)
	}
	if now.Cur != 0 || now.Max != 0 {
		t.Errorf("RLIMIT_CORE is %+v", now)
	}
}

// TestSafeHandlerHidesSecrets writes a message, a value, a key, a group's member and a
// handler's attribute that holds what starts a secret as [hidden], and leaves the rest.
func TestSafeHandlerHidesSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(pool.SafeHandler(slog.NewJSONHandler(&buf, nil))).With(slog.String("with", "QAK_x"))
	log.Info("a line with qrk_a.b", slog.String("plain", "dp_0000000000000000"), slog.String("v", "x qrk_a.b"),
		slog.Any("err", errorText("Qrk_a")), slog.Group("g", slog.String("m", "qak_y"), slog.Int("n", 1)), slog.String("qrk_key", "1"))
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	g, _ := m["g"].(map[string]any)
	if m["msg"] != "[hidden]" || m["plain"] != "dp_0000000000000000" || m["v"] != "[hidden]" || m["err"] != "[hidden]" ||
		m["with"] != "[hidden]" || g["m"] != "[hidden]" || g["n"] != float64(1) || m["[hidden]"] != "1" {
		t.Errorf("the line is %s", buf.String())
	}
}

// errorText is an error whose text is itself.
type errorText string

func (e errorText) Error() string { return string(e) }

// TestAnExploreRunWithTheRealLauncher runs explore runs through explore.Launch and a
// fake qory: the run key reaches qory run on its descriptor 3 alone, no argument and
// no variable of either command holds a secret or a QORY_ or ANTHROPIC_ variable, and a
// run going on when the pool stops gets SIGTERM and posts cancelled, pool_stopped.
func TestAnExploreRunWithTheRealLauncher(t *testing.T) {
	for _, c := range []struct {
		name, mode, state, reason string
	}{{"succeeded", "", "succeeded", ""}, {"stopped", "block", "cancelled", "pool_stopped"}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1, 1)
			h.pool.Explore = nil
			t.Setenv("HOME", h.root)
			t.Setenv("QORY_RUN_KEY", testKey)
			t.Setenv("QORY_ACCESS_KEY_SECRET", "qak_notakey")
			t.Setenv("ANTHROPIC_API_KEY", "sk-test")
			runConfig := h.pool.Settings.Explore.RunConfig
			b, _ := json.Marshal(script{RunID: runA, KeySum: sum([]byte(testKey)), Record: true, Mode: c.mode})
			if err := os.WriteFile(filepath.Join(runConfig, scriptFile), b, 0o600); err != nil {
				t.Fatal(err)
			}
			h.srv.Offer(provisionertest.Run{RunID: runA, RunKey: testKey})
			h.start()
			if c.mode == "block" {
				h.waitFor("qory run", func() bool { _, err := os.Stat(filepath.Join(runConfig, readyFile)); return err == nil })
				if err := h.stop(); err != nil {
					t.Fatal(err)
				}
			} else {
				h.settled(1)
				h.stop()
			}
			if o, ok := h.outcome(runA); !ok || o.State != c.state || o.Reason != c.reason {
				t.Errorf("the outcome is %+v, %v", o, ok)
			}
			h.notReleased()
			for _, verb := range []string{"harness", "run"} {
				b, err := os.ReadFile(filepath.Join(runConfig, "report-"+verb+".json"))
				if err != nil {
					t.Fatalf("qory %s did not run: %v", verb, err)
				}
				var r report
				if err := json.Unmarshal(b, &r); err != nil {
					t.Fatal(err)
				}
				if verb == "run" && (!r.KeyOK || !slices.Contains(r.Args, "--run-key-fd")) {
					t.Errorf("qory run did not get its key on descriptor 3: %v", r.Args)
				}
				for _, kv := range r.Env {
					if strings.HasPrefix(kv, "QORY_") || strings.HasPrefix(kv, "ANTHROPIC_") {
						t.Errorf("qory %s got %s", verb, kv)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(h.pool.Settings.Explore.State, "pool", "runs", runA)); !os.IsNotExist(err) {
				t.Errorf("the run directory is still there: %v", err)
			}
		})
	}
}

// TestALaunchErrorWithASecretIsHidden logs the error of a launch that could not start,
// and never what in it starts a secret: the run key itself, here, which is printed as
// a phrase.
func TestALaunchErrorWithASecretIsHidden(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.setLaunch(func(ctx context.Context, r explore.Run) (explore.Result, error) {
		return explore.Result{Ending: explore.EndingOf(false, child.Result{})}, errors.New("the gateway refused " + testKey)
	})
	h.srv.Offer(provisionertest.Run{RunID: runA, RunKey: testKey})
	h.start()
	h.settled(1)
	h.stop()
	m := h.log.with("the run could not be started")
	if len(m) != 1 || m[0]["error"] != "the gateway refused (a name that contains a run key)" {
		t.Errorf("the launch's lines are %v", m)
	}
}
