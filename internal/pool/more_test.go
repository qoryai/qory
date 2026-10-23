package pool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/pool"
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
// handler's attribute that contains what starts a secret, qak_, qrk_ or qecr_ in any
// case, as [hidden], and leaves the rest.
func TestSafeHandlerHidesSecrets(t *testing.T) {
	for _, prefix := range []string{"qrk_", "QECR_"} {
		var buf bytes.Buffer
		log := slog.New(pool.SafeHandler(slog.NewJSONHandler(&buf, nil))).With(slog.String("with", "QAK_x"))
		log.Info("a line with "+prefix+"a.b", slog.String("plain", "dp_0000000000000000"), slog.String("v", "x qecr_a.b"),
			slog.Any("err", errorText("Qrk_a")), slog.Group("g", slog.String("m", "qak_y"), slog.String("c", "Qecr_z"), slog.Int("n", 1)), slog.String(prefix+"key", "1"))
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		g, _ := m["g"].(map[string]any)
		if m["msg"] != "[hidden]" || m["plain"] != "dp_0000000000000000" || m["v"] != "[hidden]" || m["err"] != "[hidden]" ||
			m["with"] != "[hidden]" || g["m"] != "[hidden]" || g["c"] != "[hidden]" || g["n"] != float64(1) || m["[hidden]"] != "1" {
			t.Errorf("the line is %s", buf.String())
		}
	}
}

// errorText is an error whose text is itself.
type errorText string

func (e errorText) Error() string { return string(e) }
