package cmd_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// TestReceiveStoresWhatVerifies runs the receiver on the webhook configuration's
// address: a signed batch is accepted and stored once, its redelivery is accepted and
// not stored again, a batch with a wrong signature is refused, and a stop leaves the
// file with the events it took.
func TestReceiveStoresWhatVerifies(t *testing.T) {
	dir := emptyDir(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	secret := "test-secret-of-sixteen-or-more"
	hook := filepath.Join(dir, "webhook.yaml")
	writeFile(t, hook, "version: 1\nurl: http://"+addr+"/events\nsecret: "+secret+"\n")
	out := filepath.Join(dir, "got.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var buf strings.Builder
	root := cmd.Root()
	root.SetArgs([]string{"receive", "--webhook", hook, "--out", out})
	root.SetOut(&buf)
	root.SetErr(&buf)
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	url := "http://" + addr + "/events"
	for i := 0; ; i++ {
		if r, err := http.Get(url); err == nil {
			r.Body.Close()
			break
		}
		if i > 100 {
			t.Fatal("the receiver did not come up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	body := []byte(`[{"specversion":"1.0","id":"e1","source":"urn:qory:run:r1","type":"ai.qory.ping","subject":"r1","time":"2026-09-16T10:00:00.000Z","data":{}}]`)
	post := func(sig string) int {
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/cloudevents-batch+json")
		req.Header.Set("X-Qory-Delivery", "d1")
		req.Header.Set("X-Qory-Signature-256", sig)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := post(good); got != http.StatusAccepted {
		t.Errorf("a signed batch got %d", got)
	}
	if got := post(good); got != http.StatusAccepted {
		t.Errorf("a redelivery got %d", got)
	}
	if got := post("sha256=" + strings.Repeat("0", 64)); got != http.StatusUnauthorized {
		t.Errorf("a wrong signature got %d", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 {
		t.Errorf("%d events stored, want 1:\n%s", lines, data)
	}
	wants(t, buf.String(), "qory receive", addr, "stopped; 1 events")
}

// TestReceiveNeedsAConfiguration is a receiver with nothing to receive for: no webhook
// file in the configuration directory is an input error naming the file.
func TestReceiveNeedsAConfiguration(t *testing.T) {
	emptyDir(t)
	_, err := run(t, "receive")
	if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "webhook.yaml") {
		t.Errorf("got %v", err)
	}
}
